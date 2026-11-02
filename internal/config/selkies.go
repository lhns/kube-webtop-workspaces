// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"
)

// MaxSelkiesValueLen caps a Selkies value or the rendered title, in characters.
const MaxSelkiesValueLen = 256

// KeySelkiesUITitle is the setting the rendered title is delivered as.
const KeySelkiesUITitle = "SELKIES_UI_TITLE"

var selkiesKeyRE = regexp.MustCompile(`^SELKIES_[A-Z0-9_]+$`)

// TitleData is what desktop.title can reference. Data only: user-controlled text is never
// part of the template source.
type TitleData struct {
	User        string // the login (Desktop.spec.user)
	DisplayName string // falls back to User
	Desktop     string // the resource name
}

// RenderTitle executes desktop.title for the data and makes the result a single line. ok is false
// when no title is configured or the result is empty.
func (c *Config) RenderTitle(data TitleData) (title string, ok bool, err error) {
	if c.Desktop.Title == "" {
		return "", false, nil
	}
	t := c.titleTmpl
	if t == nil { // a Config built by hand rather than parsed
		if t, err = parseTitle(c.Desktop.Title); err != nil {
			return "", false, err
		}
	}
	var sb strings.Builder
	if err := t.Execute(&sb, data); err != nil {
		return "", false, err
	}
	title = SingleLine(sb.String())
	return title, title != "", nil
}

func parseTitle(src string) (*template.Template, error) {
	return template.New("title").Option("missingkey=error").Parse(src)
}

// SingleLine makes s safe for one KEY="value" line: invalid UTF-8 becomes U+FFFD, control
// characters (CR, LF, tab, DEL, C1) and the Unicode line separators become spaces, the ends are
// trimmed, and it is cut to MaxSelkiesValueLen characters.
func SingleLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	if utf8.RuneCountInString(s) > MaxSelkiesValueLen {
		s = string([]rune(s)[:MaxSelkiesValueLen])
	}
	return strings.TrimSpace(s)
}

// validateSelkies checks desktop.selkies and desktop.title. An invalid value is refused rather than
// cleaned: the admin wrote it and should see the mistake.
func (c *Config) validateSelkies() error {
	var errs []error
	for k, v := range c.Desktop.Selkies {
		switch {
		case !selkiesKeyRE.MatchString(k):
			errs = append(errs, fmt.Errorf("desktop.selkies %q: key must match %s", k, selkiesKeyRE))
		case k == "SELKIES_PORT":
			errs = append(errs, fmt.Errorf("desktop.selkies %q: the port is desktop.port", k))
		case SingleLine(v) != v:
			errs = append(errs, fmt.Errorf("desktop.selkies %q: value must be a single line of at most %d characters, without control characters or surrounding space", k, MaxSelkiesValueLen))
		}
	}
	if c.Desktop.Title != "" {
		t, err := parseTitle(c.Desktop.Title)
		if err == nil {
			c.titleTmpl = t
			_, _, err = c.RenderTitle(TitleData{User: "u", DisplayName: "d", Desktop: "n"})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("desktop.title: %w", err))
		}
	}
	return errors.Join(errs...)
}
