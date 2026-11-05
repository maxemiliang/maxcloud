package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RecoverFromAnnotation on a Database tells a Blocked controller which
// backup generation to restore ("3"). The controller removes it once it
// has acted on it. An annotation, not a spec field, because the registry
// owns the spec.
const RecoverFromAnnotation = "maxcloud.gronblom.dev/recover-from"

// DatabaseSpec is the app's Postgres database, run by CloudNativePG: a
// Cluster named like the Database (db), whose connection details are in
// the Secret db-app, backed up continuously to the app's bucket
// mc-<app>--db.
type DatabaseSpec struct {
	// Instances: 1, or 2 for a streaming replica on the other node, so a
	// node drain fails over instead of taking the database down.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2
	// +kubebuilder:default=1
	// +optional
	Instances int32 `json:"instances,omitempty"`

	// Size of each instance's disk. It can grow, never shrink.
	// +kubebuilder:default="1Gi"
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// Resources of each Postgres instance (default: 20m CPU and 128Mi
	// requested, 512Mi limit).
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Backup settings.
	// +optional
	Backup DatabaseBackup `json:"backup,omitempty"`

	// ReclaimPolicy of the backup bucket: Retain (default) keeps the
	// backups when this object is deleted; Delete deletes them.
	// +kubebuilder:default=Retain
	// +optional
	ReclaimPolicy ReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

type DatabaseBackup struct {
	// Schedule of the base backups, CloudNativePG's six-field cron
	// (seconds first). WAL is archived continuously in between.
	// +kubebuilder:default="0 0 5 * * *"
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Retention of backups and WAL, e.g. 7d: how far back a restore can go.
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*[dwm]$`
	// +kubebuilder:default="7d"
	// +optional
	Retention string `json:"retention,omitempty"`
}

// DatabasePhase is where the database is in its life.
type DatabasePhase string

const (
	PhaseCreating  DatabasePhase = "Creating"
	PhaseRestoring DatabasePhase = "Restoring"
	PhaseReady     DatabasePhase = "Ready"
	// Blocked: the controller won't guess (backups it can't account for,
	// or a generation without a base backup). See the Blocked condition,
	// and RecoverFromAnnotation.
	PhaseBlocked DatabasePhase = "Blocked"
)

type DatabaseStatus struct {
	// Bucket holding the backups.
	// +optional
	Bucket string `json:"bucket,omitempty"`

	// Generation of the backups the database writes now: they go to the
	// folder db-<generation> in the bucket. A restore starts a new one, so
	// older backups are never written over. The bucket's marker
	// (maxcloud.json) is the source of truth; this is a copy.
	// +optional
	Generation int64 `json:"generation,omitempty"`

	// RestoredFrom is the generation the current one was restored from.
	// +optional
	RestoredFrom int64 `json:"restoredFrom,omitempty"`

	// +optional
	Phase DatabasePhase `json:"phase,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready, Blocked.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Database is a Postgres database for an app on the run platform. The app
// registry creates it; apps can only read it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=maxcloud
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Generation",type=integer,JSONPath=`.status.generation`
// +kubebuilder:printcolumn:name="Instances",type=integer,JSONPath=`.spec.instances`
// +kubebuilder:printcolumn:name="Size",type=string,JSONPath=`.spec.size`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// One database per app for now: its Cluster is db, its backups mc-<app>--db.
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'db'",message="name: must be db (one database per app)"
type Database struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec DatabaseSpec `json:"spec,omitempty"`
	// +optional
	Status DatabaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DatabaseList is a list of Databases.
type DatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Database `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Database{}, &DatabaseList{})
}
