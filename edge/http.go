package edge

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bit-precise/agentd/connection"
	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/types"
)

func canonicalHost(authority string) (string, error) {
	host := authority
	if strings.Contains(authority, ":") {
		var err error
		host, _, err = net.SplitHostPort(authority)
		if err != nil {
			return "", ErrDenied
		}
	}
	return policy.CanonicalDomain(host)
}

func stripIdentity(r *http.Request) {
	for key := range r.Header {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-agents-") || strings.HasPrefix(lower, "x-auth-request-") || strings.HasPrefix(lower, "x-forwarded-") || lower == "forwarded" {
			r.Header.Del(key)
		}
	}
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, cookie := range cookies {
		if !strings.HasPrefix(cookie.Name, "__Host-agents") && !strings.HasPrefix(cookie.Name, "agents-dev-") {
			r.AddCookie(cookie)
		}
	}
}

// Closing net/http's server request body can otherwise wait for a client that
// stopped uploading. Do not expire the HTTP connection after a fully read body;
// only abort the read side when unread request bytes remain.
type requestBody struct {
	io.ReadCloser
	controller *http.ResponseController
	remaining  atomic.Int64
	complete   atomic.Bool
	once       sync.Once
	err        error
}

func (b *requestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.remaining.Load() >= 0 && b.remaining.Add(-int64(n)) <= 0 {
		b.complete.Store(true)
	}
	if errors.Is(err, io.EOF) {
		b.complete.Store(true)
	}
	return n, err
}
func (b *requestBody) Close() error {
	b.once.Do(func() {
		if !b.complete.Load() {
			_ = b.controller.SetReadDeadline(time.Now())
		}
		b.err = b.ReadCloser.Close()
	})
	return b.err
}

// GuardRequestBody makes rejection/abort safe even before policy resolution.
// The caller must defer closing the returned request's body. Nested gateway and
// Edge handlers share the same guard, so successful uploads keep their HTTP
// connection while rejected, partially uploaded bodies cannot block cleanup.
func GuardRequestBody(w http.ResponseWriter, r *http.Request) *http.Request {
	if r.Body == nil {
		return r
	}
	if _, ok := r.Body.(*requestBody); ok {
		return r
	}
	body := &requestBody{ReadCloser: r.Body, controller: http.NewResponseController(w)}
	body.remaining.Store(r.ContentLength)
	body.complete.Store(r.Body == http.NoBody || r.ContentLength == 0)
	r = r.Clone(r.Context())
	r.Body = body
	return r
}

// IsServiceHost reports whether authority names a published service,
// <service>.<workspace>.<baseDomain>. Platform paths such as /healthz and the
// internal API are never routed for these hosts, so they cannot shadow the
// paths of a tenant's service.
func IsServiceHost(authority, baseDomain string) bool {
	host, err := canonicalHost(authority)
	suffix := "." + baseDomain
	if err != nil || baseDomain == "" || !strings.HasSuffix(host, suffix) {
		return false
	}
	return strings.Count(strings.TrimSuffix(host, suffix), ".") == 1
}

// serveFailure keeps a Control outage or an unknown host distinguishable from
// an authorization denial; every failure still rejects the request.
func serveFailure(err error) (int, string) {
	switch types.KindOf(err) {
	case string(types.KindNotFound):
		return http.StatusNotFound, "unknown service"
	case string(types.KindInvalid):
		return http.StatusBadRequest, "invalid service request"
	case string(types.KindUnavailable), "internal":
		return http.StatusServiceUnavailable, "service unavailable"
	default:
		return http.StatusForbidden, "service access denied"
	}
}

// ServeHTTP handles exactly one authorized HTTP request on one provider stream.
// userID must come from the root gateway's authenticated browser session. An
// empty userID is passed to control as anonymous; Edge never infers membership.
func (e *Edge) ServeHTTP(w http.ResponseWriter, r *http.Request, userID string) {
	r = GuardRequestBody(w, r)
	if r.Body != nil {
		defer r.Body.Close()
	}
	if r.Method == http.MethodConnect {
		http.Error(w, "CONNECT is not a published HTTP service", http.StatusMethodNotAllowed)
		return
	}
	host, err := canonicalHost(r.Host)
	if err != nil {
		http.Error(w, "invalid service authority", http.StatusBadRequest)
		return
	}
	d, err := e.backend.ResolveServe(host, userID)
	if err != nil {
		status, message := serveFailure(err)
		http.Error(w, message, status)
		return
	}
	if !validDecision(d, connection.PurposeServe) || d.Hostname != host {
		http.Error(w, "service access denied", http.StatusForbidden)
		return
	}
	f, finish, err := e.beginFlow(r.Context(), d, connection.PurposeServe, userID, host, r.Body, nil)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
		} else {
			http.Error(w, "service access denied", http.StatusForbidden)
		}
		return
	}
	defer finish()
	downClosed := make(chan struct{})
	stopDown := context.AfterFunc(f.ctx, func() { _ = http.NewResponseController(w).SetWriteDeadline(time.Now()); close(downClosed) })
	defer func() {
		if !stopDown() {
			<-downClosed
		}
	}()
	out, err := e.openProvider(f)
	if err != nil {
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
		return
	}
	var dialMu sync.Mutex
	used := false
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false, MaxConnsPerHost: 1,
		ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 64 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialMu.Lock()
			defer dialMu.Unlock()
			if used || ctx.Err() != nil || f.ctx.Err() != nil {
				return nil, ErrUnavailable
			}
			used = true
			return out, nil
		}}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{Transport: transport, FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: d.Hostname})
			pr.Out.Host = d.Hostname
			stripIdentity(pr.Out)
		},
		ModifyResponse: func(response *http.Response) error {
			// A published origin cannot replace platform sessions or set cookies
			// covering sibling services/workspaces.
			cookies := response.Cookies()
			response.Header.Del("Set-Cookie")
			for _, cookie := range cookies {
				if cookie.Domain == "" && !strings.HasPrefix(cookie.Name, "__Host-agents") && !strings.HasPrefix(cookie.Name, "agents-dev-") {
					response.Header.Add("Set-Cookie", cookie.String())
				}
			}
			response.Header.Set("Cache-Control", "private, no-store")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "provider HTTP request failed", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r.WithContext(f.ctx))
}
