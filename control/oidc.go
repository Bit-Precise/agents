package control

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	controlstate "github.com/bit-precise/agents/control/state"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const oidcTransactionTTL = 5 * time.Minute

type oidcTransaction struct {
	Nonce, Verifier, ReturnTo, Host string
	ExpiresAt                       time.Time
}

type oidcAuth struct {
	mu          sync.Mutex
	pending     map[string]oidcTransaction
	verifier    *oidc.IDTokenVerifier
	oauth       oauth2.Config
	http        *http.Client
	adminEmails map[string]struct{}
}

func newOIDC(ctx context.Context, cfg Config) (*oidcAuth, error) {
	if !cfg.OIDC.Enabled {
		return nil, nil
	}
	secret := ""
	if cfg.OIDC.ClientSecretFile != "" {
		var err error
		secret, err = ReadSecret(cfg.OIDC.ClientSecretFile)
		if err != nil {
			return nil, fmt.Errorf("read OIDC client secret: %w", err)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	discoveryCtx, cancel := context.WithTimeout(oidc.ClientContext(ctx, client), 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(discoveryCtx, cfg.OIDC.Issuer)
	if err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	var discovery struct {
		JWKS string `json:"jwks_uri"`
	}
	if err = provider.Claims(&discovery); err != nil {
		client.CloseIdleConnections()
		return nil, errors.New("invalid OIDC discovery document")
	}
	endpoint := provider.Endpoint()
	if secret == "" {
		// Public clients identify themselves in the token request and rely on
		// PKCE, without an empty Basic credential or an auth-style retry.
		endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	for _, value := range []string{endpoint.AuthURL, endpoint.TokenURL, discovery.JWKS} {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(cfg.DevelopmentHTTP && u.Scheme == "http" && localHost(u.Hostname()))) {
			client.CloseIdleConnections()
			return nil, errors.New("OIDC discovered endpoints must use HTTPS (loopback HTTP requires developmentHTTP)")
		}
	}
	scopes := cfg.OIDC.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	if !slices.Contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	adminEmails := make(map[string]struct{}, len(cfg.OIDC.AdminEmails))
	for _, value := range cfg.OIDC.AdminEmails {
		if email, ok := normalizedOIDCEmail(value); ok {
			adminEmails[email] = struct{}{}
		}
	}
	if len(adminEmails) > 0 && !slices.Contains(scopes, "email") {
		// Discovery scopes_supported can be incomplete. Request the claim needed
		// by this policy even when the provider does not advertise the scope.
		scopes = append(slices.Clone(scopes), "email")
	}
	return &oidcAuth{pending: make(map[string]oidcTransaction), http: client, adminEmails: adminEmails,
		verifier: provider.VerifierContext(oidc.ClientContext(context.Background(), client), &oidc.Config{ClientID: cfg.OIDC.ClientID}),
		oauth:    oauth2.Config{ClientID: cfg.OIDC.ClientID, ClientSecret: secret, Endpoint: endpoint, RedirectURL: cfg.oidcRedirectURL(), Scopes: scopes}}, nil
}

func safeOIDCReturnTo(value string) (string, bool) {
	// The Console sends this exact path explicitly; omitted targets use it too.
	if value == "" || value == "/console/" {
		return "/console/", true
	}
	if len(value) > 8192 {
		return "", false
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || u.RawPath != "" {
		return "", false
	}
	switch {
	case u.Path == "/auth/authorize" && strings.HasPrefix(value, "/auth/authorize?"):
	case u.Path == "/oauth/login" && strings.HasPrefix(value, "/oauth/login?"):
		// A Device Flow sign-in continues at Hydra's login challenge only.
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query) != 1 || len(query["login_challenge"]) != 1 || !validChallenge(query.Get("login_challenge")) {
			return "", false
		}
	default:
		return "", false
	}
	return u.RequestURI(), true
}

// avatarURL keeps an IdP picture only as a plain HTTPS URL; the Console shows
// it as an image and falls back to initials otherwise.
func avatarURL(value string) string {
	u, err := url.Parse(value)
	if err != nil || len(value) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.ContainsAny(value, "\r\n\x00\"'<> ") {
		return ""
	}
	return u.String()
}

func (a *App) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !a.validOrigin(origin) {
		apiError(w, 403, "forbidden", "origin rejected")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["returnTo"]) > 1 {
		apiError(w, 400, "invalid", "invalid login return target")
		return
	}
	returnTo, ok := safeOIDCReturnTo(query.Get("returnTo"))
	if !ok {
		apiError(w, 400, "invalid", "invalid login return target")
		return
	}
	state, nonce, verifier := randomSecret(), randomSecret(), oauth2.GenerateVerifier()
	transaction := oidcTransaction{Nonce: nonce, Verifier: verifier, ReturnTo: returnTo, Host: authorityKey(r.Host, a.publicScheme()), ExpiresAt: time.Now().Add(oidcTransactionTTL)}
	a.oidc.mu.Lock()
	for key, pending := range a.oidc.pending {
		if !pending.ExpiresAt.After(time.Now()) {
			delete(a.oidc.pending, key)
		}
	}
	if previous, ok := a.readCookie(r, "oidc"); ok {
		delete(a.oidc.pending, secretHash(previous))
	}
	if len(a.oidc.pending) >= 4096 {
		a.oidc.mu.Unlock()
		apiError(w, 429, "capacity", "too many pending logins")
		return
	}
	a.oidc.pending[secretHash(state)] = transaction
	a.oidc.mu.Unlock()
	a.setCookie(w, "oidc", state, transaction.ExpiresAt)
	location := a.oidc.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (a *App) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	cookie, ok := a.readCookie(r, "oidc")
	state := query.Get("state")
	if err != nil || !ok || len(query["state"]) != 1 || len(state) != 64 || subtle.ConstantTimeCompare([]byte(cookie), []byte(state)) != 1 {
		apiError(w, 403, "forbidden", "invalid login state")
		return
	}
	a.oidc.mu.Lock()
	transaction, found := a.oidc.pending[secretHash(state)]
	if found && transaction.Host == authorityKey(r.Host, a.publicScheme()) && transaction.ExpiresAt.After(time.Now()) {
		delete(a.oidc.pending, secretHash(state))
	} else {
		found = false
	}
	a.oidc.mu.Unlock()
	if !found {
		apiError(w, 403, "forbidden", "expired or already consumed login state")
		return
	}
	a.setCookie(w, "oidc", "", time.Unix(1, 0))
	// Consume the transaction before token exchange. A callback can never be
	// replayed after exchange failure, IdP denial, or a failed ID-token check.
	if query.Get("error") != "" || len(query["code"]) != 1 || query.Get("code") == "" || len(query.Get("code")) > 8192 || len(query["iss"]) > 1 || (query.Get("iss") != "" && query.Get("iss") != a.Config.OIDC.Issuer) {
		apiError(w, 401, "unauthorized", "identity provider login rejected")
		return
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), a.oidc.http), 10*time.Second)
	defer cancel()
	token, err := a.oidc.oauth.Exchange(ctx, query.Get("code"), oauth2.VerifierOption(transaction.Verifier))
	if err != nil {
		apiError(w, 401, "unauthorized", "identity provider token exchange failed")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || len(rawIDToken) > 64<<10 {
		apiError(w, 401, "unauthorized", "identity provider ID token missing")
		return
	}
	identity, err := a.oidc.verifier.Verify(ctx, rawIDToken)
	if err != nil || identity.Issuer != a.Config.OIDC.Issuer || identity.Subject == "" || subtle.ConstantTimeCompare([]byte(identity.Nonce), []byte(transaction.Nonce)) != 1 {
		apiError(w, 401, "unauthorized", "identity provider ID token rejected")
		return
	}
	var claims struct {
		Name              string          `json:"name"`
		PreferredUsername string          `json:"preferred_username"`
		Picture           string          `json:"picture"`
		AuthorizedParty   string          `json:"azp"`
		Email             json.RawMessage `json:"email"`
		EmailVerified     json.RawMessage `json:"email_verified"`
	}
	if identity.Claims(&claims) != nil || (len(identity.Audience) > 1 && claims.AuthorizedParty != a.Config.OIDC.ClientID) || (claims.AuthorizedParty != "" && claims.AuthorizedParty != a.Config.OIDC.ClientID) {
		apiError(w, 401, "unauthorized", "identity provider claims rejected")
		return
	}
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	var verified bool
	var email string
	if json.Unmarshal(claims.EmailVerified, &verified) != nil || !verified || json.Unmarshal(claims.Email, &email) != nil {
		email = ""
	}
	verifiedEmail, _ := normalizedOIDCEmail(email)
	_, emailAdmin := a.oidc.adminEmails[verifiedEmail]
	user, err := a.State.OIDCUser(identity.Issuer, identity.Subject, name, controlstate.OIDCUserOptions{
		BootstrapAdmin: slices.Contains(a.Config.OIDC.BootstrapAdminSubjects, identity.Subject),
		VerifiedEmail:  verifiedEmail,
		EmailAdmin:     emailAdmin,
		Picture:        avatarURL(claims.Picture),
	})
	if err != nil {
		apiError(w, 403, "forbidden", "local account is unavailable")
		return
	}
	expiry := identity.Expiry
	if limit := time.Now().Add(8 * time.Hour); expiry.After(limit) {
		expiry = limit
	}
	if !expiry.After(time.Now()) {
		apiError(w, 401, "unauthorized", "identity provider ID token expired")
		return
	}
	if _, err = a.newSession(w, user.ID, "", transaction.Host, true, expiry); err != nil {
		apiError(w, 429, "capacity", "browser session limit reached")
		return
	}
	// No IdP access/refresh/ID token is stored or returned to the browser.
	http.Redirect(w, r, transaction.ReturnTo, http.StatusSeeOther)
}
