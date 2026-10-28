package powerhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/api"
)

var now = time.Unix(10_000, 0)

func opts(t *testing.T) Options {
	return Options{
		StateDir: t.TempDir(), IdleThreshold: 60 * time.Second,
		Timeout: 2 * time.Second, BackoffStart: time.Millisecond, BackoffMax: 4 * time.Millisecond,
		Now: func() time.Time { return now },
	}
}

func write(t *testing.T, o Options, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(o.StateDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRequestMapping(t *testing.T) {
	idle := func(ago time.Duration) func(*testing.T, Options) {
		return func(t *testing.T, o Options) {
			write(t, o, "idle-since", strconv.FormatInt(now.Add(-ago).Unix(), 10)+"\n")
		}
	}
	tests := []struct {
		name, instance string
		setup          func(*testing.T, Options)
		want           api.PowerRequest
		ok             bool
	}{
		{"halt", "halt", nil, api.PowerRequest{Action: "stop", Reason: "kube-stop"}, true},
		{"reboot", "reboot", nil, api.PowerRequest{Action: "reboot"}, true},
		{"poweroff user", "poweroff", nil, api.PowerRequest{Action: "stop", Reason: "user"}, true},
		{"poweroff empty idle file", "poweroff", func(t *testing.T, o Options) { write(t, o, "idle-since", "") },
			api.PowerRequest{Action: "stop", Reason: "user"}, true},
		{"poweroff idle below", "poweroff", idle(59 * time.Second), api.PowerRequest{Action: "stop", Reason: "user"}, true},
		{"poweroff idle at threshold", "poweroff", idle(60 * time.Second), api.PowerRequest{Action: "stop", Reason: "idle"}, true},
		{"poweroff suspend wins", "poweroff", func(t *testing.T, o Options) {
			idle(time.Hour)(t, o)
			write(t, o, "stop-reason", "suspend\n")
		}, api.PowerRequest{Action: "suspend", Reason: "suspend"}, true},
		{"unknown", "kexec", nil, api.PowerRequest{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := opts(t)
			if tc.setup != nil {
				tc.setup(t, o)
			}
			got, ok := Request(tc.instance, o)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %+v,%v want %+v,%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

type server struct {
	*httptest.Server
	mu    sync.Mutex
	calls int
	auths []string
	body  api.PowerRequest
}

func newServer(t *testing.T, statuses ...int) *server {
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		i := min(s.calls, len(statuses)-1)
		s.calls++
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		json.NewDecoder(r.Body).Decode(&s.body)
		w.WriteHeader(statuses[i])
		w.Write([]byte("body"))
	}))
	t.Cleanup(s.Close)
	return s
}

func client(t *testing.T, url string) (*api.Client, string) {
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte(" tok1\n"), 0o644)
	return api.New(url, tok), tok
}

func TestRetries(t *testing.T) {
	tests := []struct {
		name     string
		statuses []int
		calls    int
	}{
		{"5xx then ok", []int{500, 503, 200}, 3},
		{"409 then ok", []int{409, 200}, 2},
		{"429 and 401 then ok", []int{429, 401, 200}, 3},
		{"400 permanent", []int{400, 200}, 1},
		{"403 permanent", []int{403, 200}, 1},
		{"404 permanent", []int{404, 200}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, tc.statuses...)
			c, _ := client(t, s.URL)
			Run(context.Background(), c, "halt", opts(t))
			if s.calls != tc.calls {
				t.Fatalf("calls = %d, want %d", s.calls, tc.calls)
			}
			if s.body.Reason != "kube-stop" || s.auths[0] != "Bearer tok1" {
				t.Fatalf("body %+v auth %q", s.body, s.auths[0])
			}
		})
	}
}

func TestTimeoutRespected(t *testing.T) {
	s := newServer(t, 500)
	c, _ := client(t, s.URL)
	o := opts(t)
	o.Timeout = 50 * time.Millisecond
	start := time.Now()
	Run(context.Background(), c, "halt", o)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
	if s.calls < 2 {
		t.Fatalf("calls = %d", s.calls)
	}
}

func TestTokenReReadPerAttempt(t *testing.T) {
	s := newServer(t, 401, 200)
	c, tok := client(t, s.URL)
	c.HTTP.Transport = rewriteToken(tok)
	Run(context.Background(), c, "reboot", opts(t))
	if len(s.auths) != 2 || s.auths[0] != "Bearer tok1" || s.auths[1] != "Bearer tok2" {
		t.Fatalf("auths = %v", s.auths)
	}
}

// rewriteToken changes the token file after the first request completes.
type rewriteToken string

func (p rewriteToken) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	os.WriteFile(string(p), []byte("tok2\n"), 0o644)
	return resp, err
}

func TestDisabledWithoutURL(t *testing.T) {
	c, _ := client(t, "")
	Run(context.Background(), c, "halt", opts(t)) // must return without posting
}
