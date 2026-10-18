// Package assets serves the checked-in React/shadcn console build.
package assets

import (
	"bytes"
	"embed"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed web
var files embed.FS

// Base UI uses inline style properties for accessible popup positioning. Only
// styles allow this; scripts remain same-origin with no inline/eval allowance.
const policy = "default-src 'none'; script-src 'self'; script-src-attr 'none'; style-src 'self' 'unsafe-inline'; img-src 'self'; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"

// Handler serves only embedded build assets or extensionless SPA navigation.
// Missing API/auth routes and unknown files never become successful HTML pages.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", policy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name, mime := "web/index.html", "text/html; charset=utf-8"
		route := r.URL.Path
		if strings.HasPrefix(route, "/assets/") {
			if path.Clean(route) != route {
				http.NotFound(w, r)
				return
			}
			relative := strings.TrimPrefix(route, "/assets/")
			for _, segment := range strings.Split(relative, "/") {
				if segment == "" || strings.HasPrefix(segment, ".") {
					http.NotFound(w, r)
					return
				}
			}
			mime = map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".woff2": "font/woff2", ".woff": "font/woff", ".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon"}[path.Ext(relative)]
			if mime == "" {
				http.NotFound(w, r)
				return
			}
			name = "web/assets/" + relative
		} else if route != "/" && route != "/index.html" && route != "/console" && route != "/console/" {
			if route == "/api" || route == "/auth" || strings.HasPrefix(route, "/api/") || strings.HasPrefix(route, "/auth/") || path.Ext(route) != "" || path.Clean(route) != route {
				http.NotFound(w, r)
				return
			}
		}
		data, err := files.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime)
		http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(data))
	})
}
