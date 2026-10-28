// SPDX-License-Identifier: Apache-2.0

// Package v1alpha1 contains the Desktop API.
// +kubebuilder:object:generate=true
// +groupName=webtop.lhns.github.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version of this package.
	GroupVersion = schema.GroupVersion{Group: "webtop.lhns.github.io", Version: "v1alpha1"}

	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	AddToScheme = SchemeBuilder.AddToScheme
)
