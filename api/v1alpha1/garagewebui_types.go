package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GarageWebUISpec defines the desired state of GarageWebUI
type GarageWebUISpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas"`

	// Docker image for garage-webui
	// +optional
	Image string `json:"image,omitempty"`

	// Version of garage-webui to deploy (e.g. "v1.1.0")
	// +kubebuilder:default="1.1.0"
	Version string `json:"version"`

	// AutoUpdate configures automatic garage-webui version upgrades
	// +optional
	AutoUpdate AutoUpdateSpec `json:"autoUpdate,omitempty"`

	// GarageClusterRef references the GarageCluster this UI connects to
	GarageClusterRef GarageWebUIClusterRef `json:"garageClusterRef"`

	// Expose configures the OpenShift Route for the WebUI
	// +optional
	Expose WebUIExposeSpec `json:"expose,omitempty"`

	// Auth configures basic authentication for the WebUI
	// +optional
	Auth *WebUIAuthSpec `json:"auth,omitempty"`

	// Resources sets compute resources for the WebUI pods
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// GarageWebUIClusterRef identifies the GarageCluster to connect to
type GarageWebUIClusterRef struct {
	// Name of the GarageCluster in the same namespace
	Name string `json:"name"`
}

// WebUIExposeSpec configures external access to the WebUI
type WebUIExposeSpec struct {
	// Route configures an OpenShift Route for the WebUI
	// +optional
	Route RouteSpec `json:"route,omitempty"`
}

// WebUIAuthSpec configures basic auth for the WebUI
type WebUIAuthSpec struct {
	// SecretRef points to a Secret with a "credentials" key in the format "user:bcrypt_hash"
	SecretRef SecretKeyRef `json:"secretRef"`
}

// GarageWebUIStatus defines the observed state of GarageWebUI
type GarageWebUIStatus struct {
	// Phase is the lifecycle state: Provisioning, Ready, Degraded
	// +optional
	Phase string `json:"phase,omitempty"`

	// ReadyReplicas is the number of ready WebUI pods
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// CurrentVersion is the garage-webui version currently running
	// +optional
	CurrentVersion string `json:"currentVersion,omitempty"`

	// AvailableVersion is the latest garage-webui version detected upstream
	// +optional
	AvailableVersion string `json:"availableVersion,omitempty"`

	// LastUpdateCheck is the timestamp of the last upstream version check
	// +optional
	LastUpdateCheck *metav1.Time `json:"lastUpdateCheck,omitempty"`

	// URL is the external URL of the WebUI (from the Route)
	// +optional
	URL string `json:"url,omitempty"`

	// Conditions contains detailed status conditions
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.currentVersion`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GarageWebUI is the Schema for the garagewebuis API
type GarageWebUI struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GarageWebUISpec   `json:"spec,omitempty"`
	Status GarageWebUIStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GarageWebUIList contains a list of GarageWebUI
type GarageWebUIList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GarageWebUI `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GarageWebUI{}, &GarageWebUIList{})
}
