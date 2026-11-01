package apps

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
)

// Opener is what the HTTP API needs from a Launcher.
type Opener interface {
	Open(ctx context.Context, name string) error
}

// Handler is the gateway-facing API (docs/contract.md, 7.7). It has no authentication:
// the desktop NetworkPolicy lets only the gateway reach the port (ADR 0010).
//
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
	return mux
}
