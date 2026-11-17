package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PreviewSpec: a short-lived copy of the app, deployed in an environment of
// its own (namespace <app>--<preview name>) at its own URLs
// (<service>--<app>--<preview name>.<domain>), with an empty database (if
// the app's maxcloud.yaml asks for one) and empty buckets, both deleted with
// it. Never the app's secrets, data or images' build cache. The app's
// previews: in maxcloud.yaml turns previews on; without it, a Preview
// waits.
type PreviewSpec struct {
	// PullRequest is the number of the GitHub pull request this previews.
	// The controller makes these by itself, one per open pull request of a
	// GitOps app labelled `preview`, and deletes them when the pull request
	// closes or loses the label. Flux deploys the branch
	// maxcloud/preview/<number>, which the app's deploy workflow writes for
	// such a pull request. 0: a preview of a working tree, which mcloud
	// preview deploy builds and applies.
	// +kubebuilder:validation:Minimum=0
	// +optional
	PullRequest int64 `json:"pullRequest,omitempty"`

	// Revision is the pull request's head commit the controller last saw.
	// A new one extends the preview by the app's previews.ttl.
	// +optional
	Revision string `json:"revision,omitempty"`

	// ExpiresAt is when the preview is removed. mcloud preview deploy sets
	// it (now plus the app's previews.ttl) and every deploy moves it on;
	// for a pull request, the controller does on every push. An expired
	// working-tree preview is deleted; an expired pull request's stays,
	// Expired, until its next push.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// PreviewPhase is where a preview is in its life.
type PreviewPhase string

const (
	// PreviewActive: its environment's registry entry exists.
	PreviewActive PreviewPhase = "Active"
	// PreviewWaiting: it can't have an environment now: previews are off
	// for the app, the app already has as many as it may, or the platform
	// has. The Ready condition says which.
	PreviewWaiting PreviewPhase = "Waiting"
	// PreviewExpired: past expiresAt; its environment is gone.
	PreviewExpired PreviewPhase = "Expired"
)

// PreviewStatus is what the controller last saw.
type PreviewStatus struct {
	// Environment is the preview's registry entry and namespace,
	// <app>--<preview name>.
	// +optional
	Environment string `json:"environment,omitempty"`

	// Phase: Active, Waiting or Expired.
	// +optional
	Phase PreviewPhase `json:"phase,omitempty"`

	// ObservedGeneration is the spec generation the conditions describe.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready, with the reason Active, PreviewsOff, OverLimit,
	// NameTaken or Expired.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Preview is a short-lived deploy of an app (a pull request, or a
// developer's working tree) in an environment of its own. It lives in the
// app's own namespace; the controller turns it into a registry entry
// <app>--<name> (the environment), and removes that when the preview
// expires or is deleted. Only the app's owners may make one (mcloud
// preview deploy); the controller makes pull requests' itself.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=maxcloud
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Environment",type=string,JSONPath=`.status.environment`
// +kubebuilder:printcolumn:name="PR",type=integer,JSONPath=`.spec.pullRequest`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.spec.expiresAt`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z]([a-z0-9]|-[a-z0-9])*$') && self.metadata.name.size() <= 12",message="name: lowercase letters, digits and single hyphens, starting with a letter, at most 12 characters"
type Preview struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec PreviewSpec `json:"spec,omitempty"`
	// +optional
	Status PreviewStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PreviewList is a list of Previews.
type PreviewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Preview `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Preview{}, &PreviewList{})
}
