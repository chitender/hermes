package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ─── Spec sub-types ──────────────────────────────────────────────────────────

// VaultPath identifies one KV-v2 secret path and an optional key prefix.
// When prefix is set, every key read from this path is prefixed before being
// merged into the template data map. Last-path-wins for conflicting keys.
type VaultPath struct {
	// Path is the full Vault KV v2 path, e.g. "secret/data/myapp/db".
	// +kubebuilder:validation:Required
	Path string `json:"path"`

	// Prefix is prepended to every key read from this path before merging.
	// E.g. prefix "PG_" turns key "PASSWORD" into "PG_PASSWORD".
	// +optional
	Prefix string `json:"prefix,omitempty"`
}

// VaultRef points to the Kubernetes Secret that holds Vault credentials.
type VaultRef struct {
	// SecretRef is "namespace/name" of a K8s Secret containing vault auth fields.
	// Supported fields: authMethod, vaultAddress, token (token auth);
	//   username, password, mountPath (userpass); vaultRole, mountPath (kubernetes).
	// +kubebuilder:validation:Required
	SecretRef string `json:"secretRef"`
}

// EtcdRef holds etcd connection information.
type EtcdRef struct {
	// SecretRef is "namespace/name" of a K8s Secret with fields: username, password.
	// +kubebuilder:validation:Required
	SecretRef string `json:"secretRef"`

	// Endpoints is the list of etcd client endpoints.
	// +kubebuilder:validation:MinItems=1
	Endpoints []string `json:"endpoints"`

	// TLSSecretRef is "namespace/name" of a K8s Secret containing:
	//   ca.crt, tls.crt, tls.key  — used for mutual TLS with etcd.
	// +optional
	TLSSecretRef string `json:"tlsSecretRef,omitempty"`
}

// ─── Main Spec / Status ───────────────────────────────────────────────────────

// VaultEtcdSyncSpec defines the desired state of VaultEtcdSync.
type VaultEtcdSyncSpec struct {
	// VaultPaths lists the Vault KV v2 paths to read. All secret data maps are
	// merged into a single flat map used for template rendering (last path wins
	// on key collisions; prefix is applied per-path before merging).
	// +kubebuilder:validation:MinItems=1
	VaultPaths []VaultPath `json:"vaultPaths"`

	// EtcdKey is the key under which the rendered JSON is written in etcd.
	// +kubebuilder:validation:Required
	EtcdKey string `json:"etcdKey"`

	// JSONTemplate is a Go text/template string that is rendered against the
	// merged Vault secret map. The output must be valid JSON.
	// +kubebuilder:validation:Required
	JSONTemplate string `json:"jsonTemplate"`

	// SyncInterval is the period between periodic re-syncs (e.g. "60s", "5m").
	// The operator also re-syncs immediately on Vault secret version changes.
	// +kubebuilder:default="60s"
	SyncInterval string `json:"syncInterval"`

	// VaultRef references the K8s Secret that holds Vault authentication config.
	// +kubebuilder:validation:Required
	VaultRef VaultRef `json:"vaultRef"`

	// EtcdRef holds etcd endpoint and credential information.
	// +kubebuilder:validation:Required
	EtcdRef EtcdRef `json:"etcdRef"`
}

// VaultEtcdSyncStatus defines the observed state of VaultEtcdSync.
type VaultEtcdSyncStatus struct {
	// LastSyncTime is the RFC3339 timestamp of the most recent sync attempt.
	// +optional
	LastSyncTime string `json:"lastSyncTime,omitempty"`

	// LastSyncStatus is "Success" or "Failed".
	// +optional
	LastSyncStatus string `json:"lastSyncStatus,omitempty"`

	// Conditions holds standard K8s conditions.
	// Supported types: Ready.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// ─── Root object ─────────────────────────────────────────────────────────────

// VaultEtcdSync is the Schema for the vaultedcsyncs API.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ves,categories=all
// +kubebuilder:printcolumn:name="EtcdKey",type=string,JSONPath=`.spec.etcdKey`
// +kubebuilder:printcolumn:name="Interval",type=string,JSONPath=`.spec.syncInterval`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.lastSyncStatus`
// +kubebuilder:printcolumn:name="LastSync",type=string,JSONPath=`.status.lastSyncTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type VaultEtcdSync struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VaultEtcdSyncSpec   `json:"spec,omitempty"`
	Status VaultEtcdSyncStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VaultEtcdSyncList contains a list of VaultEtcdSync.
type VaultEtcdSyncList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VaultEtcdSync `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VaultEtcdSync{}, &VaultEtcdSyncList{})
}
