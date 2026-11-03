// Package api is the client for the controller's power/report API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Client struct {
	URL       string
	TokenPath string
	HTTP      *http.Client
	// LoginName and HomePaths go with every report (they are fixed for the pod's lifetime).
	LoginName string
	HomePaths []string
	// LoginError is why the account could not be renamed ("" if it could).
	LoginError string

	mu      sync.Mutex
	restore *SoftwareRestore
}

func New(url, tokenPath string) *Client {
	return &Client{URL: url, TokenPath: tokenPath, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Enabled reports whether a controller URL is configured.
func (c *Client) Enabled() bool { return c.URL != "" }

// StatusError is an unexpected HTTP status.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

type PowerRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// Report fields left empty are omitted, and the controller leaves them unchanged.
type Report struct {
	Stage            string `json:"stage,omitempty"`
	BootProgress     *int   `json:"bootProgress,omitempty"`
	Ready            *bool  `json:"ready,omitempty"`
	ConnectedClients *int   `json:"connectedClients,omitempty"`
	IdleSeconds      *int   `json:"idleSeconds,omitempty"`
	Asleep           *bool  `json:"asleep,omitempty"`
	// Set by Client.Report.
	LoginName string   `json:"loginName,omitempty"`
	HomePaths []string `json:"homePaths,omitempty"`
	// Sent with LoginName, empty included, so it clears an earlier pod's.
	LoginError *string `json:"loginError,omitempty"`
	// Set by Client.Report once SetRestore was called.
	SoftwareRestore *SoftwareRestore `json:"softwareRestore,omitempty"`
}

// SoftwareRestore is the progress of the restore after an image update.
type SoftwareRestore struct {
	Active bool `json:"active"`
	Done   int  `json:"done"`
	Total  int  `json:"total"`
	Failed bool `json:"failed"`
}

// SetRestore sets what the following reports carry; it reports whether that changed.
func (c *Client) SetRestore(r SoftwareRestore) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := c.restore == nil || *c.restore != r
	c.restore = &r
	return changed
}

func (c *Client) Power(ctx context.Context, r PowerRequest) error {
	return c.post(ctx, "/api/v1/power", r, http.StatusOK)
}

func (c *Client) Report(ctx context.Context, r Report) error {
	r.LoginName, r.HomePaths = c.LoginName, c.HomePaths
	if c.LoginName != "" {
		r.LoginError = &c.LoginError
	}
	c.mu.Lock()
	r.SoftwareRestore = c.restore
	c.mu.Unlock()
	return c.post(ctx, "/api/v1/report", r, http.StatusNoContent)
}

func (c *Client) post(ctx context.Context, path string, body any, want int) error {
	// kubelet rotates the token, so read it on every request.
	tok, err := os.ReadFile(c.TokenPath)
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != want {
		return &StatusError{resp.StatusCode, strings.TrimSpace(string(b))}
	}
	return nil
}
