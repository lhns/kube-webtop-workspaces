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
// when the client count changes (polled every Poll).
type Steady struct {
	Sender   Sender
	Clients  func(ctx context.Context) (int, error) // connected web clients
	Idle     func() int                             // idle seconds
	Poll     time.Duration
	Interval time.Duration
}

func (s *Steady) Run(ctx context.Context) {
	var (
		clients  int
		sentAt   time.Time
		sentOnce bool
		sentN    int
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
		if !sentOnce || clients != sentN || time.Since(sentAt) >= s.Interval {
			idle := s.Idle()
			if err := s.Sender.Report(ctx, api.Report{ConnectedClients: &clients, IdleSeconds: &idle}); err != nil {
				log.Printf("steady: report: %v", err) // retried on the next poll
			} else {
				sentOnce, sentN, sentAt = true, clients, time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SelkiesClients returns a Clients func reading the session count from Selkies.
func SelkiesClients(port int) func(ctx context.Context) (int, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/api/sessions", port)
	hc := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		var v struct {
			Sessions []json.RawMessage `json:"sessions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			return 0, err
		}
		return len(v.Sessions), nil
	}
}
