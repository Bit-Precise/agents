package control

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
	jose "github.com/go-jose/go-jose/v4"
)

const oidcTestAdmin = "OIDC-test-bootstrap-admin-secret-32-characters"
const oidcTestClient = "agents-test-client"
const oidcTestSecret = "private-oidc-client-secret"

type testOIDCProfile struct {
	Subject, Name, Email, Picture                           string
	BadIssuer, BadAudience, BadNonce, BadSignature, Expired bool
	EmailVerified                                           json.RawMessage
	OmitEmailVerified                                       bool
	EmailClaim                                              json.RawMessage
	OmitEmail                                               bool
}
type testOIDCCode struct {
	Challenge, Redirect, Nonce string
	Profile                    testOIDCProfile
}
type testOIDCProvider struct {
	server        *httptest.Server
	mu            sync.Mutex
	next          testOIDCProfile
	codes         map[string]testOIDCCode
	exchanges     int
	pkce          bool
	public        bool
	scopes        string
	key, wrongKey *rsa.PrivateKey
}

func newTestOIDCProvider(t *testing.T) *testOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &testOIDCProvider{codes: make(map[string]testOIDCCode), key: key, wrongKey: wrongKey}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token", "jwks_uri": p.server.URL + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"openid"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}}})
		case "/authorize":
			q := r.URL.Query()
			if q.Get("client_id") != oidcTestClient || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || len(q.Get("state")) != 64 || len(q.Get("nonce")) != 64 {
				http.Error(w, "invalid authorize request", 400)
				return
			}
			p.mu.Lock()
			p.scopes = q.Get("scope")
			code := randomSecret()
			p.codes[code] = testOIDCCode{Challenge: q.Get("code_challenge"), Redirect: q.Get("redirect_uri"), Nonce: q.Get("nonce"), Profile: p.next}
			p.mu.Unlock()
			target, _ := url.Parse(q.Get("redirect_uri"))
			query := target.Query()
			query.Set("code", code)
			query.Set("state", q.Get("state"))
			query.Set("iss", p.server.URL)
			target.RawQuery = query.Encode()
			http.Redirect(w, r, target.String(), 303)
		case "/token":
			if r.ParseForm() != nil {
				http.Error(w, "invalid token request", 400)
				return
			}
			client, secret, ok := r.BasicAuth()
			validClient := ok && client == oidcTestClient && secret == oidcTestSecret
			if p.public {
				validClient = !ok && r.Form.Get("client_id") == oidcTestClient && r.Form.Get("client_secret") == ""
			}
			if !validClient || r.Form.Get("grant_type") != "authorization_code" {
				http.Error(w, "invalid client", 401)
				return
			}
			p.mu.Lock()
			code, found := p.codes[r.Form.Get("code")]
			delete(p.codes, r.Form.Get("code"))
			p.exchanges++
			verifier := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			valid := found && len(r.Form.Get("code_verifier")) >= 43 && base64.RawURLEncoding.EncodeToString(verifier[:]) == code.Challenge && r.Form.Get("redirect_uri") == code.Redirect
			p.pkce = valid
			p.mu.Unlock()
			if !valid {
				http.Error(w, "invalid PKCE or code", 400)
				return
			}
			issuer, audience, nonce := p.server.URL, oidcTestClient, code.Nonce
			if code.Profile.BadIssuer {
				issuer = "https://wrong-issuer.invalid"
			}
			if code.Profile.BadAudience {
				audience = "another-client"
			}
			if code.Profile.BadNonce {
				nonce = "replayed-nonce"
			}
			expires := time.Now().Add(40 * time.Minute).Unix()
			if code.Profile.Expired {
				expires = time.Now().Add(-time.Minute).Unix()
			}
			claims := map[string]any{"iss": issuer, "sub": code.Profile.Subject, "aud": audience, "nonce": nonce, "exp": expires, "iat": time.Now().Unix(), "name": code.Profile.Name, "email": code.Profile.Email, "email_verified": true, "picture": code.Profile.Picture, "admin": true, "roles": []string{"admin"}}
			if code.Profile.OmitEmail {
				delete(claims, "email")
			} else if code.Profile.EmailClaim != nil {
				claims["email"] = code.Profile.EmailClaim
			}
			if code.Profile.OmitEmailVerified {
				delete(claims, "email_verified")
			} else if code.Profile.EmailVerified != nil {
				claims["email_verified"] = code.Profile.EmailVerified
			}
			key := p.key
			if code.Profile.BadSignature {
				key = p.wrongKey
			}
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "fixture"))
			if err != nil {
				t.Error(err)
				http.Error(w, "fixture signer", 500)
				return
			}
			data, _ := json.Marshal(claims)
			signed, err := signer.Sign(data)
			if err != nil {
				t.Error(err)
				return
			}
			token, err := signed.CompactSerialize()
			if err != nil {
				t.Error(err)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "private-idp-access-token", "refresh_token": "private-idp-refresh-token", "token_type": "Bearer", "expires_in": 3600, "id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

type oidcFixture struct {
	t         *testing.T
	app       *App
	server    *httptest.Server
	client    *http.Client
	cfg       Config
	idp       *testOIDCProvider
	closeOnce sync.Once
}
type oidcAttempt struct {
	Callback string
	Cookie   *http.Cookie
}

func startOIDCFixture(t *testing.T, idp *testOIDCProvider, dbPath string, configure ...func(*Config)) *oidcFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	base := "http://localhost:" + port
	secretFile := filepath.Join(t.TempDir(), "oidc-secret")
	if err := os.WriteFile(secretFile, []byte(oidcTestSecret), 0600); err != nil {
		t.Fatal(err)
	}
	if idp.public {
		secretFile = ""
	}
	cfg := Config{ListenAddress: listener.Addr().String(), ServerURL: base, EdgeURL: base + "/agent", DevelopmentHTTP: true, OIDC: OIDCConfig{Enabled: true, Issuer: idp.server.URL, ClientID: oidcTestClient, ClientSecretFile: secretFile, BootstrapAdminSubjects: []string{"bootstrap-subject"}}}
	cfg.Database.Type, cfg.Database.DSN = "sqlite", dbPath
	for _, configure := range configure {
		configure(&cfg)
	}
	app, err := New(t.Context(), cfg, oidcTestAdmin)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(app.Handler())
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	f := &oidcFixture{t: t, app: app, server: server, client: client, cfg: cfg, idp: idp}
	t.Cleanup(f.close)
	return f
}

func TestOIDCPublicClientUsesPKCEWithoutClientSecret(t *testing.T) {
	idp := newTestOIDCProvider(t)
	idp.public = true
	f := startOIDCFixture(t, idp, filepath.Join(t.TempDir(), "control.db"))
	f.finish(f.begin(testOIDCProfile{Subject: "public-client-user", Name: "Public client"}, ""), 303)
	user, _ := f.me()
	if user.Admin {
		t.Fatal("public-client user unexpectedly admin")
	}
	idp.mu.Lock()
	pkce := idp.pkce
	idp.mu.Unlock()
	if !pkce {
		t.Fatal("public client skipped PKCE")
	}
}
func (f *oidcFixture) close() {
	f.closeOnce.Do(func() { f.client.CloseIdleConnections(); f.app.Close(); f.server.Close() })
}
func (f *oidcFixture) read(response *http.Response, want int) []byte {
	f.t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != want {
		f.t.Fatalf("response %d want %d: %s (%v)", response.StatusCode, want, data, err)
	}
	return data
}
func (f *oidcFixture) begin(profile testOIDCProfile, returnTo string) oidcAttempt {
	f.t.Helper()
	f.idp.mu.Lock()
	f.idp.next = profile
	f.idp.mu.Unlock()
	response, err := f.client.Get(f.cfg.ServerURL + "/auth/oidc/login?" + url.Values{"returnTo": []string{returnTo}}.Encode())
	if err != nil {
		f.t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, c := range response.Cookies() {
		if c.Name == "agents-dev-oidc" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" {
		f.t.Fatal("missing bound OIDC transaction cookie")
	}
	location := response.Header.Get("Location")
	f.read(response, 303)
	response, err = f.client.Get(location)
	if err != nil {
		f.t.Fatal(err)
	}
	callback := response.Header.Get("Location")
	f.read(response, 303)
	return oidcAttempt{Callback: callback, Cookie: cookie}
}
func (f *oidcFixture) finish(attempt oidcAttempt, want int) *http.Response {
	f.t.Helper()
	response, err := f.client.Get(attempt.Callback)
	if err != nil {
		f.t.Fatal(err)
	}
	data := f.read(response, want)
	if bytes := string(data); strings.Contains(bytes, "private-idp-") || strings.Contains(bytes, oidcTestSecret) || strings.Contains(bytes, "id_token") {
		f.t.Fatal("OIDC secrets exposed in callback")
	}
	return response
}
func (f *oidcFixture) me() (types.User, string) {
	f.t.Helper()
	response, err := f.client.Get(f.cfg.ServerURL + "/v1/sessions/current")
	if err != nil {
		f.t.Fatal(err)
	}
	var result struct {
		User types.User `json:"user"`
		CSRF string     `json:"csrfToken"`
	}
	if err := json.Unmarshal(f.read(response, 200), &result); err != nil {
		f.t.Fatal(err)
	}
	return result.User, result.CSRF
}

func TestOIDCDiscoveryPKCEBrowserSessionSSOAndLogout(t *testing.T) {
	idp := newTestOIDCProvider(t)
	f := startOIDCFixture(t, idp, filepath.Join(t.TempDir(), "control.db"))
	response, err := f.client.Get(f.cfg.ServerURL + "/v1/instance")
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		OIDC struct {
			Enabled  bool   `json:"enabled"`
			LoginURL string `json:"loginURL"`
		} `json:"oidc"`
	}
	data := f.read(response, 200)
	if json.Unmarshal(data, &info) != nil || !info.OIDC.Enabled || info.OIDC.LoginURL != "/auth/oidc/login" || strings.Contains(string(data), oidcTestSecret) {
		t.Fatal("invalid public OIDC capabilities")
	}
	// The Console login button explicitly sends its canonical return path.
	attempt := f.begin(testOIDCProfile{Subject: "alice-sub", Name: "Alice", Email: "Alice@Example.test", Picture: "https://avatars.example.test/alice.png"}, "/console/")
	response = f.finish(attempt, 303)
	if response.Header.Get("Location") != "/console/" {
		t.Fatal("unexpected post-login redirect")
	}
	user, csrf := f.me()
	if user.Admin || csrf == "" {
		t.Fatal("claims granted admin or session lacks CSRF")
	}
	if user.Email != "alice@example.test" || user.Picture != "https://avatars.example.test/alice.png" {
		t.Fatalf("verified email and avatar were not kept: %+v", user)
	}
	idp.mu.Lock()
	pkce, exchanges := idp.pkce, idp.exchanges
	idp.mu.Unlock()
	if !pkce || exchanges != 1 {
		t.Fatal("PKCE exchange not verified")
	}
	// Each SSO login refreshes them; a non-HTTPS avatar is dropped.
	f.finish(f.begin(testOIDCProfile{Subject: "alice-sub", Name: "Alice", Email: "alice@example.test", Picture: "javascript:alert(1)"}, "/console/"), 303)
	if refreshed, _ := f.me(); refreshed.ID != user.ID || refreshed.Picture != "" || refreshed.Email != "alice@example.test" {
		t.Fatalf("avatar was not refreshed safely: %+v", refreshed)
	}
	snapshot, err := f.app.State.SnapshotForUser(user.ID)
	if err != nil || len(snapshot.Workspaces) != 1 || len(snapshot.Memberships) != 1 {
		t.Fatal("new OIDC user did not receive exactly one personal workspace")
	}
	if snapshot.Workspaces[0].Name != "personal-"+user.ID || snapshot.Memberships[0].UserID != user.ID || snapshot.Memberships[0].WorkspaceID != snapshot.Workspaces[0].ID || snapshot.Memberships[0].Role != "owner" {
		t.Fatal("personal workspace is not exclusively owned by the new OIDC user")
	}
	f.app.auth.mu.Lock()
	for _, session := range f.app.auth.sessions {
		if session.UserID == user.ID && time.Until(session.ExpiresAt) > 40*time.Minute {
			t.Error("session exceeds ID-token expiry")
		}
	}
	f.app.auth.mu.Unlock()
	admin, err := f.app.State.Identity(oidcTestAdmin)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := f.app.State.CreateWorkspace(admin.UserID, "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.State.SetMembership(admin.UserID, workspace.ID, user.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	desired := api.AgentSpec{Serve: api.ServeSpec{Enabled: true, Services: []api.ServiceSpec{{Name: "demo", Origin: "http://127.0.0.1:3000"}}}}
	enrollDevice(t, f.app.State, user.ID, workspace, "provider", desired)
	serverURL, _ := url.Parse(f.cfg.ServerURL)
	nonce := strings.Repeat("b", 64)
	returnTo := "/auth/authorize?" + url.Values{"return_to": []string{"http://demo.work.localhost:" + serverURL.Port() + "/private"}, "state": []string{nonce}}.Encode()
	attempt = f.begin(testOIDCProfile{Subject: "alice-sub", Name: "Alice"}, returnTo)
	response = f.finish(attempt, 303)
	if response.Header.Get("Location") != returnTo {
		t.Fatal("safe service authorization continuation lost")
	}
	response, err = f.client.Get(f.cfg.ServerURL + returnTo)
	if err != nil {
		t.Fatal(err)
	}
	callback, _ := url.Parse(response.Header.Get("Location"))
	f.read(response, 303)
	request := httptest.NewRequest("GET", callback.String(), nil)
	request.AddCookie(&http.Cookie{Name: "agents-dev-state", Value: nonce})
	recorder := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != 303 {
		t.Fatalf("OIDC browser session did not reach service SSO: %d %s", recorder.Code, recorder.Body.String())
	}
	_, csrf = f.me()
	request, _ = http.NewRequest("DELETE", f.cfg.ServerURL+"/v1/sessions/current", nil)
	request.Header.Set("Origin", f.cfg.ServerURL)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err = f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	f.read(response, 204)
	response, err = f.client.Get(f.cfg.ServerURL + "/v1/sessions/current")
	if err != nil {
		t.Fatal(err)
	}
	f.read(response, 401)
}

func TestOIDCRejectsReplayWrongBrowserAndInvalidTokens(t *testing.T) {
	idp := newTestOIDCProvider(t)
	f := startOIDCFixture(t, idp, filepath.Join(t.TempDir(), "control.db"))
	attempt := f.begin(testOIDCProfile{Subject: "valid", Name: "Valid"}, "")
	stranger := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer stranger.CloseIdleConnections()
	response, err := stranger.Get(attempt.Callback)
	if err != nil {
		t.Fatal(err)
	}
	f.read(response, 403)
	badURL, _ := url.Parse(attempt.Callback)
	q := badURL.Query()
	q.Set("state", strings.Repeat("f", 64))
	badURL.RawQuery = q.Encode()
	f.finish(oidcAttempt{Callback: badURL.String()}, 403)
	f.finish(attempt, 303)
	replay, _ := http.NewRequest("GET", attempt.Callback, nil)
	replay.AddCookie(attempt.Cookie)
	response, err = stranger.Do(replay)
	if err != nil {
		t.Fatal(err)
	}
	f.read(response, 403)
	for _, profile := range []testOIDCProfile{{Subject: "wrong-issuer", BadIssuer: true}, {Subject: "wrong-audience", BadAudience: true}, {Subject: "wrong-nonce", BadNonce: true}, {Subject: "wrong-signature", BadSignature: true}, {Subject: "expired", Expired: true}} {
		t.Run(profile.Subject, func(t *testing.T) {
			attempt := f.begin(profile, "")
			result := f.finish(attempt, 401)
			for _, cookie := range result.Cookies() {
				if cookie.Name == "agents-dev-session" && cookie.Value != "" {
					t.Fatal("invalid ID token created browser session")
				}
			}
			replay, _ := http.NewRequest("GET", attempt.Callback, nil)
			replay.AddCookie(attempt.Cookie)
			response, err := stranger.Do(replay)
			if err != nil {
				t.Fatal(err)
			}
			f.read(response, 403)
		})
	}
	for _, target := range []string{
		"https://evil.test/", "//evil.test/", "/console//evil.test/", "/console/../auth/authorize?x=1",
		"/console/?returnTo=https://evil.test/", "/console/#evil", "/%63onsole/", "/console%2f",
		"/auth/authorizeevil?x=1", "/auth/%61uthorize?x=1",
	} {
		response, err := f.client.Get(f.cfg.ServerURL + "/auth/oidc/login?returnTo=" + url.QueryEscape(target))
		if err != nil {
			t.Fatal(err)
		}
		f.read(response, 400)
	}
	attempt = f.begin(testOIDCProfile{Subject: "transaction-expired"}, "")
	f.app.oidc.mu.Lock()
	transaction := f.app.oidc.pending[secretHash(attempt.Cookie.Value)]
	transaction.ExpiresAt = time.Now().Add(-time.Second)
	f.app.oidc.pending[secretHash(attempt.Cookie.Value)] = transaction
	f.app.oidc.mu.Unlock()
	f.finish(attempt, 403)
}

func TestOIDCIdentityBindingEmailCollisionAndDatabaseRestart(t *testing.T) {
	idp := newTestOIDCProvider(t)
	path := filepath.Join(t.TempDir(), "control.db")
	f := startOIDCFixture(t, idp, path)
	admin, _ := f.app.State.Identity(oidcTestAdmin)
	local, _, err := f.app.State.CreateUser(admin.UserID, "Same Name", false)
	if err != nil {
		t.Fatal(err)
	}
	f.finish(f.begin(testOIDCProfile{Subject: "subject-one", Name: "Same Name", Email: "shared@example.test"}, ""), 303)
	first, _ := f.me()
	f.finish(f.begin(testOIDCProfile{Subject: "subject-two", Name: "Same Name", Email: "shared@example.test"}, ""), 303)
	second, _ := f.me()
	if first.ID == second.ID || first.ID == local.ID || second.ID == local.ID || first.Admin || second.Admin {
		t.Fatal("OIDC merged accounts by email/name or trusted admin claim")
	}
	f.finish(f.begin(testOIDCProfile{Subject: "bootstrap-subject", Name: "Operator"}, ""), 303)
	operator, _ := f.me()
	if !operator.Admin {
		t.Fatal("explicit bootstrap subject did not receive admin")
	}
	f.close()
	restarted := startOIDCFixture(t, idp, path)
	restarted.finish(restarted.begin(testOIDCProfile{Subject: "subject-one", Name: "Changed Name", Email: "changed@example.test"}, ""), 303)
	after, _ := restarted.me()
	if after.ID != first.ID || after.Name != first.Name {
		t.Fatal("restart or changed profile broke stable issuer/subject binding")
	}
	snapshot, err := restarted.app.State.SnapshotForUser(first.ID)
	if err != nil || len(snapshot.Workspaces) != 1 || len(snapshot.Memberships) != 1 {
		t.Fatal("repeat login created extra workspaces or exposed another user's workspace")
	}
	// Transfer personal-workspace ownership before disabling its sole owner.
	if _, err := restarted.app.State.SetMembership(admin.UserID, snapshot.Workspaces[0].ID, admin.UserID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.app.State.DisableUser(admin.UserID, first.ID); err != nil {
		t.Fatal(err)
	}
	restarted.finish(restarted.begin(testOIDCProfile{Subject: "subject-one", Name: "New Name"}, ""), 403)
	restarted.close()
	otherIDP := newTestOIDCProvider(t)
	other := startOIDCFixture(t, otherIDP, path)
	other.finish(other.begin(testOIDCProfile{Subject: "subject-two", Name: "Same Name", Email: "shared@example.test"}, ""), 303)
	otherUser, _ := other.me()
	if otherUser.ID == second.ID {
		t.Fatal("different issuer reused another issuer's subject binding")
	}
}

func TestOIDCAdminEmailsRequireVerifiedExactAddressAndRequestEmailScope(t *testing.T) {
	idp := newTestOIDCProvider(t)
	f := startOIDCFixture(t, idp, filepath.Join(t.TempDir(), "control.db"), func(cfg *Config) {
		cfg.OIDC.AdminEmails = []string{"  Operator@EXAMPLE.test  "}
		cfg.OIDC.Scopes = []string{"openid"}
	})
	cases := []struct {
		name    string
		profile testOIDCProfile
		admin   bool
	}{
		{name: "verified", profile: testOIDCProfile{Email: " OPERATOR@example.test "}, admin: true},
		{name: "false", profile: testOIDCProfile{Email: "operator@example.test", EmailVerified: json.RawMessage(`false`)}},
		{name: "missing", profile: testOIDCProfile{Email: "operator@example.test", OmitEmailVerified: true}},
		{name: "null", profile: testOIDCProfile{Email: "operator@example.test", EmailVerified: json.RawMessage(`null`)}},
		{name: "string-true", profile: testOIDCProfile{Email: "operator@example.test", EmailVerified: json.RawMessage(`"true"`)}},
		{name: "numeric", profile: testOIDCProfile{Email: "operator@example.test", EmailVerified: json.RawMessage(`1`)}},
		{name: "empty-email", profile: testOIDCProfile{}},
		{name: "missing-email", profile: testOIDCProfile{OmitEmail: true}},
		{name: "null-email", profile: testOIDCProfile{EmailClaim: json.RawMessage(`null`)}},
		{name: "numeric-email", profile: testOIDCProfile{EmailClaim: json.RawMessage(`123`)}},
		{name: "invalid-email", profile: testOIDCProfile{Email: "Operator <operator@example.test>"}},
		{name: "same-domain", profile: testOIDCProfile{Email: "another@example.test"}},
		{name: "suffix", profile: testOIDCProfile{Email: "operator@example.test.evil.test"}},
		{name: "plus-alias", profile: testOIDCProfile{Email: "operator+alias@example.test"}},
		{name: "operator@example.test", profile: testOIDCProfile{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.profile.Subject, tc.profile.Name = tc.name, tc.name
			f.finish(f.begin(tc.profile, ""), 303)
			user, _ := f.me()
			if user.Admin != tc.admin {
				t.Fatalf("admin=%v, want %v", user.Admin, tc.admin)
			}
		})
	}
	idp.mu.Lock()
	scopes := strings.Fields(idp.scopes)
	idp.mu.Unlock()
	if !slices.Contains(scopes, "email") {
		t.Fatal("admin email policy did not request email scope despite incomplete discovery")
	}
}

func TestOIDCExistingUserEmailAdminRequiresFreshVerifiedLogin(t *testing.T) {
	idp := newTestOIDCProvider(t)
	path := filepath.Join(t.TempDir(), "control.db")
	profile := testOIDCProfile{Subject: "existing-user", Email: "operator@example.test"}
	f := startOIDCFixture(t, idp, path)
	f.finish(f.begin(profile, ""), 303)
	before, _ := f.me()
	if before.Admin {
		t.Fatal("unlisted email granted admin")
	}
	f.close()
	f = startOIDCFixture(t, idp, path, func(cfg *Config) {
		cfg.OIDC.AdminEmails = []string{profile.Email}
	})
	user, err := f.app.State.CheckUser(before.ID)
	if err != nil || user.Admin {
		t.Fatal("configuration addition promoted a user from stale stored claims")
	}
	f.finish(f.begin(profile, ""), 303)
	promoted, _ := f.me()
	if promoted.ID != before.ID || !promoted.Admin {
		t.Fatal("fresh verified login did not promote existing local identity")
	}
	profile.EmailVerified = json.RawMessage(`false`)
	f.finish(f.begin(profile, ""), 303)
	demoted, _ := f.me()
	if demoted.ID != before.ID || demoted.Admin {
		t.Fatal("unverified email retained email-derived admin")
	}
}

func TestOIDCAdminEmailRemovalRevokesExistingBearerOnStartup(t *testing.T) {
	idp := newTestOIDCProvider(t)
	for _, disabled := range []bool{false, true} {
		name := "allowlist-removed"
		if disabled {
			name = "oidc-disabled"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.db")
			f := startOIDCFixture(t, idp, path, func(cfg *Config) {
				cfg.OIDC.AdminEmails = []string{"operator@example.test"}
			})
			f.finish(f.begin(testOIDCProfile{Subject: "email-admin", Email: "operator@example.test"}, ""), 303)
			user, _ := f.me()
			if !user.Admin {
				t.Fatal("allowlisted user was not promoted")
			}
			token, err := f.app.State.RotateUserToken(user.ID, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			checkCreateUser := func(f *oidcFixture, want int) {
				t.Helper()
				request := httptest.NewRequest("POST", f.cfg.ServerURL+"/v1/admin/users", strings.NewReader(`{"name":"created-by-email-admin"}`))
				request.Header.Set("Authorization", "Bearer "+token)
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				f.app.Handler().ServeHTTP(recorder, request)
				if recorder.Code != want {
					t.Fatalf("bearer admin operation returned %d, want %d: %s", recorder.Code, want, recorder.Body.String())
				}
			}
			checkCreateUser(f, 201)
			f.close()
			restarted := startOIDCFixture(t, idp, path, func(cfg *Config) {
				cfg.OIDC.Enabled = !disabled
			})
			checkCreateUser(restarted, 403)
			identity, err := restarted.app.State.Identity(token)
			if err != nil || identity.UserID != user.ID {
				t.Fatal("demotion should preserve the user's ordinary bearer authentication")
			}
			admin, err := restarted.app.State.Identity(oidcTestAdmin)
			if err != nil {
				t.Fatal(err)
			}
			bootstrap, err := restarted.app.State.CheckUser(admin.UserID)
			if err != nil || !bootstrap.Admin {
				t.Fatal("email policy removal affected the bootstrap administrator")
			}
		})
	}
}
