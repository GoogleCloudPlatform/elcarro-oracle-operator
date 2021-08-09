package v1alpha1

//+kubebuilder:object:generate=true

// ConfigSpec defines the desired state of Config.
type ConfigSpec struct {
	// Service agent and other data plane agent images.
	// This is an optional map that allows a customer to specify agent images
	// different from those chosen/provided by the operator by default.
	// +optional
	Images map[string]string `json:"images,omitempty"`

	// Deployment platform.
	// Presently supported values are: GCP (default), BareMetal, Minikube and Kind.
	// +optional
	// +kubebuilder:validation:Enum=GCP;BareMetal;Minikube;Kind
	Platform string `json:"platform,omitempty"`

	// Disks slice describes at minimum two disks:
	// data and log (archive log), and optionally a backup disk.
	Disks []DiskSpec `json:"disks,omitempty"`

	// Storage class to use for dynamic provisioning.
	// This value varies depending on a platform.
	// For GCP (and the default) it is "csi-gce-pd".
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// Volume Snapshot class to use for storage snapshots.
	// This value varies depending on a platform.
	// For GCP (and the default) it is "csi-gce-pd-snapshot-class".
	// +optional
	VolumeSnapshotClass string `json:"volumeSnapshotClass,omitempty"`

	// Log Levels for the various components.
	// This is an optional map for component -> log level
	// +optional
	LogLevel map[string]string `json:"logLevel,omitempty"`

	// HostAntiAffinityNamespaces is an optional list of namespaces that need
	// to be included in anti-affinity by hostname rule. The effect of the rule
	// is forbidding scheduling a database pod in the current namespace on a host
	// that already runs a database pod in any of the listed namespaces.
	// +optional
	HostAntiAffinityNamespaces []string `json:"hostAntiAffinityNamespaces,omitempty"`
}
