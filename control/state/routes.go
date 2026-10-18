package state

import (
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/connection"
	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/types"
)

func service(snap *types.Snapshot, id string) *types.Service {
	for i := range snap.Services {
		if snap.Services[i].ID == id {
			return &snap.Services[i]
		}
	}
	return nil
}
func route(snap *types.Snapshot, id string) *types.EgressRoute {
	for i := range snap.EgressRoutes {
		if snap.EgressRoutes[i].ID == id {
			return &snap.EgressRoutes[i]
		}
	}
	return nil
}
func canManageService(snap *types.Snapshot, actorID string, v *types.Service) error {
	if err := requireManager(snap, actorID, v.WorkspaceID); err == nil {
		return nil
	}
	u, err := requireUser(snap, actorID)
	if err != nil {
		return err
	}
	m := member(snap, v.WorkspaceID, actorID)
	a := agent(snap, v.AgentID)
	if !u.Disabled && m != nil && m.Role == "developer" && a != nil && isUser(a.Principal, actorID) {
		return nil
	}
	return problem(types.KindForbidden, "service management is not permitted")
}

func (s *State) CreateEgressRoute(actorID string, value types.EgressRoute) (types.EgressRoute, error) {
	var err error
	value.Name, err = label(value.Name)
	if err != nil {
		return value, err
	}
	if err = value.Rule.Validate(true); err != nil {
		return value, problem(types.KindInvalid, err.Error())
	}
	err = s.mutate(func(snap *types.Snapshot) error {
		w := workspace(snap, value.WorkspaceID)
		if w == nil {
			return problem(types.KindNotFound, "workspace not found")
		}
		if err := requireManager(snap, actorID, w.ID); err != nil {
			return err
		}
		a := agent(snap, value.AgentID)
		if a == nil || a.WorkspaceID != w.ID {
			return problem(types.KindForbidden, "exit agent must belong to this workspace")
		}
		if a.Status != api.StatusActive || !slices.Contains(roleCeiling(snap, a), api.RoleEgress) {
			return problem(types.KindForbidden, "agent cannot provide egress")
		}
		for _, r := range snap.EgressRoutes {
			if r.WorkspaceID != w.ID {
				continue
			}
			if r.Name == value.Name {
				return problem(types.KindConflict, "route name already exists")
			}
			if r.Enabled && r.Priority == value.Priority && r.Rule.Domain == value.Rule.Domain {
				for _, port := range value.Rule.Ports {
					if slices.Contains(r.Rule.Ports, port) {
						return problem(types.KindConflict, "ambiguous egress rule at the same priority")
					}
				}
			}
		}
		value.ID = randomID()
		value.WorkspaceID = w.ID
		value.Enabled = true
		snap.EgressRoutes = append(snap.EgressRoutes, value)
		audit(snap, actorID, "egress_route.create", value.ID)
		return nil
	})
	return value, err
}

func (s *State) CreateGrant(actorID string, value types.Grant) (types.Grant, error) {
	err := s.mutate(func(snap *types.Snapshot) error {
		w := workspace(snap, value.WorkspaceID)
		if w == nil {
			return problem(types.KindNotFound, "workspace not found")
		}
		switch value.ResourceType {
		case "service":
			v := service(snap, value.ResourceID)
			if v == nil || v.WorkspaceID != w.ID {
				return problem(types.KindForbidden, "service does not belong to this workspace")
			}
			if err := canManageService(snap, actorID, v); err != nil {
				return err
			}
		case "egress":
			v := route(snap, value.ResourceID)
			if v == nil || v.WorkspaceID != w.ID {
				return problem(types.KindForbidden, "egress route does not belong to this workspace")
			}
			if err := requireManager(snap, actorID, w.ID); err != nil {
				return err
			}
		default:
			return problem(types.KindInvalid, "resourceType must be service or egress")
		}
		external := false
		switch value.SubjectType {
		case "anonymous":
			if value.ResourceType != "service" || value.SubjectID != "" {
				return problem(types.KindInvalid, "anonymous grants only apply to services without a subject ID")
			}
			external = true
		case "user":
			if _, err := requireTargetUser(snap, value.SubjectID); err != nil {
				return err
			}
			external = member(snap, w.ID, value.SubjectID) == nil
		case "workspace":
			subject := workspace(snap, value.SubjectID)
			if subject == nil {
				return problem(types.KindNotFound, "subject workspace not found")
			}
			value.SubjectID = subject.ID
			external = subject.ID != w.ID
		case "service_account":
			// Only the ingress path authenticates a Service Account consumer; the
			// browser Serve path has no machine credential to present.
			if value.ResourceType != "egress" {
				return problem(types.KindInvalid, "service accounts can only be granted egress routes")
			}
			sa := serviceAccount(snap, value.SubjectID)
			if sa == nil || sa.WorkspaceID != w.ID || sa.Status != api.StatusActive {
				return problem(types.KindNotFound, "service account not found in this workspace")
			}
		default:
			return problem(types.KindInvalid, "invalid grant subject type")
		}
		if value.ResourceType == "egress" && external {
			return problem(types.KindForbidden, "egress cannot be shared outside its workspace")
		}
		if external {
			if err := requireManager(snap, actorID, w.ID); err != nil {
				return problem(types.KindForbidden, "external or anonymous sharing requires a workspace manager")
			}
		}
		now := time.Now()
		if value.StartsAt.IsZero() {
			value.StartsAt = now.UTC()
		}
		if value.ExpiresAt.IsZero() || !value.StartsAt.Before(value.ExpiresAt) || !value.ExpiresAt.After(now) || value.ExpiresAt.Sub(value.StartsAt) > TokenTTL {
			return problem(types.KindInvalid, "grant needs an ordered, finite window of at most 30 days")
		}
		value.ID = randomID()
		value.WorkspaceID = w.ID
		value.Revoked = false
		value.External = external
		value.CreatedBy = actorID
		snap.Grants = append(snap.Grants, value)
		audit(snap, actorID, "grant.create", value.ID)
		return nil
	})
	return value, err
}

func (s *State) RevokeGrant(actorID, id string) error {
	return s.mutate(func(snap *types.Snapshot) error {
		for i := range snap.Grants {
			g := &snap.Grants[i]
			if g.ID != id {
				continue
			}
			if g.ResourceType == "service" {
				v := service(snap, g.ResourceID)
				if v == nil {
					return problem(types.KindNotFound, "service not found")
				}
				if err := canManageService(snap, actorID, v); err != nil {
					return err
				}
			} else if err := requireManager(snap, actorID, g.WorkspaceID); err != nil {
				return err
			}
			g.Revoked = true
			audit(snap, actorID, "grant.revoke", id)
			return nil
		}
		return problem(types.KindNotFound, "grant not found")
	})
}
func (s *State) RevokeAgent(actorID, id string) error {
	return s.mutate(func(snap *types.Snapshot) error {
		a := agent(snap, id)
		if a == nil {
			return problem(types.KindNotFound, "agent not found")
		}
		if !isUser(a.Principal, actorID) {
			if err := requireManager(snap, actorID, a.WorkspaceID); err != nil {
				return err
			}
		} else if _, err := requireUser(snap, actorID); err != nil {
			return err
		}
		revokeAgent(snap, a, actorID, "explicit")
		return nil
	})
}
func (s *State) RevokeEnrollmentKey(actorID, id string) error {
	return s.mutate(func(snap *types.Snapshot) error {
		for i := range snap.EnrollmentKeys {
			k := &snap.EnrollmentKeys[i]
			if k.ID != id {
				continue
			}
			if k.UserID != actorID {
				if err := requireManager(snap, actorID, k.WorkspaceID); err != nil {
					return err
				}
			} else if _, err := requireUser(snap, actorID); err != nil {
				return err
			}
			revokeEnrollmentKey(snap, k)
			audit(snap, actorID, "enrollment_key.revoke", id)
			return nil
		}
		return problem(types.KindNotFound, "enrollment key not found")
	})
}
func (s *State) SetServiceEnabled(actorID, id string, enabled bool) (types.Service, error) {
	var out types.Service
	err := s.mutate(func(snap *types.Snapshot) error {
		v := service(snap, id)
		if v == nil {
			return problem(types.KindNotFound, "service not found")
		}
		if err := canManageService(snap, actorID, v); err != nil {
			return err
		}
		v.Enabled = enabled
		out = *v
		audit(snap, actorID, "service.enabled", id)
		return nil
	})
	return out, err
}
func (s *State) SetEgressRouteEnabled(actorID, id string, enabled bool) (types.EgressRoute, error) {
	var out types.EgressRoute
	err := s.mutate(func(snap *types.Snapshot) error {
		v := route(snap, id)
		if v == nil {
			return problem(types.KindNotFound, "egress route not found")
		}
		if err := requireManager(snap, actorID, v.WorkspaceID); err != nil {
			return err
		}
		if enabled {
			for _, other := range snap.EgressRoutes {
				if other.ID != v.ID && other.Enabled && other.WorkspaceID == v.WorkspaceID && other.Priority == v.Priority && other.Rule.Domain == v.Rule.Domain {
					for _, port := range other.Rule.Ports {
						if slices.Contains(v.Rule.Ports, port) {
							return problem(types.KindConflict, "ambiguous egress route")
						}
					}
				}
			}
		}
		v.Enabled = enabled
		out = *v
		audit(snap, actorID, "egress_route.enabled", id)
		return nil
	})
	return out, err
}

// grantDeadline evaluates the scope of each grant now, not when it was made:
// an internal user grant only matches while its subject is still a member of
// the resource Workspace, so leaving cannot turn it into an external share.
// User, Service Account and Workspace-member grants never stand in for each
// other; a Workspace grant covers its human members only.
func grantDeadline(snap *types.Snapshot, resourceType, resourceID string, consumer api.Principal, now time.Time) time.Time {
	var until time.Time
	u := user(snap, consumer.ID)
	humanActive := consumer.Type == api.PrincipalUser && u != nil && !u.Disabled
	sa := serviceAccount(snap, consumer.ID)
	machineActive := consumer.Type == api.PrincipalServiceAccount && sa != nil && sa.Status == api.StatusActive
	for _, g := range snap.Grants {
		if g.ResourceType != resourceType || g.ResourceID != resourceID || g.Revoked || now.Before(g.StartsAt) || !now.Before(g.ExpiresAt) {
			continue
		}
		match := g.SubjectType == "anonymous" && resourceType == "service"
		if humanActive {
			userMatch := g.SubjectType == "user" && g.SubjectID == consumer.ID && (g.External || member(snap, g.WorkspaceID, consumer.ID) != nil)
			match = match || userMatch || g.SubjectType == "workspace" && member(snap, g.SubjectID, consumer.ID) != nil
		}
		if machineActive {
			match = match || g.SubjectType == "service_account" && g.SubjectID == consumer.ID && sa.WorkspaceID == g.WorkspaceID
		}
		if match && g.ExpiresAt.After(until) {
			until = g.ExpiresAt
		}
	}
	return until
}
func cappedDeadline(until time.Time) time.Time {
	limit := time.Now().Add(time.Minute)
	if until.Before(limit) {
		return until
	}
	return limit
}
func canonicalHost(host string) (string, error) {
	if strings.Contains(host, ":") {
		h, p, err := net.SplitHostPort(host)
		if err != nil {
			return "", problem(types.KindInvalid, "invalid request authority")
		}
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return "", problem(types.KindInvalid, "invalid request authority port")
		}
		host = h
	}
	d, err := policy.CanonicalDomain(host)
	if err != nil {
		return "", problem(types.KindInvalid, "invalid request domain")
	}
	return d, nil
}

func (s *State) ResolveServe(host, userID string) (types.Decision, error) {
	domain, err := canonicalHost(host)
	if err != nil {
		return types.Decision{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := &s.snapshot
	if userID != "" {
		if _, err := requireUser(snap, userID); err != nil {
			return types.Decision{}, err
		}
	}
	var found *types.Service
	for i := range snap.Services {
		if snap.Services[i].Hostname == domain {
			found = &snap.Services[i]
			break
		}
	}
	if found == nil {
		return types.Decision{}, problem(types.KindNotFound, "unknown service")
	}
	if !found.Enabled {
		return types.Decision{}, problem(types.KindUnavailable, "service disabled")
	}
	consumer := api.Principal{}
	if userID != "" {
		consumer = userPrincipal(userID)
	}
	until := grantDeadline(snap, "service", found.ID, consumer, time.Now())
	if until.IsZero() {
		return types.Decision{}, problem(types.KindForbidden, "service access is not granted")
	}
	a := agent(snap, found.AgentID)
	if a == nil || !agentAllowed(snap, a) {
		return types.Decision{}, problem(types.KindUnavailable, "service provider unavailable")
	}
	if _, ok := a.Effective.Service(found.Name); !ok {
		return types.Decision{}, problem(types.KindUnavailable, "service is not in the provider's effective configuration")
	}
	return types.Decision{ResourceType: "service", ResourceID: found.ID, WorkspaceID: found.WorkspaceID, Workspace: workspace(snap, found.WorkspaceID).Name, AgentID: a.ID, ServiceName: found.Name, Hostname: domain, Consumer: consumer, Revision: a.Effective.Revision, ExpiresAt: cappedDeadline(until)}, nil
}

// ResolveEgress decides for the ingress Agent's principal: a User must still be
// a member of the Workspace, a Service Account must belong to it.
func (s *State) ResolveEgress(workspaceSlug string, consumer api.Principal, host string, port int) (types.Decision, error) {
	domain, err := policy.CanonicalDomain(host)
	if err != nil || port < 1 || port > 65535 {
		return types.Decision{}, problem(types.KindInvalid, "invalid egress destination")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := &s.snapshot
	if consumer.Type == api.PrincipalUser {
		if _, err := requireUser(snap, consumer.ID); err != nil {
			return types.Decision{}, err
		}
	}
	w := workspace(snap, workspaceSlug)
	if w == nil {
		return types.Decision{}, problem(types.KindNotFound, "workspace not found")
	}
	if !principalActive(snap, w.ID, consumer) {
		return types.Decision{}, problem(types.KindForbidden, "egress requires a current member or a service account of this workspace")
	}
	var candidates []types.EgressRoute
	for _, r := range snap.EgressRoutes {
		if r.WorkspaceID == w.ID && r.Enabled && policy.Match(r.Rule, domain, port, "tls") {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return types.Decision{}, problem(types.KindForbidden, "no egress route matches")
	}
	slices.SortFunc(candidates, func(a, b types.EgressRoute) int {
		if a.Priority != b.Priority {
			if a.Priority < b.Priority {
				return -1
			}
			return 1
		}
		if (a.Rule.Domain.Exact != "") != (b.Rule.Domain.Exact != "") {
			if a.Rule.Domain.Exact != "" {
				return -1
			}
			return 1
		}
		return len(b.Rule.Domain.Suffix) - len(a.Rule.Domain.Suffix)
	})
	r := candidates[0]
	until := grantDeadline(snap, "egress", r.ID, consumer, time.Now())
	if until.IsZero() {
		return types.Decision{}, problem(types.KindForbidden, "selected egress route is not granted")
	}
	a := agent(snap, r.AgentID)
	if a == nil || !agentAllowed(snap, a) {
		return types.Decision{}, problem(types.KindUnavailable, "exit agent is inactive")
	}
	if !policy.Allowed(a.Effective.Egress, a.Effective.Deny, domain, port, "tls") {
		return types.Decision{}, problem(types.KindForbidden, "exit agent does not permit this destination")
	}
	return types.Decision{ResourceType: "egress", ResourceID: r.ID, WorkspaceID: w.ID, Workspace: w.Name, AgentID: a.ID, Consumer: consumer, Destination: connection.Destination{Host: domain, Port: uint16(port)}, Revision: a.Effective.Revision, ExpiresAt: cappedDeadline(until)}, nil
}
