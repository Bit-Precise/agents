package control

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bit-precise/agents/control/types"
	"github.com/bit-precise/agents/internal/controlrpc"
)

func TestSplitControlAuthenticatesInternalAPIAndStripsConsoleCredentials(t *testing.T) {
	const admin = "admin-secret-with-more-than-thirty-two-characters"
	const internal = "internal-secret-with-more-than-thirty-two-characters"
	consoleCalls := 0
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		consoleCalls++
		for _, key := range []string{"Cookie", "Authorization", "Proxy-Authorization", "X-CSRF-Token"} {
			if r.Header.Get(key) != "" {
				t.Errorf("console received %s", key)
			}
		}
		_, _ = io.WriteString(w, "static console")
	}))
	defer console.Close()
	cfg := Config{ListenAddress: "127.0.0.1:8080", ServerURL: "https://agents.test", EdgeURL: "https://edge.agents.test/agent", TLSOffload: true, InternalTokenFile: "configured-secret", ConsoleURL: console.URL, EdgeInternalURL: "http://127.0.0.1:1"}
	cfg.Database.Type, cfg.Database.DSN = "sqlite", filepath.Join(t.TempDir(), "control.db")
	app, err := NewControl(t.Context(), cfg, admin, internal)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.Edge != nil {
		t.Fatal("Control embedded an Edge")
	}
	request := httptest.NewRequest("GET", cfg.ServerURL+"/console/", nil)
	for _, key := range []string{"Cookie", "Authorization", "Proxy-Authorization", "X-CSRF-Token"} {
		request.Header.Set(key, "private-value")
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != 200 || response.Body.String() != "static console" || consoleCalls != 1 {
		t.Fatalf("console response: %d %s", response.Code, response.Body.String())
	}
	data, _ := json.Marshal(map[string]string{"token": admin})
	for _, credentials := range [][]string{nil, {"Bearer wrong"}, {"Bearer " + internal, "Bearer " + internal}, {"Bearer " + internal}} {
		request := httptest.NewRequest("POST", cfg.ServerURL+controlrpc.Prefix+"identity", bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		request.Header["Authorization"] = credentials
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		want := 401
		if len(credentials) == 1 && credentials[0] == "Bearer "+internal {
			want = 200
		}
		if response.Code != want {
			t.Fatalf("internal response %d want %d", response.Code, want)
		}
	}
	request = httptest.NewRequest("POST", "https://service.work.agents.test/upload", strings.NewReader("business body"))
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatal("Control accepted business request")
	}
	adminIdentity, err := app.State.Identity(admin)
	if err != nil {
		t.Fatal(err)
	}
	var firstToken, firstUserID, firstWorkspace, foreignWorkspace string
	for _, name := range []string{"one", "two"} {
		user, token, err := app.State.CreateUser(adminIdentity.UserID, name, false)
		if err != nil {
			t.Fatal(err)
		}
		workspace, err := app.State.CreateWorkspace(adminIdentity.UserID, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.State.SetMembership(adminIdentity.UserID, workspace.ID, user.ID, "developer"); err != nil {
			t.Fatal(err)
		}
		if name == "one" {
			firstToken, firstUserID, firstWorkspace = token, user.ID, workspace.ID
		} else {
			foreignWorkspace = workspace.ID
		}
	}
	getResource := func(path string, want int, out any) {
		t.Helper()
		request := httptest.NewRequest("GET", cfg.ServerURL+path, nil)
		request.Header.Set("Authorization", "Bearer "+firstToken)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("GET %s returned %d, want %d: %s", path, response.Code, want, response.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(response.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
		}
	}
	var status struct {
		Revision      uint64 `json:"revision"`
		EdgeAvailable bool   `json:"edgeAvailable"`
	}
	getResource("/v1/instance/status", 200, &status)
	if status.EdgeAvailable || status.Revision == 0 {
		t.Fatalf("invalid Edge-unavailable instance status: %+v", status)
	}
	var workspaces struct {
		Items []types.Workspace `json:"items"`
	}
	getResource("/v1/workspaces", 200, &workspaces)
	if len(workspaces.Items) != 2 {
		t.Fatalf("Edge loss did not preserve scoped workspaces: %+v", workspaces.Items)
	}
	for _, workspace := range workspaces.Items {
		if workspace.ID != firstWorkspace && workspace.Name != "personal-"+firstUserID {
			t.Fatal("Edge loss exposed a workspace outside the user's personal and assigned workspaces")
		}
	}
	getResource("/v1/workspaces/"+foreignWorkspace, 404, nil)
	getResource("/v1/workspaces/"+firstWorkspace+"/connections", 503, nil)
	getResource("/v1/workspaces/"+firstWorkspace+"/flows", 503, nil)
	getResource("/api/v1/state", 404, nil)
	if consoleCalls != 1 {
		t.Fatal("management API request fell through to the Console")
	}
}

func TestProjectedSecretsAndDatabaseDSNFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "projected-data")
	if err := os.WriteFile(target, []byte("postgres://user:private@db/agents\n"), 0440); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "database-dsn")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if value, err := ReadSecret(link); err != nil || value != "postgres://user:private@db/agents" {
		t.Fatalf("projected secret: %q %v", value, err)
	}
	configPath := filepath.Join(dir, "agents.yaml")
	data := "listenAddress: ':8080'\nserverURL: https://agents.test\nedgeURL: https://edge.agents.test/agent\ntlsOffload: true\ninternalTokenFile: internal.token\nedgeInternalURL: http://edge:8080\nconsoleURL: http://console:8080\ndatabase:\n  type: postgres\n  dsnFile: database-dsn\n"
	if err := os.WriteFile(configPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFor(configPath, "control")
	if err != nil || cfg.Database.DSN != "postgres://user:private@db/agents" || cfg.Database.DSNFile != link {
		t.Fatalf("DSN file config: %v", err)
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecret(link); err == nil {
		t.Fatal("world-readable server secret accepted")
	}
	if _, err := LoadConfigFor(configPath, "control"); err == nil {
		t.Fatal("world-readable database secret accepted")
	}
	if _, err := LoadConfigFor(configPath, "console"); err != nil {
		t.Fatalf("Console read private database secret: %v", err)
	}
}
