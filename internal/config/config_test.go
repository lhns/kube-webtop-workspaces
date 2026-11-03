// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte("namespace: ws\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Names.Prefix != "desktop-" || c.Desktop.Port != 8080 || c.LDAP.MemberAttribute != "memberOf" ||
		!*c.Controller.RequireAgentReady || len(c.Controller.CleanExitCodes) != 1 || c.Storage.MountPath != "/persist" ||
		c.Controller.MaxConcurrentReconciles != 4 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.ValidateController() == nil {
		t.Fatal("controller config without image/url must fail")
	}
}

func TestRejects(t *testing.T) {
	for _, in := range []string{
		"namespace: ws\nnames: {prefix: 1x}\n",
		"namespace: ws\nunknownField: 1\n",
		"namespace: ws\nldap: {url: 'ldap://x', groupDN: cn=g}\n",
		"namespace: ws\nldap: {errorPolicy: maybe}\n",
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) succeeded", in)
		}
	}
}

func TestSelkiesValidation(t *testing.T) {
	base := "namespace: ns\ndesktop:\n"
	for name, tc := range map[string]struct {
		yaml string
		ok   bool
	}{
		"plain":            {"  selkies:\n    SELKIES_FRAMERATE: \"60\"\n    SELKIES_EMPTY: \"\"\n", true},
		"quote and dollar": {"  selkies:\n    SELKIES_X: 'a\"b$c\\d=e'\n", true},
		"lowercase key":    {"  selkies:\n    selkies_x: a\n", false},
		"foreign key":      {"  selkies:\n    PATH: a\n", false},
		"port":             {"  selkies:\n    SELKIES_PORT: \"1\"\n", false},
		"newline":          {"  selkies:\n    SELKIES_X: \"a\\nSELKIES_Y=b\"\n", false},
		"carriage return":  {"  selkies:\n    SELKIES_X: \"a\\rb\"\n", false},
		"nul":              {"  selkies:\n    SELKIES_X: \"a\\u0000b\"\n", false},
		"tab":              {"  selkies:\n    SELKIES_X: \"a\\tb\"\n", false},
		"too long":         {"  selkies:\n    SELKIES_X: " + strings.Repeat("a", 257) + "\n", false},
		"title":            {"  title: '{{ .User }} - {{ .DisplayName }} - {{ .Desktop }}'\n", true},
		"title bad field":  {"  title: '{{ .Nope }}'\n", false},
		"title bad syntax": {"  title: '{{ .User'\n", false},
	} {
		_, err := Parse([]byte(base + tc.yaml))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
	}
}
