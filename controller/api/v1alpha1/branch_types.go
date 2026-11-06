package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DatabaseBranchSpec: a copy of the app's database, as it is now or as it
// was at a point in time, restored from its backups into a Cluster of its
// own (db-<branch name>, connection details in db-<branch name>-app) with
// one instance. The branch doesn't back itself up and never writes to the
// database's backups: it's for trying things (a migration, a preview
// deploy), then deleting. The spec can't change after it's created.
type DatabaseBranchSpec struct {
	// Database to branch, in the same namespace. It needs at least one
	// finished base backup.
	// +kubebuilder:default=db
	// +optional
	Database string `json:"database,omitempty"`

	// At: the point in time to branch from (RFC 3339). Empty: the latest.
	// It can't be before the end of the oldest finished base backup the
	// database still has.
	// +optional
	At *metav1.Time `json:"at,omitempty"`

	// Size of the branch's disk (default: the database's spec.size).
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`
}

// BranchPhase is where the branch is in its life.
type BranchPhase string

const (
	// BranchCreating: waiting for the database's backups, or restoring.
	BranchCreating BranchPhase = "Creating"
	// BranchReady: the branch's Cluster is ready.
	BranchReady BranchPhase = "Ready"
	// BranchFailed: the branch can't be made, or its Cluster was deleted.
	// The Ready condition says why. Only a missing Database is tried again
	// (every minute); otherwise delete the branch and make a new one.
	BranchFailed BranchPhase = "Failed"
)

// DatabaseBranchStatus is what the controller last saw.
type DatabaseBranchStatus struct {
	// Cluster is the branch's CloudNativePG Cluster; its connection details
	// are in the Secret <Cluster>-app (key uri).
	// +optional
	Cluster string `json:"cluster,omitempty"`

	// FromGeneration is the database's backup generation it was restored from.
	// +optional
	FromGeneration int64 `json:"fromGeneration,omitempty"`

	// Phase is where the branch is: Creating, Ready or Failed.
	// +optional
	Phase BranchPhase `json:"phase,omitempty"`

	// ObservedGeneration is the spec generation the conditions describe.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready. When the branch failed, its reason is one of
	// NoDatabase, NoBackupBefore, ClusterGone, Blocked (a Cluster of that
	// name that isn't the branch's) or NotATenant.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// DatabaseBranch is a throwaway copy of an app's database. Unlike Database
// and Bucket, apps make these themselves (mcloud db branch): it counts
// against the app's quota, and deleting it deletes the copy.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=maxcloud,shortName=dbbranch
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.status.cluster`
// +kubebuilder:printcolumn:name="At",type=date,JSONPath=`.spec.at`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([a-z0-9]|-[a-z0-9])*$') && self.metadata.name.size() <= 30",message="name: lowercase letters, digits and single hyphens, at most 30 characters"
type DatabaseBranch struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is what to copy: which database, from when, and how big a disk.
	// It can't change: a branch is restored once. Make a new one.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="a branch can't be changed; delete it and make a new one"
	// +optional
	Spec DatabaseBranchSpec `json:"spec,omitempty"`
	// +optional
	Status DatabaseBranchStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DatabaseBranchList is a list of DatabaseBranches.
type DatabaseBranchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DatabaseBranch `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DatabaseBranch{}, &DatabaseBranchList{})
}
