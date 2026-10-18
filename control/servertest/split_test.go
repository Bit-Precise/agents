package servertest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bit-precise/agentd/config"
	"github.com/bit-precise/agents/control"
	"github.com/bit-precise/agents/internal/controlrpc"
	"gopkg.in/yaml.v3"
)

const splitToken = "test-private-internal-token-with-32-characters"

func TestIndependentProcessesServeEgressAndControlFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and launches three real server processes")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "agents")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "../../cmd/agents")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agents: %v\n%s", err, output)
	}
	controlAddr, edgeAddr, consoleAddr := freeAddress(t), freeAddress(t), freeAddress(t)
	origin := func(addr string) string { return "http://localhost:" + strings.Split(addr, ":")[1] }
	cfg := control.Config{ListenAddress: controlAddr, ServerURL: origin(controlAddr), EdgeURL: origin(edgeAddr) + "/agent", StateDir: filepath.Join(dir, "state"), AdminTokenFile: filepath.Join(dir, "admin.token"), InternalTokenFile: filepath.Join(dir, "internal.token"), DevelopmentHTTP: true, ControlURL: origin(controlAddr), EdgeInternalURL: origin(edgeAddr), ConsoleURL: origin(consoleAddr)}
	cfg.Database.Type, cfg.Database.DSN = "sqlite", filepath.Join(dir, "state", "control.db")
	for path, secret := range map[string]string{cfg.AdminTokenFile: adminToken, cfg.InternalTokenFile: splitToken} {
		if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(client.CloseIdleConnections)
	start := func(mode, addr string) func() {
		t.Helper()
		processCfg := cfg
		processCfg.ListenAddress = addr
		if mode != "control" {
			// An Edge/Console process must not read or create database/admin state.
			processCfg.StateDir = filepath.Join(dir, mode+"-must-not-exist")
			processCfg.AdminTokenFile = filepath.Join(dir, "absent-admin-token")
			processCfg.Database.Type, processCfg.Database.DSN = "postgres", "postgres://invalid.invalid/forbidden"
		}
		path := filepath.Join(dir, mode+".yaml")
		data, _ := yaml.Marshal(processCfg)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(dir, mode+".log")
		log, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, mode, "--config", path)
		command.Stdout, command.Stderr = log, log
		if err := command.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait(); log.Close() }()
		var once sync.Once
		stop := func() { once.Do(func() { _ = command.Process.Kill(); <-done }) }
		t.Cleanup(stop)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := client.Get(origin(addr) + "/healthz")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return stop
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		logs, _ := os.ReadFile(logPath)
		t.Fatalf("%s did not become healthy: %s", mode, logs)
		return stop
	}
	start("console", consoleAddr)
	stopControl := start("control", controlAddr)
	start("edge", edgeAddr)
	status, err := controlrpc.NewClient(cfg.EdgeInternalURL, splitToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(status.Close)
	h := &harness{t: t, cfg: cfg, base: cfg.ServerURL, edgeBase: cfg.EdgeInternalURL, remoteStatus: status, client: client}
	for _, base := range []string{cfg.ControlURL, cfg.EdgeInternalURL} {
		response, err := client.Post(base+controlrpc.Prefix+"snapshot", "application/json", strings.NewReader("null"))
		if err != nil {
			t.Fatal(err)
		}
		readResponse(t, response, 401)
	}
	response, err := client.Get(cfg.ServerURL + "/console/")
	if err != nil {
		t.Fatal(err)
	}
	if body := readResponse(t, response, 200); !strings.Contains(body, "id=\"root\"") || !strings.Contains(body, "/assets/app.js") {
		t.Fatal("console assets not served through Control")
	}
	response, err = client.Get(cfg.ServerURL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "javascript") {
		t.Fatal("compiled Console asset returned a non-JavaScript response")
	}
	if len(readResponse(t, response, 200)) == 0 {
		t.Fatal("compiled Console asset is empty")
	}
	for _, mode := range []string{"edge", "console"} {
		if _, err := os.Stat(filepath.Join(dir, mode+"-must-not-exist")); !os.IsNotExist(err) {
			t.Fatalf("%s created private state", mode)
		}
	}
	// Exercise actual supervisors, TLS HTTP CONNECT and SOCKS, grants and
	// resource-specific revocation over independent Control/Edge processes.
	testHTTPAndSOCKSEgressOnHarness(t, h, false)
	testSplitServiceSSO(t, h, client)

	w, u := h.workspace("failure", "failure-user")
	originCanceled := make(chan struct{})
	originServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ready\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(originCanceled)
	}))
	t.Cleanup(originServer.Close)
	agentCfg := h.agentConfig(w.Name, "provider")
	agentCfg.Serve = config.ServeConfig{Enabled: true, Services: []config.ServiceConfig{{Name: "stream", Origin: originServer.URL}}}
	enrolled, _ := h.enroll(agentCfg, w, u, []string{"serve"})
	h.runAgent(agentCfg, enrolled)
	await(t, func() bool { return h.connected(enrolled.Agent.ID, "provider") })
	stream, err := h.serveRequest(serviceHost("stream", w.Name), u.Token, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if line, err := bufio.NewReader(stream.Body).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("stream start: %q %v", line, err)
	}
	stopControl()
	select {
	case <-originCanceled:
	case <-time.After(6 * time.Second):
		t.Fatal("Control loss did not close active flow")
	}
	response, err = h.serveRequest(serviceHost("stream", w.Name), u.Token, "/")
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, response, 503)
	await(t, func() bool {
		snapshot, err := status.Snapshot(t.Context())
		return err == nil && len(snapshot.Connections) == 0 && len(snapshot.Flows) == 0
	})
}

func testSplitServiceSSO(t *testing.T, h *harness, client *http.Client) {
	w, u := h.workspace("sso", "sso-user")
	originServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]string{"authorization": r.Header.Get("Authorization"), "cookie": r.Header.Get("Cookie"), "body": string(body)})
	}))
	defer originServer.Close()
	cfg := h.agentConfig(w.Name, "provider")
	cfg.Serve = config.ServeConfig{Enabled: true, Services: []config.ServiceConfig{{Name: "private", Origin: originServer.URL}}}
	enrolled, _ := h.enroll(cfg, w, u, []string{"serve"})
	stop := h.runAgent(cfg, enrolled)
	defer stop()
	await(t, func() bool { return h.connected(enrolled.Agent.ID, "provider") })
	serverURL, _ := url.Parse(h.base)
	host := serviceHost("private", w.Name) + ":" + serverURL.Port()
	request := func(base, method, path, token string, cookies []*http.Cookie, body string, html bool) *http.Response {
		t.Helper()
		r, _ := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader(body))
		if base == h.edgeBase {
			r.Host = host
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if html {
			r.Header.Set("Accept", "text/html")
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	login := request(h.base, "POST", "/v1/sessions", "", nil, `{"token":"`+u.Token+`"}`, false)
	loginCookies := login.Cookies()
	readResponse(t, login, 201)
	first := request(h.edgeBase, "GET", "/", "", nil, "", true)
	stateCookies := first.Cookies()
	authorize, _ := url.Parse(first.Header.Get("Location"))
	readResponse(t, first, 303)
	approved := request(h.base, "GET", authorize.RequestURI(), "", loginCookies, "", false)
	callback, _ := url.Parse(approved.Header.Get("Location"))
	readResponse(t, approved, 303)
	callbackResponse := request(h.edgeBase, "GET", callback.RequestURI(), "", stateCookies, "", false)
	serviceCookies := callbackResponse.Cookies()
	readResponse(t, callbackResponse, 303)
	// Business bodies are independent of Control's bounded JSON decoder.
	payload := strings.Repeat("private-request-body", 70000)
	response := request(h.edgeBase, "POST", "/", u.Token, serviceCookies, payload, false)
	var got map[string]string
	data := readResponse(t, response, 200)
	if err := json.Unmarshal([]byte(data), &got); err != nil {
		t.Fatal(err)
	}
	if got["body"] != payload || got["authorization"] != "" || strings.Contains(got["cookie"], "agents-") {
		t.Fatal("request body or platform credential isolation failed")
	}
	response = request(h.edgeBase, "GET", "/", "tenant-token", serviceCookies, "", false)
	if body := readResponse(t, response, 200); !bytes.Contains([]byte(body), []byte("Bearer tenant-token")) {
		t.Fatal("tenant bearer was consumed")
	}
	replay := request(h.edgeBase, "GET", callback.RequestURI(), "", stateCookies, "", false)
	readResponse(t, replay, 403)
}
