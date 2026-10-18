// Package api embeds the versioned public OpenAPI contract used by Console.
package api

import (
	"embed"
	"net/http"
)

//go:embed openapi.json
var files embed.FS

func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.FileServer(http.FS(files)).ServeHTTP(w, r)
	})
}
