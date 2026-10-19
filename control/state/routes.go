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
	"github.com/bit-precise/agents/control/authz"
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

// requireServiceManager checks authz.CanManageService for the actor.
func requireServiceManager(snap *types.Snapshot, actorID string, v *types.Service) error {
	if _, err := requireUser(snap, actorID); err != nil {
		return err
	}
	if !authz.CanManageService(authz.RoleOf(snap, actorID, v.WorkspaceID), agent(snap, v.AgentID), actorID) {
		return problem(types.KindForbidden, "permission services.manage, or services.manage_own for a service of your own device, required")
	}
	return nil
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
		if err := authz.Require(snap, actorID, w.ID, authz.ManageEgressRoutes); err != nil {
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

// CreateGrant is the control-plane operation that adds a data-plane grant: the
// actor must manage the resource, the subject must suit it (authz.Accepts),
// egress stays inside its Workspace and external sharing needs
// authz.ShareExternally.
func (s *State) CreateGrant(actorID string, value types.Grant) (types.Grant, error) {
	err := s.mutate(func(snap *types.Snapshot) error {
		w := workspace(snap, value.WorkspaceID)
		if w == nil {
			return problem(types.KindNotFound, "workspace not found")
		}
		value.WorkspaceID = w.ID
		if _, err := requireUser(snap, actorID); err != nil {
			return err
		}
		role := authz.RoleOf(snap, actorID, w.ID)
		switch value.ResourceType {
		case authz.ResourceService:
			v := service(snap, value.ResourceID)
			if v == nil || v.WorkspaceID != w.ID {
				return problem(types.KindForbidden, "service does not belong to this workspace")
			}
			if err := requireServiceManager(snap, actorID, v); err != nil {
				return err
			}
		case authz.ResourceEgress:
			v := route(snap, value.ResourceID)
			if v == nil || v.WorkspaceID != w.ID {
				return problem(types.KindForbidden, "egress route does not belong to this workspace")
			}
			if !role.Can(authz.ManageEgressRoutes) {
				return authz.Denied(authz.ManageEgressRoutes)
			}
		default:
			return problem(types.KindInvalid, "resourceType must be service or egress")
		}
		if !slices.Contains(authz.SubjectTypes, value.SubjectType) {
			return problem(types.KindInvalid, "invalid grant subject type")
		}
		if !authz.Accepts(value.ResourceType, value.SubjectType) {
			return problem(types.KindInvalid, value.SubjectType+" grants cannot open a "+value.ResourceType+" resource")
		}
		if !authz.NamesSubject(value.SubjectType) && value.SubjectID != "" {
			return problem(types.KindInvalid, value.SubjectType+" grants take no subject ID")
		}
		switch value.SubjectType {
		case authz.SubjectUser:
			if _, err := requireTargetUser(snap, value.SubjectID); err != nil {
				return err
			}
		case authz.SubjectWorkspace:
			subject := workspace(snap, value.SubjectID)
			if subject == nil {
				return problem(types.KindNotFound, "subject workspace not found")
			}
			value.SubjectID = subject.ID
		case authz.SubjectServiceAccount:
			sa := serviceAccount(snap, value.SubjectID)
			if sa == nil || sa.WorkspaceID != w.ID || sa.Status != api.StatusActive {
				return problem(types.KindNotFound, "service account not found in this workspace")
			}
		}
		external := authz.External(snap, value)
		if value.ResourceType == authz.ResourceEgress && external {
			return problem(types.KindForbidden, "egress cannot be shared outside its workspace")
		}
		if external && !role.Can(authz.ShareExternally) {
			return authz.Denied(authz.ShareExternally)
		}
		now := time.Now()
		if value.StartsAt.IsZero() {
			value.StartsAt = now.UTC()
		}
		if value.ExpiresAt.IsZero() || !value.StartsAt.Before(value.ExpiresAt) || !value.ExpiresAt.After(now) || value.ExpiresAt.Sub(value.StartsAt) > TokenTTL {
			return problem(types.KindInvalid, "grant needs an ordered, finite window of at most 30 days")
		}
		value.ID = randomID()
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
			if g.ResourceType == authz.ResourceService {
				v := service(snap, g.ResourceID)
				if v == nil {
					return problem(types.KindNotFound, "service not found")
				}
				if err := requireServiceManager(snap, actorID, v); err != nil {
					return err
				}
			} else if err := authz.Require(snap, actorID, g.WorkspaceID, authz.ManageEgressRoutes); err != nil {
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
		if _, err := requireUser(snap, actorID); err != nil {
			return err
		}
		if !authz.CanManageAgent(authz.RoleOf(snap, actorID, a.WorkspaceID), a, actorID) {
			return authz.Denied(authz.ManageAgents)
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
			if _, err := requireUser(snap, actorID); err != nil {
				return err
			}
			if !authz.CanManageEnrollmentKey(authz.RoleOf(snap, actorID, k.WorkspaceID), k, actorID) {
				return authz.Denied(authz.ManageServiceAccounts)
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
		if err := requireServiceManager(snap, actorID, v); err != nil {
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
		if err := authz.Require(snap, actorID, v.WorkspaceID, authz.ManageEgressRoutes); err != nil {
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
	until := authz.Deadline(snap, authz.ResourceService, found.ID, consumer, time.Now())
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
	until := authz.Deadline(snap, authz.ResourceEgress, r.ID, consumer, time.Now())
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
