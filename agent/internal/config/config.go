// Package config reads the agent's settings from the environment.
package config

import (
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ControllerURL    string // empty disables the API
	TokenPath        string
	HTTPPort         int
	StateDir         string
	WaylandSocket    string // empty = discover from the user manager
	User             string
	PowerHookTimeout time.Duration
	IdleThreshold    time.Duration
}

// FromEnv builds a Config; getenv is os.Getenv in production.
func FromEnv(getenv func(string) string) Config {
	str := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	dur := func(k string, def time.Duration) time.Duration {
		if d, err := time.ParseDuration(str(k, "")); err == nil && d > 0 {
			return d
		}
		return def
	}
	port := 8080
	if p, err := strconv.Atoi(str("WEBTOP_HTTP_PORT", "")); err == nil && p > 0 {
		port = p
	}
	return Config{
		ControllerURL:    strings.TrimRight(str("WEBTOP_CONTROLLER_URL", ""), "/"),
		TokenPath:        str("WEBTOP_TOKEN_PATH", "/run/webtop/sa/token"),
		HTTPPort:         port,
		StateDir:         str("WEBTOP_AGENT_STATE_DIR", "/run/webtop"),
		WaylandSocket:    str("WEBTOP_AGENT_WAYLAND_SOCKET", ""),
		User:             str("WEBTOP_AGENT_USER", "user"),
		PowerHookTimeout: dur("WEBTOP_POWER_HOOK_TIMEOUT", 60*time.Second),
		IdleThreshold:    dur("WEBTOP_IDLE_REASON_THRESHOLD", 60*time.Second),
	}
}
