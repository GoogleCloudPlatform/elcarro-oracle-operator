package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"time"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/pkg/utils"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/go-logr/logr"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type oracleBackupFactory interface {
	newOracleBackup(r *BackupReconciler, backup *v1alpha1.Backup, inst *v1alpha1.Instance, log logr.Logger) oracleBackup
}

type RealOracleBackupFactory struct{}

func (f *RealOracleBackupFactory) newOracleBackup(r *BackupReconciler, backup *v1alpha1.Backup, inst *v1alpha1.Instance, log logr.Logger) oracleBackup {
	var b oracleBackup
	if backup.Spec.Type == commonv1alpha1.BackupTypeSnapshot {
		b = oracleBackup(&snapshotBackup{
			r:      r,
			backup: backup,
			inst:   inst,
			log:    log,
		})
	} else {
		b = oracleBackup(&physicalBackup{
			r:      r,
			backup: backup,
			log:    log,
		})
	}
	return b
}

type oracleBackup interface {
	create(ctx context.Context) error
	status(ctx context.Context) (done bool, err error)
	generateID() string
}

type snapshotBackup struct {
	r      *BackupReconciler
	backup *v1alpha1.Backup
	inst   *v1alpha1.Instance
	log    logr.Logger
}

func (b *snapshotBackup) create(ctx context.Context) error {
	// Load default preferences (aka "config") if provided by a customer.
	config, err := b.r.BackupCtrl.LoadConfig(b.backup.Namespace)
	if err != nil {
		return err
	}

	var configSpec *commonv1alpha1.ConfigSpec
	if config != nil {
		configSpec = &config.Spec.ConfigSpec
		b.log.Info("customer config loaded", "config", config)
	} else {
		b.log.Info("no customer specific config found, assuming all defaults")
	}

	vsc, err := utils.FindVolumeSnapshotClassName(b.backup.Spec.VolumeSnapshotClass, configSpec, utils.PlatformGCP, utils.EngineOracle)
	if err != nil || vsc == "" {
		return fmt.Errorf("failed to identify a volumeSnapshotClassName for instance: %q", b.backup.Spec.Instance)
	}
	b.log.Info("VolumeSnapshotClass", "volumeSnapshotClass", vsc)

	getPvcNames := func(spec commonv1alpha1.DiskSpec) (string, string, string) {
		shortPVCName, mount := controllers.GetPVCNameAndMount(b.inst.Name, spec.Name)
		fullPVCName := fmt.Sprintf("%s-%s-0", shortPVCName, fmt.Sprintf(controllers.StsName, b.inst.Name))
		snapshotName := fmt.Sprintf("%s-%s", b.backup.Status.BackupID, mount)
		return fullPVCName, snapshotName, vsc
	}
	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("backup-controller")}

	return utils.SnapshotDisks(ctx, controllers.DiskSpecs(b.inst, config), b.backup, b.r.Client, b.r.Scheme, getPvcNames, applyOpts)
}

func (b *snapshotBackup) status(ctx context.Context) (done bool, err error) {
	b.log.Info("found a backup request in-progress")
	ns := b.backup.Namespace
	sel := labels.NewSelector()
	vsLabels := []string{b.backup.Status.BackupID + "-u02", b.backup.Status.BackupID + "-u03"}
	req1, err := labels.NewRequirement("name", selection.In, vsLabels)
	if err != nil {
		return false, err
	}
	sel.Add(*req1)

	req2, err := labels.NewRequirement("namespace", selection.Equals, []string{ns})
	if err != nil {
		return false, err
	}
	sel.Add(*req2)

	listOpts := []client.ListOption{
		client.InNamespace(ns),
		client.MatchingLabelsSelector{Selector: sel},
	}

	var volSnaps snapv1.VolumeSnapshotList
	if err := b.r.List(ctx, &volSnaps, listOpts...); err != nil {
		b.log.Error(err, "failed to get a volume snapshot")
		return false, err
	}
	b.log.Info("list of found volume snapshots", "volSnaps", volSnaps)

	if len(volSnaps.Items) < 1 {
		b.log.Info("no volume snapshots found for a backup request marked as in-progress.", "backup.Status", b.backup.Status)
		return false, errors.New("no volume snapshots found")
	}
	b.log.Info("found a volume snapshot(s) for a backup request in-progress")

	vsStatus := make(map[string]bool)
	for i, vs := range volSnaps.Items {
		b.log.Info("iterating over volume snapshots", "VolumeSnapshot#", i, "name", vs.Name)
		vsStatus[vs.Name] = false

		if vs.Status == nil {
			b.log.Info("not yet ready: Status missing for Volume Snapshot", "namespace", vs.Namespace, "volumeSnapshotName", vs.Name, "volumeSnapshotStatus", vs.Status)
			return false, nil
		}

		if vs.Status.Error != nil {
			b.log.Error(errors.New("the volumeSnapshot is failed"), "volumeSnapshot failed", "namespace", vs.Namespace, "volumeSnapshotName", vs.Name, "volumeSnapshotStatus", vs.Status, "VolumeSnapshotError", vs.Status.Error)
			return true, fmt.Errorf("volumeSnapshot %s/%s failed with: %s", vs.Namespace, vs.Name, *vs.Status.Error.Message)
		}

		if !*vs.Status.ReadyToUse {
			b.log.Info("not yet ready: Status found, but it's not flipped to DONE yet for VolumeSnapshot", "namespace", vs.Namespace, "volumeSnapshotName", vs.Name, "volumeSnapshotStatus", vs.Status)
			return false, nil
		}

		b.log.Info("ready to use status", "VolumeSnapshot#", i, "name", vs, "status", *vs.Status.ReadyToUse)
		vsStatus[vs.Name] = true
	}
	b.log.Info("summary of VolumeSnapshot statuses", "vsStatus", vsStatus)

	return true, nil
}

func (b *snapshotBackup) generateID() string {
	return fmt.Sprintf(backupName, b.backup.Spec.Instance, timeNow().Format("20060102"), "snap", timeNow().Nanosecond())
}

type physicalBackup struct {
	r      *BackupReconciler
	backup *v1alpha1.Backup
	log    logr.Logger
}

func (b *physicalBackup) create(ctx context.Context) error {
	timeLimitMinutes := controllers.PhysBackupTimeLimitDefault
	if b.backup.Spec.TimeLimitMinutes != 0 {
		timeLimitMinutes = time.Duration(b.backup.Spec.TimeLimitMinutes) * time.Minute
	}

	dop := int32(1)
	if b.backup.Spec.Dop != 0 {
		dop = b.backup.Spec.Dop
	}

	// the default is backupset true, not image copy
	backupset := pointer.Bool(true)
	if b.backup.Spec.Backupset != nil {
		backupset = b.backup.Spec.Backupset
	}

	ctxBackup, cancel := context.WithTimeout(ctx, timeLimitMinutes)
	defer cancel()

	caClient, closeConn, err := b.r.ClientFactory.New(ctxBackup, b.r, b.backup.Namespace, b.backup.Spec.Instance)
	if err != nil {
		b.log.Error(err, "failed to create config agent client")
		return err
	}
	defer closeConn()

	if _, err := caClient.PhysicalBackup(ctxBackup, &capb.PhysicalBackupRequest{
		BackupSubType: backupSubType(b.backup.Spec.Subtype),
		BackupItems:   b.backup.Spec.BackupItems,
		Backupset:     *backupset,
		CheckLogical:  b.backup.Spec.CheckLogical,
		Compressed:    b.backup.Spec.Compressed,
		Dop:           dop,
		Level:         b.backup.Spec.Level,
		Filesperset:   b.backup.Spec.Filesperset,
		SectionSize:   b.backup.SectionSize(),
		LocalPath:     b.backup.Spec.LocalPath,
		GcsPath:       b.backup.Spec.GcsPath,
		LroInput:      &capb.LROInput{OperationId: lroOperationID(b.backup)},
	}); err != nil && !controllers.IsAlreadyExistsError(err) {
		return fmt.Errorf("failed on PhysicalBackup gRPC call: %v", err)
	}
	return nil
}

func (b *physicalBackup) status(ctx context.Context) (done bool, err error) {
	id := lroOperationID(b.backup)
	operation, err := controllers.GetLROOperation(b.r.ClientFactory, ctx, b.r, b.backup.Namespace, id, b.backup.Spec.Instance)
	if err != nil {
		b.log.Error(err, "GetLROOperation error")
		return false, err
	}

	if operation.Done {
		b.log.Info("LRO is DONE", "id", id)
		if operation.GetError() != nil {
			err = errors.New(operation.GetError().GetMessage())
		}
		if err := controllers.DeleteLROOperation(b.r.ClientFactory, ctx, b.r, b.backup.Namespace, id, b.backup.Spec.Instance); err != nil {
			b.log.Error(err, "failed to delete a LRO ")
		}
		return true, err
	}
	b.log.Info("LRO is in progress", "id", id)
	return false, nil
}

func (b *physicalBackup) generateID() string {
	return fmt.Sprintf(backupName, b.backup.Spec.Instance, time.Now().Format("20060102"), "phys", time.Now().Nanosecond())
}
