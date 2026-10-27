// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Package deps pins modules that are needed before the code importing them exists.
// Temporary: removed once the real code imports them.
package deps

import (
	_ "github.com/go-ldap/ldap/v3"
	_ "github.com/prometheus/client_golang/prometheus"
	_ "sigs.k8s.io/controller-runtime/pkg/envtest"
)
