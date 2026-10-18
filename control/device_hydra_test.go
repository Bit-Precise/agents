package control

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bit-precise/agents/internal/hydra/hydratest"
)

// TestDeviceFlowAgainstRealHydra runs the Device Flow tests against the Ory
// Hydra binary named by AGENTS_TEST_HYDRA_BIN (v26.2.0 is the tested version),
// so the in-memory stand-in cannot drift from the real redirects and answers.
func TestDeviceFlowAgainstRealHydra(t *testing.T) {
	binary := os.Getenv("AGENTS_TEST_HYDRA_BIN")
	if binary == "" {
		t.Skip("set AGENTS_TEST_HYDRA_BIN to a Hydra binary to test against a real authorization server")
	}
	start := realHydra(binary)
	t.Run("confirmed principal", func(t *testing.T) { testConfirmedPrincipal(t, startDeviceFixtureWith(t, start)) })
	t.Run("service account and substitution", func(t *testing.T) { testServiceAccountAndSubstitution(t, startDeviceFixtureWith(t, start)) })
	t.Run("decline and sessions", func(t *testing.T) { testDeclineAndSessions(t, startDeviceFixtureWith(t, start)) })
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return port
}

func realHydra(binary string) hydraStarter {
	return func(t *testing.T, urls hydratest.URLs) (string, string, *hydratest.Server) {
		t.Helper()
		public, admin := freePort(t), freePort(t)
		issuer := "http://127.0.0.1:" + public
		config := fmt.Sprintf(`dsn: memory
serve:
  public: {host: 127.0.0.1, port: %s}
  admin: {host: 127.0.0.1, port: %s}
urls:
  self: {issuer: %q}
  login: %q
  consent: %q
  error: %q
  device: {verification: %q, success: %q}
secrets:
  system: ["real-hydra-test-system-secret-0123456789"]
strategies:
  access_token: opaque
oauth2:
  device_authorization:
    token_polling_interval: 1s
log:
  level: warn
`, public, admin, issuer, urls.Login, urls.Consent, urls.Error, urls.Verification, urls.Success)
		dir := t.TempDir()
		path := filepath.Join(dir, "hydra.yaml")
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		logs, err := os.Create(filepath.Join(dir, "hydra.log"))
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "serve", "all", "--dev", "--config", path)
		command.Stdout, command.Stderr = logs, logs
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait(); logs.Close() })
		adminURL := "http://127.0.0.1:" + admin
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if response, err := http.Get(adminURL + "/health/ready"); err == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return issuer, adminURL, nil
				}
			}
			if time.Now().After(deadline) {
				data, _ := os.ReadFile(filepath.Join(dir, "hydra.log"))
				t.Fatalf("hydra did not become ready: %s", data)
			}
		}
	}
}
