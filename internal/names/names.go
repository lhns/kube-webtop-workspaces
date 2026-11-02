// SPDX-License-Identifier: Apache-2.0

// Package names holds the naming, label and annotation rules of docs/contract.md.
package names

import (
	"errors"
	"strings"
	"unicode"
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

	// AgentPort is where the in-pod agent serves the gateway (docs/contract.md 7.7). Also apps.Port in
	// the agent module and $agentPort in the chart's networkpolicy.yaml: keep the three equal.
	AgentPort = 8081

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

// ValidLogin reports whether a desktop name is also a valid login name for the desktop user:
// it must start with a letter (docs/contract.md, section 4). The image additionally refuses
// names of its own accounts.
func ValidLogin(name string) bool {
	if name == "" || len(name) > MaxNameLength || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// MaxDisplayName is the longest display name passed to the desktop, in characters.
const MaxDisplayName = 128

// DisplayName makes an LDAP display name safe for the passwd GECOS field and the pod's env file:
// no control characters, field separators (':' ',') or quotes and backslashes, trimmed, at most
// MaxDisplayName characters.
func DisplayName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) || strings.ContainsRune(":,\"'\\", r) || r == unicode.ReplacementChar {
			continue
		}
		if n == MaxDisplayName {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
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
