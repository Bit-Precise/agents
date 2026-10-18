package servertest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/bit-precise/agentd/config"
	"github.com/bit-precise/agentd/policy"
)

// Local reloads change the effective configuration automatically. The shared
// provider session carries every revision: no reconnect, no approval step.
func TestReloadWithdrawsAndRestoresServiceOnTheSameSession(t *testing.T) {
	h := newHarness(t)
	w, u := h.workspace("workspace", "alice")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "service-restored") }))
	defer origin.Close()
	cfg := h.agentConfig(w.Name, "provider")
	cfg.Serve = config.ServeConfig{Enabled: true, Services: []config.ServiceConfig{{Name: "demo", Origin: origin.URL}}}
	cfg.Egress = config.EgressConfig{Enabled: true, Allow: []policy.Rule{tlsRule("example.com")}}
	enrolled, _ := h.login(cfg, w, u)
	runtime, _ := h.runAgentInstance(cfg, enrolled)
	await(t, func() bool { return h.connected(enrolled.Agent.ID, "provider") })
	session := func() string {
		for _, c := range h.app.Edge.Snapshot().Connections {
			if c.AgentID == enrolled.Agent.ID && c.Role == "provider" {
				return c.SessionID
			}
		}
		return ""
	}
	original := session()
	disabled := *cfg
	disabled.Serve.Enabled = false
	if err := runtime.Reload(&disabled); err != nil {
		t.Fatal(err)
	}
	response, err := h.serveRequest(serviceHost("demo", w.Name), u.Token, "/")
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, response, 503)
	if err := runtime.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		response, err := h.serveRequest(serviceHost("demo", w.Name), u.Token, "/")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode == 200 && string(body) == "service-restored"
	})
	if current := session(); current != original {
		t.Fatalf("effective configuration changes reconnected the provider: %s -> %s", original, current)
	}
	agent, err := h.app.State.Agent(enrolled.Agent.ID)
	if err != nil || agent.Effective.Revision != enrolled.Credentials.Effective.Revision+2 {
		t.Fatalf("withdraw and restore did not each produce one effective revision: %+v %v", agent.Effective, err)
	}
	await(t, func() bool {
		for _, c := range h.app.Edge.Snapshot().Connections {
			if c.AgentID == enrolled.Agent.ID && c.Role == "provider" && slices.Contains(c.Services, "demo") && c.Revision == agent.Effective.Revision {
				return true
			}
		}
		return false
	})
}
