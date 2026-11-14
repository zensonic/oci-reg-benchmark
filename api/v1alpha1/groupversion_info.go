// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package v1alpha1 contains the ORB custom resource types for the API group
// orb.kubectl.dk (ARCH-2, ARCH-3).
//
// +kubebuilder:object:generate=true
// +groupName=orb.kubectl.dk
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the API group and version of the ORB resources.
	GroupVersion = schema.GroupVersion{Group: "orb.kubectl.dk", Version: "v1alpha1"}

	// SchemeBuilder registers the ORB types with a runtime scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the ORB types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// addKnownTypes registers every ORB resource type and its list type.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&OrbBenchmark{}, &OrbBenchmarkList{},
		&OrbBenchmarkRun{}, &OrbBenchmarkRunList{},
		&OrbBenchmarkResult{}, &OrbBenchmarkResultList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
