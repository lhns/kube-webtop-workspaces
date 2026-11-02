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

// guard decides which files the agent, which runs as root, may read on behalf of the gateway.
// The user's ~/.local/share is user-controlled, so a symlink there could point at a root-only file.
// A file is readable if, with symlinks resolved, it lies in the Nix store or in the user's own
// data home (a link leaving that tree is refused).
type guard struct{ roots []string }

func (l *Launcher) newGuard() *guard {
	g := &guard{}
	store := l.StoreRoot
	if store == "" {
		store = "/nix/store"
	}
	if r, err := filepath.EvalSymlinks(store); err == nil {
		g.roots = append(g.roots, r)
	}
	if l.Home != "" {
		// ~/.local/share itself may be a link: it only counts if it stays inside the home.
		home, err1 := filepath.EvalSymlinks(l.Home)
		data, err2 := filepath.EvalSymlinks(filepath.Join(l.Home, ".local", "share"))
		if err1 == nil && err2 == nil && within(home, data) {
			g.roots = append(g.roots, data)
		}
	}
	return g
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && filepath.IsLocal(rel)
}

// resolve returns p with symlinks resolved, and its size, if that is a regular file the guard allows.
func (g *guard) resolve(p string) (string, int64, bool) {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", 0, false
	}
	st, err := os.Stat(r)
	if err != nil || !st.Mode().IsRegular() {
		return "", 0, false
	}
	for _, root := range g.roots {
		if within(root, r) {
			return r, st.Size(), true
		}
	}
	return "", 0, false
}

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
	g := l.newGuard()
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
			fields, ok := parseDesktop(g, filepath.Join(d, "applications", f.Name()))
			if !ok {
				continue
			}
			seen[id] = true
			if !listed(fields) {
				continue
			}
			p, _ := resolveIcon(g, dirs, fields["Icon"])
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
	g := l.newGuard()
	for _, d := range dirs {
		if fields, ok := parseDesktop(g, filepath.Join(d, "applications", name+".desktop")); ok {
			if p, ct := resolveIcon(g, dirs, fields["Icon"]); p != "" {
				return p, ct, nil
			}
			break
		}
	}
	return "", "", ErrNotFound
}

// parseDesktop reads the unlocalized keys of the [Desktop Entry] group; ok is false if the file
// does not exist or the guard refuses it.
func parseDesktop(g *guard, file string) (map[string]string, bool) {
	file, _, ok := g.resolve(file)
	if !ok {
		return nil, false
	}
	f, err := openNoSymlinks(file)
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

// resolveIcon finds the image file for an Icon= value: an absolute path (the guard decides where
// it may lie), or a name in the breeze/hicolor themes, then pixmaps. Only PNG and SVG files up to
// maxIcon count.
func resolveIcon(g *guard, dirs []string, icon string) (file, contentType string) {
	if icon == "" {
		return "", ""
	}
	if filepath.IsAbs(icon) {
		return iconFile(g, filepath.Clean(icon))
	}
	if !iconNameRE.MatchString(icon) {
		return "", ""
	}
	for _, d := range dirs {
		for _, theme := range []string{"breeze", "hicolor"} {
			for _, size := range []string{"scalable", "256x256", "128x128", "64x64", "48x48"} {
				for _, ext := range []string{".svg", ".png"} {
					if f, ct := iconFile(g, filepath.Join(d, "icons", theme, size, "apps", icon+ext)); f != "" {
						return f, ct
					}
				}
			}
		}
		for _, ext := range []string{".svg", ".png"} {
			if f, ct := iconFile(g, filepath.Join(d, "pixmaps", icon+ext)); f != "" {
				return f, ct
			}
		}
	}
	return "", ""
}

func iconFile(g *guard, p string) (string, string) {
	var ct string
	switch strings.ToLower(filepath.Ext(p)) {
	case ".svg":
		ct = "image/svg+xml"
	case ".png":
		ct = "image/png"
	default:
		return "", ""
	}
	p, size, ok := g.resolve(p)
	if !ok || size == 0 || size > maxIcon {
		return "", ""
	}
	return p, ct
}
