// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

const testNS = "ns"

// recStore records the writes the handler makes.
type recStore struct {
	Store
	mu       sync.Mutex
	creates  []string
	powerOns []string
}

func (s *recStore) Create(ctx context.Context, d *v1alpha1.Desktop) error {
	s.mu.Lock()
	s.creates = append(s.creates, d.Name)
	s.mu.Unlock()
	return s.Store.Create(ctx, d)
}

func (s *recStore) PowerOn(ctx context.Context, d *v1alpha1.Desktop) error {
	s.mu.Lock()
	s.powerOns = append(s.powerOns, d.Name)
	s.mu.Unlock()
	return s.Store.PowerOn(ctx, d)
}

type env struct {
	h   *Handler
	c   client.Client
	st  *recStore
	hub *Hub
	reg *prometheus.Registry
}

func gwConfig(t *testing.T, mod func(*config.Gateway)) config.Gateway {
	t.Helper()
	cfg, err := config.Parse([]byte("namespace: " + testNS + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if mod != nil {
		mod(&cfg.Gateway)
	}
	return cfg.Gateway
}

func newEnv(t *testing.T, mod func(*config.Gateway), objs ...client.Object) *env {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Desktop{}).Build()
	st := &recStore{Store: clientStore{c: c, ns: testNS}}
	hub := NewHub()
	reg := prometheus.NewRegistry()
	h, err := NewHandler(Options{
		Config: gwConfig(t, mod), Namespace: testNS, Store: st, Hub: hub,
		Metrics: NewMetrics(reg), PingInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{h: h, c: c, st: st, hub: hub, reg: reg}
}

type dopt func(*v1alpha1.Desktop)

func desktop(name, user string, opts ...dopt) *v1alpha1.Desktop {
	d := &v1alpha1.Desktop{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec:       v1alpha1.DesktopSpec{User: user, PowerState: v1alpha1.PowerOff},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

func powered(p v1alpha1.PowerState) dopt { return func(d *v1alpha1.Desktop) { d.Spec.PowerState = p } }
func phase(p v1alpha1.Phase) dopt        { return func(d *v1alpha1.Desktop) { d.Status.Phase = p } }
func running(u string) dopt {
	return func(d *v1alpha1.Desktop) {
		d.Spec.PowerState = v1alpha1.PowerOn
		d.Status.Phase = v1alpha1.PhaseRunning
		d.Status.URL = u
	}
}
func lastStop(r v1alpha1.StopReason) dopt {
	return func(d *v1alpha1.Desktop) {
		d.Status.LastStop = &v1alpha1.LastStop{Time: metav1.NowMicro(), Reason: r}
	}
}
func unauthorized() dopt {
	return func(d *v1alpha1.Desktop) {
		d.Status.Conditions = []metav1.Condition{{
			Type: v1alpha1.ConditionAuthorized, Status: metav1.ConditionFalse, Reason: v1alpha1.ReasonNotMember,
			LastTransitionTime: metav1.Now(),
		}}
	}
}

var navHeaders = map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}

func (e *env) do(method, target string, hdr ...map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Remote-User", "alice")
	for _, m := range hdr {
		for k, v := range m {
			if v == "" {
				req.Header.Del(k)
			} else {
				req.Header.Set(k, v)
			}
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) desktop(t *testing.T, name string) *v1alpha1.Desktop {
	t.Helper()
	d := &v1alpha1.Desktop{}
	if err := e.c.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: name}, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func counter(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
	next:
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if want, ok := labels[l.GetName()]; ok && want != l.GetValue() {
					continue next
				}
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
			return m.GetGauge().GetValue()
		}
	}
	return 0
}

func TestIsNavigation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		hdr    map[string]string
		want   bool
	}{
		{"navigate document", "GET", map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}, true},
		{"navigate no dest", "GET", map[string]string{"Sec-Fetch-Mode": "navigate"}, true},
		{"navigate iframe", "GET", map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"}, false},
		{"cors", "GET", map[string]string{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}, false},
		{"websocket mode", "GET", map[string]string{"Sec-Fetch-Mode": "websocket"}, false},
		{"sec-fetch wins over accept", "GET", map[string]string{"Sec-Fetch-Mode": "no-cors", "Accept": "text/html"}, false},
		{"fallback html", "GET", map[string]string{"Accept": "text/html,application/xhtml+xml"}, true},
		{"fallback json", "GET", map[string]string{"Accept": "application/json"}, false},
		{"fallback no accept", "GET", nil, false},
		{"fallback upgrade", "GET", map[string]string{"Accept": "text/html", "Upgrade": "websocket"}, false},
		{"fallback post", "POST", map[string]string{"Accept": "text/html"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/", nil)
			for k, v := range tc.hdr {
				r.Header.Set(k, v)
			}
			if got := IsNavigation(r); got != tc.want {
				t.Fatalf("IsNavigation = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckCSRF(t *testing.T) {
	const tok = "abc123"
	tests := []struct {
		name   string
		cookie string
		field  string
		hdr    map[string]string
		want   bool
	}{
		{"valid sec-fetch-site", tok, tok, map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"valid origin", tok, tok, map[string]string{"Origin": "https://gw.example"}, true},
		{"field differs", tok, "other", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"field empty", tok, "", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"cookie missing", "", tok, map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"cross-site", tok, tok, map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"same-site is not enough", tok, tok, map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"origin mismatch", tok, tok, map[string]string{"Origin": "https://evil.example"}, false},
		{"origin null", tok, tok, map[string]string{"Origin": "null"}, false},
		{"neither header", tok, tok, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://gw.example/.webtop/start", strings.NewReader(url.Values{csrfField: {tc.field}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: csrfCookie, Value: tc.cookie})
			}
			for k, v := range tc.hdr {
				r.Header.Set(k, v)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := checkCSRF(r); got != tc.want {
				t.Fatalf("checkCSRF = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIdentityAndRouting(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "backend")
	}))
	defer backend.Close()

	objs := func() []client.Object {
		return []client.Object{
			desktop("alice", "alice", running(backend.URL)),
			desktop("bob", "bob", running(backend.URL)),
			desktop("carol-x", "Carol.X", running(backend.URL)),   // sanitize("Carol.X")
			desktop("dave", "someone-else", running(backend.URL)), // collision for user "Dave"
		}
	}
	tests := []struct {
		name   string
		mod    func(*config.Gateway)
		target string
		hdr    map[string]string
		remote string
		want   int
	}{
		{"missing user", nil, "/", map[string]string{"Remote-User": ""}, "", 401},
		{"own desktop", nil, "/", nil, "", 200},
		{"required group missing", func(g *config.Gateway) { g.RequiredGroup = "desktop" }, "/", map[string]string{"Remote-Groups": "other"}, "", 403},
		{"required group present", func(g *config.Gateway) { g.RequiredGroup = "desktop" }, "/", map[string]string{"Remote-Groups": "x, desktop"}, "", 200},
		{"admin without required group opens other", func(g *config.Gateway) { g.AdminGroup = "admins"; g.RequiredGroup = "desktop" }, "/u/bob/x", map[string]string{"Remote-Groups": "admins"}, "", 200},
		{"admin without required group own via /u/", func(g *config.Gateway) { g.AdminGroup = "admins"; g.RequiredGroup = "desktop" }, "/u/alice/x", map[string]string{"Remote-Groups": "admins"}, "", 200},
		{"admin without required group own", func(g *config.Gateway) { g.AdminGroup = "admins"; g.RequiredGroup = "desktop" }, "/", map[string]string{"Remote-Groups": "admins"}, "", 200},
		{"untrusted remote", func(g *config.Gateway) { g.TrustedProxyCIDRs = []string{"10.0.0.0/8"} }, "/", nil, "192.0.2.7:1000", 403},
		{"trusted remote", func(g *config.Gateway) { g.TrustedProxyCIDRs = []string{"10.0.0.0/8"} }, "/", nil, "10.1.2.3:1000", 200},
		{"admin opens other", func(g *config.Gateway) { g.AdminGroup = "admins" }, "/u/bob/x", map[string]string{"Remote-Groups": "admins"}, "", 200},
		{"admin unknown is 404", func(g *config.Gateway) { g.AdminGroup = "admins" }, "/u/nobody/", map[string]string{"Remote-Groups": "admins"}, "", 404},
		{"non-admin other desktop", nil, "/u/bob/", nil, "", 403},
		{"non-admin unknown desktop hidden", nil, "/u/nobody/", nil, "", 403},
		{"own via /u/", nil, "/u/alice/x", nil, "", 200},
		{"own via /u/ unknown", nil, "/u/zed/", map[string]string{"Remote-User": "zed"}, "", 404},
		{"/u/ without slash redirects", nil, "/u/alice", nil, "", 308},
		{"/u/ unsanitizable", nil, "/u/%21%21/", nil, "", 400},
		{"/u/ empty", nil, "/u/", nil, "", 400},
		{"collision", nil, "/", map[string]string{"Remote-User": "Dave"}, "", 409},
		{"sanitized name match", nil, "/", map[string]string{"Remote-User": "Carol.X"}, "", 200},
		{"empty sanitize", nil, "/", map[string]string{"Remote-User": "!!!"}, "", 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.mod, objs()...)
			req := httptest.NewRequest("GET", tc.target, nil)
			req.Header.Set("Remote-User", "alice")
			for k, v := range tc.hdr {
				if v == "" {
					req.Header.Del(k)
				} else {
					req.Header.Set(k, v)
				}
			}
			if tc.remote != "" {
				req.RemoteAddr = tc.remote
			}
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want != 200 && rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("gateway error without no-store: %q", rec.Header().Get("Cache-Control"))
			}
			if len(e.st.creates)+len(e.st.powerOns) != 0 {
				t.Errorf("unexpected writes: %v %v", e.st.creates, e.st.powerOns)
			}
		})
	}
}

func TestStateTable(t *testing.T) {
	ws := map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-Fetch-Mode": "websocket"}
	xhr := map[string]string{"Sec-Fetch-Mode": "cors", "Accept": "application/json"}

	tests := []struct {
		name         string
		obj          *v1alpha1.Desktop
		hdr          map[string]string
		wantCode     int
		wantBody     string
		wantCreate   bool
		wantPatch    bool
		wantOn       bool // powerState On afterwards
		wantRetry    bool
		wantCookie   bool
		wantNoStore  bool
		wantOutcome  string
		wantTrigger  string
		wantTriggerN float64
	}{
		{name: "nav, no desktop: create", hdr: navHeaders, wantCode: 200, wantBody: "Starting your desktop",
			wantCreate: true, wantOn: true, wantNoStore: true, wantOutcome: outBoot, wantTrigger: triggerCreate, wantTriggerN: 1},
		{name: "xhr, no desktop: 503", hdr: xhr, wantCode: 503, wantRetry: true, wantNoStore: true, wantOutcome: outUnavailable},
		{name: "websocket, no desktop: 503", hdr: ws, wantCode: 503, wantRetry: true, wantNoStore: true, wantOutcome: outUnavailable},
		{name: "xhr, off: 503", obj: desktop("alice", "alice"), hdr: xhr, wantCode: 503, wantRetry: true, wantOutcome: outUnavailable},
		{name: "websocket, off: 503", obj: desktop("alice", "alice"), hdr: ws, wantCode: 503, wantRetry: true, wantOutcome: outUnavailable},
		{name: "nav, off no lastStop: patched", obj: desktop("alice", "alice", phase(v1alpha1.PhaseOff)), hdr: navHeaders,
			wantCode: 200, wantBody: "Starting your desktop", wantPatch: true, wantOn: true, wantNoStore: true,
			wantOutcome: outBoot, wantTrigger: triggerNavigate, wantTriggerN: 1},
		{name: "nav, off with lastStop: off page", obj: desktop("alice", "alice", phase(v1alpha1.PhaseOff), lastStop(v1alpha1.StopIdle)),
			hdr: navHeaders, wantCode: 200, wantBody: "stopped because it was idle", wantCookie: true, wantNoStore: true, wantOutcome: outOff},
		{name: "xhr, off with lastStop: 503 no patch", obj: desktop("alice", "alice", lastStop(v1alpha1.StopUser)), hdr: xhr,
			wantCode: 503, wantRetry: true, wantOutcome: outUnavailable},
		{name: "nav, unauthorized: 403", obj: desktop("alice", "alice", unauthorized()), hdr: navHeaders,
			wantCode: 403, wantNoStore: true, wantOutcome: outDenied},
		{name: "xhr, unauthorized: 503", obj: desktop("alice", "alice", unauthorized()), hdr: xhr,
			wantCode: 503, wantRetry: true, wantOutcome: outUnavailable},
		{name: "nav, on and starting: boot", obj: desktop("alice", "alice", powered(v1alpha1.PowerOn), phase(v1alpha1.PhaseStarting),
			func(d *v1alpha1.Desktop) { d.Status.Stage = "Booting"; d.Status.BootProgress = 40 }),
			hdr: navHeaders, wantCode: 200, wantBody: "Booting", wantOn: true, wantNoStore: true, wantOutcome: outBoot},
		{name: "nav, on and error: boot", obj: desktop("alice", "alice", powered(v1alpha1.PowerOn), phase(v1alpha1.PhaseError)),
			hdr: navHeaders, wantCode: 200, wantBody: "Starting your desktop", wantOn: true, wantOutcome: outBoot},
		{name: "xhr, on and starting: 503", obj: desktop("alice", "alice", powered(v1alpha1.PowerOn), phase(v1alpha1.PhaseStarting)),
			hdr: xhr, wantCode: 503, wantRetry: true, wantOn: true, wantOutcome: outUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var objs []client.Object
			if tc.obj != nil {
				objs = append(objs, tc.obj)
			}
			e := newEnv(t, nil, objs...)
			rec := e.do("GET", "/some/path", tc.hdr)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantCode, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body lacks %q: %s", tc.wantBody, rec.Body)
			}
			if got := rec.Header().Get("Retry-After") == "5"; got != tc.wantRetry {
				t.Errorf("Retry-After 5 = %v, want %v", got, tc.wantRetry)
			}
			if tc.wantNoStore && rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
			}
			if got := len(rec.Result().Cookies()) > 0; got != tc.wantCookie {
				t.Errorf("csrf cookie set = %v, want %v", got, tc.wantCookie)
			}
			if got := len(e.st.creates) > 0; got != tc.wantCreate {
				t.Errorf("created = %v, want %v", got, tc.wantCreate)
			}
			if got := len(e.st.powerOns) > 0; got != tc.wantPatch {
				t.Errorf("patched = %v, want %v", got, tc.wantPatch)
			}
			if tc.wantCreate || tc.obj != nil {
				d := e.desktop(t, "alice")
				if got := d.Spec.PowerState == v1alpha1.PowerOn; got != tc.wantOn {
					t.Errorf("powerState On = %v, want %v", got, tc.wantOn)
				}
				if tc.wantCreate && (d.Spec.User != "alice" || d.Namespace != testNS) {
					t.Errorf("created desktop = %+v", d)
				}
			}
			if tc.wantOutcome != "" && counter(t, e.reg, "webtop_gateway_requests_total", map[string]string{"outcome": tc.wantOutcome}) != 1 {
				t.Errorf("outcome %s not counted once", tc.wantOutcome)
			}
			if tc.wantTrigger != "" && counter(t, e.reg, "webtop_gateway_starts_total", map[string]string{"trigger": tc.wantTrigger}) != tc.wantTriggerN {
				t.Errorf("trigger %s not counted", tc.wantTrigger)
			}
		})
	}
}

func TestPrefixedBootPageUsesBase(t *testing.T) {
	e := newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" },
		desktop("bob", "bob", powered(v1alpha1.PowerOn), phase(v1alpha1.PhaseStarting)))
	rec := e.do("GET", "/u/bob/", navHeaders, map[string]string{"Remote-Groups": "admins"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `/u/bob/.webtop/events`) {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
}

func TestStartPost(t *testing.T) {
	const tok = "tok"
	post := func(e *env, target string, field string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", target, strings.NewReader(url.Values{csrfField: {field}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Remote-User", "alice")
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: tok})
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	off := func() *v1alpha1.Desktop { return desktop("alice", "alice", lastStop(v1alpha1.StopIdle)) }

	t.Run("valid", func(t *testing.T) {
		e := newEnv(t, nil, off())
		rec := post(e, "/.webtop/start", tok, same)
		if rec.Code != 303 || rec.Header().Get("Location") != "/" {
			t.Fatalf("code %d loc %q", rec.Code, rec.Header().Get("Location"))
		}
		if e.desktop(t, "alice").Spec.PowerState != v1alpha1.PowerOn {
			t.Error("not powered on")
		}
		if counter(t, e.reg, "webtop_gateway_starts_total", map[string]string{"trigger": triggerButton}) != 1 {
			t.Error("button start not counted")
		}
	})
	t.Run("prefixed redirect", func(t *testing.T) {
		e := newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" }, desktop("bob", "bob", lastStop(v1alpha1.StopIdle)))
		rec := post(e, "/u/bob/.webtop/start", tok, map[string]string{"Sec-Fetch-Site": "same-origin", "Remote-Groups": "admins"})
		if rec.Code != 303 || rec.Header().Get("Location") != "/u/bob/" {
			t.Fatalf("code %d loc %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("csrf failures", func(t *testing.T) {
		for name, tc := range map[string]struct {
			field string
			hdr   map[string]string
		}{
			"field mismatch": {"x", same},
			"cross-site":     {tok, map[string]string{"Sec-Fetch-Site": "cross-site"}},
			"no headers":     {tok, nil},
		} {
			e := newEnv(t, nil, off())
			rec := post(e, "/.webtop/start", tc.field, tc.hdr)
			if rec.Code != 403 || len(e.st.powerOns) != 0 {
				t.Errorf("%s: code %d, patches %v", name, rec.Code, e.st.powerOns)
			}
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", lastStop(v1alpha1.StopIdle), unauthorized()))
		if rec := post(e, "/.webtop/start", tok, same); rec.Code != 403 || len(e.st.powerOns) != 0 {
			t.Fatalf("code %d patches %v", rec.Code, e.st.powerOns)
		}
	})
	t.Run("someone else's desktop", func(t *testing.T) {
		e := newEnv(t, nil, desktop("bob", "bob", lastStop(v1alpha1.StopIdle)))
		if rec := post(e, "/u/bob/.webtop/start", tok, same); rec.Code != 403 || len(e.st.powerOns) != 0 {
			t.Fatalf("code %d", rec.Code)
		}
	})
	t.Run("GET not allowed", func(t *testing.T) {
		e := newEnv(t, nil, off())
		if rec := e.do("GET", "/.webtop/start"); rec.Code != 405 {
			t.Fatalf("code %d", rec.Code)
		}
	})
}

func TestOffPageFormMatchesCookie(t *testing.T) {
	e := newEnv(t, nil, desktop("alice", "alice", lastStop(v1alpha1.StopUser)))
	rec := e.do("GET", "/", navHeaders)
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != csrfCookie || !cs[0].HttpOnly || !cs[0].Secure ||
		cs[0].SameSite != http.SameSiteStrictMode || cs[0].Path != "/" {
		t.Fatalf("cookie = %+v", cs)
	}
	if !strings.Contains(rec.Body.String(), `value="`+cs[0].Value+`"`) || !strings.Contains(rec.Body.String(), `action="/.webtop/start"`) {
		t.Fatalf("form lacks token or action: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "shut down from the desktop") {
		t.Errorf("no reason wording: %s", rec.Body)
	}

	e = newEnv(t, func(g *config.Gateway) { f := false; g.CookieSecure = &f }, desktop("alice", "alice", lastStop(v1alpha1.StopUser)))
	if c := e.do("GET", "/", navHeaders).Result().Cookies(); len(c) != 1 || c[0].Secure {
		t.Fatalf("cookieSecure=false still Secure: %+v", c)
	}
}

func TestReservedPathsNeverProxied(t *testing.T) {
	hits := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		io.WriteString(w, "backend")
	}))
	defer backend.Close()
	e := newEnv(t, nil, desktop("alice", "alice", running(backend.URL)))
	for _, p := range []string{"/.webtop/other", "/.webtop", "/.webtop/", "/x/../.webtop/secret", "//.webtop/secret", "/.webtop/events/x", "/%2ewebtop/secret"} {
		rec := e.do("GET", p, navHeaders)
		if rec.Code == 200 && strings.Contains(rec.Body.String(), "backend") {
			t.Errorf("%s reached the backend", p)
		}
	}
	for _, p := range []string{"/.webtop/other", "/.webtop/start/x"} {
		if rec := e.do("POST", p); rec.Code != 404 {
			t.Errorf("POST %s = %d, want 404", p, rec.Code)
		}
	}
	if hits != 0 {
		t.Fatalf("backend hit %d times", hits)
	}
	// Unreserved paths still proxy.
	if rec := e.do("GET", "/webtop", navHeaders); rec.Body.String() != "backend" {
		t.Fatalf("normal path not proxied: %d %s", rec.Code, rec.Body)
	}
}

func TestProxy(t *testing.T) {
	var got *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		w.Header().Set("X-Backend", "1")
		io.WriteString(w, "hello")
	}))
	defer backend.Close()

	e := newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" },
		desktop("alice", "alice", running(backend.URL)),
		desktop("bob", "bob", running(backend.URL)))

	t.Run("own", func(t *testing.T) {
		rec := e.do("GET", "/a/b%20c?x=1", map[string]string{"Remote-Groups": "g1"})
		if rec.Code != 200 || rec.Body.String() != "hello" || rec.Header().Get("X-Backend") != "1" {
			t.Fatalf("code %d body %q", rec.Code, rec.Body)
		}
		if got.URL.Path != "/a/b c" || got.URL.RawQuery != "x=1" {
			t.Errorf("upstream url = %v", got.URL)
		}
		if got.Header.Get("X-Forwarded-Prefix") != "" {
			t.Errorf("prefix header set: %q", got.Header.Get("X-Forwarded-Prefix"))
		}
		if got.Header.Get("X-Forwarded-Host") != "example.com" || got.Header.Get("X-Forwarded-Proto") != "http" || got.Header.Get("X-Forwarded-For") == "" {
			t.Errorf("forwarded headers: %v", got.Header)
		}
		if got.Header.Get("Remote-User") != "alice" || got.Header.Get("Remote-Groups") != "g1" {
			t.Errorf("auth headers not passed: %v", got.Header)
		}
	})
	t.Run("spoofed prefix header dropped", func(t *testing.T) {
		e.do("GET", "/", map[string]string{"X-Forwarded-Prefix": "/evil", "X-Forwarded-Host": "evil"})
		if got.Header.Get("X-Forwarded-Prefix") != "" || got.Header.Get("X-Forwarded-Host") != "example.com" {
			t.Errorf("spoofed headers kept: %v", got.Header)
		}
	})
	t.Run("admin prefix", func(t *testing.T) {
		for target, wantPath := range map[string]string{"/u/bob/": "/", "/u/bob/x/y": "/x/y"} {
			rec := e.do("GET", target, map[string]string{"Remote-Groups": "admins"})
			if rec.Code != 200 {
				t.Fatalf("%s: code %d", target, rec.Code)
			}
			if got.URL.Path != wantPath || got.Header.Get("X-Forwarded-Prefix") != "/u/bob" {
				t.Errorf("%s: path %q prefix %q", target, got.URL.Path, got.Header.Get("X-Forwarded-Prefix"))
			}
		}
	})
	t.Run("trusted proxy keeps forwarded headers", func(t *testing.T) {
		e := newEnv(t, func(g *config.Gateway) { g.TrustedProxyCIDRs = []string{"10.0.0.0/8"} },
			desktop("alice", "alice", running(backend.URL)))
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.5:1"
		req.Header.Set("Remote-User", "alice")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "desk.example.org")
		req.Header.Add("X-Forwarded-For", "203.0.113.9")
		req.Header.Add("X-Forwarded-For", "198.51.100.1")
		e.h.ServeHTTP(httptest.NewRecorder(), req)
		if got.Header.Get("X-Forwarded-Proto") != "https" || got.Header.Get("X-Forwarded-Host") != "desk.example.org" ||
			got.Header.Get("X-Forwarded-For") != "203.0.113.9, 198.51.100.1, 10.0.0.5" {
			t.Errorf("forwarded headers: %v", got.Header)
		}
	})
	t.Run("backend down is 502 no-store", func(t *testing.T) {
		dead := httptest.NewServer(nil)
		url := dead.URL
		dead.Close()
		e := newEnv(t, nil, desktop("alice", "alice", running(url)))
		rec := e.do("GET", "/")
		if rec.Code != 502 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("code %d cc %q", rec.Code, rec.Header().Get("Cache-Control"))
		}
		if p, er := counter(t, e.reg, "webtop_gateway_requests_total", map[string]string{"outcome": "proxy"}),
			counter(t, e.reg, "webtop_gateway_requests_total", map[string]string{"outcome": "error"}); p != 0 || er != 1 {
			t.Errorf("outcomes proxy=%v error=%v, want 0 and 1", p, er)
		}
	})
}

func TestWebsocketThroughProxy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "no upgrade", 400)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(rw, buf); err == nil {
			rw.Write(append([]byte("echo:"), buf...))
			rw.Flush()
		}
	}))
	defer backend.Close()

	e := newEnv(t, nil, desktop("alice", "alice", running(backend.URL)))
	srv := httptest.NewServer(e.h)
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: x\r\nRemote-User: alice\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if counter(t, e.reg, "webtop_gateway_websockets", nil) != 1 {
		t.Error("websocket gauge not 1 while open")
	}
	io.WriteString(conn, "ping")
	out := make([]byte, len("echo:ping"))
	if _, err := io.ReadFull(br, out); err != nil || string(out) != "echo:ping" {
		t.Fatalf("echo = %q, %v", out, err)
	}
	conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for counter(t, e.reg, "webtop_gateway_websockets", nil) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("websocket gauge not back to 0")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readEvent(t *testing.T, br *bufio.Reader) (event, data string, err error) {
	t.Helper()
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return event, data, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if event != "" {
				return event, data, nil
			}
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, ":"):
			event, data = "ping", line
			return event, data, nil
		}
	}
}

func TestSSE(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("backend reached: %s", r.URL)
	}))
	defer backend.Close()

	e := newEnv(t, nil, desktop("alice", "alice", powered(v1alpha1.PowerOn), phase(v1alpha1.PhaseStarting),
		func(d *v1alpha1.Desktop) { d.Status.Stage = "Booting"; d.Status.BootProgress = 40 }))
	srv := httptest.NewServer(e.h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/.webtop/events", nil)
	req.Header.Set("Remote-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", resp.Header)
	}
	br := bufio.NewReader(resp.Body)

	ev, data, err := readEvent(t, br)
	if err != nil || ev != "status" || data != `{"phase":"Starting","stage":"Booting","bootProgress":40,"message":"","authorized":true}` {
		t.Fatalf("first event = %q %q %v", ev, data, err)
	}
	// Idle: keep-alive pings arrive (interval is 50ms in tests).
	if ev, _, err := readEvent(t, br); err != nil || ev != "ping" {
		t.Fatalf("expected ping, got %q %v", ev, err)
	}

	d := e.desktop(t, "alice")
	d.Status.Phase, d.Status.Stage, d.Status.BootProgress = v1alpha1.PhaseStarting, "Ready soon", 90
	if err := e.c.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	e.hub.Notify("alice")
	for {
		ev, data, err = readEvent(t, br)
		if err != nil {
			t.Fatal(err)
		}
		if ev == "ping" {
			continue
		}
		if ev != "status" || !strings.Contains(data, `"bootProgress":90`) {
			t.Fatalf("event = %q %q", ev, data)
		}
		break
	}

	d = e.desktop(t, "alice")
	d.Status.Phase, d.Status.URL = v1alpha1.PhaseRunning, backend.URL
	if err := e.c.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	e.hub.Notify("alice")
	var seen []string
	for {
		ev, _, err = readEvent(t, br)
		if err != nil {
			t.Fatalf("stream ended before ready (saw %v): %v", seen, err)
		}
		if ev == "ping" {
			continue
		}
		seen = append(seen, ev)
		if ev == "ready" {
			break
		}
	}
	if rest, _ := io.ReadAll(br); strings.Contains(string(rest), "event:") {
		t.Errorf("events after ready: %q", rest)
	}
	if len(e.st.creates)+len(e.st.powerOns) != 0 {
		t.Error("SSE wrote to the cluster")
	}
}

func TestSSEMissingDesktopKeepsWaiting(t *testing.T) {
	e := newEnv(t, nil)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/.webtop/events", nil)
	req.Header.Set("Remote-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	ev, data, err := readEvent(t, br)
	if err != nil || ev != "status" || !strings.Contains(data, `"phase":""`) {
		t.Fatalf("event = %q %q %v", ev, data, err)
	}
	if len(e.st.creates)+len(e.st.powerOns) != 0 {
		t.Error("SSE created or started something")
	}
	// The desktop appears: the stream picks it up.
	if err := e.c.Create(context.Background(), desktop("alice", "alice", powered(v1alpha1.PowerOn), phase(v1alpha1.PhaseStarting))); err != nil {
		t.Fatal(err)
	}
	e.hub.Notify("alice")
	for {
		ev, data, err = readEvent(t, br)
		if err != nil {
			t.Fatal(err)
		}
		if ev == "status" {
			if !strings.Contains(data, `"phase":"Starting"`) {
				t.Fatalf("data = %s", data)
			}
			return
		}
	}
}

func TestSSEAuthz(t *testing.T) {
	e := newEnv(t, nil, desktop("bob", "bob", phase(v1alpha1.PhaseStarting)))
	if rec := e.do("GET", "/u/bob/.webtop/events"); rec.Code != 403 {
		t.Fatalf("code %d", rec.Code)
	}
	if rec := e.do("POST", "/.webtop/events"); rec.Code != 405 {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestPagesAreSelfContained(t *testing.T) {
	e := newEnv(t, nil, desktop("alice", "alice", powered(v1alpha1.PowerOn), phase(v1alpha1.PhaseStarting)))
	body := e.do("GET", "/", navHeaders).Body.String()
	for _, bad := range []string{"http://", "https://", "src=", "<link"} {
		if strings.Contains(body, bad) {
			t.Errorf("boot page references external resource (%q)", bad)
		}
	}
	if !strings.Contains(body, `new EventSource("/.webtop/events")`) {
		t.Errorf("boot page has no EventSource: %s", body)
	}
}
