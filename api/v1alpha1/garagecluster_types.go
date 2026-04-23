package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GarageClusterSpec defines the desired state of GarageCluster
type GarageClusterSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	Replicas int32 `json:"replicas"`

	// Docker image for Garage. Defaults to dxflrs/garage:<version>
	// +optional
	Image string `json:"image,omitempty"`

	// Version of Garage to deploy (e.g. "v1.0.1")
	// +kubebuilder:default="v1.0.1"
	Version string `json:"version"`

	// AutoUpdate configures automatic Garage version upgrades
	// +optional
	AutoUpdate AutoUpdateSpec `json:"autoUpdate,omitempty"`

	// Storage configures persistent volumes for metadata and data
	Storage StorageSpec `json:"storage"`

	// Config contains Garage-specific configuration parameters
	// +optional
	Config GarageConfigSpec `json:"config,omitempty"`

	// Expose configures OpenShift Routes for external access
	// +optional
	Expose ExposeSpec `json:"expose,omitempty"`

	// RPCSecretRef points to a Secret containing the RPC secret key.
	// If not set, the operator generates a random secret.
	// +optional
	RPCSecretRef *SecretKeyRef `json:"rpcSecretRef,omitempty"`

	// AdminTokenRef points to a Secret containing the admin API token.
	// If not set, the operator generates a random token.
	// +optional
	AdminTokenRef *SecretKeyRef `json:"adminTokenRef,omitempty"`

	// Resources sets compute resources for Garage pods
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector constrains Garage pods to nodes matching labels
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for Garage pods
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// AutoUpdateSpec configures automatic version upgrades for Garage
type AutoUpdateSpec struct {
	// Enabled activates periodic version checks and automatic upgrades
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// Schedule is a cron expression for how often to check for updates (e.g. "0 2 * * *")
	// +kubebuilder:default="0 2 * * *"
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// AllowPreRelease allows upgrading to pre-release (rc, beta, alpha) versions
	// +kubebuilder:default=false
	// +optional
	AllowPreRelease bool `json:"allowPreRelease,omitempty"`
}

// StorageSpec defines PVC configuration for Garage
type StorageSpec struct {
	// MetaStorageSize is the size of the PVC for Garage metadata (recommended: fast SSD)
	// +kubebuilder:default="3Gi"
	MetaStorageSize resource.Quantity `json:"metaStorageSize"`

	// DataStorageSize is the size of the PVC for Garage object data
	// +kubebuilder:default="30Gi"
	DataStorageSize resource.Quantity `json:"dataStorageSize"`

	// StorageClassName for PVCs. Uses the cluster default if unset.
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`
}

// GarageConfigSpec maps to Garage configuration parameters
type GarageConfigSpec struct {
	// S3Region returned in S3 API responses
	// +kubebuilder:default="garage"
	// +optional
	S3Region string `json:"s3Region,omitempty"`

	// S3RootDomain is the root domain suffix for vhost-style bucket access
	// +optional
	S3RootDomain string `json:"s3RootDomain,omitempty"`

	// WebRootDomain is the root domain for static website hosting
	// +optional
	WebRootDomain string `json:"webRootDomain,omitempty"`

	// DBEngine selects the metadata database backend: "lmdb" (default) or "sqlite"
	// +kubebuilder:validation:Enum=lmdb;sqlite
	// +kubebuilder:default="lmdb"
	// +optional
	DBEngine string `json:"dbEngine,omitempty"`

	// ReplicationFactor sets how many copies of each data block are stored.
	// Must be <= Replicas. 1 = no redundancy, 3 = tolerate 1 node failure.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=7
	// +kubebuilder:default=1
	// +optional
	ReplicationFactor int `json:"replicationFactor,omitempty"`

	// ConsistencyMode controls read/write quorum behaviour
	// +kubebuilder:validation:Enum=consistent;degraded;dangerous
	// +kubebuilder:default="consistent"
	// +optional
	ConsistencyMode string `json:"consistencyMode,omitempty"`

	// BlockSize is the chunk size for object storage (e.g. "1MiB", "10MiB")
	// +optional
	BlockSize string `json:"blockSize,omitempty"`

	// CompressionLevel sets the zstd compression level (-99 to 22). 0 = use default (1).
	// +optional
	CompressionLevel int `json:"compressionLevel,omitempty"`

	// Zone is the datacenter/zone label assigned to all nodes in this cluster
	// +kubebuilder:default="dc1"
	// +optional
	Zone string `json:"zone,omitempty"`
}

// ExposeSpec configures OpenShift Routes for Garage services
type ExposeSpec struct {
	// S3APIRoute exposes the S3 API endpoint
	// +optional
	S3APIRoute RouteSpec `json:"s3APIRoute,omitempty"`

	// WebRoute exposes Garage's static website hosting endpoint
	// +optional
	WebRoute RouteSpec `json:"webRoute,omitempty"`

	// AdminRoute exposes the Garage admin API (use with caution)
	// +optional
	AdminRoute RouteSpec `json:"adminRoute,omitempty"`
}

// RouteSpec defines an OpenShift Route
type RouteSpec struct {
	// Enabled creates the Route when true
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// Hostname for the Route. Auto-generated by OpenShift if empty.
	// +optional
	Hostname string `json:"hostname,omitempty"`

	// TLSTermination strategy: "edge", "passthrough", or "reencrypt"
	// +kubebuilder:validation:Enum=edge;passthrough;reencrypt
	// +kubebuilder:default="edge"
	// +optional
	TLSTermination string `json:"tlsTermination,omitempty"`
}

// SecretKeyRef points to a key inside a Kubernetes Secret
type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// GarageClusterStatus defines the observed state of GarageCluster
type GarageClusterStatus struct {
	// Phase is the high-level lifecycle state: Provisioning, Ready, Degraded, Updating
	// +optional
	Phase string `json:"phase,omitempty"`

	// ReadyReplicas is the number of Garage pods that are ready
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// CurrentVersion is the Garage version currently running
	// +optional
	CurrentVersion string `json:"currentVersion,omitempty"`

	// AvailableVersion is the latest Garage version detected upstream (auto-update)
	// +optional
	AvailableVersion string `json:"availableVersion,omitempty"`

	// LastUpdateCheck is the timestamp of the last upstream version check
	// +optional
	LastUpdateCheck *metav1.Time `json:"lastUpdateCheck,omitempty"`

	// LayoutApplied is true once the Garage cluster layout has been initialised
	// +optional
	LayoutApplied bool `json:"layoutApplied,omitempty"`

	// S3Endpoint is the in-cluster S3 API endpoint
	// +optional
	S3Endpoint string `json:"s3Endpoint,omitempty"`

	// AdminEndpoint is the in-cluster Admin API endpoint
	// +optional
	AdminEndpoint string `json:"adminEndpoint,omitempty"`

	// Conditions contains detailed status conditions
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.currentVersion`
// +kubebuilder:printcolumn:name="Layout",type=boolean,JSONPath=`.status.layoutApplied`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GarageCluster is the Schema for the garageclusters API
type GarageCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GarageClusterSpec   `json:"spec,omitempty"`
	Status GarageClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GarageClusterList contains a list of GarageCluster
type GarageClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GarageCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GarageCluster{}, &GarageClusterList{})
}
