package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/api"
)

// Steady drives the phase after ready: a report every Interval, and at once
// when the client count or the sleep state changes (polled every Poll).
type Steady struct {
	Sender   Sender
	Clients  func(ctx context.Context) (int, error)  // connected web clients
	Idle     func() int                              // idle seconds
	Asleep   func() bool                             // optional: frozen by Sleep
	Logout   func() (at string, pref *bool, ok bool) // optional: the session logged out
	Poll     time.Duration
	Interval time.Duration
}

func (s *Steady) Run(ctx context.Context) {
	var (
		clients  int
		sentAt   time.Time
		sentOnce bool
		sentN    int
		sentZ    bool
		sentOut  string
		warnedAt time.Time
	)
	t := time.NewTicker(s.Poll)
	defer t.Stop()
	for {
		if n, err := s.Clients(ctx); err != nil {
			if time.Since(warnedAt) > time.Minute {
				log.Printf("steady: sessions: %v", err)
				warnedAt = time.Now()
			}
		} else {
			clients = n
		}
		asleep := s.Asleep != nil && s.Asleep()
		var outAt string
		var outPref *bool
		if s.Logout != nil {
			if at, pref, ok := s.Logout(); ok {
				outAt, outPref = at, pref
			}
		}
		if !sentOnce || clients != sentN || asleep != sentZ || outAt != sentOut || time.Since(sentAt) >= s.Interval {
			idle := s.Idle()
			r := api.Report{ConnectedClients: &clients, IdleSeconds: &idle}
			if s.Asleep != nil {
				r.Asleep = &asleep
			}
			if outAt != "" {
				r.LoggedOutAt, r.LogoutPref = &outAt, outPref
			}
			if err := s.Sender.Report(ctx, r); err != nil {
				log.Printf("steady: report: %v", err) // retried on the next poll
			} else {
				sentOnce, sentN, sentZ, sentOut, sentAt = true, clients, asleep, outAt, time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SelkiesSessions returns a func listing the ids of the connected Selkies sessions. A session
// without an id field is listed as "" (callers then only get the count).
func SelkiesSessions(port int) func(ctx context.Context) ([]string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/api/sessions", port)
	hc := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context) ([]string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		var v struct {
			Sessions []struct {
				ID any `json:"id"`
			} `json:"sessions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			return nil, err
		}
		ids := make([]string, len(v.Sessions))
		for i, s := range v.Sessions {
			if s.ID != nil {
				ids[i] = fmt.Sprint(s.ID)
			}
		}
		return ids, nil
	}
}

// SelkiesClients returns a Clients func reading the session count from Selkies.
func SelkiesClients(port int) func(ctx context.Context) (int, error) {
	sessions := SelkiesSessions(port)
	return func(ctx context.Context) (int, error) {
		ids, err := sessions(ctx)
		return len(ids), err
	}
}
