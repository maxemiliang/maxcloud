package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReclaimPolicy is what happens to the storage in UpCloud when the object
// asking for it is deleted.
// +kubebuilder:validation:Enum=Retain;Delete
type ReclaimPolicy string

const (
	// Retain keeps the bucket and its objects (still billed). The default.
	Retain ReclaimPolicy = "Retain"
	// Delete deletes the bucket and everything in it.
	Delete ReclaimPolicy = "Delete"
)

// BucketSpec is what the app gets: a bucket named mc-<namespace>--<name> in
// the maxcloud-apps Object Storage, readable and writable with the app's
// credentials (the Secret maxcloud-s3 in its namespace).
type BucketSpec struct {
	// Public lets anyone read the bucket's objects without credentials
	// (s3:GetObject for everyone). Listing and writing still need the app's
	// credentials.
	// +optional
	Public bool `json:"public,omitempty"`

	// ReclaimPolicy: Retain (default) keeps the bucket and its objects when
	// this object is deleted; Delete deletes them.
	// +kubebuilder:default=Retain
	// +optional
	ReclaimPolicy ReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

// BucketStatus is what the controller last saw.
type BucketStatus struct {
	// BucketName is the bucket's name in Object Storage.
	// +optional
	BucketName string `json:"bucketName,omitempty"`

	// Endpoint is the S3 endpoint the bucket is reached at.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// URL a public bucket's objects are served at (behind the CDN), when
	// the cluster gives public buckets hostnames.
	// +optional
	URL string `json:"url,omitempty"`

	// SizeBytes and Objects are UpCloud's bucket metrics, refreshed every
	// few minutes.
	// +optional
	SizeBytes *int64 `json:"sizeBytes,omitempty"`
	// +optional
	Objects *int64 `json:"objects,omitempty"`

	// ObservedGeneration is the spec generation the conditions describe.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Bucket is an S3 bucket for an app on the run platform. The app registry
// creates these; apps can only read them.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=maxcloud
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.status.bucketName`
// +kubebuilder:printcolumn:name="Public",type=boolean,JSONPath=`.spec.public`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Size",type=integer,JSONPath=`.status.sizeBytes`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// The name becomes part of the bucket's: lowercase letters, digits and
// single hyphens (no "--", which separates app and bucket), and not "db",
// which is the app's database backups.
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([a-z0-9]|-[a-z0-9])*$')",message="name: lowercase letters, digits and single hyphens"
// +kubebuilder:validation:XValidation:rule="self.metadata.name != 'db'",message="name: db is reserved for the app's database backups"
type Bucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec BucketSpec `json:"spec,omitempty"`
	// +optional
	Status BucketStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketList is a list of Buckets.
type BucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bucket `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Bucket{}, &BucketList{})
}
