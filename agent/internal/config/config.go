// Package config reads the agent's settings from the environment.
package config

import (
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ControllerURL     string // empty disables the API
	TokenPath         string
	HTTPPort          int
	StateDir          string
	WaylandSocket     string   // empty = discover from the user manager
	User              string   // empty = uid 1000
	HomePaths         []string // mounted by webtop-init; reported to the controller
	RestoreMarker     string   // exists while an image update's software restore is pending
	PowerHookTimeout  time.Duration
	IdleThreshold     time.Duration
	CompositorTimeout time.Duration
	SuspendMode       string // "freeze" (default) or "poweroff"
	MaxAppScreens     int    // WEBTOP_MAX_APP_SCREENS; 0 when unset or invalid
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
	maxScreens := 0
	if n, err := strconv.Atoi(str("WEBTOP_MAX_APP_SCREENS", "")); err == nil && n > 0 {
		maxScreens = n
	}
	return Config{
		MaxAppScreens:     maxScreens,
		ControllerURL:     strings.TrimRight(str("WEBTOP_CONTROLLER_URL", ""), "/"),
		TokenPath:         str("WEBTOP_TOKEN_PATH", "/run/webtop/sa/token"),
		HTTPPort:          port,
		StateDir:          str("WEBTOP_AGENT_STATE_DIR", "/run/webtop"),
		WaylandSocket:     str("WEBTOP_AGENT_WAYLAND_SOCKET", ""),
		User:              str("WEBTOP_AGENT_USER", ""),
		HomePaths:         strings.Fields(getenv("WEBTOP_HOME_PATHS")),
		RestoreMarker:     str("WEBTOP_RESTORE_MARKER", "/persist/nix/reinstall/pending"),
		PowerHookTimeout:  dur("WEBTOP_POWER_HOOK_TIMEOUT", 60*time.Second),
		IdleThreshold:     dur("WEBTOP_IDLE_REASON_THRESHOLD", 60*time.Second),
		CompositorTimeout: dur("WEBTOP_COMPOSITOR_TIMEOUT", 3*time.Minute),
		SuspendMode:       suspendMode(str("WEBTOP_SUSPEND_MODE", "")),
	}
}

// suspendMode is "poweroff" only when asked for; anything else freezes.
func suspendMode(v string) string {
	if strings.EqualFold(v, "poweroff") {
		return "poweroff"
	}
	return "freeze"
}
