package control

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/types"
)

// RawMessage distinguishes an absent workspaceId from an explicit null.
func mutationWorkspace(r *http.Request, bodyWorkspace json.RawMessage) (string, error) {
	if id := r.PathValue("workspaceId"); id != "" {
		if len(bodyWorkspace) != 0 {
			return "", &types.Error{Kind: types.KindInvalid, Message: "workspaceId belongs in the path, not the request body"}
		}
		return id, nil
	}
	var workspaceID string
	if json.Unmarshal(bodyWorkspace, &workspaceID) != nil || workspaceID == "" {
		return "", &types.Error{Kind: types.KindInvalid, Message: "workspaceId is required"}
	}
	return workspaceID, nil
}

func (a *App) restCreate(resource string, admin bool) http.HandlerFunc {
	return a.restAuth(admin, func(w http.ResponseWriter, r *http.Request, u types.User) {
		if _, err := a.restSnapshot(r, u); err != nil {
			respond(w, nil, err, 201)
			return
		}
		var value any
		var id string
		var err error
		switch resource {
		case "users":
			var body struct {
				Name  string `json:"name"`
				Admin bool   `json:"admin"`
			}
			if !decode(w, r, &body) {
				return
			}
			user, token, e := a.State.CreateUser(u.ID, body.Name, body.Admin)
			value = map[string]any{"user": user, "token": token}
			id = user.ID
			err = e
		case "workspaces":
			var body struct {
				Name string `json:"name"`
			}
			if !decode(w, r, &body) {
				return
			}
			workspace, e := a.State.CreateWorkspace(u.ID, body.Name)
			value = workspace
			id = workspace.ID
			err = e
		case "enrollment-keys":
			var body struct {
				WorkspaceID json.RawMessage `json:"workspaceId"`
				UserID      string          `json:"userId"`
				Roles       []string        `json:"roles"`
				MaxUses     int             `json:"maxUses"`
				ExpiresAt   time.Time       `json:"expiresAt"`
			}
			if !decode(w, r, &body) {
				return
			}
			workspace, e := mutationWorkspace(r, body.WorkspaceID)
			if e != nil {
				respond(w, nil, e, 201)
				return
			}
			key, token, e := a.State.CreateEnrollmentKey(u.ID, types.EnrollmentKey{WorkspaceID: workspace, UserID: body.UserID, Roles: body.Roles, MaxUses: body.MaxUses, ExpiresAt: body.ExpiresAt})
			value = map[string]any{"key": key, "token": token}
			id = key.ID
			err = e
		case "routes":
			var body struct {
				WorkspaceID json.RawMessage `json:"workspaceId"`
				Kind        string          `json:"kind"`
				AgentID     string          `json:"agentId"`
				Name        string          `json:"name"`
				Rule        policy.Rule     `json:"rule"`
				Priority    int             `json:"priority"`
			}
			if !decode(w, r, &body) {
				return
			}
			if body.Kind != "egress" {
				apiError(w, 400, "invalid", "only egress routes can be created; serve routes are derived from agent configuration")
				return
			}
			workspace, e := mutationWorkspace(r, body.WorkspaceID)
			if e != nil {
				respond(w, nil, e, 201)
				return
			}
			route, e := a.State.CreateEgressRoute(u.ID, types.EgressRoute{WorkspaceID: workspace, AgentID: body.AgentID, Name: body.Name, Rule: body.Rule, Priority: body.Priority})
			value = egressRouteView(route)
			id = route.ID
			err = e
		case "grants":
			var body struct {
				WorkspaceID  json.RawMessage `json:"workspaceId"`
				ResourceType string          `json:"resourceType"`
				ResourceID   string          `json:"resourceId"`
				SubjectType  string          `json:"subjectType"`
				SubjectID    string          `json:"subjectId"`
				StartsAt     time.Time       `json:"startsAt"`
				ExpiresAt    time.Time       `json:"expiresAt"`
			}
			if !decode(w, r, &body) {
				return
			}
			workspace, e := mutationWorkspace(r, body.WorkspaceID)
			if e != nil {
				respond(w, nil, e, 201)
				return
			}
			grant, e := a.State.CreateGrant(u.ID, types.Grant{WorkspaceID: workspace, ResourceType: body.ResourceType, ResourceID: body.ResourceID, SubjectType: body.SubjectType, SubjectID: body.SubjectID, StartsAt: body.StartsAt, ExpiresAt: body.ExpiresAt})
			value = grant
			id = grant.ID
			err = e
		}
		if err == nil {
			w.Header().Set("Location", resourceLocation(r, id))
		}
		respond(w, value, err, 201)
	})
}
func (a *App) visibleResource(r *http.Request, u types.User, resource string) (resourceRow, error) {
	snap, err := a.restSnapshot(r, u)
	if err != nil {
		return resourceRow{}, err
	}
	rows, err := a.resourceRows(r, u, snap, resource)
	if err != nil {
		return resourceRow{}, err
	}
	id := restID(r, resource)
	for _, row := range rows {
		if row.id == id {
			return row, nil
		}
	}
	return resourceRow{}, &types.Error{Kind: types.KindNotFound, Message: "resource not found"}
}
func (a *App) restUpdate(resource string, admin bool) http.HandlerFunc {
	return a.restAuth(admin, func(w http.ResponseWriter, r *http.Request, u types.User) {
		current, err := a.visibleResource(r, u, resource)
		if err != nil {
			respond(w, nil, err, 200)
			return
		}
		var value any
		switch resource {
		case "users":
			var body struct {
				Disabled *bool `json:"disabled"`
			}
			if !decode(w, r, &body) {
				return
			}
			if body.Disabled == nil || !*body.Disabled {
				apiError(w, 400, "invalid", "only disabled:true is supported")
				return
			}
			err = a.State.DisableUser(u.ID, current.id)
			user := current.value.(types.User)
			user.Disabled = true
			value = user
		case "agents":
			var body struct {
				Status string `json:"status"`
			}
			if !decode(w, r, &body) {
				return
			}
			if body.Status != "revoked" {
				apiError(w, 400, "invalid", "only status:revoked is supported")
				return
			}
			err = a.State.RevokeAgent(u.ID, current.id)
			if err == nil {
				value, err = a.State.Agent(current.id)
			}
		case "enrollment-keys", "grants":
			var body struct {
				Revoked *bool `json:"revoked"`
			}
			if !decode(w, r, &body) {
				return
			}
			if body.Revoked == nil || !*body.Revoked {
				apiError(w, 400, "invalid", "only revoked:true is supported")
				return
			}
			if resource == "enrollment-keys" {
				err = a.State.RevokeEnrollmentKey(u.ID, current.id)
				key := current.value.(types.EnrollmentKey)
				key.Revoked = true
				value = key
			} else {
				err = a.State.RevokeGrant(u.ID, current.id)
				grant := current.value.(types.Grant)
				grant.Revoked = true
				value = grant
			}
		case "servers", "routes":
			var body struct {
				Enabled *bool `json:"enabled"`
			}
			if !decode(w, r, &body) {
				return
			}
			if body.Enabled == nil {
				apiError(w, 400, "invalid", "enabled is required")
				return
			}
			if resource == "servers" {
				server, e := a.State.SetServiceEnabled(u.ID, current.id, *body.Enabled)
				err = e
				if err == nil {
					value = a.serverView(server, nil)
				}
			} else if current.value.(RouteView).Kind == "serve" {
				server, e := a.State.SetServiceEnabled(u.ID, current.id, *body.Enabled)
				err = e
				value = a.serveRoute(server)
			} else {
				route, e := a.State.SetEgressRouteEnabled(u.ID, current.id, *body.Enabled)
				err = e
				value = egressRouteView(route)
			}
		}
		respond(w, value, err, 200)
	})
}
func (a *App) restToken(w http.ResponseWriter, r *http.Request) {
	a.restAuth(true, func(w http.ResponseWriter, r *http.Request, u types.User) {
		row, err := a.visibleResource(r, u, "users")
		if err != nil {
			respond(w, nil, err, 201)
			return
		}
		token, err := a.State.RotateUserToken(u.ID, row.id)
		respond(w, map[string]string{"token": token}, err, 201)
	})(w, r)
}
func (a *App) restMembership(admin bool) http.HandlerFunc {
	return a.restAuth(admin, func(w http.ResponseWriter, r *http.Request, u types.User) {
		if _, err := a.restSnapshot(r, u); err != nil {
			respond(w, nil, err, 200)
			return
		}
		workspaceID, userID := r.PathValue("workspaceId"), r.PathValue("userId")
		if r.Method == http.MethodDelete {
			if _, err := a.visibleResource(r, u, "memberships"); err != nil {
				respond(w, nil, err, 200)
				return
			}
			if err := a.State.RemoveMembership(u.ID, workspaceID, userID); err != nil {
				respond(w, nil, err, 200)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var body struct {
			Role string `json:"role"`
		}
		if !decode(w, r, &body) {
			return
		}
		value, err := a.State.SetMembership(u.ID, workspaceID, userID, body.Role)
		respond(w, value, err, 200)
	})
}
