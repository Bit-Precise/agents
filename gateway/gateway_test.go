package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	g, err := New(t.Context(), control.URL, "http://localhost:8080/agent", "localhost", "shared-internal-secret-of-32-characters", edge.Config{AllowLoopbackHTTP: true})
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

func TestPlatformPathsDoNotShadowPublishedServices(t *testing.T) {
	var authorized atomic.Int32
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != controlrpc.Prefix+"authorize" {
			http.Error(w, "unavailable", 503)
			return
		}
		authorized.Add(1)
		_ = json.NewEncoder(w).Encode(controlrpc.MetadataResult{Status: 404, Body: []byte("service")})
	}))
	defer control.Close()
	g, err := New(t.Context(), control.URL, "http://connect.localhost:8080/agent", "localhost", "shared-internal-secret-of-32-characters", edge.Config{AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	server := httptest.NewServer(g.Handler())
	defer server.Close()
	get := func(host, path string) (int, string) {
		r, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		r.Host = host
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	if status, body := get("connect.localhost", "/healthz"); status != 200 || body != "ok\n" {
		t.Fatalf("platform health check = %d %q", status, body)
	}
	for _, path := range []string{"/healthz", controlrpc.Prefix + "snapshot"} {
		if status, body := get("demo.work.localhost", path); status != 404 || body != "service" {
			t.Fatalf("%s on a service host was answered by the platform: %d %q", path, status, body)
		}
	}
	if authorized.Load() != 2 {
		t.Fatalf("service requests reached Control authorization %d times", authorized.Load())
	}
}
