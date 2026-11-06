package apps

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
)

var screenRE = regexp.MustCompile(`^[0-9]{1,9}$`)

// Opener is what the HTTP API needs from a Launcher.
type Opener interface {
	// Open returns the app's display number.
	Open(ctx context.Context, name string) (int, error)
	// Focus raises "main" or a display number.
	Focus(ctx context.Context, screen string) error
	List(ctx context.Context) ([]App, error)
	// Icon returns the icon file of an app and its Content-Type.
	Icon(ctx context.Context, name string) (file, contentType string, err error)
}

// Handler is the gateway-facing API (docs/contract.md, 7.7). It has no authentication:
// the desktop NetworkPolicy lets only the gateway reach the port (ADR 0010).
//
//	GET  /api/v1/apps              200 [{"id","name","icon","running"}] sorted by name
//	GET  /api/v1/apps/<name>/icon  200 the PNG/SVG file; 404 no such app or icon
//	POST /api/v1/apps/<name>   200 {"display":n} placed (and started if needed); 400 bad name;
//	                           404 no such application; 409 {"max","open":[ids]} over the screen
//	                           cap; 502 KWin refused; 500 the app did not start
//	POST /api/v1/focus/<n|main> 204; 400 bad screen; 404 no app on n; 502 KWin refused
func Handler(o Opener) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /api/v1/apps/{name}", func(w http.ResponseWriter, r *http.Request) {
		n, err := o.Open(r.Context(), r.PathValue("name"))
		if err == nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"display": n})
			return
		}
		var ce *CapError
		if errors.As(err, &ce) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"max": ce.Max, "open": ce.Open})
			return
		}
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrInvalidName):
			code = http.StatusBadRequest
		case errors.Is(err, ErrNotFound):
			code = http.StatusNotFound
		case errors.Is(err, ErrPlace):
			code = http.StatusBadGateway
		}
		if code >= 500 {
			log.Printf("apps: open %q: %v", r.PathValue("name"), err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	})
	mux.HandleFunc("POST /api/v1/focus/{screen}", func(w http.ResponseWriter, r *http.Request) {
		screen := r.PathValue("screen")
		if screen != "main" && !screenRE.MatchString(screen) {
			http.Error(w, "bad screen", http.StatusBadRequest)
			return
		}
		switch err := o.Focus(r.Context(), screen); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, ErrUnknownScreen):
			http.Error(w, "no such screen", http.StatusNotFound)
		default:
			log.Printf("apps: focus %q: %v", screen, err)
			http.Error(w, "focus failed", http.StatusBadGateway)
		}
	})
	mux.HandleFunc("GET /api/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		list, err := o.List(r.Context())
		if err != nil {
			log.Printf("apps: list: %v", err)
			http.Error(w, "listing failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("GET /api/v1/apps/{name}/icon", func(w http.ResponseWriter, r *http.Request) {
		file, ct, err := o.Icon(r.Context(), r.PathValue("name"))
		if err == nil {
			var f *os.File
			if f, err = openNoSymlinks(file); err == nil {
				defer f.Close()
				st, serr := f.Stat()
				if serr != nil || !st.Mode().IsRegular() || st.Size() > maxIcon {
					http.Error(w, "icon unreadable", http.StatusNotFound)
					return
				}
				h := w.Header()
				h.Set("Content-Type", ct)
				h.Set("Cache-Control", "max-age=3600")
				h.Set("X-Content-Type-Options", "nosniff")
				http.ServeContent(w, r, "", st.ModTime(), f)
				return
			}
		}
		code := http.StatusNotFound
		if errors.Is(err, ErrInvalidName) {
			code = http.StatusBadRequest
		}
		http.Error(w, "no icon", code)
	})
	return mux
}
