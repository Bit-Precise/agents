package control

import (
	"net/http"
	"strings"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
)

// restAuth authenticates before authorization, so invalid credentials are 401
// while an authenticated caller with a rejected Origin, CSRF token or role is 403.
func (a *App) restAuth(admin bool, fn func(http.ResponseWriter, *http.Request, types.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _, err := a.actor(r, false)
		if err != nil {
			apiError(w, 401, "unauthorized", "authentication required")
			return
		}
		if admin && !u.Admin {
			apiError(w, 403, "forbidden", "instance administrator required")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !a.validOrigin(origin) {
			apiError(w, 403, "forbidden", "origin rejected")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if _, _, err = a.actor(r, true); err != nil {
				apiError(w, 403, "forbidden", "CSRF validation failed")
				return
			}
		}
		fn(w, r, u)
	}
}

func (a *App) restSnapshot(r *http.Request, u types.User) (types.Snapshot, error) {
	snap, err := a.State.SnapshotForUser(u.ID)
	if err != nil {
		return snap, err
	}
	if workspaceID := r.PathValue("workspaceId"); workspaceID != "" {
		for _, workspace := range snap.Workspaces {
			if workspace.ID == workspaceID {
				return snap, nil
			}
		}
		return types.Snapshot{}, &types.Error{Kind: types.KindNotFound, Message: "workspace not found"}
	}
	return snap, nil
}

func (a *App) restRead(resource string, admin bool) http.HandlerFunc {
	return a.restAuth(admin, func(w http.ResponseWriter, r *http.Request, u types.User) {
		snap, err := a.restSnapshot(r, u)
		if err != nil {
			respond(w, nil, err, 200)
			return
		}
		rows, err := a.resourceRows(r, u, snap, resource)
		if err != nil {
			respond(w, nil, err, 200)
			return
		}
		if id := restID(r, resource); id != "" {
			for _, row := range rows {
				if row.id == id {
					writeJSON(w, 200, row.value)
					return
				}
			}
			apiError(w, 404, "not_found", "resource not found")
			return
		}
		page, err := resourcePage(r, resource, rows, snap)
		respond(w, page, err, 200)
	})
}

func restID(r *http.Request, resource string) string {
	if id := r.PathValue("id"); id != "" {
		return id
	}
	switch resource {
	case "workspaces":
		return r.PathValue("workspaceId")
	case "memberships":
		if user := r.PathValue("userId"); user != "" {
			return r.PathValue("workspaceId") + "/" + user
		}
	case "users":
		return r.PathValue("userId")
	case "agents", "ingresses", "egresses":
		return r.PathValue("agentId")
	case "servers":
		return r.PathValue("serviceId")
	case "routes":
		return r.PathValue("routeId")
	case "enrollment-keys":
		return r.PathValue("keyId")
	case "service-accounts":
		return r.PathValue("serviceAccountId")
	case "grants":
		return r.PathValue("grantId")
	case "connections":
		return r.PathValue("connectionId")
	case "flows":
		return r.PathValue("flowId")
	}
	return ""
}

func (a *App) registerREST() {
	m := a.router
	m.HandleFunc("GET /v1/instance", func(w http.ResponseWriter, r *http.Request) {
		info := api.InstanceInfo{ServerURL: a.Config.ServerURL, EdgeURL: a.Config.EdgeURL, DevelopmentHTTP: a.Config.DevelopmentHTTP, OIDC: api.InstanceOIDC{Enabled: a.oidc != nil, LoginURL: "/auth/oidc/login"}}
		if a.device != nil {
			info.DeviceAuthorization = a.device.info()
		}
		writeJSON(w, 200, info)
	})
	m.HandleFunc("GET /v1/instance/status", a.restAuth(false, func(w http.ResponseWriter, r *http.Request, u types.User) {
		snap, err := a.State.SnapshotForUser(u.ID)
		if err != nil {
			respond(w, nil, err, 200)
			return
		}
		_, err = a.edgeSnapshot(r)
		writeJSON(w, 200, map[string]any{"revision": snap.Revision, "edgeAvailable": err == nil})
	}))
	m.HandleFunc("POST /v1/sessions", a.login)
	m.HandleFunc("GET /v1/sessions/current", a.restAuth(false, func(w http.ResponseWriter, r *http.Request, u types.User) {
		if _, bearer := bearerToken(r); bearer {
			apiError(w, 401, "unauthorized", "browser session required")
			return
		}
		_, s, err := a.actor(r, false)
		if err != nil {
			respond(w, nil, err, 200)
			return
		}
		writeJSON(w, 200, map[string]any{"user": u, "csrfToken": s.CSRF})
	}))
	m.HandleFunc("DELETE /v1/sessions/current", a.restAuth(false, func(w http.ResponseWriter, r *http.Request, u types.User) { a.logout(w, r) }))
	m.HandleFunc("GET /v1/users/me", a.restAuth(false, func(w http.ResponseWriter, r *http.Request, u types.User) { writeJSON(w, 200, u) }))
	m.HandleFunc("POST /v1/users/me/tokens", a.restAuth(false, func(w http.ResponseWriter, r *http.Request, u types.User) {
		token, err := a.State.RotateUserToken(u.ID, u.ID)
		respond(w, map[string]string{"token": token}, err, 201)
	}))
	m.HandleFunc("GET /v1/admin/users", a.restRead("users", true))
	m.HandleFunc("GET /v1/admin/users/{id}", a.restRead("users", true))
	m.HandleFunc("GET /v1/admin/workspaces", a.restRead("workspaces", true))
	m.HandleFunc("GET /v1/admin/workspaces/{id}", a.restRead("workspaces", true))
	m.HandleFunc("GET /v1/admin/memberships", a.restRead("memberships", true))
	m.HandleFunc("GET /v1/admin/agents", a.restRead("agents", true))
	m.HandleFunc("GET /v1/admin/agents/{id}", a.restRead("agents", true))
	m.HandleFunc("GET /v1/admin/service-accounts", a.restRead("service-accounts", true))
	m.HandleFunc("GET /v1/admin/service-accounts/{id}", a.restRead("service-accounts", true))
	m.HandleFunc("GET /v1/admin/enrollment-keys", a.restRead("enrollment-keys", true))
	m.HandleFunc("GET /v1/admin/enrollment-keys/{id}", a.restRead("enrollment-keys", true))
	m.HandleFunc("GET /v1/admin/servers", a.restRead("servers", true))
	m.HandleFunc("GET /v1/admin/servers/{id}", a.restRead("servers", true))
	m.HandleFunc("GET /v1/admin/routes", a.restRead("routes", true))
	m.HandleFunc("GET /v1/admin/routes/{id}", a.restRead("routes", true))
	m.HandleFunc("GET /v1/admin/grants", a.restRead("grants", true))
	m.HandleFunc("GET /v1/admin/grants/{id}", a.restRead("grants", true))
	m.HandleFunc("GET /v1/admin/audit-events", a.restRead("audit-events", true))
	m.HandleFunc("GET /v1/admin/audit-events/{id}", a.restRead("audit-events", true))
	m.HandleFunc("GET /v1/admin/connections", a.restRead("connections", true))
	m.HandleFunc("GET /v1/admin/connections/{id}", a.restRead("connections", true))
	m.HandleFunc("GET /v1/admin/flows", a.restRead("flows", true))
	m.HandleFunc("GET /v1/admin/flows/{id}", a.restRead("flows", true))
	m.HandleFunc("GET /v1/admin/ingresses", a.restRead("ingresses", true))
	m.HandleFunc("GET /v1/admin/ingresses/{id}", a.restRead("ingresses", true))
	m.HandleFunc("GET /v1/admin/egresses", a.restRead("egresses", true))
	m.HandleFunc("GET /v1/admin/egresses/{id}", a.restRead("egresses", true))
	m.HandleFunc("GET /v1/workspaces", a.restRead("workspaces", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}", a.restRead("workspaces", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/users", a.restRead("users", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/users/{userId}", a.restRead("users", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/agents", a.restRead("agents", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/agents/{agentId}", a.restRead("agents", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/service-accounts", a.restRead("service-accounts", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/service-accounts/{serviceAccountId}", a.restRead("service-accounts", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/enrollment-keys", a.restRead("enrollment-keys", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/enrollment-keys/{keyId}", a.restRead("enrollment-keys", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/servers", a.restRead("servers", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/servers/{serviceId}", a.restRead("servers", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/routes", a.restRead("routes", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/routes/{routeId}", a.restRead("routes", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/grants", a.restRead("grants", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/grants/{grantId}", a.restRead("grants", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/connections", a.restRead("connections", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/connections/{connectionId}", a.restRead("connections", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/flows", a.restRead("flows", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/flows/{flowId}", a.restRead("flows", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/ingresses", a.restRead("ingresses", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/ingresses/{agentId}", a.restRead("ingresses", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/egresses", a.restRead("egresses", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/egresses/{agentId}", a.restRead("egresses", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/memberships", a.restRead("memberships", false))
	m.HandleFunc("GET /v1/workspaces/{workspaceId}/memberships/{userId}", a.restRead("memberships", false))
	m.HandleFunc("POST /v1/admin/users", a.restCreate("users", true))
	m.HandleFunc("POST /v1/admin/workspaces", a.restCreate("workspaces", true))
	m.HandleFunc("POST /v1/admin/service-accounts", a.restCreate("service-accounts", true))
	m.HandleFunc("POST /v1/admin/routes", a.restCreate("routes", true))
	m.HandleFunc("POST /v1/admin/grants", a.restCreate("grants", true))
	m.HandleFunc("POST /v1/workspaces/{workspaceId}/service-accounts", a.restCreate("service-accounts", false))
	m.HandleFunc("POST /v1/workspaces/{workspaceId}/service-accounts/{serviceAccountId}/enrollment-keys", a.restCreate("enrollment-keys", false))
	m.HandleFunc("POST /v1/workspaces/{workspaceId}/routes", a.restCreate("routes", false))
	m.HandleFunc("POST /v1/workspaces/{workspaceId}/grants", a.restCreate("grants", false))
	m.HandleFunc("PATCH /v1/admin/users/{id}", a.restUpdate("users", true))
	m.HandleFunc("PATCH /v1/admin/agents/{id}", a.restUpdate("agents", true))
	m.HandleFunc("PATCH /v1/workspaces/{workspaceId}/agents/{agentId}", a.restUpdate("agents", false))
	m.HandleFunc("PATCH /v1/admin/service-accounts/{id}", a.restUpdate("service-accounts", true))
	m.HandleFunc("PATCH /v1/workspaces/{workspaceId}/service-accounts/{serviceAccountId}", a.restUpdate("service-accounts", false))
	m.HandleFunc("PATCH /v1/admin/enrollment-keys/{id}", a.restUpdate("enrollment-keys", true))
	m.HandleFunc("PATCH /v1/workspaces/{workspaceId}/enrollment-keys/{keyId}", a.restUpdate("enrollment-keys", false))
	m.HandleFunc("PATCH /v1/admin/servers/{id}", a.restUpdate("servers", true))
	m.HandleFunc("PATCH /v1/workspaces/{workspaceId}/servers/{serviceId}", a.restUpdate("servers", false))
	m.HandleFunc("PATCH /v1/admin/routes/{id}", a.restUpdate("routes", true))
	m.HandleFunc("PATCH /v1/workspaces/{workspaceId}/routes/{routeId}", a.restUpdate("routes", false))
	m.HandleFunc("PATCH /v1/admin/grants/{id}", a.restUpdate("grants", true))
	m.HandleFunc("PATCH /v1/workspaces/{workspaceId}/grants/{grantId}", a.restUpdate("grants", false))
	m.HandleFunc("POST /v1/admin/users/{id}/tokens", a.restToken)
	m.HandleFunc("GET /v1/admin/workspaces/{workspaceId}/memberships/{userId}", a.restRead("memberships", true))
	m.HandleFunc("PUT /v1/admin/workspaces/{workspaceId}/memberships/{userId}", a.restMembership(true))
	m.HandleFunc("DELETE /v1/admin/workspaces/{workspaceId}/memberships/{userId}", a.restMembership(true))
	m.HandleFunc("PUT /v1/workspaces/{workspaceId}/memberships/{userId}", a.restMembership(false))
	m.HandleFunc("DELETE /v1/workspaces/{workspaceId}/memberships/{userId}", a.restMembership(false))
}

func resourceLocation(r *http.Request, id string) string {
	return strings.TrimSuffix(r.URL.Path, "/") + "/" + id
}
