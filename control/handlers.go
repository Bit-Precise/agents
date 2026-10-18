package control

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"kind": kind, "message": message}})
}
func respond(w http.ResponseWriter, value any, err error, status int) {
	if err == nil {
		writeJSON(w, status, value)
		return
	}
	var e *types.Error
	if errors.As(err, &e) {
		codes := map[types.ErrorKind]int{types.KindInvalid: 400, types.KindUnauthorized: 401, types.KindForbidden: 403, types.KindNotFound: 404, types.KindConflict: 409, types.KindExpired: 401, types.KindUnavailable: 503}
		code := codes[e.Kind]
		if code == 0 {
			code = 500
		}
		apiError(w, code, string(e.Kind), e.Message)
		return
	}
	apiError(w, 500, "internal", "request could not be completed")
}
func decode(w http.ResponseWriter, r *http.Request, target any) (complete bool) {
	// Only bounded control JSON bodies have this read timeout. A server-wide
	// ReadTimeout would terminate legitimate XHTTP downloads and service streams.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer func() {
		// Leave the bound in place if net/http must drain an unread/invalid body.
		if complete {
			_ = controller.SetReadDeadline(time.Time{})
		}
	}()
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || !strings.EqualFold(mediaType, "application/json") {
		apiError(w, 415, "invalid", "application/json required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apiError(w, 413, "invalid", "request JSON exceeds 1 MiB")
		} else {
			apiError(w, 400, "invalid", "invalid request JSON")
		}
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apiError(w, 413, "invalid", "request JSON exceeds 1 MiB")
		} else {
			apiError(w, 400, "invalid", "one JSON document required")
		}
		return false
	}
	return true
}

// routes keeps the agentd wire protocol stable while management uses resource APIs.
func (a *App) routes() {
	m := a.router
	m.HandleFunc("GET /auth/authorize", a.authorizeService)
	m.HandleFunc("GET /auth/oidc/login", a.oidcLogin)
	m.HandleFunc("GET /auth/oidc/callback", a.oidcCallback)
	m.HandleFunc("POST /api/v1/enroll", a.enroll)
	m.HandleFunc("GET /api/v1/agent/credentials", a.agentCredentials)
	m.HandleFunc("POST /api/v1/agent/config", a.agentConfig)
	a.deviceRoutes()
	a.registerREST()
}

func (a *App) enroll(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); o != "" && !a.validOrigin(o) {
		apiError(w, 403, "forbidden", "origin rejected")
		return
	}
	var b types.EnrollRequest
	if !decode(w, r, &b) {
		return
	}
	if strings.TrimSuffix(b.Instance, "/") != strings.TrimSuffix(a.Config.ServerURL, "/") || strings.TrimSuffix(b.Endpoint, "/") != strings.TrimSuffix(a.Config.EdgeURL, "/") {
		apiError(w, 400, "invalid", "agent instance and edge endpoint must match this server")
		return
	}
	v, err := a.State.Enroll(b)
	v.Credentials.ControlURL = strings.TrimSuffix(a.Config.ServerURL, "/")
	respond(w, v, err, 201)
}
func controlToken(r *http.Request) string {
	token, _ := bearerToken(r)
	return token
}
func (a *App) agentCredentials(w http.ResponseWriter, r *http.Request) {
	v, err := a.State.GetAgentCredentials(controlToken(r))
	v.ControlURL = strings.TrimSuffix(a.Config.ServerURL, "/")
	respond(w, v, err, 200)
}

// agentConfig accepts or rejects a desired spec automatically; the device's
// control token identifies the Agent, so no instance or endpoint is resent.
func (a *App) agentConfig(w http.ResponseWriter, r *http.Request) {
	var update api.ConfigUpdate
	if !decode(w, r, &update) {
		return
	}
	v, err := a.State.UpdateAgent(controlToken(r), update)
	respond(w, v, err, 200)
}
