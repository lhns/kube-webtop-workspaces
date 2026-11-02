// SPDX-License-Identifier: Apache-2.0

package names

import (
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{
		"alice":                        "alice",
		"Bob.Smith":                    "bob-smith",
		"--weird__name--":              "weird-name",
		"a..b":                         "a-b",
		"Ünïcode":                      "n-code",
		"user@example.com":             "user-example-com",
		"123":                          "123",
		strings.Repeat("a", 45):        strings.Repeat("a", 40),
		strings.Repeat("a", 39) + ".b": strings.Repeat("a", 39),
	} {
		got, err := Sanitize(in)
		if err != nil || got != want {
			t.Errorf("Sanitize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "---", "äöü", "..."} {
		if got, err := Sanitize(in); err == nil {
			t.Errorf("Sanitize(%q) = %q; want error", in, got)
		}
	}
}

func TestValidLogin(t *testing.T) {
	for in, want := range map[string]bool{
		"alice": true, "bob-smith": true, "a1": true, strings.Repeat("a", 40): true,
		"": false, "123": false, "1abc": false, "-a": false, "Alice": false, "a_b": false, "a.b": false,
		strings.Repeat("a", 41): false,
	} {
		if got := ValidLogin(in); got != want {
			t.Errorf("ValidLogin(%q) = %v; want %v", in, got, want)
		}
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"Pierre Kisters":           "Pierre Kisters",
		"  Jürgen Müller ":         "Jürgen Müller",
		"Smith, John":              "Smith John",
		"a:b\nc\td\"e'f\\g":        "abcdefg",
		"\x00":                     "",
		strings.Repeat("é", 130):   strings.Repeat("é", MaxDisplayName),
		"x" + string(rune(0xFFFD)): "x",
	} {
		if got := DisplayName(in); got != want {
			t.Errorf("DisplayName(%q) = %q; want %q", in, got, want)
		}
	}
}
