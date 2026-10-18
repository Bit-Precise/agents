// Package controlrpc is the authenticated, metadata-only contract between the
// single Control owner and Edge. Business request bodies never enter this API.
package controlrpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
	"github.com/bit-precise/agents/edge"
)

const Prefix = "/internal/v1/"
const MaxMessage = 2 << 20

type Metadata struct {
	Method string      `json:"method"`
	Host   string      `json:"host"`
	URI    string      `json:"uri"`
	Header http.Header `json:"header"`
}

type Authorization struct {
	UserID         string    `json:"userId"`
	IdentityExpiry time.Time `json:"identityExpiry"`
	ConsumeBearer  bool      `json:"consumeBearer"`
}

type MetadataResult struct {
	Authorization *Authorization `json:"authorization,omitempty"`
	Status        int            `json:"status,omitempty"`
	Header        http.Header    `json:"header,omitempty"`
	Body          []byte         `json:"body,omitempty"`
}

type Watch struct {
	Revision uint64 `json:"revision"`
}

// Protect does not trust network location, Host, or forwarded headers.
func Protect(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if len(token) < 32 || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			http.Error(w, "internal authentication required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type Client struct {
	base, token string
	http        *http.Client
}

func NewClient(base, token string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") || len(token) < 32 {
		return nil, errors.New("internal client requires an HTTP(S) origin and a shared token of at least 32 characters")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxConnsPerHost = 64
	transport.ResponseHeaderTimeout = 25 * time.Second
	return &Client{base: strings.TrimSuffix(base, "/"), token: token, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) call(ctx context.Context, operation string, input, output any, timeout time.Duration) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+Prefix+operation, bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return &types.Error{Kind: types.KindUnavailable, Message: "control unavailable"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxMessage+1))
	if err != nil || len(body) > MaxMessage {
		return errors.New("invalid internal response")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error struct {
				Kind    types.ErrorKind `json:"kind"`
				Message string          `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Error.Kind != "" {
			return &types.Error{Kind: failure.Error.Kind, Message: failure.Error.Message}
		}
		return &types.Error{Kind: types.KindUnavailable, Message: "internal request rejected"}
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal(body, output)
}

func (c *Client) Identity(token string) (out types.Identity, err error) {
	err = c.call(context.Background(), "identity", map[string]string{"token": token}, &out, 3*time.Second)
	return
}
func (c *Client) Agent(id string) (out api.Agent, err error) {
	err = c.call(context.Background(), "agent", map[string]string{"id": id}, &out, 3*time.Second)
	return
}
func (c *Client) ResolveServe(host, user string) (out types.Decision, err error) {
	err = c.call(context.Background(), "serve", map[string]string{"host": host, "user": user}, &out, 3*time.Second)
	return
}
func (c *Client) ResolveEgress(workspace, user, host string, port int) (out types.Decision, err error) {
	err = c.call(context.Background(), "egress", struct {
		Workspace, User, Host string
		Port                  int
	}{workspace, user, host, port}, &out, 3*time.Second)
	return
}
func (c *Client) Authorize(ctx context.Context, request Metadata) (out MetadataResult, err error) {
	err = c.call(ctx, "authorize", request, &out, 3*time.Second)
	return
}
func (c *Client) Snapshot(ctx context.Context) (out edge.Snapshot, err error) {
	err = c.call(ctx, "snapshot", nil, &out, 3*time.Second)
	return
}

// A long poll is only a hint: Edge also revalidates on its bounded timer and
// fails closed when RPCs fail. Reconnects cannot hide a missed mutation.
func (c *Client) Subscribe() (<-chan struct{}, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	go func() {
		defer close(events)
		var revision Watch
		for ctx.Err() == nil {
			err := c.call(ctx, "watch", revision, &revision, 25*time.Second)
			if ctx.Err() != nil {
				return
			}
			select {
			case events <- struct{}{}:
			default:
			}
			if err != nil {
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
	}()
	return events, cancel
}

var _ edge.Backend = (*Client)(nil)
