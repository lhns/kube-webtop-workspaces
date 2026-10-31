// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

const page = `<!doctype html><html><head><title>Selkies</title></head><body>hi</body></html>`

func htmlBackend(t *testing.T, mod func(w http.ResponseWriter, r *http.Request) (handled bool)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mod != nil && mod(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page)
	}))
	t.Cleanup(srv.Close)
	return srv
}

var scriptTag = regexp.MustCompile(`<script src="([^"]+)" defer></script>`)

func TestInjectReloadScript(t *testing.T) {
	var upstream http.Header
	b := htmlBackend(t, func(w http.ResponseWriter, r *http.Request) bool {
		upstream = r.Header.Clone()
		return false
	})
	e := newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" },
		desktop("alice", "alice", running(b.URL)), desktop("bob", "bob", running(b.URL)))

	t.Run("plain", func(t *testing.T) {
		rec := e.do("GET", "/", navHeaders)
		body := rec.Body.String()
		m := scriptTag.FindStringSubmatch(body)
		if rec.Code != 200 || m == nil || m[1] != "/.webtop/reload.js" {
			t.Fatalf("code %d body %q", rec.Code, body)
		}
		// Before </head>, nothing else changed.
		want := strings.Replace(page, "</head>", m[0]+"</head>", 1)
		if body != want {
			t.Errorf("body = %q\nwant   %q", body, want)
		}
		if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(body)) {
			t.Errorf("Content-Length %q, body is %d bytes", cl, len(body))
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("content type %q", ct)
		}
	})
	t.Run("admin prefix", func(t *testing.T) {
		rec := e.do("GET", "/u/bob/", navHeaders, map[string]string{"Remote-Groups": "admins"})
		if m := scriptTag.FindStringSubmatch(rec.Body.String()); m == nil || m[1] != "/u/bob/.webtop/reload.js" {
			t.Fatalf("body %q", rec.Body)
		}
	})
	t.Run("gzip from the desktop is decoded and injected", func(t *testing.T) {
		gz := htmlBackend(t, func(w http.ResponseWriter, r *http.Request) bool {
			upstream = r.Header.Clone()
			w.Header().Set("Content-Type", "text/html")
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				io.WriteString(w, page)
				return true
			}
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			io.WriteString(zw, page)
			zw.Close()
			return true
		})
		e := newEnv(t, nil, desktop("alice", "alice", running(gz.URL)))
		rec := e.do("GET", "/", navHeaders, map[string]string{"Accept-Encoding": "br, gzip"})
		if !scriptTag.MatchString(rec.Body.String()) || rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("encoding %q body %q", rec.Header().Get("Content-Encoding"), rec.Body)
		}
		if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(rec.Body.Len()) {
			t.Errorf("Content-Length %q, body is %d bytes", cl, rec.Body.Len())
		}
		if strings.Contains(upstream.Get("Accept-Encoding"), "br") {
			t.Errorf("the client's Accept-Encoding reached the desktop: %q", upstream.Get("Accept-Encoding"))
		}
	})
	t.Run("a body that stays encoded is left alone", func(t *testing.T) {
		var zbuf bytes.Buffer
		zw := gzip.NewWriter(&zbuf)
		io.WriteString(zw, page)
		zw.Close()
		enc := htmlBackend(t, func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(zbuf.Bytes())
			return true
		})
		e := newEnv(t, nil, desktop("alice", "alice", running(enc.URL)))
		e.h.proxy.Transport = &http.Transport{DisableCompression: true}
		rec := e.do("GET", "/", navHeaders)
		if rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(rec.Body.Bytes(), zbuf.Bytes()) {
			t.Fatalf("encoded body was modified: %q", rec.Header().Get("Content-Encoding"))
		}
	})
	t.Run("only the top-level document", func(t *testing.T) {
		for name, hdr := range map[string]map[string]string{
			"iframe": {"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"},
			"fetch":  {"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"},
			"plain":  nil,
		} {
			rec := e.do("GET", "/", hdr, map[string]string{"Accept-Encoding": "gzip"})
			if rec.Body.String() != page {
				t.Errorf("%s: body modified: %q", name, rec.Body)
			}
		}
		// Their Accept-Encoding is passed on untouched.
		e.do("GET", "/", map[string]string{"Sec-Fetch-Mode": "cors", "Accept-Encoding": "gzip"})
		if upstream.Get("Accept-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding %q", upstream.Get("Accept-Encoding"))
		}
	})
	t.Run("non-HTML and non-200 untouched", func(t *testing.T) {
		other := htmlBackend(t, func(w http.ResponseWriter, r *http.Request) bool {
			switch r.URL.Path {
			case "/app.js":
				w.Header().Set("Content-Type", "text/javascript")
				io.WriteString(w, "</head>")
			case "/missing":
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(404)
				io.WriteString(w, page)
			default:
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"a":"</body>"}`)
			}
			return true
		})
		e := newEnv(t, nil, desktop("alice", "alice", running(other.URL)))
		for path, want := range map[string]string{"/app.js": "</head>", "/missing": page, "/x": `{"a":"</body>"}`} {
			if got := e.do("GET", path, navHeaders).Body.String(); got != want {
				t.Errorf("%s: body %q", path, got)
			}
		}
	})
	t.Run("no marker, uppercase marker, body fallback", func(t *testing.T) {
		pages := map[string]string{"/none": "<p>plain</p>", "/upper": "<HTML><HEAD></HEAD><BODY></BODY></HTML>", "/nohead": "<body>x</body>"}
		o := htmlBackend(t, func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, pages[r.URL.Path])
			return true
		})
		e := newEnv(t, nil, desktop("alice", "alice", running(o.URL)))
		if got := e.do("GET", "/none", navHeaders).Body.String(); got != pages["/none"] {
			t.Errorf("modified: %q", got)
		}
		if got := e.do("GET", "/upper", navHeaders).Body.String(); !strings.Contains(got, "<script ") || strings.Index(got, "<script ") > strings.Index(got, "</HEAD>") {
			t.Errorf("uppercase head: %q", got)
		}
		if got := e.do("GET", "/nohead", navHeaders).Body.String(); !strings.HasPrefix(got, "<body>x<script ") {
			t.Errorf("body fallback: %q", got)
		}
	})
	t.Run("huge page passes unchanged", func(t *testing.T) {
		big := strings.Repeat("a", maxInjectBody+10) + "</head>"
		o := htmlBackend(t, func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, big)
			return true
		})
		e := newEnv(t, nil, desktop("alice", "alice", running(o.URL)))
		if got := e.do("GET", "/", navHeaders).Body.String(); got != big {
			t.Errorf("big body changed (len %d, want %d)", len(got), len(big))
		}
	})
	t.Run("validators of the original are dropped", func(t *testing.T) {
		o := htmlBackend(t, func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("ETag", `"abc"`)
			w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
			io.WriteString(w, page)
			return true
		})
		e := newEnv(t, nil, desktop("alice", "alice", running(o.URL)))
		rec := e.do("GET", "/", navHeaders)
		if !scriptTag.MatchString(rec.Body.String()) || rec.Header().Get("ETag") != "" || rec.Header().Get("Last-Modified") != "" {
			t.Errorf("headers %v", rec.Header())
		}
		// An untouched page keeps its ETag.
		other := e.do("GET", "/", map[string]string{"Sec-Fetch-Mode": "cors"})
		if other.Header().Get("ETag") != `"abc"` {
			t.Errorf("ETag of an untouched response: %v", other.Header())
		}
	})
}

func TestReloadScriptServed(t *testing.T) {
	b := htmlBackend(t, func(w http.ResponseWriter, r *http.Request) bool {
		t.Errorf("backend reached: %s", r.URL)
		return true
	})
	e := newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" },
		desktop("alice", "alice", running(b.URL)),
		desktop("bob", "bob", powered(v1alpha1.PowerOff), phase(v1alpha1.PhaseOff)))
	admin := map[string]string{"Remote-Groups": "admins"}

	for _, tc := range []struct {
		path string
		hdr  map[string]string
		code int
	}{
		{"/.webtop/reload.js", nil, 200},
		{"/u/alice/.webtop/reload.js", nil, 200},
		{"/u/bob/.webtop/reload.js", admin, 200}, // also while the desktop is off
		{"/u/bob/.webtop/reload.js", nil, 403},
	} {
		rec := e.do("GET", tc.path, tc.hdr)
		if rec.Code != tc.code {
			t.Errorf("%s: code %d, want %d", tc.path, rec.Code, tc.code)
			continue
		}
		if tc.code == 200 {
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
				t.Errorf("%s: content type %q", tc.path, ct)
			}
			if !bytes.Equal(rec.Body.Bytes(), reloadScript) || !strings.Contains(rec.Body.String(), "location.reload()") {
				t.Errorf("%s: wrong body", tc.path)
			}
		}
	}
	if rec := e.do("POST", "/.webtop/reload.js"); rec.Code != 405 {
		t.Errorf("POST code %d", rec.Code)
	}
}

// The script opens events?watch=1 next to itself; with watch the stream outlives "ready".
func TestSSEWatchKeepsStreamOpen(t *testing.T) {
	b := htmlBackend(t, nil)
	e := newEnv(t, nil, desktop("alice", "alice", running(b.URL), func(d *v1alpha1.Desktop) { d.Status.PodUID = "pod-1" }))
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/.webtop/events?watch=1", nil)
	req.Header.Set("Remote-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	ev, data, err := readEvent(t, br)
	if err != nil || ev != "status" || !strings.Contains(data, `"phase":"Running"`) || !strings.Contains(data, `"podUID":"pod-1"`) {
		t.Fatalf("first event = %q %q %v", ev, data, err)
	}
	// No "ready" and no end of stream: the next thing is a ping.
	if ev, _, err := readEvent(t, br); err != nil || ev != "ping" {
		t.Fatalf("expected ping, got %q %v", ev, err)
	}
	d := e.desktop(t, "alice")
	d.Status.Phase = v1alpha1.PhaseStopping
	if err := e.c.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	e.hub.Notify("alice")
	for {
		ev, data, err = readEvent(t, br)
		if err != nil {
			t.Fatal(err)
		}
		if ev == "status" {
			if !strings.Contains(data, `"phase":"Stopping"`) {
				t.Fatalf("data = %s", data)
			}
			return
		}
	}
}

func TestEventOfPull(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 10, 0, 0, time.UTC)
	started := metav1.NewTime(now.Add(-130 * time.Second))
	d := desktop("alice", "alice", func(d *v1alpha1.Desktop) {
		d.Status.Stage = "PullingImage"
		d.Status.ImagePull = &v1alpha1.ImagePull{Image: "i", StartedAt: &started, LastSizeBytes: 3745009478, LastDurationSeconds: 553}
	})
	ev := eventOf(d, now)
	if ev.Stage != "Downloading image" || ev.Pull == nil || ev.Pull.ElapsedSeconds != 130 || ev.Pull.SizeBytes != 3745009478 || ev.Pull.LastDurationSeconds != 553 {
		t.Fatalf("%+v %+v", ev, ev.Pull)
	}
	// Only while pulling, and only with a start time.
	d.Status.Stage = "CreatingContainer"
	if eventOf(d, now).Pull != nil {
		t.Error("pull shown outside PullingImage")
	}
	d.Status.Stage, d.Status.ImagePull.StartedAt = "PullingImage", nil
	if eventOf(d, now).Pull != nil {
		t.Error("pull shown without startedAt")
	}
	// A clock behind the event never shows a negative time.
	d.Status.ImagePull.StartedAt = &started
	if p := eventOf(d, now.Add(-time.Hour)).Pull; p == nil || p.ElapsedSeconds != 0 {
		t.Errorf("negative elapsed: %+v", p)
	}
}
