package controlrpc

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestInternalClientNeverFollowsRedirects(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	c, err := NewClient(redirect.URL, "internal-secret-with-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Identity("provider-token"); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("internal credentials followed redirect")
	}
}
