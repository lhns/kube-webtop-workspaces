// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"net/http"
	"strings"
)

// IsNavigation reports whether r is a top-level page load (docs/contract.md, 7.4).
func IsNavigation(r *http.Request) bool {
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		dest := r.Header.Get("Sec-Fetch-Dest")
		return mode == "navigate" && (dest == "" || dest == "document")
	}
	return r.Method == http.MethodGet &&
		strings.Contains(r.Header.Get("Accept"), "text/html") &&
		r.Header.Get("Upgrade") == ""
}
