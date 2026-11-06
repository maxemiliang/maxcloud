// Package v1alpha1 is the maxcloud controller's API: what the app registry
// asks for on an app's behalf (Buckets, Databases, Domains), and the
// DatabaseBranches apps make themselves.
//
// +kubebuilder:object:generate=true
// +groupName=maxcloud.gronblom.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "maxcloud.gronblom.dev", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)
