// Package servertest exercises the public control API and the actual agent
// supervisor over loopback HTTP; policy is persisted in a real SQLite database.
package servertest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/config"
	"github.com/bit-precise/agentd/controlclient"
	"github.com/bit-precise/agentd/deviceflow"
	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agentd/supervisor"
	"github.com/bit-precise/agents/control"
	"github.com/bit-precise/agents/control/types"
	"github.com/bit-precise/agents/edge"
	"github.com/bit-precise/agents/internal/controlrpc"
	"github.com/bit-precise/agents/internal/hydra/hydratest"
	"github.com/rs/zerolog"
)

const adminToken = "test-bootstrap-admin-token-with-at-least-32-bytes"

type harness struct {
	t            *testing.T
	app          *control.App
	server       *httptest.Server
	cfg          control.Config
	base         string
	edgeBase     string
	remoteStatus *controlrpc.Client
	client       *http.Client
	hydra        *hydratest.Server
	closeOnce    sync.Once
}

// newHydra stands in for the instance's Hydra, pointed at Control's pages.
func newHydra(t *testing.T, base string) (*hydratest.Server, control.HydraConfig) {
	t.Helper()
	fake := hydratest.New(hydratest.URLs{Login: base + "/oauth/login", Consent: base + "/oauth/consent", Verification: base + "/oauth/device", Success: base + "/oauth/device/done", Error: base + "/oauth/error"})
	t.Cleanup(fake.Close)
	return fake, control.HydraConfig{Enabled: true, PublicURL: fake.Issuer(), AdminURL: fake.Admin.URL}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return startHarness(t, filepath.Join(t.TempDir(), "control.sqlite"), "127.0.0.1:0")
}

func startHarness(t *testing.T, databasePath, listenAddress string) *harness {
	t.Helper()
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	base := "http://localhost:" + port
	cfg := control.Config{ListenAddress: listener.Addr().String(), ServerURL: base, EdgeURL: base + "/agent", StateDir: filepath.Dir(databasePath), DevelopmentHTTP: true}
	fake, hydraConfig := newHydra(t, base)
	cfg.Hydra = hydraConfig
	cfg.Database.Type = "sqlite"
	cfg.Database.DSN = databasePath
	if err := cfg.Validate(); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	app, err := control.New(t.Context(), cfg, adminToken)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(app.Handler())
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil}}
	h := &harness{t: t, app: app, server: server, cfg: cfg, base: base, client: client, hydra: fake}
	t.Cleanup(h.close)
	return h
}

func (h *harness) close() {
	h.closeOnce.Do(func() { h.client.CloseIdleConnections(); _ = h.app.Close(); h.server.Close() })
}

func (h *harness) api(method, path, token string, body, out any, want int) {
	h.t.Helper()
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
	}
	r, err := http.NewRequestWithContext(h.t.Context(), method, h.base+path, bytes.NewReader(encoded))
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.client.Do(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != want {
		h.t.Fatalf("%s %s returned %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if out != nil {
		if err = json.Unmarshal(data, out); err != nil {
			h.t.Fatalf("decode %s: %v: %s", path, err, data)
		}
	}
}

type userCredential struct {
	User  types.User `json:"user"`
	Token string     `json:"token"`
}

// readResourceList composes numbered resource pages explicitly for tests that
// need to inspect all visible records. It never substitutes an aggregate API.
func readResourceList[T any](h *harness, path, token string) []T {
	h.t.Helper()
	resourceURL, err := url.Parse(path)
	if err != nil {
		h.t.Fatal(err)
	}
	items := []T{}
	for page := 1; ; page++ {
		query := resourceURL.Query()
		query.Set("page", strconv.Itoa(page))
		query.Set("pageSize", "100")
		resourceURL.RawQuery = query.Encode()
		var result struct {
			Items      []T `json:"items"`
			Pagination struct {
				Page       int `json:"page"`
				PageSize   int `json:"pageSize"`
				Total      int `json:"total"`
				TotalPages int `json:"totalPages"`
			} `json:"pagination"`
		}
		h.api("GET", resourceURL.String(), token, nil, &result, http.StatusOK)
		if result.Items == nil || result.Pagination.Page != page || result.Pagination.PageSize != 100 {
			h.t.Fatalf("invalid resource page from %s: %+v", resourceURL, result)
		}
		items = append(items, result.Items...)
		if page >= result.Pagination.TotalPages {
			if len(items) != result.Pagination.Total {
				h.t.Fatalf("resource total from %s: got %d items, total %d", resourceURL, len(items), result.Pagination.Total)
			}
			return items
		}
	}
}

func (h *harness) workspace(name, userName string) (types.Workspace, userCredential) {
	h.t.Helper()
	var w types.Workspace
	var u userCredential
	h.api("POST", "/v1/admin/workspaces", adminToken, map[string]string{"name": name}, &w, 201)
	h.api("POST", "/v1/admin/users", adminToken, map[string]any{"name": userName, "admin": false}, &u, 201)
	h.api("PUT", "/v1/admin/workspaces/"+w.ID+"/memberships/"+u.User.ID, adminToken, map[string]string{"role": "developer"}, nil, 200)
	return w, u
}

// login registers a personal device as `agentd login` does: Device Flow with
// the agentd client, the person confirming in a browser signed in with their
// token (developmentHTTP), and the one-time exchange.
func (h *harness) login(cfg *config.Config, w types.Workspace, u userCredential) (types.EnrollResponse, *controlclient.Client) {
	h.t.Helper()
	ctx := h.t.Context()
	client, err := controlclient.New(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(client.Close)
	info, err := client.Instance(ctx)
	if err != nil || info.DeviceAuthorization == nil {
		h.t.Fatalf("instance does not offer Device Flow: %+v %v", info, err)
	}
	flow := &deviceflow.Client{HTTP: client.HTTPClient(), Sleep: func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
			return nil
		}
	}}
	endpoints, err := flow.Discover(ctx, info.DeviceAuthorization.Issuer)
	if err != nil {
		h.t.Fatal(err)
	}
	var authorization deviceflow.Authorization
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		// Control registers the device client in the background.
		if authorization, err = flow.Start(ctx, endpoints.DeviceAuthorization, info.DeviceAuthorization.ClientID, info.DeviceAuthorization.Scope, info.DeviceAuthorization.Audience); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		h.t.Fatal(err)
	}
	h.confirmInBrowser(authorization, u, w.ID, api.Principal{Type: api.PrincipalUser, ID: u.User.ID})
	token, err := flow.Poll(ctx, endpoints.Token, info.DeviceAuthorization.ClientID, authorization)
	if err != nil {
		h.t.Fatal(err)
	}
	response, err := client.ExchangeDeviceEnrollment(ctx, token.AccessToken)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := client.ExchangeDeviceEnrollment(ctx, token.AccessToken); err == nil {
		h.t.Fatal("one Device Flow authorization registered a second device")
	}
	return response, client
}

// confirmInBrowser plays the person: type the code, sign in, choose, consent.
func (h *harness) confirmInBrowser(authorization deviceflow.Authorization, u userCredential, workspaceID string, p api.Principal) {
	h.t.Helper()
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer browser.CloseIdleConnections()
	send := func(method, target, contentType, csrf string, body io.Reader) *http.Response {
		r, _ := http.NewRequestWithContext(h.t.Context(), method, target, body)
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
			r.Header.Set("Origin", h.base)
			r.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := browser.Do(r)
		if err != nil {
			h.t.Fatal(err)
		}
		return response
	}
	// follow stops at the Console page, which has nothing to redirect.
	follow := func(target string) (*http.Response, *url.URL) {
		for range 10 {
			response := send("GET", target, "", "", nil)
			next, err := response.Location()
			if err != nil {
				return response, response.Request.URL
			}
			response.Body.Close()
			if strings.HasPrefix(next.Path, "/device/") {
				return nil, next
			}
			target = next.String()
		}
		h.t.Fatal("redirect loop")
		return nil, nil
	}
	encoded, _ := json.Marshal(map[string]string{"token": u.Token})
	var session struct {
		CSRF string `json:"csrfToken"`
	}
	response := send("POST", h.base+"/v1/sessions", "application/json", "", bytes.NewReader(encoded))
	if err := json.Unmarshal([]byte(readResponse(h.t, response, 201)), &session); err != nil {
		h.t.Fatal(err)
	}
	page, at := follow(authorization.VerificationURIComplete)
	readResponse(h.t, page, 200)
	form := url.Values{"device_challenge": {at.Query().Get("device_challenge")}, "user_code": {authorization.UserCode}}
	response = send("POST", h.base+"/oauth/device", "application/x-www-form-urlencoded", "", strings.NewReader(form.Encode()))
	location, err := response.Location()
	if err != nil {
		h.t.Fatalf("code page: %s", readResponse(h.t, response, 303))
	}
	response.Body.Close()
	_, console := follow(location.String())
	id := strings.TrimPrefix(console.Path, "/device/")
	encoded, _ = json.Marshal(map[string]any{"workspaceId": workspaceID, "principal": p})
	response = send("POST", h.base+"/v1/device-authorizations/"+id+"/confirm", "application/json", session.CSRF, bytes.NewReader(encoded))
	var accepted struct {
		RedirectTo string `json:"redirectTo"`
	}
	if err := json.Unmarshal([]byte(readResponse(h.t, response, 200)), &accepted); err != nil {
		h.t.Fatal(err)
	}
	done, _ := follow(accepted.RedirectTo)
	if body := readResponse(h.t, done, 200); !strings.Contains(body, "设备已授权") {
		h.t.Fatalf("browser did not finish the authorization: %s", body)
	}
}

// enrollServiceAccount registers an unattended device with a one-use key of
// a new Service Account limited to roles.
func (h *harness) enrollServiceAccount(cfg *config.Config, w types.Workspace, roles []string) (types.EnrollResponse, *controlclient.Client) {
	h.t.Helper()
	var sa types.ServiceAccount
	h.api("POST", "/v1/workspaces/"+w.ID+"/service-accounts", adminToken, map[string]any{"name": cfg.Agent.Name + "-sa"}, &sa, 201)
	var key struct {
		Key   types.EnrollmentKey `json:"key"`
		Token string              `json:"token"`
	}
	h.api("POST", "/v1/workspaces/"+w.ID+"/service-accounts/"+sa.ID+"/enrollment-keys", adminToken, map[string]any{"roles": roles, "maxUses": 1, "expiresAt": time.Now().Add(time.Hour)}, &key, 201)
	client, err := controlclient.New(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(client.Close)
	response, err := client.Enroll(h.t.Context(), key.Token)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := client.Enroll(h.t.Context(), key.Token); err == nil {
		h.t.Fatal("one-use enrollment key admitted a second registration")
	}
	return response, client
}

func (h *harness) agentConfig(workspace, name string) *config.Config {
	h.t.Helper()
	return &config.Config{APIVersion: "agents/v1alpha1", Kind: "AgentConfig", Instance: h.base, Workspace: workspace,
		Agent:     config.AgentConfig{Name: name, StateDir: filepath.Join(h.t.TempDir(), "agent-state")},
		Transport: config.TransportConfig{Type: "xhttp", Mode: "packet-up", Endpoint: h.cfg.EdgeURL, AllowHTTP: true},
		Ingress:   config.IngressConfig{OnUnmatched: "reject"}}
}

func (h *harness) runAgent(cfg *config.Config, enrolled types.EnrollResponse, options ...supervisor.Option) func() {
	_, stop := h.runAgentInstance(cfg, enrolled, options...)
	return stop
}

func (h *harness) runAgentInstance(cfg *config.Config, enrolled types.EnrollResponse, options ...supervisor.Option) (*supervisor.Supervisor, func()) {
	h.t.Helper()
	s, err := supervisor.New(cfg, &enrolled.Credentials, zerolog.Nop(), options...)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					h.t.Errorf("supervisor shutdown: %v", err)
				}
			case <-time.After(12 * time.Second):
				h.t.Error("supervisor failed to stop")
			}
		})
	}
	h.t.Cleanup(stop)
	return s, stop
}

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for data-plane state")
}

func (h *harness) connected(agentID, role string) bool {
	var connections []edge.ConnectionStatus
	if h.remoteStatus != nil {
		snapshot, err := h.remoteStatus.Snapshot(h.t.Context())
		if err != nil {
			return false
		}
		connections = snapshot.Connections
	} else {
		connections = h.app.Edge.Snapshot().Connections
	}
	for _, c := range connections {
		if c.AgentID == agentID && c.Role == role {
			return true
		}
	}
	return false
}

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	return address
}

func tlsRule(host string) policy.Rule {
	return policy.Rule{Domain: policy.Domain{Exact: host}, Ports: []int{443}, Protocol: "tls"}
}

func (h *harness) serveRequest(host, token, path string) (*http.Response, error) {
	base := h.base
	if h.edgeBase != "" {
		base = h.edgeBase
	}
	r, err := http.NewRequestWithContext(h.t.Context(), "GET", base+path, nil)
	if err != nil {
		return nil, err
	}
	r.Host = host
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return h.client.Do(r)
}

func readResponse(t *testing.T, response *http.Response, want int) string {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("response status %d want %d: %s", response.StatusCode, want, data)
	}
	return string(data)
}

func (h *harness) grantRoute(w types.Workspace, u userCredential, agentID, domain, name string) (types.EgressRoute, types.Grant) {
	h.t.Helper()
	var r types.EgressRoute
	var g types.Grant
	h.api("POST", "/v1/admin/routes", adminToken, map[string]any{"workspaceId": w.ID, "agentId": agentID, "name": name, "rule": tlsRule(domain), "kind": "egress"}, &r, 201)
	h.api("POST", "/v1/admin/grants", adminToken, map[string]any{"workspaceId": w.ID, "resourceType": "egress", "resourceId": r.ID, "subjectType": "user", "subjectId": u.User.ID, "expiresAt": time.Now().Add(time.Hour)}, &g, 201)
	return r, g
}

func serviceHost(service, workspace string) string {
	return fmt.Sprintf("%s.%s.localhost", service, workspace)
}
