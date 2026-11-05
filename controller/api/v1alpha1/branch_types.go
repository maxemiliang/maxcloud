package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DatabaseBranchSpec: a copy of the app's database, as it is now or as it
// was at a point in time, restored from its backups into a Cluster of its
// own (db-<branch name>, connection details in db-<branch name>-app). The
// branch doesn't back itself up: it's for trying things (a migration, a
// preview deploy), then deleting.
type DatabaseBranchSpec struct {
	// Database to branch, in the same namespace.
	// +kubebuilder:default=db
	// +optional
	Database string `json:"database,omitempty"`

	// At: the point in time to branch from. Empty: the latest.
	// +optional
	At *metav1.Time `json:"at,omitempty"`

	// Size of the branch's disk (default: the database's).
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`
}

type BranchPhase string

const (
	BranchCreating BranchPhase = "Creating"
	BranchReady    BranchPhase = "Ready"
	BranchFailed   BranchPhase = "Failed"
)

type DatabaseBranchStatus struct {
	// Cluster is the branch's CloudNativePG Cluster; its connection details
	// are in the Secret <Cluster>-app (key uri).
	// +optional
	Cluster string `json:"cluster,omitempty"`

	// FromGeneration is the database's backup generation it was restored from.
	// +optional
	FromGeneration int64 `json:"fromGeneration,omitempty"`

	// +optional
	Phase BranchPhase `json:"phase,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready.
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

	// The spec can't change: a branch is restored once. Make a new one.
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
