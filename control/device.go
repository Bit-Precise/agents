package control

// Device Flow registration (`agentd login`). Hydra runs the OAuth protocol;
// Control is its device-verification, login and consent app and owns every
// decision: who signed in, which Workspace and principal they chose, and the
// one-time exchange of the resulting access token for an Agent.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bit-precise/agentd/api"
	controlstate "github.com/bit-precise/agents/control/state"
	"github.com/bit-precise/agents/control/types"
	"github.com/bit-precise/agents/internal/hydra"
)

const (
	deviceScope = "agents:enroll"
	// deviceTokenLifespan bounds the registration access token; the Agent
	// receives its own credentials at the exchange.
	deviceTokenLifespan = "5m"
	maxChallenge        = 4096
)

var errSSORequired = errors.New("sign in with single sign-on to register a device")

// deviceTransaction binds a Hydra login challenge to the browser that began
// it. The cookie value never leaves Control; only its hash is kept.
type deviceTransaction struct {
	AuthorizationID, UserID, LoginChallenge, UserCode, Host string
	ExpiresAt                                               time.Time
}

type deviceFlow struct {
	hydra    *hydra.Client
	issuer   *url.URL
	clientID string
	audience string
	mu       sync.Mutex
	pending  map[string]deviceTransaction
}

func newDeviceFlow(cfg Config) (*deviceFlow, error) {
	if !cfg.Hydra.Enabled {
		return nil, nil
	}
	token := ""
	if cfg.Hydra.AdminTokenFile != "" {
		var err error
		if token, err = ReadSecret(cfg.Hydra.AdminTokenFile); err != nil {
			return nil, errors.New("read hydra admin token: " + err.Error())
		}
	}
	issuer, _ := url.Parse(cfg.Hydra.PublicURL)
	return &deviceFlow{hydra: hydra.New(hydra.Config{AdminURL: cfg.Hydra.AdminURL, AdminToken: token}), issuer: issuer,
		clientID: cfg.DeviceClientID(), audience: cfg.DeviceAudience(), pending: map[string]deviceTransaction{}}, nil
}

// reconcile keeps Hydra's public device client equal to this definition. It
// retries until it succeeds, so Control starts even while Hydra does not.
func (d *deviceFlow) reconcile(ctx context.Context) {
	client := hydra.OAuthClient{ClientID: d.clientID, ClientName: "agentd", GrantTypes: []string{hydra.DeviceCodeGrant}, ResponseTypes: []string{}, RedirectURIs: []string{},
		Scope: deviceScope, Audience: []string{d.audience}, TokenEndpointAuthMethod: "none", AccessTokenStrategy: "opaque"}
	for delay := time.Second; ; delay = min(2*delay, time.Minute) {
		err := func() error {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			_, err := d.hydra.GetOAuthClient(ctx, d.clientID)
			switch {
			case hydra.IsStatus(err, http.StatusNotFound):
				_, err = d.hydra.CreateOAuthClient(ctx, client)
			case err == nil:
				_, err = d.hydra.UpdateOAuthClient(ctx, client)
			}
			if err != nil {
				return err
			}
			return d.hydra.SetLifespans(ctx, d.clientID, hydra.Lifespans{DeviceAccessToken: deviceTokenLifespan})
		}()
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (d *deviceFlow) info() *api.DeviceAuthorizationInfo {
	return &api.DeviceAuthorizationInfo{Issuer: d.issuer.String(), ClientID: d.clientID, Scope: deviceScope, Audience: d.audience}
}

// hydraRedirect accepts only Hydra's own public OAuth endpoints as the next
// browser destination.
func (d *deviceFlow) hydraRedirect(target string) bool {
	u, err := url.Parse(target)
	return err == nil && u.User == nil && u.Scheme == d.issuer.Scheme && u.Host == d.issuer.Host && strings.HasPrefix(u.Path, strings.TrimSuffix(d.issuer.Path, "/")+"/oauth2/")
}

// matches checks that Hydra is asking for exactly the registration scope and
// audience of the agentd client, never a broader or empty audience.
func (d *deviceFlow) matches(clientID string, scope, audience []string) bool {
	return clientID == d.clientID && slices.Equal(scope, []string{deviceScope}) && slices.Equal(audience, []string{d.audience})
}

func validChallenge(value string) bool {
	return value != "" && len(value) <= maxChallenge && strings.IndexFunc(value, func(r rune) bool { return r <= ' ' || r >= 0x7f }) < 0
}

// validUserCode admits the characters Hydra issues; anything else cannot be a
// code and is not stored in a cookie.
func validUserCode(value string) bool {
	return value != "" && len(value) <= 64 && strings.IndexFunc(value, func(r rune) bool {
		return r > unicode.MaxASCII || (r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r))
	}) < 0
}

func errorMessage(err error) string {
	var e *types.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "the registration could not be completed"
}

func (a *App) deviceRoutes() {
	if a.device == nil {
		return
	}
	m := a.router
	m.HandleFunc("GET /oauth/device", a.deviceVerification)
	m.HandleFunc("POST /oauth/device", a.deviceVerificationSubmit)
	m.HandleFunc("GET /oauth/login", a.deviceLogin)
	m.HandleFunc("GET /oauth/consent", a.deviceConsent)
	m.HandleFunc("GET /oauth/device/done", a.deviceDone)
	m.HandleFunc("GET /oauth/error", a.deviceError)
	m.HandleFunc("GET /v1/device-authorizations/{id}", a.deviceAuthorization)
	m.HandleFunc("POST /v1/device-authorizations/{id}/confirm", a.deviceConfirm)
	m.HandleFunc("POST /v1/device-authorizations/{id}/reject", a.deviceReject)
	m.HandleFunc("POST /v1/agent-enrollments", a.agentEnrollment)
}

// deviceHuman is the person behind a Device Flow step: a Console browser
// session, never a Bearer token, verified by the upstream IdP unless this is a
// loopback development instance without one.
func (a *App) deviceHuman(r *http.Request, mutating bool) (types.User, browserSession, error) {
	if len(r.Header.Values("Authorization")) != 0 {
		return types.User{}, browserSession{}, errors.New("a browser session is required")
	}
	user, s, err := a.actor(r, mutating)
	if err != nil {
		return types.User{}, browserSession{}, err
	}
	if a.oidc != nil && !s.OIDC {
		return types.User{}, browserSession{}, errSSORequired
	}
	return user, s, nil
}

func (a *App) beginDeviceTransaction(w http.ResponseWriter, r *http.Request, t deviceTransaction) {
	value := randomSecret()
	t.Host, t.ExpiresAt = authorityKey(r.Host, a.publicScheme()), time.Now().Add(controlstate.AuthorizationTTL)
	d := a.device
	d.mu.Lock()
	now := time.Now()
	for key, pending := range d.pending {
		if !pending.ExpiresAt.After(now) {
			delete(d.pending, key)
		}
	}
	if previous, ok := a.readCookie(r, "device"); ok {
		delete(d.pending, secretHash(previous))
	}
	d.pending[secretHash(value)] = t
	d.mu.Unlock()
	a.setCookie(w, "device", value, t.ExpiresAt)
}

// deviceTransaction returns the browser's Device Flow transaction for the
// person, or false when this browser did not begin it.
func (a *App) deviceTransaction(r *http.Request, userID string) (deviceTransaction, bool) {
	value, ok := a.readCookie(r, "device")
	if !ok {
		return deviceTransaction{}, false
	}
	a.device.mu.Lock()
	defer a.device.mu.Unlock()
	t, ok := a.device.pending[secretHash(value)]
	return t, ok && t.UserID == userID && t.Host == authorityKey(r.Host, a.publicScheme()) && t.ExpiresAt.After(time.Now())
}

func (a *App) endDeviceTransaction(w http.ResponseWriter, r *http.Request) {
	if value, ok := a.readCookie(r, "device"); ok {
		a.device.mu.Lock()
		delete(a.device.pending, secretHash(value))
		a.device.mu.Unlock()
	}
	a.setCookie(w, "device", "", time.Unix(1, 0))
	a.setCookie(w, "device-code", "", time.Unix(1, 0))
}

// sameOriginForm rejects cross-site form posts; browsers that send neither
// header are treated as same-origin, as for any top-level form.
func (a *App) sameOriginForm(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" && !a.validOrigin(origin) {
		return false
	}
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin" || site == "none"
}

// deviceVerification asks for the code agentd printed. Hydra passes it along
// when agentd opened the complete verification URL; the person still submits
// it, which is the check that the browser and the terminal belong together.
func (a *App) deviceVerification(w http.ResponseWriter, r *http.Request) {
	challenge := r.URL.Query().Get("device_challenge")
	if !validChallenge(challenge) {
		a.devicePage(w, http.StatusBadRequest, pageInvalid)
		return
	}
	code := r.URL.Query().Get("user_code")
	if !validUserCode(code) {
		code = ""
	}
	a.deviceCodeForm(w, http.StatusOK, challenge, code, "")
}

func (a *App) deviceVerificationSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if !a.sameOriginForm(r) || r.ParseForm() != nil {
		a.devicePage(w, http.StatusForbidden, pageInvalid)
		return
	}
	challenge, code := r.PostForm.Get("device_challenge"), strings.TrimSpace(r.PostForm.Get("user_code"))
	if !validChallenge(challenge) {
		a.devicePage(w, http.StatusBadRequest, pageInvalid)
		return
	}
	if !validUserCode(code) {
		a.deviceCodeForm(w, http.StatusBadRequest, challenge, code, "请输入 agentd 显示的验证码。")
		return
	}
	accepted, err := a.device.hydra.AcceptDeviceUserCode(r.Context(), challenge, code)
	if err == nil && !a.device.hydraRedirect(accepted.RedirectTo) {
		err = errors.New("unexpected Hydra redirect")
	}
	if hydra.IsClientError(err) {
		a.deviceCodeForm(w, http.StatusBadRequest, challenge, code, "验证码无效或已过期。请核对终端中的验证码，或重新运行 agentd login。")
		return
	}
	if err != nil {
		a.devicePage(w, http.StatusServiceUnavailable, pageUnavailable)
		return
	}
	// Shown on the confirmation page so the person can match it to the
	// terminal; it authorizes nothing.
	a.setCookie(w, "device-code", code, time.Now().Add(controlstate.AuthorizationTTL))
	http.Redirect(w, r, accepted.RedirectTo, http.StatusSeeOther)
}

// deviceLogin is Hydra's login URL. It never authenticates anyone itself: it
// requires the person's Console session, records the login challenge for them
// and hands over to the Console confirmation page.
func (a *App) deviceLogin(w http.ResponseWriter, r *http.Request) {
	challenge := r.URL.Query().Get("login_challenge")
	if !validChallenge(challenge) {
		a.devicePage(w, http.StatusBadRequest, pageInvalid)
		return
	}
	login, err := a.device.hydra.GetLoginRequest(r.Context(), challenge)
	if err != nil {
		a.deviceHydraFailure(w, err)
		return
	}
	if !a.device.matches(login.Client.ClientID, login.RequestedScope, login.RequestedAudience) {
		a.rejectLogin(w, r, challenge, "invalid_request", "This sign-in page only registers agentd devices.")
		return
	}
	if login.Skip {
		// Nothing is remembered, so a remembered subject cannot be reused.
		a.rejectLogin(w, r, challenge, "login_required", "Run agentd login again.")
		return
	}
	user, _, err := a.deviceHuman(r, false)
	if err != nil {
		continuation := "/oauth/login?" + url.Values{"login_challenge": {challenge}}.Encode()
		if a.oidc != nil {
			http.Redirect(w, r, "/auth/oidc/login?"+url.Values{"returnTo": {continuation}}.Encode(), http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/console/?"+url.Values{"returnTo": {continuation}}.Encode(), http.StatusSeeOther)
		}
		return
	}
	authorization, err := a.State.BeginDeviceAuthorization(user.ID, login.Client.ClientID, challenge)
	if err != nil {
		a.devicePage(w, http.StatusConflict, pageConflict)
		return
	}
	code, _ := a.readCookie(r, "device-code")
	if !validUserCode(code) {
		code = ""
	}
	a.beginDeviceTransaction(w, r, deviceTransaction{AuthorizationID: authorization.ID, UserID: user.ID, LoginChallenge: challenge, UserCode: code})
	http.Redirect(w, r, "/console/#device/"+url.PathEscape(authorization.ID), http.StatusSeeOther)
}

func (a *App) rejectLogin(w http.ResponseWriter, r *http.Request, challenge, code, description string) {
	if _, err := a.device.hydra.RejectLogin(r.Context(), challenge, hydra.RejectRequest{Error: code, ErrorDescription: description}); err != nil {
		a.deviceHydraFailure(w, err)
		return
	}
	page := pageDeclined
	page.Detail = description
	a.devicePage(w, http.StatusBadRequest, page)
}

func (a *App) deviceHydraFailure(w http.ResponseWriter, err error) {
	if hydra.IsClientError(err) {
		a.devicePage(w, http.StatusBadRequest, pageExpired)
		return
	}
	a.devicePage(w, http.StatusServiceUnavailable, pageUnavailable)
}

// DeviceAuthorizationResponse is what the Console confirmation page shows.
type DeviceAuthorizationResponse struct {
	controlstate.DeviceAuthorizationView
	User     types.User `json:"user"`
	UserCode string     `json:"userCode,omitempty"`
}

// deviceRequest authenticates a Console call about one authorization: the
// person's SSO session, CSRF for changes, and the browser transaction that
// began this very authorization.
func (a *App) deviceRequest(w http.ResponseWriter, r *http.Request, mutating bool) (types.User, deviceTransaction, bool) {
	user, _, err := a.deviceHuman(r, mutating)
	if errors.Is(err, errSSORequired) {
		apiError(w, 403, "forbidden", err.Error())
		return types.User{}, deviceTransaction{}, false
	}
	if err != nil {
		code := 401
		if mutating {
			code = 403
		}
		apiError(w, code, "unauthorized", "browser session required")
		return types.User{}, deviceTransaction{}, false
	}
	t, ok := a.deviceTransaction(r, user.ID)
	if !ok || t.AuthorizationID != r.PathValue("id") {
		apiError(w, 404, "not_found", "device authorization not found in this browser; run agentd login again")
		return types.User{}, deviceTransaction{}, false
	}
	return user, t, true
}

func (a *App) deviceAuthorization(w http.ResponseWriter, r *http.Request) {
	user, t, ok := a.deviceRequest(w, r, false)
	if !ok {
		return
	}
	view, err := a.State.DeviceAuthorization(user.ID, t.AuthorizationID)
	if view.Choices == nil {
		view.Choices = []controlstate.DeviceChoice{}
	}
	respond(w, DeviceAuthorizationResponse{DeviceAuthorizationView: view, User: user, UserCode: t.UserCode}, err, 200)
}

// deviceConfirm fixes the person's choice and completes Hydra's login with
// that typed principal as the subject.
func (a *App) deviceConfirm(w http.ResponseWriter, r *http.Request) {
	user, t, ok := a.deviceRequest(w, r, true)
	if !ok {
		return
	}
	var body struct {
		WorkspaceID string        `json:"workspaceId"`
		Principal   api.Principal `json:"principal"`
	}
	if !decode(w, r, &body) {
		return
	}
	authorization, err := a.State.AuthorizeDevice(user.ID, t.AuthorizationID, body.WorkspaceID, body.Principal)
	if err != nil {
		respond(w, nil, err, 200)
		return
	}
	accepted, err := a.device.hydra.AcceptLogin(r.Context(), t.LoginChallenge, hydra.AcceptLoginRequest{Subject: authorization.Principal.Subject(), Context: map[string]any{"authorizationId": authorization.ID}})
	if err == nil && !a.device.hydraRedirect(accepted.RedirectTo) {
		err = errors.New("unexpected Hydra redirect")
	}
	switch {
	case hydra.IsClientError(err):
		apiError(w, 409, "conflict", "the login request expired; run agentd login again")
	case err != nil:
		apiError(w, 503, "unavailable", "the authorization server is unavailable; try again")
	default:
		writeJSON(w, 200, map[string]string{"redirectTo": accepted.RedirectTo})
	}
}

// deviceReject ends the authorization and tells Hydra. Hydra keeps the device
// polling until its code expires, so the Console tells the person to stop
// agentd; there is nowhere useful to send the browser.
func (a *App) deviceReject(w http.ResponseWriter, r *http.Request) {
	user, t, ok := a.deviceRequest(w, r, true)
	if !ok {
		return
	}
	if err := a.State.RevokeDeviceAuthorization(user.ID, t.AuthorizationID); err != nil {
		respond(w, nil, err, 200)
		return
	}
	_, _ = a.device.hydra.RejectLogin(r.Context(), t.LoginChallenge, hydra.RejectRequest{Error: "access_denied", ErrorDescription: "The registration was declined in the browser."})
	a.endDeviceTransaction(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// deviceConsent is Hydra's consent URL. It grants only the registration scope
// and audience, and only when the request matches the confirmed authorization
// and the person may still make that choice.
func (a *App) deviceConsent(w http.ResponseWriter, r *http.Request) {
	challenge := r.URL.Query().Get("consent_challenge")
	if !validChallenge(challenge) {
		a.devicePage(w, http.StatusBadRequest, pageInvalid)
		return
	}
	consent, err := a.device.hydra.GetConsentRequest(r.Context(), challenge)
	if err != nil {
		a.deviceHydraFailure(w, err)
		return
	}
	// Hydra answers a refused device consent with a bare error document, so
	// the person sees Control's page; the terminal stops at code expiry.
	reject := func(description string) {
		_, _ = a.device.hydra.RejectConsent(r.Context(), challenge, hydra.RejectRequest{Error: "access_denied", ErrorDescription: description})
		a.endDeviceTransaction(w, r)
		page := pageDeclined
		page.Detail = description
		a.devicePage(w, http.StatusForbidden, page)
	}
	id, _ := consent.Context["authorizationId"].(string)
	if !a.device.matches(consent.Client.ClientID, consent.RequestedScope, consent.RequestedAudience) || id == "" {
		reject("This consent page only registers agentd devices.")
		return
	}
	user, _, err := a.deviceHuman(r, false)
	if err != nil {
		reject("Sign in again and run agentd login again.")
		return
	}
	t, ok := a.deviceTransaction(r, user.ID)
	if !ok || t.AuthorizationID != id {
		reject("Finish the registration in the browser that started it.")
		return
	}
	if _, err = a.State.CheckDeviceConsent(user.ID, id, consent.Subject); err != nil {
		reject(errorMessage(err))
		return
	}
	accepted, err := a.device.hydra.AcceptConsent(r.Context(), challenge, hydra.AcceptConsentRequest{GrantScope: []string{deviceScope}, GrantAudience: []string{a.device.audience},
		Session: &hydra.ConsentSession{AccessToken: map[string]any{"authorizationId": id}}})
	if err == nil && !a.device.hydraRedirect(accepted.RedirectTo) {
		err = errors.New("unexpected Hydra redirect")
	}
	if err != nil {
		a.deviceHydraFailure(w, err)
		return
	}
	http.Redirect(w, r, accepted.RedirectTo, http.StatusSeeOther)
}

func (a *App) deviceDone(w http.ResponseWriter, r *http.Request) {
	a.endDeviceTransaction(w, r)
	a.devicePage(w, http.StatusOK, pageDone)
}

// deviceError is Hydra's error URL; its query is untrusted display text.
func (a *App) deviceError(w http.ResponseWriter, r *http.Request) {
	description := r.URL.Query().Get("error_description")
	if description == "" {
		description = r.URL.Query().Get("error")
	}
	if len(description) > 300 || strings.IndexFunc(description, unicode.IsControl) >= 0 {
		description = ""
	}
	a.endDeviceTransaction(w, r)
	page := pageDeclined
	page.Detail = description
	a.devicePage(w, http.StatusOK, page)
}

// agentEnrollment spends a registration access token on one new Agent. Only
// a token Hydra issued to the agentd client for exactly this scope and
// audience is accepted; Console sessions and platform tokens are not.
func (a *App) agentEnrollment(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer scope="`+deviceScope+`"`)
		apiError(w, 401, "unauthorized", "registration access token required")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		apiError(w, 403, "forbidden", "agent registration is not a browser request")
		return
	}
	var body api.AgentEnrollment
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSuffix(body.Instance, "/") != strings.TrimSuffix(a.Config.ServerURL, "/") || strings.TrimSuffix(body.Endpoint, "/") != strings.TrimSuffix(a.Config.EdgeURL, "/") {
		apiError(w, 400, "invalid", "agent instance and edge endpoint must match this server")
		return
	}
	introspected, err := a.device.hydra.Introspect(r.Context(), token)
	if err != nil {
		apiError(w, 503, "unavailable", "the authorization server is unavailable; try again")
		return
	}
	subject, subjectErr := api.ParseSubject(introspected.Subject)
	id, _ := introspected.Ext["authorizationId"].(string)
	if !introspected.Active || introspected.ExpiresAt <= time.Now().Unix() || (introspected.TokenUse != "" && introspected.TokenUse != "access_token") ||
		strings.TrimSuffix(introspected.Issuer, "/") != strings.TrimSuffix(a.device.issuer.String(), "/") || subjectErr != nil || id == "" ||
		!a.device.matches(introspected.ClientID, strings.Fields(introspected.Scope), introspected.Audience) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		apiError(w, 401, "unauthorized", "invalid or expired registration access token")
		return
	}
	response, err := a.State.ExchangeDeviceEnrollment(id, subject, body)
	if err == nil {
		response.Credentials.ControlURL = strings.TrimSuffix(a.Config.ServerURL, "/")
		w.Header().Set("Location", "/v1/workspaces/"+response.Agent.WorkspaceID+"/agents/"+response.Agent.ID)
	}
	respond(w, response, err, 201)
}
