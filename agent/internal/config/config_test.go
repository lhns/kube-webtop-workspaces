package config

import (
	"reflect"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c := FromEnv(env(nil))
	want := Config{
		TokenPath: "/run/webtop/sa/token", HTTPPort: 8080, StateDir: "/run/webtop", HomePaths: []string{}, RestoreMarker: "/persist/nix/reinstall/pending",
		PowerHookTimeout: 60 * time.Second, IdleThreshold: 60 * time.Second, CompositorTimeout: 3 * time.Minute,
	}
	if !reflect.DeepEqual(c, want) {
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
		"WEBTOP_HOME_PATHS":            "/home/bob /home/user",
	}))
	if c.ControllerURL != "http://c:8082" || c.HTTPPort != 9090 || c.PowerHookTimeout != 5*time.Second ||
		c.CompositorTimeout != 90*time.Second || c.WaylandSocket != "/tmp/wl" || c.User != "bob" || c.IdleThreshold != 2*time.Minute ||
		!reflect.DeepEqual(c.HomePaths, []string{"/home/bob", "/home/user"}) {
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
	if c.HTTPPort != 8080 || c.CompositorTimeout != 3*time.Minute || c.PowerHookTimeout != 60*time.Second || c.User != "" {
		t.Fatalf("got %+v", c)
	}
	if FromEnv(env(map[string]string{"WEBTOP_HTTP_PORT": "0"})).HTTPPort != 8080 {
		t.Fatal("port 0 accepted")
	}
}

func TestMaxAppScreens(t *testing.T) {
	for in, want := range map[string]int{"": 0, "2": 2, "0": 0, "-1": 0, "x": 0} {
		got := FromEnv(func(k string) string {
			if k == "WEBTOP_MAX_APP_SCREENS" {
				return in
			}
			return ""
		}).MaxAppScreens
		if got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}
