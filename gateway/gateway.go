// Package gateway assembles an independently runnable Edge. It has no database
// or browser-session store: Control returns authentication metadata only.
package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bit-precise/agents/edge"
	"github.com/bit-precise/agents/internal/controlrpc"
)

type Gateway struct {
	Edge     *edge.Edge
	backend  *controlrpc.Client
	endpoint *url.URL
	internal http.Handler
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	once     sync.Once
}

func New(ctx context.Context, controlURL, edgeURL, token string, cfg edge.Config) (*Gateway, error) {
	endpoint, err := url.Parse(edgeURL)
	if err != nil || endpoint.Host == "" || endpoint.Path != "/agent" {
		return nil, errors.New("edgeURL must identify the public /agent endpoint")
	}
	backend, err := controlrpc.NewClient(controlURL, token)
	if err != nil {
		return nil, err
	}
	e, err := edge.New(backend, cfg)
	if err != nil {
		backend.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	g := &Gateway{Edge: e, backend: backend, endpoint: endpoint, cancel: cancel}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+controlrpc.Prefix+"snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, e.Snapshot())
	})
	g.internal = controlrpc.Protect(token, mux)
	g.wg.Add(1)
	go func() { defer g.wg.Done(); _ = e.Run(ctx) }()
	return g, nil
}

func (g *Gateway) Close() error {
	g.once.Do(func() {
		g.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = g.Edge.Shutdown(ctx)
		g.wg.Wait()
		g.backend.Close()
	})
	return nil
}
func (g *Gateway) Handler() http.Handler { return http.HandlerFunc(g.serve) }

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.Path == "/healthz" {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	if strings.HasPrefix(r.URL.Path, controlrpc.Prefix) {
		g.internal.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/agent" && sameAuthority(r.Host, g.endpoint) {
		g.Edge.Handler().ServeHTTP(w, r)
		return
	}
	r = edge.GuardRequestBody(w, r)
	if r.Body != nil {
		defer r.Body.Close()
	}
	metadata := controlrpc.Metadata{Method: r.Method, Host: r.Host, URI: r.URL.RequestURI(), Header: make(http.Header)}
	for _, key := range []string{"Authorization", "Cookie", "Accept"} {
		metadata.Header[key] = r.Header.Values(key)
	}
	result, err := g.backend.Authorize(r.Context(), metadata)
	if err != nil {
		http.Error(w, "control unavailable", 503)
		return
	}
	if result.Authorization == nil {
		if result.Status < 200 || result.Status > 599 {
			http.Error(w, "invalid control response", 502)
			return
		}
		for key, values := range result.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(result.Status)
		_, _ = w.Write(result.Body)
		return
	}
	authorization := result.Authorization
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if !authorization.IdentityExpiry.IsZero() {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, authorization.IdentityExpiry)
		defer stop()
	}
	request := r.Clone(ctx)
	request.Header = r.Header.Clone()
	if authorization.ConsumeBearer {
		parts := strings.Fields(request.Header.Get("Authorization"))
		if len(parts) != 2 {
			http.Error(w, "invalid credential", 401)
			return
		}
		token := parts[1]
		request.Header.Del("Authorization")
		// Token revocation remains effective after initial metadata authorization.
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				id, err := g.backend.Identity(token)
				if err != nil || id.Kind != "user" || id.UserID != authorization.UserID {
					cancel()
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	g.Edge.ServeHTTP(w, request, authorization.UserID)
}

func sameAuthority(host string, u *url.URL) bool {
	v, err := url.Parse(u.Scheme + "://" + host)
	if err != nil || v.User != nil || v.Path != "" || v.RawQuery != "" || v.Fragment != "" {
		return false
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(v.Hostname(), u.Hostname()) && port(v) == port(u)
}
