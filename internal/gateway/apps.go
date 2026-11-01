// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

// ErrAppNotFound means the desktop has no such application.
var ErrAppNotFound = errors.New("no such application")

// AppOpener asks the in-pod agent to show an application on the second screen.
type AppOpener interface {
	// Open returns ErrAppNotFound for an unknown application, any other error when the agent is
	// unreachable or failed. base is the desktop's Status.URL.
	Open(ctx context.Context, base *url.URL, app string) error
}

// agentClient is the HTTP AppOpener.
type agentClient struct {
	port int
	http *http.Client
}

func newAgentClient() *agentClient {
	return &agentClient{port: names.AgentPort, http: &http.Client{
		Timeout: 30 * time.Second, // placing loads a KWin script and may start an app
		// In-cluster target: never through HTTP(S)_PROXY from the environment.
		Transport: &http.Transport{Proxy: nil},
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

// hostWithPort is base's host with the port replaced.
func hostWithPort(base *url.URL, port int) string {
	h := base.Hostname()
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	return h + ":" + strconv.Itoa(port)
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

func parseTarget(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, errors.New("no valid desktop address")
	}
	return u, nil
}

// returnApp is the "app" form field of the off page: the app the visitor asked for, so Start
// returns to /apps/<name>. Anything that is not a valid name is ignored.
func returnApp(r *http.Request) string {
	if a := r.PostForm.Get("app"); appNameRE.MatchString(a) {
		return a
	}
	return ""
}
