package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/deviceflow"
	controlstate "github.com/bit-precise/agents/control/state"
	"github.com/bit-precise/agents/control/types"
	"github.com/bit-precise/agents/internal/hydra"
	"github.com/bit-precise/agents/internal/hydra/hydratest"
)

// enrollDevice registers a personal device the way `agentd login` does,
// through the State calls behind the browser confirmation and the exchange.
func enrollDevice(t *testing.T, s *controlstate.State, userID string, w types.Workspace, name string, desired api.AgentSpec) types.EnrollResponse {
	t.Helper()
	a, err := s.BeginDeviceAuthorization(userID, "agentd", "challenge-"+name+"-"+randomSecret()[:8])
	if err != nil {
		t.Fatal(err)
	}
	p := api.Principal{Type: api.PrincipalUser, ID: userID}
	if a, err = s.AuthorizeDevice(userID, a.ID, w.ID, p); err != nil {
		t.Fatal(err)
	}
	result, err := s.ExchangeDeviceEnrollment(a.ID, p, api.AgentEnrollment{Workspace: w.Name, Name: name, Desired: desired})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type deviceFixture struct {
	*oidcFixture
	issuer, adminURL string
	fake             *hydratest.Server // nil against a real Hydra
	flow             *deviceflow.Client
}

// hydraStarter starts an authorization server pointed at Control's pages and
// returns its issuer and private Admin URL.
type hydraStarter func(t *testing.T, urls hydratest.URLs) (issuer, admin string, fake *hydratest.Server)

func startFakeHydra(t *testing.T, urls hydratest.URLs) (string, string, *hydratest.Server) {
	fake := hydratest.New(urls)
	t.Cleanup(fake.Close)
	return fake.Issuer(), fake.Admin.URL, fake
}

func startDeviceFixture(t *testing.T) *deviceFixture {
	return startDeviceFixtureWith(t, startFakeHydra)
}

func startDeviceFixtureWith(t *testing.T, start hydraStarter) *deviceFixture {
	t.Helper()
	f := &deviceFixture{flow: &deviceflow.Client{HTTP: &http.Client{Timeout: 5 * time.Second}, Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}}
	f.oidcFixture = startOIDCFixture(t, newTestOIDCProvider(t), filepath.Join(t.TempDir(), "control.db"), func(cfg *Config) {
		base := cfg.ServerURL
		f.issuer, f.adminURL, f.fake = start(t, hydratest.URLs{Login: base + "/oauth/login", Consent: base + "/oauth/consent", Verification: base + "/oauth/device", Success: base + "/oauth/device/done", Error: base + "/oauth/error"})
		cfg.Hydra = HydraConfig{Enabled: true, PublicURL: f.issuer, AdminURL: f.adminURL}
	})
	admin := hydra.New(hydra.Config{AdminURL: f.adminURL})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if client, err := admin.GetOAuthClient(t.Context(), "agentd"); err == nil && slices.Equal(client.Audience, []string{f.cfg.DeviceAudience()}) {
			// Lifespans are set right after the client; give that a moment.
			time.Sleep(50 * time.Millisecond)
			return f
		}
		if time.Now().After(deadline) {
			t.Fatal("device client was not reconciled")
		}
	}
}

// browser is one person's browser: its own cookie jar, no automatic redirects.
func (f *deviceFixture) browser(subject, name string) (*http.Client, string) {
	f.t.Helper()
	jar, _ := cookiejar.New(nil)
	f.client = &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	f.finish(f.begin(testOIDCProfile{Subject: subject, Name: name}, ""), 303)
	_, csrf := f.me()
	return f.client, csrf
}

// follow walks redirects until a response that is not a redirect or until
// the next target is Control's Console page, returning that response.
func follow(t *testing.T, client *http.Client, target string) (*http.Response, string) {
	t.Helper()
	for range 12 {
		response, err := client.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		location := response.Header.Get("Location")
		if response.StatusCode < 300 || response.StatusCode > 399 || location == "" {
			return response, target
		}
		response.Body.Close()
		next, err := response.Request.URL.Parse(location)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(next.Path, "/device/") || next.Path == "/login" || next.Path == "/auth/oidc/login" {
			return response, next.String()
		}
		target = next.String()
	}
	t.Fatal("redirect loop")
	return nil, ""
}

func body(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (f *deviceFixture) post(client *http.Client, path, csrf string, value any) *http.Response {
	f.t.Helper()
	data, _ := json.Marshal(value)
	request, _ := http.NewRequest(http.MethodPost, f.cfg.ServerURL+path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", f.cfg.ServerURL)
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	return response
}

type startedLogin struct {
	endpoints      deviceflow.Endpoints
	authorization  deviceflow.Authorization
	loginChallenge string
	id             string
}

// begin starts agentd login and walks the browser through the code page up to
// the Console confirmation page.
func (f *deviceFixture) beginLogin(client *http.Client) startedLogin {
	f.t.Helper()
	ctx := f.t.Context()
	endpoints, err := f.flow.Discover(ctx, f.issuer)
	if err != nil {
		f.t.Fatal(err)
	}
	authorization, err := f.flow.Start(ctx, endpoints.DeviceAuthorization, "agentd", "agents:enroll", f.cfg.DeviceAudience())
	if err != nil {
		f.t.Fatal(err)
	}
	page, at := follow(f.t, client, authorization.VerificationURIComplete)
	if text := body(f.t, page); page.StatusCode != 200 || !strings.Contains(text, `value="`+authorization.UserCode+`"`) || page.Header.Get("Referrer-Policy") != "same-origin" {
		f.t.Fatalf("verification page did not offer the code: %s", text)
	}
	verify, _ := url.Parse(at)
	form := url.Values{"device_challenge": {verify.Query().Get("device_challenge")}, "user_code": {authorization.UserCode}}
	request, _ := http.NewRequest(http.MethodPost, f.cfg.ServerURL+"/oauth/device", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", f.cfg.ServerURL)
	response, err := client.Do(request)
	if err != nil || response.StatusCode != http.StatusSeeOther {
		f.t.Fatalf("code submission: %v %v", response, err)
	}
	response.Body.Close()
	response, console := follow(f.t, client, response.Header.Get("Location"))
	response.Body.Close()
	login, _ := url.Parse(response.Request.URL.String())
	target, _ := url.Parse(console)
	if !strings.HasPrefix(target.Path, "/device/") || target.Fragment != "" {
		f.t.Fatalf("login did not continue to the Console confirmation: %s", console)
	}
	return startedLogin{endpoints: endpoints, authorization: authorization, loginChallenge: login.Query().Get("login_challenge"), id: strings.TrimPrefix(target.Path, "/device/")}
}

// confirm chooses a principal and walks the browser through consent.
func (f *deviceFixture) confirm(client *http.Client, csrf string, l startedLogin, workspaceID string, p api.Principal) (*http.Response, string) {
	f.t.Helper()
	response := f.post(client, "/v1/device-authorizations/"+l.id+"/confirm", csrf, map[string]any{"workspaceId": workspaceID, "principal": p})
	var accepted struct {
		RedirectTo string `json:"redirectTo"`
	}
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&accepted) != nil {
		f.t.Fatalf("confirm returned %d", response.StatusCode)
	}
	response.Body.Close()
	return follow(f.t, client, accepted.RedirectTo)
}

func (f *deviceFixture) exchange(token, workspace, name string) *http.Response {
	f.t.Helper()
	data, _ := json.Marshal(api.AgentEnrollment{Instance: f.cfg.ServerURL, Endpoint: f.cfg.EdgeURL, Workspace: workspace, Name: name, Desired: api.AgentSpec{Ingress: api.IngressSpec{Enabled: true}}})
	request, _ := http.NewRequest(http.MethodPost, f.cfg.ServerURL+"/v1/agent-enrollments", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	return response
}

func TestDeviceFlowRegistersExactlyTheConfirmedPrincipal(t *testing.T) {
	testConfirmedPrincipal(t, startDeviceFixture(t))
}

func testConfirmedPrincipal(t *testing.T, f *deviceFixture) {
	client, err := hydra.New(hydra.Config{AdminURL: f.adminURL}).GetOAuthClient(t.Context(), "agentd")
	if err != nil || !slices.Equal(client.GrantTypes, []string{hydra.DeviceCodeGrant}) || client.TokenEndpointAuthMethod != "none" || client.Scope != "agents:enroll" || !slices.Equal(client.Audience, []string{f.cfg.DeviceAudience()}) {
		t.Fatalf("device client definition: %+v", client)
	}
	var info api.InstanceInfo
	if err := json.Unmarshal(f.read(must(f.client.Get(f.cfg.ServerURL+"/v1/instance")), 200), &info); err != nil || info.DeviceAuthorization == nil || info.DeviceAuthorization.Issuer != f.issuer || info.DeviceAuthorization.Audience != f.cfg.DeviceAudience() {
		t.Fatalf("instance does not advertise Device Flow: %+v", info.DeviceAuthorization)
	}
	alice, csrf := f.browser("alice-subject", "Alice")
	me, _ := f.me()
	l := f.beginLogin(alice)
	var view DeviceAuthorizationResponse
	response := must(alice.Get(f.cfg.ServerURL + "/v1/device-authorizations/" + l.id))
	if err := json.Unmarshal(f.read(response, 200), &view); err != nil {
		t.Fatal(err)
	}
	if view.User.ID != me.ID || view.UserCode != l.authorization.UserCode || len(view.Choices) != 1 || !view.Choices[0].Self || view.Authorization.LoginChallenge != "" {
		t.Fatalf("confirmation view: %+v", view)
	}
	personal := view.Choices[0].Workspace
	// Another person's browser cannot read or confirm this authorization.
	bob, bobCSRF := f.browser("bob-subject", "Bob")
	f.read(must(bob.Get(f.cfg.ServerURL+"/v1/device-authorizations/"+l.id)), 404)
	f.read(f.post(bob, "/v1/device-authorizations/"+l.id+"/confirm", bobCSRF, map[string]any{"workspaceId": personal.ID, "principal": api.Principal{Type: "user", ID: me.ID}}), 404)
	f.read(f.post(alice, "/v1/device-authorizations/"+l.id+"/confirm", "wrong-csrf", map[string]any{"workspaceId": personal.ID, "principal": api.Principal{Type: "user", ID: me.ID}}), 403)

	done, _ := f.confirm(alice, csrf, l, personal.ID, api.Principal{Type: api.PrincipalUser, ID: me.ID})
	if page := body(t, done); done.StatusCode != 200 || !strings.Contains(page, "设备已授权") {
		t.Fatalf("browser did not reach the success page: %d %s", done.StatusCode, page)
	}
	token, err := f.flow.Poll(t.Context(), l.endpoints.Token, "agentd", l.authorization)
	if err != nil {
		t.Fatal(err)
	}
	introspected, err := hydra.New(hydra.Config{AdminURL: f.adminURL}).Introspect(t.Context(), token.AccessToken)
	if err != nil || !introspected.Active || introspected.ExpiresAt > time.Now().Add(5*time.Minute+5*time.Second).Unix() || introspected.Subject != "user:"+me.ID {
		t.Fatalf("registration token: %+v %v", introspected, err)
	}
	f.read(f.exchange(token.AccessToken, "elsewhere", "laptop"), 403)
	response = f.exchange(token.AccessToken, personal.Name, "laptop")
	var enrolled api.EnrollResponse
	if err := json.Unmarshal(f.read(response, 201), &enrolled); err != nil {
		t.Fatal(err)
	}
	if enrolled.Agent.Principal != (api.Principal{Type: api.PrincipalUser, ID: me.ID}) || enrolled.Credentials.ControlURL != f.cfg.ServerURL || response.Header.Get("Location") != "/v1/workspaces/"+personal.ID+"/agents/"+enrolled.Agent.ID {
		t.Fatalf("registered device: %+v %s", enrolled.Agent, response.Header.Get("Location"))
	}
	f.read(f.exchange(token.AccessToken, personal.Name, "second"), 409)
	if f.fake != nil {
		f.fake.Expire()
		f.read(f.exchange(token.AccessToken, personal.Name, "third"), 401)
	}
	// A platform token is not a registration token.
	userToken, err := f.app.State.RotateUserToken(me.ID, me.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.read(f.exchange(userToken, personal.Name, "fourth"), 401)
}

func TestDeviceFlowServiceAccountAndSubjectSubstitution(t *testing.T) {
	testServiceAccountAndSubstitution(t, startDeviceFixture(t))
}

func testServiceAccountAndSubstitution(t *testing.T, f *deviceFixture) {
	admin, csrf := f.browser("bootstrap-subject", "Admin")
	me, _ := f.me()
	workspace, err := f.app.State.CreateWorkspace(me.ID, "fleet")
	if err != nil {
		t.Fatal(err)
	}
	sa, err := f.app.State.CreateServiceAccount(me.ID, workspace.ID, "builder", "CI runners")
	if err != nil {
		t.Fatal(err)
	}
	robot := api.Principal{Type: api.PrincipalServiceAccount, ID: sa.ID}

	// Hydra keeps the latest login decision until it is used; a subject
	// changed behind Control's back is refused at consent.
	l := f.beginLogin(admin)
	if _, err := f.app.State.AuthorizeDevice(me.ID, l.id, workspace.ID, robot); err != nil {
		t.Fatal(err)
	}
	admin2 := hydra.New(hydra.Config{AdminURL: f.adminURL})
	redirect, err := admin2.AcceptLogin(t.Context(), l.loginChallenge, hydra.AcceptLoginRequest{Subject: "user:" + me.ID, Context: map[string]any{"authorizationId": l.id}})
	if err != nil {
		t.Fatal(err)
	}
	failed, _ := follow(t, admin, redirect.RedirectTo)
	if page := body(t, failed); failed.StatusCode != 403 || !strings.Contains(page, "设备未授权") {
		t.Fatalf("substituted subject was not refused: %s", page)
	}
	pollCtx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := f.flow.Poll(pollCtx, l.endpoints.Token, "agentd", l.authorization); err == nil {
		t.Fatal("a refused consent issued a token")
	}

	l = f.beginLogin(admin)
	if done, _ := f.confirm(admin, csrf, l, workspace.ID, robot); done.StatusCode != 200 {
		t.Fatalf("service account confirmation ended with %d", done.StatusCode)
	}
	token, err := f.flow.Poll(t.Context(), l.endpoints.Token, "agentd", l.authorization)
	if err != nil {
		t.Fatal(err)
	}
	var enrolled api.EnrollResponse
	if err := json.Unmarshal(f.read(f.exchange(token.AccessToken, "fleet", "runner-1"), 201), &enrolled); err != nil {
		t.Fatal(err)
	}
	if enrolled.Agent.Principal != robot || enrolled.Agent.CreatedBy != me.ID || enrolled.Credentials.Principal == nil || *enrolled.Credentials.Principal != robot {
		t.Fatalf("service account device: %+v", enrolled.Agent)
	}
}

func TestDeviceFlowDeclineAndSessionRequirements(t *testing.T) {
	testDeclineAndSessions(t, startDeviceFixture(t))
}

func testDeclineAndSessions(t *testing.T, f *deviceFixture) {
	// Without a session the login page continues through SSO, never the
	// emergency token login.
	jar, _ := cookiejar.New(nil)
	anonymous := &http.Client{Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx := t.Context()
	endpoints, _ := f.flow.Discover(ctx, f.issuer)
	authorization, _ := f.flow.Start(ctx, endpoints.DeviceAuthorization, "agentd", "agents:enroll", f.cfg.DeviceAudience())
	_, at := follow(t, anonymous, authorization.VerificationURIComplete)
	verify, _ := url.Parse(at)
	form := url.Values{"device_challenge": {verify.Query().Get("device_challenge")}, "user_code": {"WRONG-CODE"}}
	response := must(anonymous.PostForm(f.cfg.ServerURL+"/oauth/device", form))
	if page := body(t, response); response.StatusCode != 400 || !strings.Contains(page, "验证码无效") {
		t.Fatalf("wrong code: %d %s", response.StatusCode, page)
	}
	form.Set("user_code", authorization.UserCode)
	cross, _ := http.NewRequest(http.MethodPost, f.cfg.ServerURL+"/oauth/device", strings.NewReader(form.Encode()))
	cross.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cross.Header.Set("Origin", "https://evil.test")
	f.read(must(anonymous.Do(cross)), 403)
	response = must(anonymous.PostForm(f.cfg.ServerURL+"/oauth/device", form))
	_, next := follow(t, anonymous, response.Header.Get("Location"))
	if target, _ := url.Parse(next); target.Path != "/auth/oidc/login" || !strings.HasPrefix(target.Query().Get("returnTo"), "/oauth/login?login_challenge=") {
		t.Fatalf("sign-in continuation: %s", next)
	}
	// A pasted-token Console session is not an SSO verification.
	tokenSession := f.post(anonymous, "/v1/sessions", "", map[string]string{"token": oidcTestAdmin})
	f.read(tokenSession, 201)
	f.read(must(anonymous.Get(f.cfg.ServerURL+"/v1/device-authorizations/anything")), 403)

	alice, csrf := f.browser("alice-subject", "Alice")
	l := f.beginLogin(alice)
	f.read(f.post(alice, "/v1/device-authorizations/"+l.id+"/reject", csrf, map[string]string{}), 204)
	f.read(must(alice.Get(f.cfg.ServerURL+"/v1/device-authorizations/"+l.id)), 404)
	// Hydra's own error redirect lands on a page that only displays text.
	page := must(alice.Get(f.cfg.ServerURL + "/oauth/error?" + url.Values{"error": {"access_denied"}, "error_description": {"<script>alert(1)</script>"}}.Encode()))
	if text := body(t, page); !strings.Contains(text, "设备未授权") || strings.Contains(text, "<script>") {
		t.Fatalf("error page: %s", text)
	}
}

func must(response *http.Response, err error) *http.Response {
	if err != nil {
		panic(err)
	}
	return response
}
