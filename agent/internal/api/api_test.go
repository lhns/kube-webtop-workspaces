package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func token(t *testing.T, v string) string {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReportOmitsAbsentFields(t *testing.T) {
	var got map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	pct := 40
	c := New(srv.URL, token(t, "tok\n"))
	if err := c.Report(context.Background(), Report{Stage: "Starting Plasma", BootProgress: &pct}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" || path != "/api/v1/report" {
		t.Fatalf("auth %q path %q", auth, path)
	}
	if len(got) != 2 || got["stage"] != "Starting Plasma" || got["bootProgress"] != float64(40) {
		t.Fatalf("body = %v", got)
	}
}

func TestPowerBodyAndStatus(t *testing.T) {
	var got PowerRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/power" {
			t.Errorf("path %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := New(srv.URL, token(t, "t")).Power(context.Background(), PowerRequest{Action: "stop", Reason: "idle"}); err != nil {
		t.Fatal(err)
	}
	if got.Action != "stop" || got.Reason != "idle" {
		t.Fatalf("got %+v", got)
	}
}

func TestUnexpectedStatusIsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, " stale pod \n", http.StatusConflict)
	}))
	defer srv.Close()
	err := New(srv.URL, token(t, "t")).Report(context.Background(), Report{})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 409 || se.Body != "stale pod" || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("err = %v", err)
	}
	// Power wants 200, not 204.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv2.Close()
	if err := New(srv2.URL, token(t, "t")).Power(context.Background(), PowerRequest{Action: "stop"}); !errors.As(err, &se) || se.Status != 204 {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenReadOnEveryRequest(t *testing.T) {
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	tp := token(t, "one")
	c := New(srv.URL, tp)
	c.Report(context.Background(), Report{})
	os.WriteFile(tp, []byte("two"), 0o600)
	c.Report(context.Background(), Report{})
	if len(auths) != 2 || auths[0] != "Bearer one" || auths[1] != "Bearer two" {
		t.Fatalf("auths = %v", auths)
	}
}

func TestMissingToken(t *testing.T) {
	c := New("http://127.0.0.1:1", filepath.Join(t.TempDir(), "none"))
	if err := c.Report(context.Background(), Report{}); err == nil || !strings.Contains(err.Error(), "read token") {
		t.Fatalf("err = %v", err)
	}
}
