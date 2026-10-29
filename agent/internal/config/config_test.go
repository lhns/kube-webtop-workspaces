package config

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c := FromEnv(env(nil))
	want := Config{
		TokenPath: "/run/webtop/sa/token", HTTPPort: 8080, StateDir: "/run/webtop", User: "user",
		PowerHookTimeout: 60 * time.Second, IdleThreshold: 60 * time.Second, CompositorTimeout: 3 * time.Minute,
	}
	if c != want {
		t.Fatalf("got %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c := FromEnv(env(map[string]string{
		"WEBTOP_CONTROLLER_URL":        " http://c:8082/// ",
		"WEBTOP_HTTP_PORT":             "9090",
		"WEBTOP_POWER_HOOK_TIMEOUT":    "5s",
		"WEBTOP_COMPOSITOR_TIMEOUT":    "90s",
		"WEBTOP_AGENT_WAYLAND_SOCKET":  "/tmp/wl",
		"WEBTOP_AGENT_USER":            "bob",
		"WEBTOP_IDLE_REASON_THRESHOLD": "2m",
	}))
	if c.ControllerURL != "http://c:8082" || c.HTTPPort != 9090 || c.PowerHookTimeout != 5*time.Second ||
		c.CompositorTimeout != 90*time.Second || c.WaylandSocket != "/tmp/wl" || c.User != "bob" || c.IdleThreshold != 2*time.Minute {
		t.Fatalf("got %+v", c)
	}
}

func TestInvalidValuesFallBack(t *testing.T) {
	c := FromEnv(env(map[string]string{
		"WEBTOP_HTTP_PORT":          "abc",
		"WEBTOP_COMPOSITOR_TIMEOUT": "-5s",
		"WEBTOP_POWER_HOOK_TIMEOUT": "soon",
		"WEBTOP_AGENT_USER":         "   ",
	}))
	if c.HTTPPort != 8080 || c.CompositorTimeout != 3*time.Minute || c.PowerHookTimeout != 60*time.Second || c.User != "user" {
		t.Fatalf("got %+v", c)
	}
	if FromEnv(env(map[string]string{"WEBTOP_HTTP_PORT": "0"})).HTTPPort != 8080 {
		t.Fatal("port 0 accepted")
	}
}
