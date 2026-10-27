// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

// +kubebuilder:rbac:groups=webtop.lhns.github.io,resources=desktops,verbs=get;list;watch;create;patch

const (
	reservedPrefix = "/.webtop"
	pingInterval   = 15 * time.Second
)

// Options configure a Handler.
type Options struct {
	Config    config.Gateway
	Namespace string
	Store     Store
	Hub       *Hub
	Metrics   *Metrics
	Log       logr.Logger
	// Transport is the proxy transport; nil uses a shared default.
	Transport http.RoundTripper
	// PingInterval overrides the SSE keep-alive period (tests).
	PingInterval time.Duration
}

// Handler is the gateway's HTTP handler.
type Handler struct {
	cfg     config.Gateway
	ns      string
	store   Store
	hub     *Hub
	m       *Metrics
	log     logr.Logger
	trusted []*net.IPNet
	secure  bool
	ping    time.Duration
	proxy   *httputil.ReverseProxy
}

type ctxKey struct{}

// proxyTarget travels in the request context to the ReverseProxy's Rewrite.
type proxyTarget struct {
	url     *url.URL
	path    string // path to send upstream
	rawPath string
	prefix  string // X-Forwarded-Prefix, empty if none
	trusted bool   // peer is a configured trusted proxy: its X-Forwarded-* are kept
}

// route is the resolved request path.
type route struct {
	name     string // desktop name
	base     string // "" or "/u/<escaped user>"
	rest     string // decoded path below base, "/"-rooted
	restEsc  string
	prefixed bool
}

func NewHandler(o Options) (*Handler, error) {
	if o.Store == nil || o.Metrics == nil {
		return nil, errors.New("gateway: store and metrics are required")
	}
	h := &Handler{
		cfg: o.Config, ns: o.Namespace, store: o.Store, hub: o.Hub, m: o.Metrics, log: o.Log,
		secure: o.Config.CookieSecure == nil || *o.Config.CookieSecure,
		ping:   o.PingInterval,
	}
	if h.ping <= 0 {
		h.ping = pingInterval
	}
	if h.hub == nil {
		h.hub = NewHub()
	}
	for _, c := range o.Config.TrustedProxyCIDRs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("gateway.trustedProxyCIDRs: %w", err)
		}
		h.trusted = append(h.trusted, n)
	}
	rt := o.Transport
	if rt == nil {
		rt = &http.Transport{
			// In-cluster targets: never go through HTTP(S)_PROXY from the environment.
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	h.proxy = &httputil.ReverseProxy{
		Rewrite:       h.rewrite,
		Transport:     rt,
		FlushInterval: -1,
		ErrorHandler:  h.proxyError,
	}
	return h, nil
}

func (h *Handler) rewrite(pr *httputil.ProxyRequest) {
	t := pr.In.Context().Value(ctxKey{}).(*proxyTarget)
	pr.Out.URL.Path = t.path
	pr.Out.URL.RawPath = t.rawPath
	pr.SetURL(t.url)
	pr.Out.Host = ""
	pr.SetXForwarded()
	if t.trusted {
		// Behind our trusted proxy the external scheme/host/client are in its headers.
		if v := pr.In.Header.Get("X-Forwarded-Proto"); v != "" {
			pr.Out.Header.Set("X-Forwarded-Proto", v)
		}
		if v := pr.In.Header.Get("X-Forwarded-Host"); v != "" {
			pr.Out.Header.Set("X-Forwarded-Host", v)
		}
		if v := pr.In.Header.Get("X-Forwarded-For"); v != "" {
			pr.Out.Header.Set("X-Forwarded-For", v+", "+pr.Out.Header.Get("X-Forwarded-For"))
		}
	}
	pr.Out.Header.Del("X-Forwarded-Prefix")
	if t.prefix != "" {
		pr.Out.Header.Set("X-Forwarded-Prefix", t.prefix)
	}
}

func (h *Handler) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	h.log.V(1).Info("proxy error", "path", r.URL.Path, "error", err.Error())
	h.errorPage(w, http.StatusBadGateway, "Desktop unreachable", "The desktop did not answer. Try again in a moment.", outError)
}

func (h *Handler) count(outcome string) { h.m.Requests.WithLabelValues(outcome).Inc() }

func (h *Handler) errorPage(w http.ResponseWriter, status int, title, msg, outcome string) {
	h.count(outcome)
	writePage(w, status, "error", pageData{Title: title, Message: msg})
}

func (h *Handler) peerTrusted(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range h.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (h *Handler) groups(r *http.Request) map[string]bool {
	out := map[string]bool{}
	v := r.Header.Get(h.cfg.GroupsHeader)
	if v == "" {
		return out
	}
	parts := []string{v}
	if h.cfg.GroupsSeparator != "" {
		parts = strings.Split(v, h.cfg.GroupsSeparator)
	}
	for _, g := range parts {
		if g = strings.TrimSpace(g); g != "" {
			out[g] = true
		}
	}
	return out
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	trustedPeer := len(h.trusted) > 0 && h.peerTrusted(r)
	if len(h.trusted) > 0 && !trustedPeer {
		h.errorPage(w, http.StatusForbidden, "Forbidden", "This address may not use the gateway.", outDenied)
		return
	}
	user := strings.TrimSpace(r.Header.Get(h.cfg.UserHeader))
	if user == "" {
		h.errorPage(w, http.StatusUnauthorized, "Sign-in required", "No user was identified for this request.", outDenied)
		return
	}
	groups := h.groups(r)
	if g := h.cfg.RequiredGroup; g != "" && !groups[g] {
		h.errorPage(w, http.StatusForbidden, "No access", "Your account is not allowed to use desktops.", outDenied)
		return
	}
	admin := h.cfg.AdminGroup != "" && groups[h.cfg.AdminGroup]

	rt, d, ok := h.resolve(w, r, user, admin)
	if !ok {
		return
	}

	// Cleaned, so "/x/../.webtop/start" cannot reach a desktop's own path space either.
	if cleaned := path.Clean("/" + rt.rest); cleaned == reservedPrefix || strings.HasPrefix(cleaned, reservedPrefix+"/") {
		switch cleaned {
		case reservedPrefix + "/events":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				h.methodNotAllowed(w, http.MethodGet)
				return
			}
			h.serveEvents(w, r, rt, d)
		case reservedPrefix + "/start":
			if r.Method != http.MethodPost {
				h.methodNotAllowed(w, http.MethodPost)
				return
			}
			h.serveStart(w, r, rt, d)
		default:
			h.errorPage(w, http.StatusNotFound, "Not found", "There is nothing here.", outNotFound)
		}
		return
	}

	if d != nil && d.Status.Phase == v1alpha1.PhaseRunning && d.Status.URL != "" {
		h.serveProxy(w, r, rt, d, trustedPeer)
		return
	}
	h.notRunning(w, r, rt, d, user, false)
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	h.errorPage(w, http.StatusMethodNotAllowed, "Method not allowed", "This request is not allowed here.", outDenied)
}

// resolve maps the request to a desktop and checks the caller may use it. On failure it has
// written the response. d is nil when the caller's own Desktop does not exist yet.
func (h *Handler) resolve(w http.ResponseWriter, r *http.Request, user string, admin bool) (*route, *v1alpha1.Desktop, bool) {
	own, ownErr := names.Sanitize(user)

	if esc := r.URL.EscapedPath(); strings.HasPrefix(esc, "/u/") {
		segEsc, restEsc := esc[len("/u/"):], ""
		if i := strings.IndexByte(segEsc, '/'); i >= 0 {
			segEsc, restEsc = segEsc[:i], segEsc[i:]
		}
		seg, err := url.PathUnescape(segEsc)
		var name string
		if err == nil {
			name, err = names.Sanitize(seg)
		}
		if err != nil {
			h.errorPage(w, http.StatusBadRequest, "Bad request", "That user name cannot be used.", outDenied)
			return nil, nil, false
		}
		rest, err := url.PathUnescape(restEsc)
		if err != nil {
			h.errorPage(w, http.StatusBadRequest, "Bad request", "That path cannot be used.", outDenied)
			return nil, nil, false
		}
		bare := restEsc == ""
		if bare {
			rest, restEsc = "/", "/"
		}
		rt := &route{name: name, base: "/u/" + segEsc, rest: rest, restEsc: restEsc, prefixed: true}

		d, err := h.store.Get(r.Context(), name)
		switch {
		case apierrors.IsNotFound(err):
			// Do not reveal which desktops exist to callers who may not open them anyway.
			if admin || (ownErr == nil && name == own) {
				h.errorPage(w, http.StatusNotFound, "Not found", "This desktop does not exist.", outNotFound)
			} else {
				h.errorPage(w, http.StatusForbidden, "No access", "You may not open this desktop.", outDenied)
			}
			return nil, nil, false
		case err != nil:
			h.storeError(w, err)
			return nil, nil, false
		}
		if !admin && d.Spec.User != user {
			h.errorPage(w, http.StatusForbidden, "No access", "You may not open this desktop.", outDenied)
			return nil, nil, false
		}
		if bare {
			// "/u/<user>" without the slash: the desktop's relative URLs need it.
			target := rt.base + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return nil, nil, false
		}
		return rt, d, true
	}

	if ownErr != nil {
		h.errorPage(w, http.StatusBadRequest, "Bad request", "Your user name has no usable characters, so no desktop can be created for it.", outDenied)
		return nil, nil, false
	}
	rt := &route{name: own, rest: r.URL.Path, restEsc: r.URL.EscapedPath()}
	d, err := h.store.Get(r.Context(), own)
	switch {
	case apierrors.IsNotFound(err):
		return rt, nil, true
	case err != nil:
		h.storeError(w, err)
		return nil, nil, false
	}
	if d.Spec.User != user {
		h.collision(w)
		return nil, nil, false
	}
	return rt, d, true
}

func (h *Handler) collision(w http.ResponseWriter) {
	h.errorPage(w, http.StatusConflict, "Conflict", "Another user already owns the desktop name derived from your user name. Contact an administrator.", outDenied)
}

func (h *Handler) storeError(w http.ResponseWriter, err error) {
	h.log.Error(err, "reading Desktop")
	w.Header().Set("Retry-After", "5")
	h.errorPage(w, http.StatusServiceUnavailable, "Unavailable", "The desktop service is temporarily unavailable.", outError)
}

func (h *Handler) serveProxy(w http.ResponseWriter, r *http.Request, rt *route, d *v1alpha1.Desktop, trusted bool) {
	target, err := url.Parse(d.Status.URL)
	if err != nil || target.Host == "" {
		h.errorPage(w, http.StatusBadGateway, "Desktop unreachable", "The desktop has no valid address.", outError)
		return
	}
	t := &proxyTarget{url: target, path: r.URL.Path, rawPath: r.URL.RawPath, trusted: trusted}
	if rt.prefixed {
		t.path, t.rawPath, t.prefix = rt.rest, rt.restEsc, rt.base
		if t.rawPath == t.path {
			t.rawPath = ""
		}
	}
	h.count(outProxy)
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		h.m.Websockets.Inc()
		defer h.m.Websockets.Dec()
	}
	h.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, t)))
}

func authorizedFalse(d *v1alpha1.Desktop) bool {
	c := meta.FindStatusCondition(d.Status.Conditions, v1alpha1.ConditionAuthorized)
	return c != nil && c.Status == metav1.ConditionFalse
}

func (h *Handler) unavailable(w http.ResponseWriter) {
	h.count(outUnavailable)
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("Retry-After", "5")
	hd.Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("desktop is not running\n"))
}

func (h *Handler) startFailed(w http.ResponseWriter, err error, desktop string) {
	h.log.Error(err, "starting Desktop", "desktop", desktop)
	w.Header().Set("Retry-After", "5")
	h.errorPage(w, http.StatusServiceUnavailable, "Unavailable", "Your desktop could not be started. Try again in a moment.", outError)
}

// notRunning implements the table of docs/contract.md, 7.5.
func (h *Handler) notRunning(w http.ResponseWriter, r *http.Request, rt *route, d *v1alpha1.Desktop, user string, retried bool) {
	if !IsNavigation(r) {
		h.unavailable(w)
		return
	}
	ctx := r.Context()

	if d == nil {
		d = &v1alpha1.Desktop{
			ObjectMeta: metav1.ObjectMeta{Name: rt.name, Namespace: h.ns},
			Spec:       v1alpha1.DesktopSpec{User: user, PowerState: v1alpha1.PowerOn},
		}
		err := h.store.Create(ctx, d)
		switch {
		case err == nil:
			h.m.Starts.WithLabelValues(triggerCreate).Inc()
			h.bootPage(w, rt, d)
		case apierrors.IsAlreadyExists(err) && !retried:
			cur, gerr := h.store.Get(ctx, rt.name)
			if gerr != nil {
				// Not in the cache yet: someone just created it, so it is booting.
				h.bootPage(w, rt, d)
				return
			}
			if cur.Spec.User != user {
				h.collision(w)
				return
			}
			h.notRunning(w, r, rt, cur, user, true)
		default:
			h.log.Error(err, "creating Desktop", "desktop", rt.name)
			w.Header().Set("Retry-After", "5")
			h.errorPage(w, http.StatusServiceUnavailable, "Unavailable", "Your desktop could not be created. Try again in a moment.", outError)
		}
		return
	}

	if authorizedFalse(d) {
		h.errorPage(w, http.StatusForbidden, "No access", "Your account is not allowed to use a desktop.", outDenied)
		return
	}
	if d.Spec.PowerState != v1alpha1.PowerOn {
		if d.Status.LastStop != nil {
			h.offPage(w, rt, d)
			return
		}
		if err := h.store.PowerOn(ctx, d); err != nil {
			h.startFailed(w, err, d.Name)
			return
		}
		h.m.Starts.WithLabelValues(triggerNavigate).Inc()
	}
	h.bootPage(w, rt, d)
}

func (h *Handler) bootPage(w http.ResponseWriter, rt *route, d *v1alpha1.Desktop) {
	h.count(outBoot)
	stage := d.Status.Stage
	if stage == "" {
		stage = "Starting"
	}
	writePage(w, http.StatusOK, "boot", pageData{
		Title: "Starting your desktop", Stage: stage, Progress: d.Status.BootProgress,
		Message: d.Status.Message, EventsURL: rt.base + reservedPrefix + "/events",
	})
}

func (h *Handler) offPage(w http.ResponseWriter, rt *route, d *v1alpha1.Desktop) {
	token, err := newCSRFToken()
	if err != nil {
		h.errorPage(w, http.StatusInternalServerError, "Error", "Could not create the page.", outError)
		return
	}
	setCSRFCookie(w, token, h.secure)
	h.count(outOff)
	writePage(w, http.StatusOK, "off", pageData{
		Title: "Your desktop is off", Reason: stopWording(d.Status.LastStop.Reason),
		StartURL: rt.base + reservedPrefix + "/start", CSRF: token,
	})
}

func (h *Handler) serveStart(w http.ResponseWriter, r *http.Request, rt *route, d *v1alpha1.Desktop) {
	if d == nil {
		h.errorPage(w, http.StatusNotFound, "Not found", "This desktop does not exist.", outNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil || !checkCSRF(r) {
		h.errorPage(w, http.StatusForbidden, "Forbidden", "The request could not be verified. Reload the page and try again.", outDenied)
		return
	}
	if authorizedFalse(d) {
		h.errorPage(w, http.StatusForbidden, "No access", "Your account is not allowed to use a desktop.", outDenied)
		return
	}
	if d.Spec.PowerState != v1alpha1.PowerOn {
		if err := h.store.PowerOn(r.Context(), d); err != nil {
			h.startFailed(w, err, d.Name)
			return
		}
		h.m.Starts.WithLabelValues(triggerButton).Inc()
	}
	h.count(outStart)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, rt.base+"/", http.StatusSeeOther)
}
