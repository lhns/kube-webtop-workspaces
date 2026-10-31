// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bytes"
	_ "embed"
	"html"
	"io"
	"mime"
	"net/http"
	"strconv"
)

// maxInjectBody bounds how much of an HTML response is buffered; a bigger one is passed on as is.
const maxInjectBody = 4 << 20

// reloadScript runs in the desktop's own page. It watches the status stream and reloads the tab
// when the desktop leaves Running, so the gateway can show the boot or the "off" page.
//
//go:embed reload.js
var reloadScript []byte

// reloadPath is the script's path below the route base.
const reloadPath = reservedPrefix + "/reload.js"

// pingPath answers 204; reload.js requests it to keep the forward-auth session active.
const pingPath = reservedPrefix + "/ping"

func (h *Handler) serveReloadScript(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("Content-Type", "text/javascript; charset=utf-8")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(reloadScript)
}

// modifyResponse adds the reload script to the desktop's top-level HTML document. It leaves
// everything else alone: other content types, compressed bodies, non-200 answers, oversized pages.
// A Content-Security-Policy of the desktop is not touched; one that blocks this script or its
// status stream (Selkies sends only frame-ancestors) would need a nonce and connect-src 'self'.
func (h *Handler) modifyResponse(resp *http.Response) error {
	t, _ := resp.Request.Context().Value(ctxKey{}).(*proxyTarget)
	if t == nil || !t.inject || resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "text/html" {
		return nil
	}
	if resp.ContentLength > maxInjectBody {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInjectBody+1))
	if err != nil || len(body) > maxInjectBody {
		// Hand the part already read, and the rest, on unchanged.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return nil
	}
	_ = resp.Body.Close()

	tag := `<script src="` + html.EscapeString(t.prefix+reloadPath) + `" defer></script>`
	if out, ok := insertBefore(body, tag, "</head>", "</body>"); ok {
		body = out
		// The body no longer matches the desktop's validators.
		resp.Header.Del("ETag")
		resp.Header.Del("Last-Modified")
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Content-MD5")
	return nil
}

// insertBefore puts tag before the first of the markers that occurs (ASCII case-insensitive).
func insertBefore(body []byte, tag string, markers ...string) ([]byte, bool) {
	lower := bytes.ToLower(body)
	for _, m := range markers {
		if i := bytes.Index(lower, []byte(m)); i >= 0 {
			out := make([]byte, 0, len(body)+len(tag))
			out = append(out, body[:i]...)
			out = append(out, tag...)
			return append(out, body[i:]...), true
		}
	}
	return body, false
}
