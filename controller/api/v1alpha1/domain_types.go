package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DomainSpec puts one of the app's Knative services on a hostname of its
// own (the Domain's name), next to the <service>-<app> one Knative gives
// it. The service can be private: the hostname reaches its revisions
// directly, with the request's Host header as the client sent it.
type DomainSpec struct {
	// Service is the Knative service in this namespace (for a function,
	// the function's name). Until Knative has a route for it, the Domain
	// isn't Ready (ServiceNotFound).
	// +kubebuilder:validation:Pattern=`^[a-z]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Service string `json:"service"`

	// Proxied puts the hostname behind Cloudflare (its DNS record is
	// proxied, through external-dns's cloudflare-proxied annotation on the
	// route): WAF, caching, Cloudflare Access. Cloudflare's free edge
	// certificate only covers names one level below the zone.
	// +optional
	Proxied bool `json:"proxied,omitempty"`
}

// DomainStatus is what the controller last saw.
type DomainStatus struct {
	// URL the service is reached at on this hostname. Empty while the
	// Domain isn't Ready.
	// +optional
	URL string `json:"url,omitempty"`

	// Listener is the Gateway listener (and so the certificate) serving
	// the hostname: the HTTPS listener for exactly that name, else the
	// longest wildcard that covers it one level deep.
	// +optional
	Listener string `json:"listener,omitempty"`

	// Revisions the hostname currently sends requests to, as Knative's own
	// route for the service does.
	// +optional
	Revisions []string `json:"revisions,omitempty"`

	// ObservedGeneration is the spec generation the conditions describe.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready. When it's False, the hostname isn't routed, and
	// the reason is NotAllowed (not on the admin's allowlist for the app),
	// NoListener (no HTTPS listener covers it), ServiceNotFound (Knative
	// has no route for the service), HostInUse (another HTTPRoute on the
	// Gateway has the hostname; checked again every minute) or NotATenant.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Domain is a custom hostname for one of an app's Knative services. The
// app registry creates these from the app's entry (domains), within the
// hostnames an admin allowed the app; apps can only read them.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=maxcloud
// +kubebuilder:printcolumn:name="Service",type=string,JSONPath=`.spec.service`
// +kubebuilder:printcolumn:name="Proxied",type=boolean,JSONPath=`.spec.proxied`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// The name is the hostname: lowercase DNS labels, at least two of them.
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^([a-z0-9]([-a-z0-9]*[a-z0-9])?\\\\.)+[a-z]([-a-z0-9]*[a-z0-9])?$')",message="name: the hostname, e.g. shop.example.com"
type Domain struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec DomainSpec `json:"spec"`
	// +optional
	Status DomainStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DomainList is a list of Domains.
type DomainList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Domain `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Domain{}, &DomainList{})
}
