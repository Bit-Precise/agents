package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bit-precise/agents/edge"
	"github.com/bit-precise/agents/internal/controlrpc"
)

func TestDeniedMetadataDoesNotWaitForUnsentBusinessBody(t *testing.T) {
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != controlrpc.Prefix+"authorize" {
			http.Error(w, "unavailable", 503)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil || len(data) > 4096 || strings.Contains(string(data), "business-body") {
			t.Error("business payload entered metadata request")
		}
		_ = json.NewEncoder(w).Encode(controlrpc.MetadataResult{Status: 403, Body: []byte("denied")})
	}))
	defer control.Close()
	g, err := New(t.Context(), control.URL, "http://localhost:8080/agent", "shared-internal-secret-of-32-characters", edge.Config{AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	server := httptest.NewServer(g.Handler())
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = io.WriteString(conn, "POST /upload HTTP/1.1\r\nHost: private.work.localhost\r\nContent-Length: 100000\r\n\r\nbusiness-body")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("denied upload blocked on unfinished request body: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("response status %d", response.StatusCode)
	}
}
