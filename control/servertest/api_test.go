package servertest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/config"
	"github.com/bit-precise/agentd/controlclient"
	"github.com/bit-precise/agentd/credentials"
	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/types"
	"github.com/bit-precise/agents/edge"
)

func TestAutomaticEnrollmentThroughHTTPAPIAndSupervisor(t *testing.T) {
	h := newHarness(t)
	w, u := h.workspace("workspace", "alice")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("management bearer token leaked to origin")
		}
		_, _ = io.WriteString(w, "published-origin")
	}))
	defer origin.Close()
	cfg := h.agentConfig(w.Name, "provider")
	cfg.Serve = config.ServeConfig{Enabled: true, Services: []config.ServiceConfig{{Name: "demo", Origin: origin.URL}}}
	enrolled, client := h.enroll(cfg, w, u, []string{"serve"})
	if enrolled.Agent.Status != api.StatusActive || len(enrolled.Credentials.Effective.Services) != 1 || enrolled.Credentials.Effective.Services[0].Hostname != serviceHost("demo", w.Name) {
		t.Fatalf("validated enrollment was not effective immediately: %+v", enrolled.Agent)
	}
	h.runAgent(cfg, enrolled)
	await(t, func() bool { return h.connected(enrolled.Agent.ID, "provider") })
	refreshed, err := client.Refresh(t.Context(), enrolled.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ProviderToken != enrolled.Credentials.ProviderToken || refreshed.Effective.Revision < enrolled.Credentials.Effective.Revision {
		t.Fatal("client did not merge secret with the effective configuration")
	}
	response, err := h.serveRequest(serviceHost("demo", w.Name), u.Token, "/")
	if err != nil {
		t.Fatal(err)
	}
	if body := readResponse(t, response, 200); body != "published-origin" {
		t.Fatalf("wrong origin response %q", body)
	}
	response, err = h.serveRequest(serviceHost("demo", w.Name), "", "/")
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, response, 403)
	// A desired update beyond the enrollment key is rejected outright and the
	// running configuration stays effective.
	cfg.Egress = config.EgressConfig{Enabled: true, Allow: []policy.Rule{tlsRule("example.com")}}
	if _, err = client.Update(t.Context(), refreshed, cfg); !controlclient.Rejected(err) {
		t.Fatalf("policy violation was not rejected: %v", err)
	}
	response, err = h.serveRequest(serviceHost("demo", w.Name), u.Token, "/")
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, response, 200)
}

func TestBrowserSessionCSRFAndDisabledUser(t *testing.T) {
	h := newHarness(t)
	_, u := h.workspace("workspace", "alice")
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	request := func(method, path, origin, csrf string, body any) (*http.Response, []byte) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		r, err := http.NewRequestWithContext(t.Context(), method, h.base+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, data
	}
	login, data := request("POST", "/v1/sessions", h.base, "", map[string]string{"token": adminToken})
	if login.StatusCode != 201 || login.Header.Get("Location") != "/v1/sessions/current" {
		t.Fatalf("admin login: %d %s", login.StatusCode, data)
	}
	var logged struct {
		CSRF string `json:"csrfToken"`
	}
	if err = json.Unmarshal(data, &logged); err != nil || logged.CSRF == "" {
		t.Fatal("login omitted CSRF token")
	}
	for _, cookie := range login.Cookies() {
		if cookie.Domain != "" || !cookie.HttpOnly || cookie.Path != "/" || !cookie.Expires.After(time.Now()) {
			t.Fatalf("unsafe or expired browser cookie: %+v", cookie)
		}
	}
	resp, data := request("GET", "/v1/sessions/current", "", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("fresh bootstrap cookie not usable: %d %s", resp.StatusCode, data)
	}
	resp, _ = request("POST", "/v1/admin/workspaces", h.base, "", map[string]string{"name": "forged"})
	if resp.StatusCode != 403 {
		t.Fatalf("cookie mutation without CSRF returned %d, want 403", resp.StatusCode)
	}
	resp, _ = request("POST", "/v1/admin/workspaces", "http://evil.example", logged.CSRF, map[string]string{"name": "forged"})
	if resp.StatusCode != 403 {
		t.Fatalf("foreign Origin accepted: %d", resp.StatusCode)
	}
	resp, data = request("POST", "/v1/admin/workspaces", h.base, logged.CSRF, map[string]string{"name": "legitimate"})
	if resp.StatusCode != 201 {
		t.Fatalf("valid cookie mutation failed: %d %s", resp.StatusCode, data)
	}
	resp, _ = request("DELETE", "/v1/sessions/current", h.base, logged.CSRF, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("logout status %d", resp.StatusCode)
	}
	resp, _ = request("GET", "/v1/sessions/current", "", "", nil)
	if resp.StatusCode != 401 {
		t.Fatal("logged out cookie retained access")
	}
	resp, data = request("POST", "/v1/sessions", h.base, "", map[string]string{"token": u.Token})
	if resp.StatusCode != 201 {
		t.Fatalf("user login: %s", data)
	}
	h.api("PATCH", "/v1/admin/users/"+u.User.ID, adminToken, map[string]bool{"disabled": true}, nil, 200)
	resp, _ = request("GET", "/v1/sessions/current", "", "", nil)
	if resp.StatusCode != 401 {
		t.Fatal("disabled user's existing cookie retained access")
	}
}

func TestHTTPAPIPersistsRegistrationAcrossServerRestart(t *testing.T) {
	h := newHarness(t)
	w, u := h.workspace("workspace", "alice")
	cfg := h.agentConfig(w.Name, "device")
	cfg.Ingress = config.IngressConfig{Enabled: true, HTTP: config.ListenerConfig{Enabled: true, Listen: "127.0.0.1:18888"}, Routes: []policy.Rule{tlsRule("example.com")}, OnUnmatched: "reject"}
	enrolled, _ := h.enroll(cfg, w, u, []string{"ingress"})
	address := h.cfg.ListenAddress
	database := h.cfg.Database.DSN
	h.close()
	restarted := startHarness(t, database, address)
	agents := readResourceList[api.Agent](restarted, "/v1/workspaces/"+w.ID+"/agents", u.Token)
	workspaces := readResourceList[types.Workspace](restarted, "/v1/workspaces", u.Token)
	if len(agents) != 1 || agents[0].ID != enrolled.Agent.ID || len(workspaces) != 2 {
		t.Fatal("server restart lost scoped resource state")
	}
	for _, workspace := range workspaces {
		if workspace.ID != w.ID && workspace.Name != "personal-"+u.User.ID {
			t.Fatal("server restart exposed another user's workspace")
		}
	}
	var refreshed credentials.Credentials
	restarted.api("GET", "/api/v1/agent/credentials", enrolled.Credentials.ControlToken, nil, &refreshed, 200)
	if refreshed.AgentID != enrolled.Agent.ID || refreshed.ProviderToken != "" || refreshed.AccessToken != "" {
		t.Fatal("restart lost agent identity or exposed stored original secrets")
	}
}

func TestResourceListsDoNotLeakOtherWorkspaceSessions(t *testing.T) {
	h := newHarness(t)
	w1, alice := h.workspace("one", "alice")
	w2, bob := h.workspace("two", "bob")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer origin.Close()
	enroll := func(w types.Workspace, u userCredential, name string) types.EnrollResponse {
		cfg := h.agentConfig(w.Name, name)
		cfg.Serve = config.ServeConfig{Enabled: true, Services: []config.ServiceConfig{{Name: "demo", Origin: origin.URL}}}
		response, _ := h.enroll(cfg, w, u, []string{"serve"})
		h.runAgent(cfg, response)
		return response
	}
	first := enroll(w1, alice, "alice-agent")
	second := enroll(w2, bob, "bob-agent")
	await(t, func() bool {
		return h.connected(first.Agent.ID, "provider") && h.connected(second.Agent.ID, "provider")
	})
	view := struct {
		Workspaces  []types.Workspace       `json:"workspaces"`
		Agents      []api.Agent             `json:"agents"`
		Connections []edge.ConnectionStatus `json:"connections"`
		Flows       []edge.FlowStatus       `json:"flows"`
	}{
		Workspaces:  readResourceList[types.Workspace](h, "/v1/workspaces", alice.Token),
		Agents:      readResourceList[api.Agent](h, "/v1/workspaces/"+w1.ID+"/agents", alice.Token),
		Connections: readResourceList[edge.ConnectionStatus](h, "/v1/workspaces/"+w1.ID+"/connections", alice.Token),
		Flows:       readResourceList[edge.FlowStatus](h, "/v1/workspaces/"+w1.ID+"/flows", alice.Token),
	}
	if len(view.Workspaces) != 2 {
		t.Fatal("workspace list leaked another workspace")
	}
	for _, workspace := range view.Workspaces {
		if workspace.ID != w1.ID && workspace.Name != "personal-"+alice.User.ID {
			t.Fatal("workspace list exposed a workspace outside the user's personal and assigned workspaces")
		}
	}
	for _, a := range view.Agents {
		if a.WorkspaceID != w1.ID {
			t.Fatal("agent leaked across workspace")
		}
	}
	for _, c := range view.Connections {
		if c.WorkspaceID != w1.ID || c.AgentID == second.Agent.ID {
			t.Fatal("Edge session metadata leaked across workspace")
		}
	}
	for _, resource := range []string{"agents", "connections", "flows"} {
		h.api("GET", "/v1/workspaces/"+w2.ID+"/"+resource, alice.Token, nil, nil, 404)
	}
	h.api("POST", "/v1/admin/routes", adminToken, map[string]any{"workspaceId": w2.ID, "agentId": first.Agent.ID, "name": "cross-tenant", "rule": tlsRule("example.com"), "kind": "egress"}, nil, 403)
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), second.Credentials.ProviderToken) || strings.Contains(string(encoded), second.Agent.ID) {
		t.Fatal("foreign identity or secret escaped resource visibility filtering")
	}
}

func TestBootstrapTokenRotationSurvivesHTTPServerRestart(t *testing.T) {
	h := newHarness(t)
	var me types.User
	h.api("GET", "/v1/users/me", adminToken, nil, &me, 200)
	var rotated struct {
		Token string `json:"token"`
	}
	h.api("POST", "/v1/users/me/tokens", adminToken, nil, &rotated, 201)
	if rotated.Token == "" || rotated.Token == adminToken {
		t.Fatal("rotation did not issue a new secret")
	}
	h.api("GET", "/v1/users/me", adminToken, nil, nil, 401)
	h.api("GET", "/v1/users/me", rotated.Token, nil, nil, 200)
	address, path := h.cfg.ListenAddress, h.cfg.Database.DSN
	h.close()
	restarted := startHarness(t, path, address)
	restarted.api("GET", "/v1/users/me", adminToken, nil, nil, 401)
	var restored types.User
	restarted.api("GET", "/v1/users/me", rotated.Token, nil, &restored, 200)
	if restored.ID != me.ID {
		t.Fatal("token rotation replaced stable administrator identity")
	}
}
