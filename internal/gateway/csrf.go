// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
)

const (
	csrfCookie = "webtop_csrf"
	csrfField  = "csrf"
)

func newCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func setCSRFCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	})
}

// checkCSRF implements the double-submit cookie plus same-origin check (docs/contract.md, 7.5).
// r.Form must be parsed.
func checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	field := r.PostForm.Get(csrfField)
	if subtle.ConstantTimeCompare([]byte(field), []byte(c.Value)) != 1 {
		return false
	}
	return sameOrigin(r)
}

func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == r.Host
}
