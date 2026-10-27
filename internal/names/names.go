// SPDX-License-Identifier: Apache-2.0

// Package names holds the naming, label and annotation rules of docs/contract.md.
package names

import (
	"errors"
	"strings"
)

const (
	Domain = "webtop.lhns.github.io"

	LabelDesktop   = Domain + "/desktop"
	LabelName      = "app.kubernetes.io/name"
	LabelInstance  = "app.kubernetes.io/instance"
	LabelManagedBy = "app.kubernetes.io/managed-by"

	AnnotationUser         = Domain + "/user"
	AnnotationTemplateHash = Domain + "/template-hash"
	AnnotationRestartedAt  = Domain + "/restarted-at"

	AppName   = "webtop-desktop"
	ManagedBy = "kube-webtop-workspaces"

	ContainerName = "desktop"
	MaxNameLength = 40
	MaxPrefixLen  = 22

	TokenDir  = "/run/webtop/sa"
	TokenFile = "token"
	TokenPath = TokenDir + "/" + TokenFile
)

// ErrEmpty is returned when a user ID sanitises to nothing.
var ErrEmpty = errors.New("user ID has no usable characters")

// Sanitize maps a user ID to a Desktop name: lowercase [a-z0-9-], no leading,
// trailing or repeated '-', at most 40 characters.
func Sanitize(user string) (string, error) {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(user) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > MaxNameLength {
		s = strings.TrimRight(s[:MaxNameLength], "-")
	}
	if s == "" {
		return "", ErrEmpty
	}
	return s, nil
}

// Labels returns the labels of every object belonging to a desktop.
func Labels(desktop string) map[string]string {
	return map[string]string{
		LabelDesktop:   desktop,
		LabelName:      AppName,
		LabelInstance:  desktop,
		LabelManagedBy: ManagedBy,
	}
}

// Selector returns the labels the Service and Deployment select on.
func Selector(desktop string) map[string]string {
	return map[string]string{LabelDesktop: desktop, LabelManagedBy: ManagedBy}
}

// Object returns the name of the Deployment, Service and PVC of a desktop.
func Object(prefix, desktop string) string { return prefix + desktop }
