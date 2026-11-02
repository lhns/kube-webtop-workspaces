package apps

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
)

// Opener is what the HTTP API needs from a Launcher.
type Opener interface {
	Open(ctx context.Context, name string) error
	List(ctx context.Context) ([]App, error)
	// Icon returns the icon file of an app and its Content-Type.
	Icon(ctx context.Context, name string) (file, contentType string, err error)
}

// Handler is the gateway-facing API (docs/contract.md, 7.7). It has no authentication:
// the desktop NetworkPolicy lets only the gateway reach the port (ADR 0010).
//
//	GET  /api/v1/apps              200 [{"id","name","icon","running"}] sorted by name
//	GET  /api/v1/apps/<name>/icon  200 the PNG/SVG file; 404 no such app or icon
//	POST /api/v1/apps/<name>   204 placed (and started if needed); 400 bad name; 404 no such
//	                           application; 502 KWin refused; 500 the app did not start
func Handler(o Opener) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /api/v1/apps/{name}", func(w http.ResponseWriter, r *http.Request) {
		err := o.Open(r.Context(), r.PathValue("name"))
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
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
			if f, err = os.Open(file); err == nil {
				defer f.Close()
				st, serr := f.Stat()
				if serr != nil {
					http.Error(w, "icon unreadable", http.StatusInternalServerError)
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
