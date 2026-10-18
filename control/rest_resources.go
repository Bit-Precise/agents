package control

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/types"
	"github.com/bit-precise/agents/edge"
)

// AgentSummary deliberately excludes Desired and Approval: server catalogues
// are visible to workspace members who cannot read another device's origins.
type AgentSummary struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	Roles           []string `json:"roles"`
	PendingApproval bool     `json:"pendingApproval"`
}
type AgentRoleView struct {
	ID               string       `json:"id"`
	WorkspaceID      string       `json:"workspaceId"`
	AgentID          string       `json:"agentId"`
	Agent            AgentSummary `json:"agent"`
	Enabled          bool         `json:"enabled"`
	ApprovedRevision uint64       `json:"approvedRevision"`
	Status           string       `json:"status"`
}
type ServerView struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspaceId"`
	ServiceID   string       `json:"serviceId"`
	AgentID     string       `json:"agentId"`
	Agent       AgentSummary `json:"agent"`
	Name        string       `json:"name"`
	Hostname    string       `json:"hostname"`
	Enabled     bool         `json:"enabled"`
}

// RouteRule describes a published route. HTTP serve routes are descriptive;
// egress creation still validates policy.Rule and only allows TLS rules.
type RouteRule struct {
	Domain   policy.Domain `json:"domain"`
	Ports    []int         `json:"ports"`
	Protocol string        `json:"protocol"`
}
type RouteView struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	TargetType  string    `json:"targetType"`
	TargetID    string    `json:"targetId"`
	AgentID     string    `json:"agentId"`
	Rule        RouteRule `json:"rule"`
	Enabled     bool      `json:"enabled"`
	Priority    int       `json:"priority"`
}

func summarizeAgent(a types.Agent) AgentSummary {
	roles := slices.Clone(a.Roles)
	if roles == nil {
		roles = []string{}
	}
	return AgentSummary{a.ID, a.Name, a.Status, roles, a.PendingApproval}
}
func (a *App) serverView(v types.Service, agents map[string]types.Agent) ServerView {
	agent, ok := agents[v.AgentID]
	if !ok {
		agent, _ = a.State.Agent(v.AgentID)
	}
	return ServerView{v.ID, v.WorkspaceID, v.ID, v.AgentID, summarizeAgent(agent), v.Name, v.Hostname, v.Enabled}
}
func (a *App) serveRoute(v types.Service) RouteView {
	server, _ := url.Parse(a.Config.ServerURL)
	port, _ := strconv.Atoi(effectivePort(server))
	protocol := "tls"
	if server.Scheme == "http" {
		protocol = "http"
	}
	return RouteView{v.ID, v.WorkspaceID, v.Name, "serve", "server", v.ID, v.AgentID, RouteRule{policy.Domain{Exact: v.Hostname}, []int{port}, protocol}, v.Enabled, 0}
}
func egressRouteView(v types.EgressRoute) RouteView {
	return RouteView{v.ID, v.WorkspaceID, v.Name, "egress", "egress", v.AgentID, v.AgentID, RouteRule{v.Rule.Domain, v.Rule.Ports, v.Rule.Protocol}, v.Enabled, v.Priority}
}

type resourceRow struct {
	id          string
	workspaceID string
	value       any
	fields      map[string]string
	search      string
}

func row(id, workspaceID string, value any, fields map[string]string) resourceRow {
	if fields == nil {
		fields = map[string]string{}
	}
	fields["id"] = id
	fields["workspaceId"] = workspaceID
	var parts []string
	for _, key := range []string{"id", "name", "hostname", "agentName", "domain"} {
		parts = append(parts, fields[key])
	}
	return resourceRow{id, workspaceID, value, fields, strings.ToLower(strings.Join(parts, " "))}
}
func boolKey(v bool) string      { return strconv.FormatBool(v) }
func timeKey(v time.Time) string { return v.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func (a *App) edgeSnapshot(r *http.Request) (edge.Snapshot, error) {
	if a.status != nil {
		v, err := a.status.Snapshot(r.Context())
		if err != nil {
			return edge.Snapshot{}, &types.Error{Kind: types.KindUnavailable, Message: "edge telemetry unavailable"}
		}
		return v, nil
	}
	if a.Edge == nil {
		return edge.Snapshot{}, &types.Error{Kind: types.KindUnavailable, Message: "edge telemetry unavailable"}
	}
	return a.Edge.Snapshot(), nil
}
func (a *App) resourceRows(r *http.Request, u types.User, snap types.Snapshot, resource string) ([]resourceRow, error) {
	rows := []resourceRow{}
	agents := map[string]types.Agent{}
	if resource == "agents" || resource == "ingresses" || resource == "egresses" || resource == "servers" {
		// Only references from the caller's visible snapshot enter this projection;
		// full projected configs are never serialized by role or server views.
		ids := []string{}
		if resource == "servers" {
			for _, server := range snap.Services {
				ids = append(ids, server.AgentID)
			}
		} else {
			for _, agent := range snap.Agents {
				ids = append(ids, agent.ID)
			}
		}
		var err error
		agents, err = a.State.AgentViews(ids)
		if err != nil {
			return nil, err
		}
	}
	switch resource {
	case "users":
		for _, v := range snap.Users {
			if wid := r.PathValue("workspaceId"); wid != "" && !membershipMatches(snap, wid, v.ID, "") {
				continue
			}
			rows = append(rows, row(v.ID, "", v, map[string]string{"name": v.Name, "admin": boolKey(v.Admin), "disabled": boolKey(v.Disabled), "createdAt": timeKey(v.CreatedAt)}))
		}
	case "workspaces":
		for _, v := range snap.Workspaces {
			rows = append(rows, row(v.ID, v.ID, v, map[string]string{"name": v.Name, "createdAt": timeKey(v.CreatedAt)}))
		}
	case "memberships":
		for _, v := range snap.Memberships {
			rows = append(rows, row(v.WorkspaceID+"/"+v.UserID, v.WorkspaceID, v, map[string]string{"userId": v.UserID, "role": v.Role}))
		}
	case "agents":
		for _, original := range snap.Agents {
			v, ok := agents[original.ID]
			if !ok {
				continue
			}
			rows = append(rows, row(v.ID, v.WorkspaceID, v, map[string]string{"userId": v.UserID, "name": v.Name, "status": v.Status, "pendingApproval": boolKey(v.PendingApproval), "createdAt": timeKey(v.CreatedAt)}))
		}
	case "ingresses", "egresses":
		role := "ingress"
		if resource == "egresses" {
			role = "egress"
		}
		for _, original := range snap.Agents {
			v, ok := agents[original.ID]
			if !ok {
				continue
			}
			if !slices.Contains(v.Roles, role) {
				continue
			}
			enabled := v.Desired.Ingress.Enabled && v.Status != "revoked"
			if role == "egress" {
				enabled = v.Desired.Egress.Enabled && v.Status == "active" && len(v.Approval.Egress) > 0
			}
			view := AgentRoleView{v.ID, v.WorkspaceID, v.ID, summarizeAgent(v), enabled, v.Approval.Revision, v.Status}
			rows = append(rows, row(v.ID, v.WorkspaceID, view, map[string]string{"agentId": v.ID, "name": v.Name, "status": v.Status, "enabled": boolKey(enabled)}))
		}
	case "enrollment-keys":
		for _, v := range snap.EnrollmentKeys {
			rows = append(rows, row(v.ID, v.WorkspaceID, v, map[string]string{"userId": v.UserID, "revoked": boolKey(v.Revoked), "expiresAt": timeKey(v.ExpiresAt)}))
		}
	case "servers":
		for _, v := range snap.Services {
			view := a.serverView(v, agents)
			rows = append(rows, row(v.ID, v.WorkspaceID, view, map[string]string{"agentId": v.AgentID, "agentName": view.Agent.Name, "name": v.Name, "hostname": v.Hostname, "enabled": boolKey(v.Enabled)}))
		}
	case "routes":
		appendRoute := func(view RouteView) {
			rows = append(rows, row(view.ID, view.WorkspaceID, view, map[string]string{"name": view.Name, "kind": view.Kind, "targetType": view.TargetType, "agentId": view.AgentID, "enabled": boolKey(view.Enabled), "priority": strconv.Itoa(view.Priority), "domain": view.Rule.Domain.Exact + " " + view.Rule.Domain.Suffix}))
		}
		for _, v := range snap.Services {
			appendRoute(a.serveRoute(v))
		}
		for _, v := range snap.EgressRoutes {
			appendRoute(egressRouteView(v))
		}
	case "grants":
		for _, v := range snap.Grants {
			rows = append(rows, row(v.ID, v.WorkspaceID, v, map[string]string{"resourceType": v.ResourceType, "resourceId": v.ResourceID, "subjectType": v.SubjectType, "subjectId": v.SubjectID, "revoked": boolKey(v.Revoked), "startsAt": timeKey(v.StartsAt), "expiresAt": timeKey(v.ExpiresAt)}))
		}
	case "audit-events":
		for _, v := range snap.Audit {
			rows = append(rows, row(v.ID, "", v, map[string]string{"actorId": v.ActorID, "resourceId": v.ResourceID, "action": v.Action, "at": timeKey(v.At)}))
		}
	case "connections", "flows":
		view, err := a.edgeSnapshot(r)
		if err != nil {
			return nil, err
		}
		managed := map[string]bool{}
		own := map[string]bool{}
		for _, m := range snap.Memberships {
			if m.UserID == u.ID && (m.Role == "owner" || m.Role == "maintainer") {
				managed[m.WorkspaceID] = true
			}
		}
		for _, v := range snap.Agents {
			if v.UserID == u.ID {
				own[v.ID] = true
			}
		}
		if resource == "connections" {
			for _, v := range view.Connections {
				if !u.Admin && !managed[v.WorkspaceID] && !own[v.AgentID] {
					continue
				}
				rows = append(rows, row(v.ID, v.WorkspaceID, v, map[string]string{"agentId": v.AgentID, "role": v.Role, "connectedAt": timeKey(v.ConnectedAt)}))
			}
		} else {
			for _, v := range view.Flows {
				// Provider ownership never grants access to another consumer's destinations.
				if !u.Admin && !managed[v.WorkspaceID] && v.UserID != u.ID {
					continue
				}
				rows = append(rows, row(v.ID, v.WorkspaceID, v, map[string]string{"agentId": v.AgentID, "userId": v.UserID, "resourceId": v.ResourceID, "purpose": v.Purpose, "state": v.State, "startedAt": timeKey(v.StartedAt)}))
			}
		}
	default:
		return nil, fmt.Errorf("unknown resource %q", resource)
	}
	if workspaceID := r.PathValue("workspaceId"); workspaceID != "" && resource != "users" {
		rows = slices.DeleteFunc(rows, func(v resourceRow) bool { return v.workspaceID != workspaceID })
	}
	return rows, nil
}
func membershipMatches(snap types.Snapshot, workspaceID, userID, role string) bool {
	for _, m := range snap.Memberships {
		if m.WorkspaceID == workspaceID && (userID == "" || m.UserID == userID) && (role == "" || m.Role == role) {
			return true
		}
	}
	return false
}
