// Package control assembles the server in the style of Headscale's hscontrol:
// state coordinates persistence/policy; HTTP and data-plane adapters consume it.
package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	serverapi "github.com/bit-precise/agents/api"
	"github.com/bit-precise/agents/control/assets"
	"github.com/bit-precise/agents/control/state"
	"github.com/bit-precise/agents/control/types"
	"github.com/bit-precise/agents/edge"
	"github.com/bit-precise/agents/internal/controlrpc"
)

type App struct {
	Config           Config
	State            *state.State
	Edge             *edge.Edge
	auth             *browserAuth
	router           *http.ServeMux
	assets           http.Handler
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	closeOnce        sync.Once
	closeErr         error
	internal         http.Handler
	status           *controlrpc.Client
	consoleTransport *http.Transport
	separate         bool
	oidc             *oidcAuth
}

func New(ctx context.Context, cfg Config, adminToken string) (*App, error) {
	return newApp(ctx, cfg, adminToken, "", false)
}

// NewControl runs the authoritative control plane without an embedded Edge or Console.
func NewControl(ctx context.Context, cfg Config, adminToken, internalToken string) (*App, error) {
	return newApp(ctx, cfg, adminToken, internalToken, true)
}
func newApp(ctx context.Context, cfg Config, adminToken, internalToken string, separate bool) (*App, error) {
	mode := "serve"
	if separate {
		mode = "control"
	}
	if err := cfg.ValidateFor(mode); err != nil {
		return nil, err
	}
	if len(adminToken) < 24 {
		return nil, errors.New("admin token must contain at least 24 high-entropy characters")
	}
	oidcAuth, err := newOIDC(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cleanupOIDC := true
	defer func() {
		if cleanupOIDC && oidcAuth != nil {
			oidcAuth.http.CloseIdleConnections()
		}
	}()
	s, err := state.Open(ctx, cfg.DBOptions(), cfg.BaseDomain())
	if err != nil {
		return nil, err
	}
	if _, err = s.BootstrapAdmin(adminToken); err != nil {
		s.Close()
		return nil, err
	}
	issuer, adminEmails := "", []string(nil)
	if cfg.OIDC.Enabled {
		issuer, adminEmails = cfg.OIDC.Issuer, cfg.OIDC.AdminEmails
	}
	if err = s.ReconcileOIDCAdminEmails(issuer, adminEmails); err != nil {
		s.Close()
		return nil, err
	}
	a := &App{Config: cfg, State: s, auth: newBrowserAuth(), router: http.NewServeMux(), assets: assets.Handler(), separate: separate, oidc: oidcAuth}
	if separate {
		status, err := controlrpc.NewClient(cfg.EdgeInternalURL, internalToken)
		if err != nil {
			s.Close()
			return nil, err
		}
		a.status = status
		target, _ := url.Parse(cfg.ConsoleURL)
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.ResponseHeaderTimeout = 5 * time.Second
		a.consoleTransport = transport
		a.assets = &httputil.ReverseProxy{Transport: transport, Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.Header.Del("Cookie")
			p.Out.Header.Del("Authorization")
			p.Out.Header.Del("Proxy-Authorization")
			p.Out.Header.Del("X-CSRF-Token")
		}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			apiError(w, 503, "unavailable", "console unavailable")
		}}
		a.internal = controlrpc.Protect(internalToken, a.internalHandler())
	} else {
		e, err := edge.New(s, edge.Config{AllowLoopbackHTTP: cfg.DevelopmentHTTP, TLSOffload: cfg.TLSOffload})
		if err != nil {
			s.Close()
			return nil, err
		}
		a.Edge = e
	}
	a.routes()
	runtimeCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	if a.Edge != nil {
		a.wg.Add(1)
		go func() { defer a.wg.Done(); _ = a.Edge.Run(runtimeCtx) }()
	}
	cleanupOIDC = false
	return a, nil
}

func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if a.Edge != nil {
			_ = a.Edge.Shutdown(ctx)
		}
		a.wg.Wait()
		if a.status != nil {
			a.status.Close()
		}
		if a.consoleTransport != nil {
			a.consoleTransport.CloseIdleConnections()
		}
		if a.oidc != nil {
			a.oidc.http.CloseIdleConnections()
		}
		a.closeErr = a.State.Close()
	})
	return a.closeErr
}
func (a *App) Handler() http.Handler { return http.HandlerFunc(a.serve) }
func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Authorization-dependent responses must never be served from a CDN cache.
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" {
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if strings.HasPrefix(r.URL.Path, controlrpc.Prefix) {
		if a.internal == nil {
			http.NotFound(w, r)
		} else {
			a.internal.ServeHTTP(w, r)
		}
		return
	}
	edgeURL, _ := url.Parse(a.Config.EdgeURL)
	controlURL, _ := url.Parse(a.Config.ServerURL)
	if a.Edge != nil && r.URL.Path == "/agent" && sameAuthority(r.Host, edgeURL) {
		a.Edge.Handler().ServeHTTP(w, r)
		return
	}
	if sameAuthority(r.Host, controlURL) {
		if r.URL.Path == "/openapi.json" {
			serverapi.Handler().ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			a.router.ServeHTTP(w, r)
			return
		}
		a.assets.ServeHTTP(w, r)
		return
	}
	if a.separate {
		http.Error(w, "published services are handled by Edge", http.StatusMisdirectedRequest)
		return
	}
	if r.URL.Path == "/__agents/callback" {
		a.serviceCallback(w, r)
		return
	}
	a.servePublished(w, r)
}

func (a *App) authorizePublished(w http.ResponseWriter, r *http.Request) (*controlrpc.Authorization, bool) {
	userID := ""
	resourceID := ""
	var identityExpiry time.Time
	consumeBearer := false
	if s, ok := a.session(r, "service"); ok {
		if _, err := a.State.CheckUser(s.UserID); err == nil {
			userID = s.UserID
			resourceID = s.ResourceID
			identityExpiry = s.ExpiresAt
		}
	}
	if len(r.Header.Values("Authorization")) > 1 {
		apiError(w, 401, "unauthorized", "ambiguous authorization header")
		return nil, false
	}
	if token, ok := bearerToken(r); ok {
		if a.State.KnownToken(token) {
			id, err := a.State.Identity(token)
			if err != nil || id.Kind != "user" || (userID != "" && userID != id.UserID) {
				apiError(w, 401, "unauthorized", "conflicting or invalid platform credential")
				return nil, false
			}
			userID = id.UserID
			resourceID = ""
			identityExpiry = id.ExpiresAt
			consumeBearer = true
		}
	}
	decision, err := a.State.ResolveServe(r.Host, userID)
	if err != nil {
		if types.KindOf(err) == string(types.KindForbidden) && userID == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.Contains(r.Header.Get("Accept"), "text/html") {
			// Only a recognized, private service starts a browser login exchange.
			nonce := randomSecret()
			a.setCookie(w, "state", nonce, time.Now().Add(5*time.Minute))
			returnTo := a.publicScheme() + "://" + r.Host + r.URL.RequestURI()
			location := strings.TrimSuffix(a.Config.ServerURL, "/") + "/auth/authorize?" + url.Values{"return_to": {returnTo}, "state": {nonce}}.Encode()
			http.Redirect(w, r, location, http.StatusSeeOther)
			return nil, false
		}
		respond(w, nil, err, 200)
		return nil, false
	}
	if resourceID != "" && resourceID != decision.ResourceID {
		apiError(w, 403, "forbidden", "service session scope mismatch")
		return nil, false
	}
	return &controlrpc.Authorization{UserID: userID, IdentityExpiry: identityExpiry, ConsumeBearer: consumeBearer}, true
}

func (a *App) servePublished(w http.ResponseWriter, r *http.Request) {
	r = edge.GuardRequestBody(w, r)
	if r.Body != nil {
		defer r.Body.Close()
	}
	authorization, ok := a.authorizePublished(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if !authorization.IdentityExpiry.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, authorization.IdentityExpiry)
		defer cancel()
	}
	if platformToken, ok := bearerToken(r); authorization.ConsumeBearer && ok {
		var cancel context.CancelFunc
		ctx, cancel = a.watchToken(ctx, platformToken)
		defer cancel()
	}
	request := r.Clone(ctx)
	request.Header = r.Header.Clone()
	if authorization.ConsumeBearer {
		request.Header.Del("Authorization")
	}
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	for _, c := range cookies {
		if !strings.HasPrefix(c.Name, "__Host-agents-") && !strings.HasPrefix(c.Name, "agents-dev-") {
			request.AddCookie(c)
		}
	}
	a.Edge.ServeHTTP(w, request, authorization.UserID)
}
