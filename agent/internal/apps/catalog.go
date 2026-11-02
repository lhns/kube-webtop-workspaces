package apps

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// maxIcon caps an icon file: it is served to the browser and relayed by the gateway.
const maxIcon = 1 << 20

// App is one entry of the overview (docs/contract.md, 7.7).
type App struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Icon    bool   `json:"icon"`    // GET /api/v1/apps/<id>/icon has an image
	Running bool   `json:"running"` // an active app-<id> unit exists
}

// iconNameRE is a themed icon name; no slash, so it cannot leave the icon directories.
var iconNameRE = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// dataDirList is the user's data directories (Launcher.DataDirs if set).
func (l *Launcher) dataDirList(ctx context.Context) []string {
	if l.DataDirs != nil {
		return l.DataDirs
	}
	env := ""
	if out, err := l.Session.Systemctl(ctx, "show-environment"); err == nil {
		env = string(out)
	}
	return dataDirs(env, l.Home)
}

// List returns the applications of the data dirs' top-level applications/*.desktop, sorted by
// name. Earlier dirs win for the same id, also when their entry is hidden (XDG precedence).
// Desktop file ids of files in subdirectories (dir-file.desktop) are not listed: Open does not
// find them either.
func (l *Launcher) List(ctx context.Context) ([]App, error) {
	dirs := l.dataDirList(ctx)
	units, err := l.Session.ActiveUnits(ctx)
	if err != nil {
		log.Printf("apps: list running units: %v", err) // the list is still useful
	}
	out := []App{}
	seen := map[string]bool{}
	for _, d := range dirs {
		files, err := os.ReadDir(filepath.Join(d, "applications"))
		if err != nil {
			continue
		}
		for _, f := range files {
			id, ok := strings.CutSuffix(f.Name(), ".desktop")
			if !ok || !NameRE.MatchString(id) || seen[id] {
				continue
			}
			fields, ok := parseDesktop(filepath.Join(d, "applications", f.Name()))
			if !ok {
				continue
			}
			seen[id] = true
			if !listed(fields) {
				continue
			}
			p, _ := resolveIcon(dirs, fields["Icon"])
			out = append(out, App{
				ID: id, Name: fields["Name"], Icon: p != "",
				Running: slices.ContainsFunc(units, func(u string) bool { return unitIsApp(u, id) }),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Icon returns the icon file of the app and its Content-Type; ErrNotFound if the app or its
// icon does not exist.
func (l *Launcher) Icon(ctx context.Context, name string) (string, string, error) {
	if !NameRE.MatchString(name) {
		return "", "", ErrInvalidName
	}
	dirs := l.dataDirList(ctx)
	for _, d := range dirs {
		if fields, ok := parseDesktop(filepath.Join(d, "applications", name+".desktop")); ok {
			if p, ct := resolveIcon(dirs, fields["Icon"]); p != "" {
				return p, ct, nil
			}
			break
		}
	}
	return "", "", ErrNotFound
}

// parseDesktop reads the unlocalized keys of the [Desktop Entry] group; ok is false if the file
// cannot be read.
func parseDesktop(file string) (map[string]string, bool) {
	f, err := os.Open(file)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fields := map[string]string{}
	in := false
	sc := bufio.NewScanner(io.LimitReader(f, 256<<10))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == '#':
		case line[0] == '[':
			in = line == "[Desktop Entry]"
		case in:
			if k, v, ok := strings.Cut(line, "="); ok {
				k = strings.TrimSpace(k)
				if !strings.Contains(k, "[") { // Name[de]: localized
					fields[k] = strings.TrimSpace(v)
				}
			}
		}
	}
	return fields, true
}

// listed applies the filters of an application menu on KDE.
func listed(f map[string]string) bool {
	if f["Type"] != "Application" || f["Name"] == "" || f["NoDisplay"] == "true" || f["Hidden"] == "true" {
		return false
	}
	if only := f["OnlyShowIn"]; only != "" && !slices.Contains(strings.Split(only, ";"), "KDE") {
		return false
	}
	return !slices.Contains(strings.Split(f["NotShowIn"], ";"), "KDE")
}

// resolveIcon finds the image file for an Icon= value: an absolute path inside a data dir, or a
// name in the breeze/hicolor themes, then pixmaps. Only PNG and SVG files up to maxIcon count.
func resolveIcon(dirs []string, icon string) (file, contentType string) {
	if icon == "" {
		return "", ""
	}
	if filepath.IsAbs(icon) {
		icon = filepath.Clean(icon)
		for _, d := range dirs {
			if rel, err := filepath.Rel(d, icon); err == nil && filepath.IsLocal(rel) {
				return iconFile(icon)
			}
		}
		return "", ""
	}
	if !iconNameRE.MatchString(icon) {
		return "", ""
	}
	for _, d := range dirs {
		for _, theme := range []string{"breeze", "hicolor"} {
			for _, size := range []string{"scalable", "256x256", "128x128", "64x64", "48x48"} {
				for _, ext := range []string{".svg", ".png"} {
					if f, ct := iconFile(filepath.Join(d, "icons", theme, size, "apps", icon+ext)); f != "" {
						return f, ct
					}
				}
			}
		}
		for _, ext := range []string{".svg", ".png"} {
			if f, ct := iconFile(filepath.Join(d, "pixmaps", icon+ext)); f != "" {
				return f, ct
			}
		}
	}
	return "", ""
}

func iconFile(p string) (string, string) {
	var ct string
	switch strings.ToLower(filepath.Ext(p)) {
	case ".svg":
		ct = "image/svg+xml"
	case ".png":
		ct = "image/png"
	default:
		return "", ""
	}
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > maxIcon {
		return "", ""
	}
	return p, ct
}
