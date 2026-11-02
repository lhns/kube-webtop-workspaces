// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

// appPrefix is the gateway-owned path "/apps/<name>": one desktop file id streamed on its own
// (docs/contract.md, 7.6). Only a top-level GET is handled here; anything else under it goes to
// the desktop like any other path.
const appPrefix = "/apps/"

// appFragment makes the Selkies page a second-display page: it grows the SELKIES-2 screen.
const appFragment = "#display2-right"

// appNameRE is a desktop file id, the same rule the agent applies.
var appNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// appsOverview is the form value of the off page's "app" field that returns to <base>/apps/. It
// is not a valid app name, so it cannot be mistaken for one.
const appsOverview = "*"

// maxIconBytes bounds an icon relayed from the agent (the agent caps files at 1 MiB).
const maxIconBytes = 1 << 20

// ErrAppNotFound means the desktop has no such application.
var ErrAppNotFound = errors.New("no such application")

// AppInfo is an entry of the agent's application list.
type AppInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Icon    bool   `json:"icon"`
	Running bool   `json:"running"`
}

// AppIcon is an icon image as the agent served it.
type AppIcon struct {
	ContentType  string
	CacheControl string
	Body         []byte
}

// AppOpener asks the in-pod agent to list applications and to show one on the second screen.
type AppOpener interface {
	// List returns the desktop's applications sorted by name.
	List(ctx context.Context, base *url.URL) ([]AppInfo, error)
	// Icon returns ErrAppNotFound if the application or its icon does not exist.
	Icon(ctx context.Context, base *url.URL, app string) (*AppIcon, error)
	// Open returns ErrAppNotFound for an unknown application, any other error when the agent is
	// unreachable or failed. base is the desktop's Status.URL.
	Open(ctx context.Context, base *url.URL, app string) error
}

// agentClient is the HTTP AppOpener.
type agentClient struct {
	port int
	http *http.Client
}

func newAgentClient(rt http.RoundTripper) *agentClient {
	return &agentClient{port: names.AgentPort, http: &http.Client{
		Timeout:   30 * time.Second, // placing loads a KWin script and may start an app
		Transport: rt,
	}}
}

func (c *agentClient) Open(ctx context.Context, base *url.URL, app string) error {
	u := url.URL{Scheme: base.Scheme, Host: hostWithPort(base, c.port), Path: "/api/v1/apps/" + app}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(nil))
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrAppNotFound
	}
	return fmt.Errorf("agent answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}

func (c *agentClient) get(ctx context.Context, base *url.URL, p string) (*http.Response, error) {
	u := url.URL{Scheme: base.Scheme, Host: hostWithPort(base, c.port), Path: p}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

func (c *agentClient) List(ctx context.Context, base *url.URL) ([]AppInfo, error) {
	resp, err := c.get(ctx, base, "/api/v1/apps")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent answered HTTP %d", resp.StatusCode)
	}
	var list []AppInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("agent app list: %w", err)
	}
	return list, nil
}

func (c *agentClient) Icon(ctx context.Context, base *url.URL, app string) (*AppIcon, error) {
	resp, err := c.get(ctx, base, "/api/v1/apps/"+app+"/icon")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrAppNotFound
	default:
		return nil, fmt.Errorf("agent answered HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxIconBytes {
		return nil, errors.New("agent icon too large")
	}
	return &AppIcon{ContentType: resp.Header.Get("Content-Type"), CacheControl: resp.Header.Get("Cache-Control"), Body: b}, nil
}

// hostWithPort is base's host with the port replaced.
func hostWithPort(base *url.URL, port int) string {
	return net.JoinHostPort(base.Hostname(), strconv.Itoa(port))
}

// appIconName extracts <id> from a cleaned "/.webtop/apps/<id>/icon" path.
func appIconName(cleaned string) (string, bool) {
	n, ok := strings.CutPrefix(cleaned, reservedPrefix+"/apps/")
	if !ok {
		return "", false
	}
	n, ok = strings.CutSuffix(n, "/icon")
	if !ok || !appNameRE.MatchString(n) {
		return "", false
	}
	return n, true
}

// appName extracts <name> from a cleaned path below the route base; ok is false for any other path.
func appName(rest string) (string, bool) {
	n, ok := strings.CutPrefix(rest, appPrefix)
	if !ok || !appNameRE.MatchString(n) {
		return "", false
	}
	return n, true
}

// serveApp handles GET /apps/<name> of a Running desktop: the agent places and starts the app,
// then the browser goes to the Selkies page as a second-display page.
func (h *Handler) serveApp(w http.ResponseWriter, r *http.Request, rt *route, d *v1alpha1.Desktop, app string) {
	base, err := parseTarget(d.Status.URL)
	if err != nil {
		h.errorPage(w, http.StatusBadGateway, "Desktop unreachable", "The desktop has no valid address.", outError)
		return
	}
	switch err := h.agent.Open(r.Context(), base, app); {
	case err == nil:
	case errors.Is(err, ErrAppNotFound):
		h.errorPage(w, http.StatusNotFound, "App not found", "This desktop has no application with the ID "+app+".", outNotFound)
		return
	default:
		h.log.Error(err, "opening app", "desktop", d.Name, "app", app)
		w.Header().Set("Retry-After", "5")
		h.errorPage(w, http.StatusServiceUnavailable, "Unavailable", "The desktop could not show the app. Try again in a moment.", outError)
		return
	}
	h.count(outApp)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, rt.base+"/"+appFragment, http.StatusSeeOther)
}

// serveAppsOverview handles GET <base>/apps/ of a Running desktop: a page listing the
// applications, each linking to its single-app route.
func (h *Handler) serveAppsOverview(w http.ResponseWriter, r *http.Request, rt *route, d *v1alpha1.Desktop) {
	base, err := parseTarget(d.Status.URL)
	if err != nil {
		h.errorPage(w, http.StatusBadGateway, "Desktop unreachable", "The desktop has no valid address.", outError)
		return
	}
	list, err := h.agent.List(r.Context(), base)
	if err != nil {
		h.log.Error(err, "listing apps", "desktop", d.Name)
		w.Header().Set("Retry-After", "5")
		h.errorPage(w, http.StatusServiceUnavailable, "Unavailable", "The desktop could not list its apps. Try again in a moment.", outError)
		return
	}
	views := make([]appView, 0, len(list))
	for _, a := range list {
		if !appNameRE.MatchString(a.ID) { // the agent is trusted, but the id ends up in URLs
			continue
		}
		v := appView{Name: a.Name, Href: rt.base + appPrefix + a.ID, Running: a.Running}
		if v.Name == "" {
			v.Name = a.ID
		}
		v.Initial = strings.ToUpper(string([]rune(v.Name)[:1]))
		if a.Icon {
			v.IconURL = rt.base + reservedPrefix + "/apps/" + a.ID + "/icon"
		}
		views = append(views, v)
	}
	h.count(outApps)
	writePage(w, http.StatusOK, "apps", pageData{Title: "Apps", Apps: views, Wide: true})
}

// serveAppIcon relays an icon for the overview page. The agent's file is untrusted content: only
// PNG and SVG pass, and an SVG gets a CSP that forbids its scripts if it is opened on its own.
func (h *Handler) serveAppIcon(w http.ResponseWriter, r *http.Request, d *v1alpha1.Desktop, app string) {
	if d == nil || !isReady(d) {
		h.unavailable(w)
		return
	}
	base, err := parseTarget(d.Status.URL)
	if err != nil {
		h.errorPage(w, http.StatusBadGateway, "Desktop unreachable", "The desktop has no valid address.", outError)
		return
	}
	icon, err := h.agent.Icon(r.Context(), base, app)
	if errors.Is(err, ErrAppNotFound) {
		h.errorPage(w, http.StatusNotFound, "Not found", "There is no icon here.", outNotFound)
		return
	}
	if err != nil {
		h.log.Error(err, "fetching app icon", "desktop", d.Name, "app", app)
		w.Header().Set("Retry-After", "5")
		h.errorPage(w, http.StatusServiceUnavailable, "Unavailable", "The desktop could not provide the icon.", outError)
		return
	}
	ct, _, _ := mime.ParseMediaType(icon.ContentType)
	if ct != "image/png" && ct != "image/svg+xml" {
		h.errorPage(w, http.StatusBadGateway, "Desktop unreachable", "The desktop sent an unusable icon.", outError)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", ct)
	hd.Set("X-Content-Type-Options", "nosniff")
	if ct == "image/svg+xml" {
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	}
	if icon.CacheControl != "" {
		hd.Set("Cache-Control", icon.CacheControl)
	}
	hd.Set("Content-Length", strconv.Itoa(len(icon.Body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(icon.Body)
	}
}

func parseTarget(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, errors.New("no valid desktop address")
	}
	return u, nil
}

// returnApp is the "app" form field of the off page: the app the visitor asked for (or
// appsOverview), so Start returns to /apps/<name> (or /apps/). Anything that is not a valid name is ignored.
func returnApp(r *http.Request) string {
	if a := r.PostForm.Get("app"); a == appsOverview || appNameRE.MatchString(a) {
		return a
	}
	return ""
}
