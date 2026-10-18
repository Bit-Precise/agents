package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/types"
	"github.com/bit-precise/agents/edge"
	"github.com/bit-precise/agents/internal/controlrpc"
)

const restBootstrap = "rest-tests-bootstrap-token-over-thirty-two-characters"

type restFixture struct {
	app                  *App
	admin, alice, bob    types.User
	aliceToken, bobToken string
	workspace, foreign   types.Workspace
}

func newRESTFixture(t *testing.T) restFixture {
	t.Helper()
	cfg := Config{ListenAddress: "127.0.0.1:8080", ServerURL: "https://agents.test:30526", EdgeURL: "https://edge.agents.test:30526/agent", TLSOffload: true}
	cfg.Database.Type = "sqlite"
	cfg.Database.DSN = filepath.Join(t.TempDir(), "rest.db")
	app, err := New(t.Context(), cfg, restBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	admin, err := app.State.BootstrapAdmin(restBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	alice, aliceToken, err := app.State.CreateUser(admin.ID, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, bobToken, err := app.State.CreateUser(admin.ID, "bob", false)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := app.State.CreateWorkspace(admin.ID, "shared")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := app.State.CreateWorkspace(admin.ID, "foreign")
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []types.User{alice, bob} {
		if _, err = app.State.SetMembership(admin.ID, workspace.ID, user.ID, "developer"); err != nil {
			t.Fatal(err)
		}
	}
	return restFixture{app, admin, alice, bob, aliceToken, bobToken, workspace, foreign}
}
func (f restFixture) request(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, f.app.Config.ServerURL+path, bytes.NewReader(data))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	out := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(out, r)
	return out
}
func restRequire(t *testing.T, r *httptest.ResponseRecorder, status int, out any) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status=%d want=%d body=%s", r.Code, status, r.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(r.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
}
func (f restFixture) enroll(t *testing.T, user types.User, name string) types.EnrollResponse {
	t.Helper()
	rule := policy.Rule{Domain: policy.Domain{Exact: "outbound.test"}, Ports: []int{443}, Protocol: "tls"}
	desired := api.AgentSpec{Ingress: api.IngressSpec{Enabled: true}, Serve: api.ServeSpec{Enabled: true, Services: []api.ServiceSpec{{Name: name + "-app", Origin: "http://127.0.0.1:37842"}}}, Egress: api.EgressSpec{Enabled: true, Allow: []policy.Rule{rule}}}
	return enrollDevice(t, f.app.State, user.ID, f.workspace, name, desired)
}

type restPage[T any] struct {
	Items      []T        `json:"items"`
	Pagination pagination `json:"pagination"`
}

func TestRESTAuthorizationScopesAndRemovedPaths(t *testing.T) {
	f := newRESTFixture(t)
	alice := f.enroll(t, f.alice, "alice-device")
	bob := f.enroll(t, f.bob, "bob-device")
	for _, resource := range []string{"users", "workspaces", "agents", "servers", "routes", "memberships", "enrollment-keys", "grants", "audit-events", "connections", "flows", "ingresses", "egresses"} {
		restRequire(t, f.request(t, "GET", "/v1/admin/"+resource, "", nil), 401, nil)
		restRequire(t, f.request(t, "GET", "/v1/admin/"+resource, f.aliceToken, nil), 403, nil)
	}
	base := "/v1/workspaces/" + f.workspace.ID
	var agents restPage[types.Agent]
	restRequire(t, f.request(t, "GET", base+"/agents", f.aliceToken, nil), 200, &agents)
	if agents.Pagination.Total != 1 || len(agents.Items) != 1 || agents.Items[0].ID != alice.Agent.ID {
		t.Fatalf("agent scope: %+v", agents)
	}
	restRequire(t, f.request(t, "GET", base+"/agents/"+bob.Agent.ID, f.aliceToken, nil), 404, nil)
	restRequire(t, f.request(t, "PATCH", base+"/agents/"+bob.Agent.ID, f.aliceToken, map[string]string{"status": "revoked"}), 404, nil)
	restRequire(t, f.request(t, "GET", "/v1/workspaces/"+f.foreign.ID+"/agents", f.aliceToken, nil), 404, nil)
	restRequire(t, f.request(t, "GET", "/v1/workspaces/"+f.foreign.ID+"/agents/"+alice.Agent.ID, restBootstrap, nil), 404, nil)
	var filtered restPage[types.Agent]
	restRequire(t, f.request(t, "GET", base+"/agents?workspaceId="+f.foreign.ID, f.aliceToken, nil), 200, &filtered)
	if filtered.Pagination.Total != 0 || filtered.Items == nil {
		t.Fatalf("filter broadened authority: %+v", filtered)
	}
	for _, path := range []string{"/api/v1/state", "/api/v1/me", "/api/v1/users", "/api/v1/info", "/v1/state", "/v1/users", "/v1/agents", "/v1/services", "/v1/routes", "/v1/admin/services", "/v1/admin/egress-routes"} {
		result := f.request(t, "GET", path, restBootstrap, nil)
		if result.Code != 404 && result.Code != 405 {
			t.Fatalf("retired path %s returned %d", path, result.Code)
		}
		if strings.Contains(result.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("API %s fell back to SPA", path)
		}
	}
}

func TestRESTPaginationFiltersAndStrictQuery(t *testing.T) {
	f := newRESTFixture(t)
	for _, name := range []string{"c-device", "a-device", "b-device"} {
		f.enroll(t, f.alice, name)
	}
	base := "/v1/workspaces/" + f.workspace.ID + "/agents"
	ids := []string{}
	for page := 1; page <= 3; page++ {
		var result restPage[types.Agent]
		restRequire(t, f.request(t, "GET", base+"?pageSize=1&sortBy=status&page="+strconv.Itoa(page), f.aliceToken, nil), 200, &result)
		if result.Pagination.Total != 3 || result.Pagination.TotalPages != 3 || result.Pagination.PageSize != 1 || len(result.Items) != 1 {
			t.Fatalf("bad page %+v", result)
		}
		ids = append(ids, result.Items[0].ID)
	}
	if !slices.IsSorted(ids) || ids[0] == ids[1] || ids[1] == ids[2] {
		t.Fatalf("unstable tie ordering: %v", ids)
	}
	var result restPage[types.Agent]
	restRequire(t, f.request(t, "GET", base+"?q=B-DEVICE&status=active&pageSize=1", f.aliceToken, nil), 200, &result)
	if result.Pagination.Total != 1 || result.Items[0].Name != "b-device" {
		t.Fatal("filter must precede total/pagination")
	}
	restRequire(t, f.request(t, "GET", base+"?page=9223372036854775807", f.aliceToken, nil), 200, &result)
	if result.Pagination.Total != 3 || len(result.Items) != 0 || result.Items == nil {
		t.Fatal("large page must be an empty array without overflow")
	}
	for _, query := range []string{"page=0", "page=-1", "pageSize=101", "pageSize=no", "page=1&page=2", "sortBy=origin", "sortOrder=sideways", "status=enabled", "pendingApproval=1", "unknown=x", "status=", "q=a;status=active"} {
		restRequire(t, f.request(t, "GET", base+"?"+query, f.aliceToken, nil), 400, nil)
	}
	var users restPage[types.User]
	restRequire(t, f.request(t, "GET", "/v1/workspaces/"+f.workspace.ID+"/users?admin=false", f.aliceToken, nil), 200, &users)
	if users.Pagination.Total != 2 {
		t.Fatalf("wrong workspace user candidates: %+v", users)
	}
}

func TestRESTRoleViewsAndRoutesKeepOriginsPrivate(t *testing.T) {
	f := newRESTFixture(t)
	provider := f.enroll(t, f.bob, "provider")
	base := "/v1/workspaces/" + f.workspace.ID
	var servers restPage[ServerView]
	response := f.request(t, "GET", base+"/servers", f.aliceToken, nil)
	restRequire(t, response, 200, &servers)
	if len(servers.Items) != 1 || servers.Items[0].Agent.ID != provider.Agent.ID || servers.Items[0].Agent.Name != "provider" {
		t.Fatalf("server role view: %+v", servers)
	}
	for _, secret := range []string{"private-origin", "37842", "\"desired\"", "\"effective\"", "\"origin\""} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("server catalogue leaked %q", secret)
		}
	}
	var roles restPage[AgentRoleView]
	restRequire(t, f.request(t, "GET", base+"/egresses", f.aliceToken, nil), 200, &roles)
	if roles.Pagination.Total != 0 {
		t.Fatal("role view granted visibility to another device")
	}
	for _, role := range []string{"ingresses", "egresses"} {
		restRequire(t, f.request(t, "GET", base+"/"+role, f.bobToken, nil), 200, &roles)
		if len(roles.Items) != 1 || roles.Items[0].ID != provider.Agent.ID || !roles.Items[0].Enabled {
			t.Fatalf("role %s: %+v", role, roles)
		}
	}
	var routes restPage[RouteView]
	restRequire(t, f.request(t, "GET", base+"/routes?kind=serve", f.aliceToken, nil), 200, &routes)
	route := routes.Items[0]
	if route.TargetType != "server" || route.TargetID != servers.Items[0].ID || route.Rule.Protocol != "tls" || !slices.Equal(route.Rule.Ports, []int{30526}) {
		t.Fatalf("serve route uses wrong target or public port: %+v", route)
	}
	var egress RouteView
	restRequire(t, f.request(t, "POST", "/v1/admin/routes", restBootstrap, map[string]any{"workspaceId": f.workspace.ID, "kind": "egress", "name": "outbound", "agentId": provider.Agent.ID, "rule": policy.Rule{Domain: policy.Domain{Exact: "outbound.test"}, Ports: []int{443}, Protocol: "tls"}, "priority": 3}), 201, &egress)
	if egress.TargetType != "egress" || egress.TargetID != provider.Agent.ID || egress.ID == provider.Agent.ID {
		t.Fatalf("egress route confused device and route: %+v", egress)
	}
	restRequire(t, f.request(t, "PATCH", base+"/routes/"+egress.ID, f.bobToken, map[string]bool{"enabled": false}), 403, nil)
	restRequire(t, f.request(t, "PATCH", "/v1/admin/routes/"+egress.ID, restBootstrap, map[string]bool{"enabled": false}), 200, &egress)
	if egress.Enabled {
		t.Fatal("route remained enabled")
	}
	var server ServerView
	restRequire(t, f.request(t, "PATCH", base+"/servers/"+servers.Items[0].ID, f.bobToken, map[string]bool{"enabled": false}), 200, &server)
	if server.Enabled {
		t.Fatal("server remained enabled")
	}
	for _, body := range []any{map[string]any{}, map[string]any{"enabled": nil}, map[string]any{"enabled": true, "origin": "http://foreign"}} {
		restRequire(t, f.request(t, "PATCH", base+"/servers/"+server.ID, f.bobToken, body), 400, nil)
	}
	restRequire(t, f.request(t, "POST", base+"/routes", f.bobToken, map[string]any{"kind": "serve"}), 400, nil)
}

func TestRESTHasNoApprovalAndShowsAutomaticEffectiveConfig(t *testing.T) {
	f := newRESTFixture(t)
	enrolled := f.enroll(t, f.alice, "device")
	for _, path := range []string{"/v1/admin/agents/" + enrolled.Agent.ID + "/approval", "/v1/workspaces/" + f.workspace.ID + "/agents/" + enrolled.Agent.ID + "/approval"} {
		if code := f.request(t, "PUT", path, restBootstrap, map[string]any{"expectedDesired": enrolled.Agent.Desired}).Code; code != 404 && code != 405 {
			t.Fatalf("removed approval endpoint %s answered %d", path, code)
		}
	}
	changed := enrolled.Agent.Desired.Clone()
	changed.Serve.Services[0].Origin = "http://127.0.0.1:37843"
	if _, err := f.app.State.UpdateAgent(enrolled.Credentials.ControlToken, api.ConfigUpdate{Workspace: f.workspace.Name, Name: "device", Desired: changed}); err != nil {
		t.Fatal(err)
	}
	response := f.request(t, "GET", "/v1/admin/agents/"+enrolled.Agent.ID, restBootstrap, nil)
	var detail api.Agent
	restRequire(t, response, 200, &detail)
	if strings.Contains(response.Body.String(), "pendingApproval") || detail.Status != api.StatusActive || detail.Effective.Revision != enrolled.Agent.Effective.Revision+1 || detail.Effective.Services[0].Origin != "http://127.0.0.1:37843" {
		t.Fatalf("automatic update not visible as the effective configuration: %s", response.Body.String())
	}
	sa, err := f.app.State.CreateServiceAccount(f.admin.ID, f.workspace.ID, "robot", "")
	if err != nil {
		t.Fatal(err)
	}
	restRequire(t, f.request(t, "POST", "/v1/workspaces/"+f.workspace.ID+"/service-accounts/"+sa.ID+"/enrollment-keys", restBootstrap, map[string]any{"roles": []string{"ingress"}, "autoApprove": true}), 400, nil)
	if code := f.request(t, "POST", "/v1/admin/enrollment-keys", restBootstrap, map[string]any{"workspaceId": f.workspace.ID, "userId": f.alice.ID, "roles": []string{"ingress"}}).Code; code != 404 && code != 405 {
		t.Fatalf("user enrollment key creation still answers %d", code)
	}
}

func TestRESTSessionsCSRFAndStrictCreation(t *testing.T) {
	f := newRESTFixture(t)
	response := f.request(t, "POST", "/v1/sessions", "", map[string]string{"token": f.aliceToken})
	var session struct {
		User types.User `json:"user"`
		CSRF string     `json:"csrfToken"`
	}
	restRequire(t, response, 201, &session)
	if response.Header().Get("Location") != "/v1/sessions/current" || session.CSRF == "" {
		t.Fatal("session creation omitted location or CSRF")
	}
	var cookie *http.Cookie
	for _, c := range response.Result().Cookies() {
		if c.Name == f.app.cookieName("session") {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("session cookie missing")
	}
	restRequire(t, f.request(t, "GET", "/v1/sessions/current", f.aliceToken, nil), 401, nil)
	me := f.request(t, "GET", "/v1/users/me", f.aliceToken, nil)
	restRequire(t, me, 200, nil)
	if len(me.Result().Cookies()) != 0 {
		t.Fatal("Bearer unexpectedly issued a session cookie")
	}
	for _, test := range []struct {
		origin, csrf string
		want         int
	}{{"", "", 403}, {f.app.Config.ServerURL, "wrong", 403}, {"https://evil.test", session.CSRF, 403}, {f.app.Config.ServerURL, session.CSRF, 204}} {
		r := httptest.NewRequest("DELETE", f.app.Config.ServerURL+"/v1/sessions/current", nil)
		r.AddCookie(cookie)
		r.Header.Set("Origin", test.origin)
		r.Header.Set("X-CSRF-Token", test.csrf)
		out := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(out, r)
		restRequire(t, out, test.want, nil)
	}
	restRequire(t, f.request(t, "DELETE", "/v1/sessions/current", "", nil), 401, nil)
	// Unattended keys belong to a Service Account; both come from the path.
	workspaceBase := "/v1/workspaces/" + f.workspace.ID
	restRequire(t, f.request(t, "POST", workspaceBase+"/service-accounts", f.aliceToken, map[string]any{"name": "ci"}), 403, nil)
	if _, err := f.app.State.SetMembership(f.admin.ID, f.workspace.ID, f.alice.ID, "maintainer"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []any{map[string]any{"name": "ci", "workspaceId": f.workspace.ID}, map[string]any{"name": "ci", "status": "active"}, map[string]any{"name": "Not A Label"}} {
		restRequire(t, f.request(t, "POST", workspaceBase+"/service-accounts", f.aliceToken, body), 400, nil)
	}
	var sa types.ServiceAccount
	response = f.request(t, "POST", workspaceBase+"/service-accounts", f.aliceToken, map[string]any{"name": "ci", "description": "build runners"})
	restRequire(t, response, 201, &sa)
	if sa.WorkspaceID != f.workspace.ID || sa.CreatedBy != f.alice.ID || response.Header().Get("Location") != workspaceBase+"/service-accounts/"+sa.ID {
		t.Fatalf("service account creation: %+v %s", sa, response.Header().Get("Location"))
	}
	base := workspaceBase + "/service-accounts/" + sa.ID + "/enrollment-keys"
	for _, body := range []any{map[string]any{"workspaceId": f.workspace.ID, "roles": []string{"ingress"}}, map[string]any{"userId": f.alice.ID, "roles": []string{"ingress"}}, map[string]any{"serviceAccountId": sa.ID, "roles": []string{"ingress"}}, map[string]any{"roles": []string{"ingress"}, "id": "injected"}, map[string]any{"roles": []string{"ingress"}, "uses": 999}} {
		restRequire(t, f.request(t, "POST", base, f.aliceToken, body), 400, nil)
	}
	response = f.request(t, "POST", base, f.aliceToken, map[string]any{"roles": []string{"ingress"}})
	var key struct {
		Key   types.EnrollmentKey `json:"key"`
		Token string              `json:"token"`
	}
	restRequire(t, response, 201, &key)
	keys := workspaceBase + "/enrollment-keys"
	if key.Key.WorkspaceID != f.workspace.ID || key.Key.ServiceAccountID != sa.ID || key.Key.UserID != "" || key.Token == "" || response.Header().Get("Location") != keys+"/"+key.Key.ID {
		t.Fatalf("service account key creation returned %+v at %s", key.Key, response.Header().Get("Location"))
	}
	var listed restPage[types.EnrollmentKey]
	restRequire(t, f.request(t, "GET", keys+"?serviceAccountId="+sa.ID, f.aliceToken, nil), 200, &listed)
	if listed.Pagination.Total != 1 || listed.Items[0].ID != key.Key.ID {
		t.Fatalf("keys by service account: %+v", listed)
	}
	restRequire(t, f.request(t, "PATCH", keys+"/"+key.Key.ID, f.aliceToken, map[string]bool{"revoked": false}), 400, nil)
}

func TestRESTTelemetryConsumerVisibilityAndUnavailable(t *testing.T) {
	f := newRESTFixture(t)
	provider := f.enroll(t, f.alice, "exit")
	status := edge.Snapshot{Connections: []edge.ConnectionStatus{{ID: "provider-connection", WorkspaceID: f.workspace.ID, AgentID: provider.Agent.ID, Role: "provider"}}, Flows: []edge.FlowStatus{{ID: "alice-flow", WorkspaceID: f.workspace.ID, AgentID: provider.Agent.ID, Consumer: &api.Principal{Type: api.PrincipalUser, ID: f.alice.ID}, Purpose: "egress"}, {ID: "bob-flow", WorkspaceID: f.workspace.ID, AgentID: provider.Agent.ID, Consumer: &api.Principal{Type: api.PrincipalUser, ID: f.bob.ID}, Purpose: "egress"}, {ID: "anonymous-flow", WorkspaceID: f.workspace.ID, AgentID: provider.Agent.ID, Purpose: "serve"}}}
	telemetry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, status) }))
	defer telemetry.Close()
	client, err := controlrpc.NewClient(telemetry.URL, "internal-test-token-over-thirty-two-characters")
	if err != nil {
		t.Fatal(err)
	}
	f.app.status = client
	var flows restPage[edge.FlowStatus]
	base := "/v1/workspaces/" + f.workspace.ID
	restRequire(t, f.request(t, "GET", base+"/flows", f.aliceToken, nil), 200, &flows)
	if len(flows.Items) != 1 || flows.Items[0].ID != "alice-flow" {
		t.Fatal("provider owner saw other consumers' flows")
	}
	restRequire(t, f.request(t, "GET", base+"/flows/bob-flow", f.aliceToken, nil), 404, nil)
	restRequire(t, f.request(t, "GET", base+"/flows", f.bobToken, nil), 200, &flows)
	if len(flows.Items) != 1 || flows.Items[0].ID != "bob-flow" {
		t.Fatal("consumer could not see own flow")
	}
	if _, err = f.app.State.SetMembership(f.admin.ID, f.workspace.ID, f.bob.ID, "maintainer"); err != nil {
		t.Fatal(err)
	}
	restRequire(t, f.request(t, "GET", base+"/flows", f.bobToken, nil), 200, &flows)
	if len(flows.Items) != 3 {
		t.Fatal("manager could not inspect workspace flows")
	}
	telemetry.Close()
	for _, resource := range []string{"connections", "flows"} {
		restRequire(t, f.request(t, "GET", base+"/"+resource, f.aliceToken, nil), 503, nil)
	}
	var instance struct {
		EdgeAvailable bool `json:"edgeAvailable"`
	}
	restRequire(t, f.request(t, "GET", "/v1/instance/status", f.aliceToken, nil), 200, &instance)
	if instance.EdgeAvailable {
		t.Fatal("unavailable Edge reported healthy")
	}
	restRequire(t, f.request(t, "GET", "/v1/workspaces", f.aliceToken, nil), 200, nil)
}

func TestRESTJSONMediaAndSizeErrors(t *testing.T) {
	f := newRESTFixture(t)
	for _, test := range []struct {
		media, body string
		want        int
	}{{"application/json-evil", `{"name":"new-user"}`, 415}, {"application/json", `{"name":"` + strings.Repeat("x", 1<<20) + `"}`, 413}, {"application/json", `{"name":"new-user"} {}`, 400}} {
		r := httptest.NewRequest("POST", f.app.Config.ServerURL+"/v1/admin/users", strings.NewReader(test.body))
		r.Header.Set("Authorization", "Bearer "+restBootstrap)
		r.Header.Set("Content-Type", test.media)
		out := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(out, r)
		restRequire(t, out, test.want, nil)
	}
}

func TestRESTServeRouteUsesDevelopmentPort(t *testing.T) {
	app := &App{Config: Config{ServerURL: "http://localhost:8080"}}
	route := app.serveRoute(types.Service{ID: "s", Hostname: "demo.work.localhost", Enabled: true})
	if route.Rule.Protocol != "http" || !slices.Equal(route.Rule.Ports, []int{8080}) {
		t.Fatalf("wrong dev route: %+v", route)
	}
	// Keep this description independent from policy mutation: egress HTTP still fails validation.
	rule := policy.Rule{Domain: route.Rule.Domain, Ports: route.Rule.Ports, Protocol: route.Rule.Protocol}
	if rule.Validate(true) == nil {
		t.Fatal("development serve view weakened egress TLS policy")
	}
}

func TestRESTDeveloperCanReadAndRevokeOwnServiceGrants(t *testing.T) {
	f := newRESTFixture(t)
	alice := f.enroll(t, f.alice, "alice-grants")
	bob := f.enroll(t, f.bob, "bob-grants")
	snap, err := f.app.State.SnapshotForUser(f.admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	var aliceService, bobGrant string
	for _, service := range snap.Services {
		if service.AgentID == alice.Agent.ID {
			aliceService = service.ID
		}
	}
	for _, grant := range snap.Grants {
		for _, service := range snap.Services {
			if service.ID == grant.ResourceID && service.AgentID == bob.Agent.ID {
				bobGrant = grant.ID
			}
		}
	}
	if aliceService == "" || bobGrant == "" {
		t.Fatal("missing fixture resources")
	}
	base := "/v1/workspaces/" + f.workspace.ID + "/grants"
	var grant types.Grant
	response := f.request(t, "POST", base, f.aliceToken, map[string]any{"resourceType": "service", "resourceId": aliceService, "subjectType": "user", "subjectId": f.bob.ID, "expiresAt": time.Now().Add(time.Hour)})
	restRequire(t, response, 201, &grant)
	path := base + "/" + grant.ID
	restRequire(t, f.request(t, "GET", path, f.aliceToken, nil), 200, &grant)
	if grant.Revoked {
		t.Fatal("created grant already revoked")
	}
	var grants restPage[types.Grant]
	restRequire(t, f.request(t, "GET", base, f.aliceToken, nil), 200, &grants)
	if grants.Pagination.Total != 2 {
		t.Fatalf("expected own default plus new grant only: %+v", grants)
	}
	for _, visible := range grants.Items {
		if visible.ResourceID != aliceService {
			t.Fatal("other provider grant exposed")
		}
	}
	restRequire(t, f.request(t, "GET", base+"/"+bobGrant, f.aliceToken, nil), 404, nil)
	restRequire(t, f.request(t, "PATCH", base+"/"+bobGrant, f.aliceToken, map[string]bool{"revoked": true}), 404, nil)
	restRequire(t, f.request(t, "PATCH", path, f.aliceToken, map[string]bool{"revoked": true}), 200, &grant)
	if !grant.Revoked {
		t.Fatal("own service grant was not revoked")
	}
}

func TestRESTRoleStatusUsesEffectiveConfigAndOwnerState(t *testing.T) {
	f := newRESTFixture(t)
	enrolled := f.enroll(t, f.bob, "role-projection")
	next := enrolled.Agent.Desired.Clone()
	next.Egress.Enabled = false
	updated, err := f.app.State.UpdateAgent(enrolled.Credentials.ControlToken, api.ConfigUpdate{Workspace: f.workspace.Name, Name: "role-projection", Desired: next})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Effective.Egress) != 0 {
		t.Fatal("fixture did not narrow the effective configuration")
	}
	base := "/v1/workspaces/" + f.workspace.ID
	var role AgentRoleView
	restRequire(t, f.request(t, "GET", base+"/egresses/"+enrolled.Agent.ID, f.bobToken, nil), 200, &role)
	if role.Enabled || role.Status != "active" || role.EffectiveRevision != updated.Effective.Revision {
		t.Fatalf("role view enabled egress outside the effective configuration: %+v", role)
	}
	if err = f.app.State.DisableUser(f.admin.ID, f.bob.ID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"ingresses", "egresses"} {
		restRequire(t, f.request(t, "GET", "/v1/admin/"+kind+"/"+enrolled.Agent.ID, restBootstrap, nil), 200, &role)
		if role.Enabled || role.Status != "revoked" || role.Agent.Status != "revoked" {
			t.Fatalf("disabled owner left %s enabled: %+v", kind, role)
		}
	}
	var adminView, memberView restPage[ServerView]
	restRequire(t, f.request(t, "GET", "/v1/admin/servers?agentId="+enrolled.Agent.ID, restBootstrap, nil), 200, &adminView)
	restRequire(t, f.request(t, "GET", base+"/servers?agentId="+enrolled.Agent.ID, f.aliceToken, nil), 200, &memberView)
	if len(adminView.Items) != 1 || len(memberView.Items) != 1 || adminView.Items[0].Agent.Status != "revoked" || memberView.Items[0].Agent.Status != "revoked" {
		t.Fatalf("inconsistent server summaries: admin=%+v member=%+v", adminView, memberView)
	}

	// Effective projection must not widen who can inspect the full configuration.
	restRequire(t, f.request(t, "GET", base+"/agents/"+enrolled.Agent.ID, f.aliceToken, nil), 404, nil)
	if _, err = f.app.State.SetMembership(f.admin.ID, f.workspace.ID, f.alice.ID, "maintainer"); err != nil {
		t.Fatal(err)
	}
	for _, viewer := range []struct{ path, token string }{{"/v1/admin/agents", restBootstrap}, {base + "/agents", f.aliceToken}} {
		var list restPage[api.Agent]
		restRequire(t, f.request(t, "GET", viewer.path+"?status=revoked", viewer.token, nil), 200, &list)
		if list.Pagination.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != enrolled.Agent.ID || list.Items[0].Status != "revoked" {
			t.Fatalf("agent list did not show the stored revocation: %+v", list)
		}
		var detail api.Agent
		restRequire(t, f.request(t, "GET", viewer.path+"/"+enrolled.Agent.ID, viewer.token, nil), 200, &detail)
		if detail.Status != "revoked" || len(detail.Effective.Services) != 0 || len(detail.Effective.Egress) != 0 || detail.Name != "role-projection" {
			t.Fatalf("agent detail projection inconsistent: %+v", detail)
		}
		restRequire(t, f.request(t, "GET", viewer.path+"?status=active", viewer.token, nil), 200, &list)
		if list.Pagination.Total != 0 || len(list.Items) != 0 {
			t.Fatal("status filter retained a revoked agent")
		}
	}
}

func TestRESTServiceAccountLifecycleAndVisibility(t *testing.T) {
	f := newRESTFixture(t)
	base := "/v1/workspaces/" + f.workspace.ID + "/service-accounts"
	restRequire(t, f.request(t, "POST", "/v1/admin/service-accounts", restBootstrap, map[string]any{"name": "fleet"}), 400, nil)
	var sa types.ServiceAccount
	restRequire(t, f.request(t, "POST", "/v1/admin/service-accounts", restBootstrap, map[string]any{"workspaceId": f.workspace.ID, "name": "fleet"}), 201, &sa)
	restRequire(t, f.request(t, "POST", base, restBootstrap, map[string]any{"name": "fleet"}), 409, nil)
	var accounts restPage[types.ServiceAccount]
	restRequire(t, f.request(t, "GET", base, f.aliceToken, nil), 200, &accounts)
	if accounts.Pagination.Total != 0 {
		t.Fatal("a developer listed service accounts")
	}
	restRequire(t, f.request(t, "PATCH", base+"/"+sa.ID, f.aliceToken, map[string]any{"description": "mine"}), 404, nil)
	for _, body := range []any{map[string]any{"description": "x", "status": "revoked"}, map[string]any{"workspaceId": f.foreign.ID}, map[string]any{"status": "active"}, map[string]any{"name": "renamed"}} {
		restRequire(t, f.request(t, "PATCH", base+"/"+sa.ID, restBootstrap, body), 400, nil)
	}
	restRequire(t, f.request(t, "PATCH", base+"/"+sa.ID, restBootstrap, map[string]any{"description": "fleet runners"}), 200, &sa)
	if sa.Description != "fleet runners" || sa.WorkspaceID != f.workspace.ID {
		t.Fatalf("updated service account: %+v", sa)
	}
	var key struct {
		Key   types.EnrollmentKey `json:"key"`
		Token string              `json:"token"`
	}
	restRequire(t, f.request(t, "POST", base+"/"+sa.ID+"/enrollment-keys", restBootstrap, map[string]any{"roles": []string{"ingress", "serve", "egress"}, "maxUses": 2}), 201, &key)
	desired := api.AgentSpec{Ingress: api.IngressSpec{Enabled: true}}
	enrolled, err := f.app.State.Enroll(types.EnrollRequest{EnrollmentKey: key.Token, Instance: f.app.Config.ServerURL, Endpoint: f.app.Config.EdgeURL, Workspace: f.workspace.Name, Name: "runner", Desired: desired})
	if err != nil {
		t.Fatal(err)
	}
	var agents restPage[types.Agent]
	restRequire(t, f.request(t, "GET", "/v1/admin/agents?principalType=service_account&principalId="+sa.ID, restBootstrap, nil), 200, &agents)
	if agents.Pagination.Total != 1 || agents.Items[0].ID != enrolled.Agent.ID || agents.Items[0].CreatedBy != f.admin.ID {
		t.Fatalf("agents by principal: %+v", agents)
	}
	restRequire(t, f.request(t, "GET", "/v1/workspaces/"+f.workspace.ID+"/agents", f.aliceToken, nil), 200, &agents)
	if agents.Pagination.Total != 0 {
		t.Fatal("a developer saw a service account device")
	}
	restRequire(t, f.request(t, "PATCH", base+"/"+sa.ID, restBootstrap, map[string]any{"status": "revoked"}), 200, &sa)
	if sa.Status != api.StatusRevoked {
		t.Fatalf("revoked service account: %+v", sa)
	}
	if agent, _ := f.app.State.Agent(enrolled.Agent.ID); agent.Status != api.StatusRevoked {
		t.Fatal("revoking the service account left its device active")
	}
	restRequire(t, f.request(t, "POST", base+"/"+sa.ID+"/enrollment-keys", restBootstrap, map[string]any{"roles": []string{"ingress"}}), 403, nil)
	restRequire(t, f.request(t, "PATCH", base+"/"+sa.ID, restBootstrap, map[string]any{"description": "again"}), 409, nil)
	restRequire(t, f.request(t, "GET", "/v1/admin/service-accounts?status=revoked", restBootstrap, nil), 200, &accounts)
	if accounts.Pagination.Total != 1 || accounts.Items[0].ID != sa.ID {
		t.Fatalf("revoked filter: %+v", accounts)
	}
}
