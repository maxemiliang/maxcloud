package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RecoverFromAnnotation on a Database tells a Blocked controller which
// backup generation to restore ("3"): it restores it into a new generation.
// It's read only while the database's Cluster is missing. The controller
// removes it once it has acted on it. An annotation, not a spec field,
// because the registry owns the spec.
const RecoverFromAnnotation = "maxcloud.gronblom.dev/recover-from"

// RestoreToAnnotation on a Database restores it, in place, to a point in
// time ("2026-11-05T10:00:00Z"): the controller takes the Cluster down and
// brings it back as it was then, as a new backup generation. Everything
// written after that time is gone from the database (still in the older
// generation's backups). The controller removes the annotation when the
// restore has started, or when it refuses the time (unreadable, in the
// future, or before the oldest base backup), which the Restore condition
// then explains.
const RestoreToAnnotation = "maxcloud.gronblom.dev/restore-to"

// DatabaseSpec is the app's Postgres database, run by CloudNativePG: a
// Cluster named like the Database (db), whose connection details are in
// the Secret db-app, backed up continuously to the app's bucket
// mc-<app>--db.
type DatabaseSpec struct {
	// Instances: 1, or 2 for a streaming replica (on another node when
	// CloudNativePG can place it there), so a node drain fails over instead
	// of taking the database down. With 2 the Cluster gets a
	// PodDisruptionBudget; with 1 it doesn't, so it never blocks a drain.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2
	// +kubebuilder:default=1
	// +optional
	Instances int32 `json:"instances,omitempty"`

	// Size of each instance's disk. It can grow (the disks are resized in
	// place), never shrink: a smaller value than the Cluster has is ignored.
	// +kubebuilder:default="1Gi"
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// Resources of each Postgres instance (default: 20m CPU and 128Mi
	// memory requested, 512Mi memory limit). Set, it replaces the default
	// as a whole; only requests and limits are used.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Backup settings: when base backups are taken and how long backups
	// are kept.
	// +optional
	Backup DatabaseBackup `json:"backup,omitempty"`

	// ReclaimPolicy of the backup bucket mc-<app>--db: Retain (default)
	// keeps the backups when this object is deleted; Delete deletes the
	// bucket and every backup in it.
	// +kubebuilder:default=Retain
	// +optional
	ReclaimPolicy ReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

// DatabaseBackup is how the database is backed up to its bucket.
type DatabaseBackup struct {
	// Schedule of the base backups, CloudNativePG's six-field cron
	// (seconds first); the default is daily at 05:00. WAL is archived
	// continuously in between, at least every 5 minutes. Each new backup
	// generation also gets a base backup of its own as soon as its Cluster
	// is ready.
	// +kubebuilder:default="0 0 5 * * *"
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Retention of backups and WAL: a number of days, weeks or months
	// (7d, 2w, 1m). It's how far back a restore can go.
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*[dwm]$`
	// +kubebuilder:default="7d"
	// +optional
	Retention string `json:"retention,omitempty"`
}

// DatabasePhase is where the database is in its life.
type DatabasePhase string

const (
	// Creating: a new database (initdb), or a Cluster waiting to be ready
	// or for its generation's first base backup.
	PhaseCreating DatabasePhase = "Creating"
	// Restoring: the Cluster is being restored from a backup generation.
	PhaseRestoring DatabasePhase = "Restoring"
	// Ready: the Cluster is ready and its generation has a base backup.
	PhaseReady DatabasePhase = "Ready"
	// Blocked: the controller won't guess (backups it can't account for,
	// or a generation without a base backup). See the Blocked condition,
	// and RecoverFromAnnotation.
	PhaseBlocked DatabasePhase = "Blocked"
)

// DatabaseStatus is what the controller last saw.
type DatabaseStatus struct {
	// Bucket holding the backups (mc-<app>--db).
	// +optional
	Bucket string `json:"bucket,omitempty"`

	// Generation of the backups the database writes now: they go to the
	// folder db-<generation> in the bucket. A restore starts a new one, so
	// older backups are never written over. The bucket's marker
	// (maxcloud.json) is the source of truth; this is a copy.
	// +optional
	Generation int64 `json:"generation,omitempty"`

	// RestoredFrom is the generation the current one was restored from;
	// unset for a database that started empty.
	// +optional
	RestoredFrom int64 `json:"restoredFrom,omitempty"`

	// RestoredTo is the point in time the current generation was restored
	// to, if it was (restore-to).
	// +optional
	RestoredTo *metav1.Time `json:"restoredTo,omitempty"`

	// Phase is where the database is: Creating, Restoring, Ready or
	// Blocked.
	// +optional
	Phase DatabasePhase `json:"phase,omitempty"`

	// ObservedGeneration is the spec generation the conditions describe.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready; Blocked (True while the controller won't act
	// until a person decides, with why in its message); Restore (the last
	// restore: True with reason Restored, or False with reason Refused
	// when a restore-to time couldn't be used).
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
