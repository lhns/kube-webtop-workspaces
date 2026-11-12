// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"io/fs"
	"testing"
	"unicode/utf8"
)

// Pages are served as UTF-8; a stray legacy byte shows as a replacement character.
func TestTemplatesAreUTF8(t *testing.T) {
	err := fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(b) {
			t.Errorf("%s is not valid UTF-8", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
