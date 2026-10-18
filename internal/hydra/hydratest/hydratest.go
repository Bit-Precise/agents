// Package hydratest is an in-memory stand-in for the parts of Ory Hydra v26
// that Device Flow registration uses: discovery, device authorization, the
// browser verification redirects, the token endpoint and the Admin API.
// It mirrors what a real Hydra answers, including that a later login accept
// replaces the subject until its verifier is used and that device requests
// are not checked against the client's audience. Control's real-Hydra test
// exercises the same flow against the actual binary.
package hydratest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bit-precise/agents/internal/hydra"
)

// URLs are Hydra's urls.* configuration: Control's pages.
type URLs struct {
	Login, Consent, Verification, Success, Error string
}

type decision struct {
	rejected bool
	error    string
	subject  string
	context  map[string]any
	scope    []string
	audience []string
	session  map[string]any
}

type device struct {
	code, userCode     string
	clientID           string
	scope, audience    []string
	expires            time.Time
	approved, consumed bool
	subject            string
	grantScope         []string
	grantAudience      []string
	ext                map[string]any
}

// flow is one browser's walk through the verification redirects, bound to a
// cookie like Hydra's CSRF cookies.
type flow struct {
	cookie    string
	device    *device
	loginID   string
	login     decision
	consentID string
	consent   decision
}

type Server struct {
	Public, Admin *httptest.Server
	URLs          URLs
	// Interval is the polling interval returned to devices, in seconds.
	Interval int

	mu        sync.Mutex
	clients   map[string]hydra.OAuthClient
	lifespans map[string]hydra.Lifespans
	devices   map[string]*device
	flows     map[string]*flow // by device, login or consent challenge and by verifier
	tokens    map[string]hydra.IntrospectedToken
}

func New(urls URLs) *Server {
	s := &Server{URLs: urls, Interval: 1, clients: map[string]hydra.OAuthClient{}, lifespans: map[string]hydra.Lifespans{}, devices: map[string]*device{}, flows: map[string]*flow{}, tokens: map[string]hydra.IntrospectedToken{}}
	public := http.NewServeMux()
	public.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	public.HandleFunc("POST /oauth2/device/auth", s.deviceAuth)
	public.HandleFunc("GET /oauth2/device/verify", s.verify)
	public.HandleFunc("POST /oauth2/token", s.token)
	admin := http.NewServeMux()
	admin.HandleFunc("PUT /admin/oauth2/auth/requests/device/accept", s.acceptDevice)
	admin.HandleFunc("GET /admin/oauth2/auth/requests/login", s.getLogin)
	admin.HandleFunc("PUT /admin/oauth2/auth/requests/login/{action}", s.decideLogin)
	admin.HandleFunc("GET /admin/oauth2/auth/requests/consent", s.getConsent)
	admin.HandleFunc("PUT /admin/oauth2/auth/requests/consent/{action}", s.decideConsent)
	admin.HandleFunc("POST /admin/oauth2/introspect", s.introspect)
	admin.HandleFunc("POST /admin/clients", s.createClient)
	admin.HandleFunc("GET /admin/clients/{id}", s.getClient)
	admin.HandleFunc("PUT /admin/clients/{id}", s.updateClient)
	admin.HandleFunc("PUT /admin/clients/{id}/lifespans", s.setLifespans)
	s.Public, s.Admin = httptest.NewServer(public), httptest.NewServer(admin)
	return s
}

func (s *Server) Close() { s.Public.Close(); s.Admin.Close() }

func (s *Server) Issuer() string { return s.Public.URL }

// Client returns a registered client definition.
func (s *Server) Client(id string) (hydra.OAuthClient, hydra.Lifespans, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	return c, s.lifespans[id], ok
}

// Expire makes every issued token inactive, as after its lifespan.
func (s *Server) Expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, t := range s.tokens {
		t.Active = false
		s.tokens[key] = t
	}
}

func random() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": code})
}

func (s *Server) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"issuer": s.Issuer(), "device_authorization_endpoint": s.Issuer() + "/oauth2/device/auth", "token_endpoint": s.Issuer() + "/oauth2/token"})
}

func (s *Server) deviceAuth(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[r.PostForm.Get("client_id")]
	scope := strings.Fields(r.PostForm.Get("scope"))
	if !ok || !slices.Contains(client.GrantTypes, hydra.DeviceCodeGrant) {
		oauthError(w, 401, "invalid_client")
		return
	}
	for _, v := range scope {
		if !slices.Contains(strings.Fields(client.Scope), v) {
			oauthError(w, 400, "invalid_scope")
			return
		}
	}
	d := &device{code: "dc_" + random(), userCode: strings.ToUpper(random()[:8]), clientID: client.ClientID, scope: scope, audience: strings.Fields(r.PostForm.Get("audience")), expires: time.Now().Add(10 * time.Minute)}
	s.devices[d.code] = d
	verify := s.Issuer() + "/oauth2/device/verify"
	writeJSON(w, 200, map[string]any{"device_code": d.code, "user_code": d.userCode, "verification_uri": verify, "verification_uri_complete": verify + "?user_code=" + d.userCode, "expires_in": 600, "interval": s.Interval})
}

func (s *Server) cookieFlow(r *http.Request, key string) *flow {
	f := s.flows[key]
	c, err := r.Cookie("fake_hydra_flow")
	if f == nil || err != nil || c.Value != f.cookie {
		return nil
	}
	return f
}

func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	redirect := func(target string, values url.Values) {
		http.Redirect(w, r, target+"?"+values.Encode(), http.StatusFound)
	}
	switch {
	case q.Has("device_verifier"):
		f := s.cookieFlow(r, "dv:"+q.Get("device_verifier"))
		if f == nil || f.device == nil {
			oauthError(w, 403, "request_forbidden")
			return
		}
		delete(s.flows, "dv:"+q.Get("device_verifier"))
		f.loginID = "lc_" + random()
		s.flows[f.loginID] = f
		redirect(s.URLs.Login, url.Values{"login_challenge": {f.loginID}})
	case q.Has("login_verifier"):
		f := s.cookieFlow(r, "lv:"+q.Get("login_verifier"))
		if f == nil {
			oauthError(w, 403, "request_forbidden")
			return
		}
		delete(s.flows, "lv:"+q.Get("login_verifier"))
		delete(s.flows, f.loginID)
		if f.login.rejected {
			redirect(s.URLs.Error, url.Values{"error": {f.login.error}, "error_description": {"declined"}})
			return
		}
		f.consentID = "cc_" + random()
		s.flows[f.consentID] = f
		redirect(s.URLs.Consent, url.Values{"consent_challenge": {f.consentID}})
	case q.Has("consent_verifier"):
		f := s.cookieFlow(r, "cv:"+q.Get("consent_verifier"))
		if f == nil {
			oauthError(w, 403, "request_forbidden")
			return
		}
		delete(s.flows, "cv:"+q.Get("consent_verifier"))
		delete(s.flows, f.consentID)
		if f.consent.rejected {
			redirect(s.URLs.Error, url.Values{"error": {f.consent.error}, "error_description": {"declined"}})
			return
		}
		d := f.device
		d.approved, d.subject, d.grantScope, d.grantAudience, d.ext = true, f.login.subject, f.consent.scope, f.consent.audience, f.consent.session
		redirect(s.URLs.Success, url.Values{"client_id": {d.clientID}})
	default:
		f := &flow{cookie: random()}
		challenge := "dch_" + random()
		s.flows[challenge] = f
		http.SetCookie(w, &http.Cookie{Name: "fake_hydra_flow", Value: f.cookie, Path: "/", HttpOnly: true})
		values := url.Values{"device_challenge": {challenge}}
		if code := q.Get("user_code"); code != "" {
			values.Set("user_code", code)
		}
		redirect(s.URLs.Verification, values)
	}
}

func (s *Server) next(f *flow, kind string) hydra.Redirect {
	verifier := random()
	s.flows[kind+":"+verifier] = f
	name := map[string]string{"dv": "device_verifier", "lv": "login_verifier", "cv": "consent_verifier"}[kind]
	return hydra.Redirect{RedirectTo: s.Issuer() + "/oauth2/device/verify?" + url.Values{"client_id": {"agentd"}, name: {verifier}}.Encode()}
}

func (s *Server) acceptDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserCode string `json:"user_code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[r.URL.Query().Get("device_challenge")]
	if f == nil {
		oauthError(w, 404, "not_found")
		return
	}
	for _, d := range s.devices {
		if d.userCode == body.UserCode && !d.approved && time.Now().Before(d.expires) {
			f.device = d
			writeJSON(w, 200, s.next(f, "dv"))
			return
		}
	}
	oauthError(w, 400, "invalid_request")
}

func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[r.URL.Query().Get("login_challenge")]
	if f == nil || f.loginID != r.URL.Query().Get("login_challenge") {
		oauthError(w, 404, "not_found")
		return
	}
	d := f.device
	writeJSON(w, 200, map[string]any{"challenge": f.loginID, "skip": false, "subject": "", "client": map[string]string{"client_id": d.clientID},
		"requested_scope": d.scope, "requested_access_token_audience": d.audience, "request_url": s.Issuer() + "/oauth2/device/verify?client_id=" + d.clientID})
}

func (s *Server) decideLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Subject string         `json:"subject"`
		Context map[string]any `json:"context"`
		Error   string         `json:"error"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[r.URL.Query().Get("login_challenge")]
	if f == nil || f.loginID != r.URL.Query().Get("login_challenge") {
		oauthError(w, 404, "not_found")
		return
	}
	// Like Hydra, the latest decision wins until its verifier is used.
	if r.PathValue("action") == "reject" {
		f.login = decision{rejected: true, error: body.Error}
	} else {
		f.login = decision{subject: body.Subject, context: body.Context}
	}
	writeJSON(w, 200, s.next(f, "lv"))
}

func (s *Server) getConsent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[r.URL.Query().Get("consent_challenge")]
	if f == nil || f.consentID != r.URL.Query().Get("consent_challenge") {
		oauthError(w, 404, "not_found")
		return
	}
	d := f.device
	// Like Hydra, consent names the login by an internal ID, not its challenge.
	writeJSON(w, 200, map[string]any{"challenge": f.consentID, "skip": false, "subject": f.login.subject, "client": map[string]string{"client_id": d.clientID},
		"requested_scope": d.scope, "requested_access_token_audience": d.audience, "context": f.login.context, "login_challenge": f.cookie[:16]})
}

func (s *Server) decideConsent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GrantScope    []string `json:"grant_scope"`
		GrantAudience []string `json:"grant_access_token_audience"`
		Session       struct {
			AccessToken map[string]any `json:"access_token"`
		} `json:"session"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[r.URL.Query().Get("consent_challenge")]
	if f == nil || f.consentID != r.URL.Query().Get("consent_challenge") {
		oauthError(w, 404, "not_found")
		return
	}
	if r.PathValue("action") == "reject" {
		f.consent = decision{rejected: true, error: body.Error}
	} else {
		f.consent = decision{scope: body.GrantScope, audience: body.GrantAudience, session: body.Session.AccessToken}
	}
	writeJSON(w, 200, s.next(f, "cv"))
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[r.PostForm.Get("device_code")]
	switch {
	case r.PostForm.Get("grant_type") != hydra.DeviceCodeGrant || d == nil || d.clientID != r.PostForm.Get("client_id") || d.consumed:
		oauthError(w, 400, "invalid_grant")
	case time.Now().After(d.expires):
		oauthError(w, 400, "expired_token")
	case !d.approved:
		oauthError(w, 400, "authorization_pending")
	default:
		d.consumed = true
		lifespan := time.Hour
		if v, err := time.ParseDuration(s.lifespans[d.clientID].DeviceAccessToken); err == nil {
			lifespan = v
		}
		token := "at_" + random()
		s.tokens[token] = hydra.IntrospectedToken{Active: true, Scope: strings.Join(d.grantScope, " "), ClientID: d.clientID, Subject: d.subject, ExpiresAt: time.Now().Add(lifespan).Unix(),
			Audience: d.grantAudience, Issuer: s.Issuer(), TokenUse: "access_token", Ext: d.ext}
		writeJSON(w, 200, map[string]any{"access_token": token, "token_type": "bearer", "expires_in": int(lifespan.Seconds()), "scope": strings.Join(d.grantScope, " ")})
	}
}

func (s *Server) introspect(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[r.PostForm.Get("token")]
	if !ok || !t.Active || time.Now().Unix() >= t.ExpiresAt {
		writeJSON(w, 200, map[string]bool{"active": false})
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) createClient(w http.ResponseWriter, r *http.Request) {
	var c hydra.OAuthClient
	_ = json.NewDecoder(r.Body).Decode(&c)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[c.ClientID]; ok {
		oauthError(w, 409, "conflict")
		return
	}
	s.clients[c.ClientID] = c
	writeJSON(w, 201, c)
}
func (s *Server) getClient(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[r.PathValue("id")]
	if !ok {
		oauthError(w, 404, "not_found")
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) updateClient(w http.ResponseWriter, r *http.Request) {
	var c hydra.OAuthClient
	_ = json.NewDecoder(r.Body).Decode(&c)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[r.PathValue("id")]; !ok || c.ClientID != r.PathValue("id") {
		oauthError(w, 404, "not_found")
		return
	}
	s.clients[c.ClientID] = c
	writeJSON(w, 200, c)
}
func (s *Server) setLifespans(w http.ResponseWriter, r *http.Request) {
	var l hydra.Lifespans
	_ = json.NewDecoder(r.Body).Decode(&l)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[r.PathValue("id")]
	if !ok {
		oauthError(w, 404, "not_found")
		return
	}
	s.lifespans[c.ClientID] = l
	writeJSON(w, 200, c)
}
