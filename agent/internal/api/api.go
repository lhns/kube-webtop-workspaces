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
	"time"
)

type Client struct {
	URL       string
	TokenPath string
	HTTP      *http.Client
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

// Report fields are pointers so absent ones are omitted (the controller leaves them unchanged).
type Report struct {
	Stage            string `json:"stage,omitempty"`
	BootProgress     *int   `json:"bootProgress,omitempty"`
	Ready            *bool  `json:"ready,omitempty"`
	ConnectedClients *int   `json:"connectedClients,omitempty"`
	IdleSeconds      *int   `json:"idleSeconds,omitempty"`
}

func (c *Client) Power(ctx context.Context, r PowerRequest) error {
	return c.post(ctx, "/api/v1/power", r, http.StatusOK)
}

func (c *Client) Report(ctx context.Context, r Report) error {
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
