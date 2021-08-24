package utils

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

func TestFindDiskSize(t *testing.T) {
	type args struct {
		diskSpec   *commonv1alpha1.DiskSpec
		configSpec *commonv1alpha1.ConfigSpec
	}

	defaultDiskSpecs := map[string]commonv1alpha1.DiskSpec{
		"DataDisk": {
			Name: "DataDisk",
			Size: resource.MustParse("100Gi"),
		},
		"LogDisk": {
			Name: "LogDisk",
			Size: resource.MustParse("150Gi"),
		},
		"BackupDisk": {
			Name: "BackupDisk",
			Size: resource.MustParse("100Gi"),
		},
	}

	defaultDiskSize := resource.MustParse("100Gi")

	tests := []struct {
		name string
		args args
		want resource.Quantity
	}{
		{
			name: "default disk size for non-existing disk name",
			args: args{
				&commonv1alpha1.DiskSpec{Name: "dummyDisk"},
				nil,
			},
			want: defaultDiskSize,
		},
		{
			name: "disk size overridden by instance level disk spec",
			args: args{
				diskSpec: &commonv1alpha1.DiskSpec{Name: "DataDisk", Size: resource.MustParse("100Gi")},
				configSpec: &commonv1alpha1.ConfigSpec{Disks: []commonv1alpha1.DiskSpec{
					{
						Name: "DataDisk", Size: resource.MustParse("50Gi"),
					},
				}},
			},
			want: resource.MustParse("100Gi"),
		},
		{
			name: "disk size overridden by disk spec in global config",
			args: args{
				diskSpec: &commonv1alpha1.DiskSpec{Name: "DataDisk"},
				configSpec: &commonv1alpha1.ConfigSpec{Disks: []commonv1alpha1.DiskSpec{
					{
						Name: "DataDisk", Size: resource.MustParse("50Gi"),
					},
				}},
			},
			want: resource.MustParse("50Gi"),
		},
		{
			name: "disk size set by default config",
			args: args{
				diskSpec: &commonv1alpha1.DiskSpec{Name: "DataDisk"},
				configSpec: &commonv1alpha1.ConfigSpec{Disks: []commonv1alpha1.DiskSpec{
					{
						Name: "LogDisk", Size: resource.MustParse("50Gi"),
					},
				}},
			},
			want: defaultDiskSpecs["DataDisk"].Size,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FindDiskSize(tt.args.diskSpec, tt.args.configSpec, defaultDiskSpecs, defaultDiskSize); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FindDiskSize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindStorageClassName(t *testing.T) {
	type args struct {
		diskSpec        *commonv1alpha1.DiskSpec
		configSpec      *commonv1alpha1.ConfigSpec
		defaultPlatform string
		engineType      string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "storage class name overridden by instance level disk spec",
			args: args{
				diskSpec:        &commonv1alpha1.DiskSpec{StorageClass: "instance-level-storage-class"},
				configSpec:      nil,
				defaultPlatform: PlatformGCP,
				engineType:      EngineOracle,
			},
			want:    "instance-level-storage-class",
			wantErr: false,
		},
		{
			name: "storage class name overridden by disk spec in global config",
			args: args{
				diskSpec: &commonv1alpha1.DiskSpec{Name: "DataDisk"},
				configSpec: &commonv1alpha1.ConfigSpec{Disks: []commonv1alpha1.DiskSpec{
					{
						Name:         "DataDisk",
						StorageClass: "config-level-storage-class-in-disk-spec",
					},
				}},
				defaultPlatform: PlatformGCP,
				engineType:      EngineOracle,
			},
			want:    "config-level-storage-class-in-disk-spec",
			wantErr: false,
		},
		{
			name: "storage class name overridden by storage class in global config",
			args: args{
				diskSpec:        &commonv1alpha1.DiskSpec{Name: "DataDisk"},
				configSpec:      &commonv1alpha1.ConfigSpec{StorageClass: "config-level-storage-class"},
				defaultPlatform: PlatformGCP,
				engineType:      EngineOracle,
			},
			want:    "config-level-storage-class",
			wantErr: false,
		},
		{
			name: "storage class name set by default platform config",
			args: args{
				diskSpec:        &commonv1alpha1.DiskSpec{Name: "DataDisk"},
				configSpec:      nil,
				defaultPlatform: PlatformGCP,
				engineType:      EngineOracle,
			},
			want:    defaultStorageClassNameGCP,
			wantErr: false,
		},
		{
			name: "empty storage class name for postgres without platform specified",
			args: args{
				diskSpec:        &commonv1alpha1.DiskSpec{Name: "DataDisk"},
				configSpec:      nil,
				defaultPlatform: "",
				engineType:      EnginePostgres,
			},
			want:    "",
			wantErr: false,
		},
		{
			name: "unsupported platform",
			args: args{
				diskSpec:        &commonv1alpha1.DiskSpec{Name: "DataDisk"},
				configSpec:      nil,
				defaultPlatform: "dummy platform",
			},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FindStorageClassName(tt.args.diskSpec, tt.args.configSpec, tt.args.defaultPlatform, tt.args.engineType)
			if (err != nil) != tt.wantErr {
				t.Errorf("FindStorageClassName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("FindStorageClassName() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindVolumeSnapshotClassName(t *testing.T) {
	type args struct {
		vcs             string
		configSpec      *commonv1alpha1.ConfigSpec
		defaultPlatform string
		engineType      string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "volume snapshot class name overridden by instance-level config",
			args: args{
				vcs: "instance-level-volume-snapshot-class",
				configSpec: &commonv1alpha1.ConfigSpec{
					VolumeSnapshotClass: "config-level-volume-snapshot-class",
				},
				defaultPlatform: PlatformGCP,
				engineType:      EngineOracle,
			},
			want:    "instance-level-volume-snapshot-class",
			wantErr: false,
		},

		{
			name: "volume snapshot class name overridden by global config",
			args: args{
				configSpec: &commonv1alpha1.ConfigSpec{
					VolumeSnapshotClass: "config-level-volume-snapshot-class",
				},
				defaultPlatform: PlatformGCP,
				engineType:      EngineOracle,
			},
			want:    "config-level-volume-snapshot-class",
			wantErr: false,
		},
		{
			name:    "volume snapshot class name set by default platform config",
			args:    args{defaultPlatform: PlatformGCP},
			want:    defaultVolumeSnapshotClassNameGCP,
			wantErr: false,
		},
		{
			name:    "empty volume snapshot class for postgres without platform specified",
			args:    args{defaultPlatform: "", engineType: EnginePostgres},
			want:    "",
			wantErr: false,
		},
		{
			name: "unsupported platform",
			args: args{
				defaultPlatform: "dummy platform",
			},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FindVolumeSnapshotClassName(tt.args.vcs, tt.args.configSpec, tt.args.defaultPlatform, tt.args.engineType)
			if (err != nil) != tt.wantErr {
				t.Errorf("FindVolumeSnapshotClassName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("FindVolumeSnapshotClassName() got = %v, want %v", got, tt.want)
			}
		})
	}
}
