// Package hydra is the subset of the Ory Hydra Admin API that Control uses to
// run agentd Device Flow registration. The Admin API is private: only Control
// reaches it, never a browser or agentd.
package hydra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceCodeGrant is the RFC 8628 grant type.
const DeviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"

type Config struct {
	AdminURL   string
	AdminToken string
	Timeout    time.Duration
}

type Client struct {
	adminURL string
	token    string
	http     *http.Client
}

// APIError keeps Hydra's status so callers can tell an unknown or used
// challenge (4xx) from an outage.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Hydra admin API returned HTTP %d: %s", e.StatusCode, e.Message)
}

func IsStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

// IsClientError reports a 4xx answer: the challenge, code or token is invalid,
// expired or already used.
func IsClientError(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}

// StringList accepts the array form of recent Hydra versions and the
// space-delimited form of older ones.
type StringList []string

func (s *StringList) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*s = nil
		return nil
	}
	if data[0] == '[' {
		var values []string
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		*s = values
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = strings.Fields(value)
	return nil
}

type ClientInfo struct {
	ClientID string `json:"client_id"`
	Name     string `json:"client_name"`
}

type LoginRequest struct {
	Challenge         string     `json:"challenge"`
	Skip              bool       `json:"skip"`
	Subject           string     `json:"subject"`
	Client            ClientInfo `json:"client"`
	RequestedScope    StringList `json:"requested_scope"`
	RequestedAudience StringList `json:"requested_access_token_audience"`
	RequestURL        string     `json:"request_url"`
}

type ConsentRequest struct {
	Challenge         string         `json:"challenge"`
	Skip              bool           `json:"skip"`
	Subject           string         `json:"subject"`
	Client            ClientInfo     `json:"client"`
	RequestedScope    StringList     `json:"requested_scope"`
	RequestedAudience StringList     `json:"requested_access_token_audience"`
	Context           map[string]any `json:"context"`
	LoginChallenge    string         `json:"login_challenge"`
}

type AcceptLoginRequest struct {
	Subject  string         `json:"subject"`
	Remember bool           `json:"remember"`
	Context  map[string]any `json:"context,omitempty"`
}

type AcceptConsentRequest struct {
	GrantScope    []string        `json:"grant_scope"`
	GrantAudience []string        `json:"grant_access_token_audience"`
	Remember      bool            `json:"remember"`
	Session       *ConsentSession `json:"session,omitempty"`
}

type ConsentSession struct {
	AccessToken map[string]any `json:"access_token,omitempty"`
}

type RejectRequest struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
	StatusCode       int    `json:"status_code,omitempty"`
}

// Redirect is Hydra's next browser destination after an accept or reject.
type Redirect struct {
	RedirectTo string `json:"redirect_to"`
}

// IntrospectedToken is the RFC 7662 answer; Ext carries the session data set
// at consent.
type IntrospectedToken struct {
	Active    bool           `json:"active"`
	Scope     string         `json:"scope"`
	ClientID  string         `json:"client_id"`
	Subject   string         `json:"sub"`
	ExpiresAt int64          `json:"exp"`
	Audience  StringList     `json:"aud"`
	Issuer    string         `json:"iss"`
	TokenUse  string         `json:"token_use"`
	Ext       map[string]any `json:"ext"`
}

// OAuthClient is the part of a Hydra client definition Control owns.
type OAuthClient struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	RedirectURIs            []string `json:"redirect_uris"`
	Scope                   string   `json:"scope"`
	Audience                []string `json:"audience"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	AccessTokenStrategy     string   `json:"access_token_strategy,omitempty"`
	SkipConsent             bool     `json:"skip_consent"`
}

// Lifespans are Go duration strings ("5m"); empty values keep Hydra defaults.
type Lifespans struct {
	DeviceAccessToken string `json:"device_authorization_grant_access_token_lifespan,omitempty"`
}

func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{adminURL: strings.TrimRight(cfg.AdminURL, "/"), token: cfg.AdminToken, http: &http.Client{Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }

func (c *Client) GetLoginRequest(ctx context.Context, challenge string) (out LoginRequest, err error) {
	err = c.do(ctx, http.MethodGet, "/admin/oauth2/auth/requests/login", url.Values{"login_challenge": {challenge}}, nil, &out)
	return
}
func (c *Client) AcceptLogin(ctx context.Context, challenge string, request AcceptLoginRequest) (out Redirect, err error) {
	err = c.do(ctx, http.MethodPut, "/admin/oauth2/auth/requests/login/accept", url.Values{"login_challenge": {challenge}}, request, &out)
	return
}
func (c *Client) RejectLogin(ctx context.Context, challenge string, request RejectRequest) (out Redirect, err error) {
	err = c.do(ctx, http.MethodPut, "/admin/oauth2/auth/requests/login/reject", url.Values{"login_challenge": {challenge}}, request, &out)
	return
}
func (c *Client) GetConsentRequest(ctx context.Context, challenge string) (out ConsentRequest, err error) {
	err = c.do(ctx, http.MethodGet, "/admin/oauth2/auth/requests/consent", url.Values{"consent_challenge": {challenge}}, nil, &out)
	return
}
func (c *Client) AcceptConsent(ctx context.Context, challenge string, request AcceptConsentRequest) (out Redirect, err error) {
	err = c.do(ctx, http.MethodPut, "/admin/oauth2/auth/requests/consent/accept", url.Values{"consent_challenge": {challenge}}, request, &out)
	return
}
func (c *Client) RejectConsent(ctx context.Context, challenge string, request RejectRequest) (out Redirect, err error) {
	err = c.do(ctx, http.MethodPut, "/admin/oauth2/auth/requests/consent/reject", url.Values{"consent_challenge": {challenge}}, request, &out)
	return
}

// AcceptDeviceUserCode binds the code a person typed to Hydra's device
// challenge. Both are secrets and must not be logged.
func (c *Client) AcceptDeviceUserCode(ctx context.Context, challenge, userCode string) (out Redirect, err error) {
	err = c.do(ctx, http.MethodPut, "/admin/oauth2/auth/requests/device/accept", url.Values{"device_challenge": {challenge}}, map[string]string{"user_code": userCode}, &out)
	return
}

func (c *Client) Introspect(ctx context.Context, token string) (out IntrospectedToken, err error) {
	err = c.do(ctx, http.MethodPost, "/admin/oauth2/introspect", nil, url.Values{"token": {token}}, &out)
	return
}

func (c *Client) GetOAuthClient(ctx context.Context, id string) (out OAuthClient, err error) {
	err = c.do(ctx, http.MethodGet, "/admin/clients/"+url.PathEscape(id), nil, nil, &out)
	return
}
func (c *Client) CreateOAuthClient(ctx context.Context, client OAuthClient) (out OAuthClient, err error) {
	err = c.do(ctx, http.MethodPost, "/admin/clients", nil, client, &out)
	return
}
func (c *Client) UpdateOAuthClient(ctx context.Context, client OAuthClient) (out OAuthClient, err error) {
	err = c.do(ctx, http.MethodPut, "/admin/clients/"+url.PathEscape(client.ClientID), nil, client, &out)
	return
}
func (c *Client) SetLifespans(ctx context.Context, id string, lifespans Lifespans) error {
	return c.do(ctx, http.MethodPut, "/admin/clients/"+url.PathEscape(id)+"/lifespans", nil, lifespans, nil)
}

// do sends a JSON body, or a form body when payload is url.Values.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, payload, target any) error {
	endpoint := c.adminURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var body io.Reader
	contentType := ""
	switch v := payload.(type) {
	case nil:
	case url.Values:
		body, contentType = strings.NewReader(v.Encode()), "application/x-www-form-urlencoded"
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode Hydra request: %w", err)
		}
		body, contentType = bytes.NewReader(encoded), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create Hydra request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call Hydra admin API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(message))}
	}
	if target == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode Hydra admin response: %w", err)
	}
	return nil
}
