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
