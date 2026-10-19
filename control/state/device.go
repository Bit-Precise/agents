package state

import (
	"slices"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/authz"
	"github.com/bit-precise/agents/control/types"
)

const (
	AuthorizationAwaitingUser = "awaiting_user"
	AuthorizationAuthorized   = "authorized"
	AuthorizationConsumed     = "consumed"
	AuthorizationRevoked      = "revoked"
	AuthorizationExpired      = "expired"
	// AuthorizationTTL bounds the browser confirmation and the token exchange.
	AuthorizationTTL = 10 * time.Minute
	// authorizationRetention keeps finished records for audit display, then
	// prunes them; the audit log keeps the permanent trail.
	authorizationRetention = 24 * time.Hour
)

func authorization(snap *types.Snapshot, id string) *types.EnrollmentAuthorization {
	for i := range snap.EnrollmentAuthorizations {
		if snap.EnrollmentAuthorizations[i].ID == id {
			return &snap.EnrollmentAuthorizations[i]
		}
	}
	return nil
}

// authorizationStatus derives expiry; stored records never pass back to a
// usable state.
func authorizationStatus(a *types.EnrollmentAuthorization) string {
	if (a.Status == AuthorizationAwaitingUser || a.Status == AuthorizationAuthorized) && !a.ExpiresAt.After(time.Now()) {
		return AuthorizationExpired
	}
	return a.Status
}

// ownAuthorization returns the actor's own record in a usable state.
func ownAuthorization(snap *types.Snapshot, actorID, id, want string) (*types.EnrollmentAuthorization, error) {
	a := authorization(snap, id)
	if a == nil || a.ActorUserID != actorID {
		return nil, problem(types.KindNotFound, "device authorization not found")
	}
	if status := authorizationStatus(a); status != want {
		return nil, problem(types.KindConflict, "device authorization is "+status)
	}
	return a, nil
}

// checkSelection is the permission check for a Device Flow choice, repeated
// at confirmation, consent and exchange so a demotion in between fails closed.
// A person may choose only themselves, and only in a Workspace they belong
// to; a Service Account requires authz.ManageServiceAccounts there.
func checkSelection(snap *types.Snapshot, actorID, workspaceID string, p api.Principal) error {
	if _, err := requireUser(snap, actorID); err != nil {
		return err
	}
	w := workspace(snap, workspaceID)
	if w == nil || w.ID != workspaceID {
		return problem(types.KindNotFound, "workspace not found")
	}
	switch p.Type {
	case api.PrincipalUser:
		if p.ID != actorID {
			return problem(types.KindForbidden, "a device can run as yourself or as a service account, never as another user")
		}
		if authz.MemberRole(snap, actorID, w.ID) == authz.None {
			return problem(types.KindForbidden, "workspace membership required")
		}
	case api.PrincipalServiceAccount:
		sa := serviceAccount(snap, p.ID)
		if sa == nil || sa.WorkspaceID != w.ID || sa.Status != api.StatusActive {
			return problem(types.KindNotFound, "service account not found in this workspace")
		}
		if !canChooseServiceAccount(snap, actorID, sa) {
			return authz.Denied(authz.ManageServiceAccounts)
		}
	default:
		return problem(types.KindInvalid, "principal must be a user or a service account")
	}
	return nil
}

// BeginDeviceAuthorization records a Hydra login challenge for a person who has
// already signed in. Nothing is selected yet. Repeating it for the same
// challenge and person returns the same record.
func (s *State) BeginDeviceAuthorization(actorID, clientID, loginChallenge string) (types.EnrollmentAuthorization, error) {
	var out types.EnrollmentAuthorization
	// Hydra's challenges are opaque and long (about 2 KiB for v26).
	if loginChallenge == "" || len(loginChallenge) > 4096 {
		return out, problem(types.KindInvalid, "invalid login challenge")
	}
	err := s.mutate(func(snap *types.Snapshot) error {
		if _, err := requireUser(snap, actorID); err != nil {
			return err
		}
		now := time.Now()
		snap.EnrollmentAuthorizations = slices.DeleteFunc(snap.EnrollmentAuthorizations, func(a types.EnrollmentAuthorization) bool {
			return now.After(a.ExpiresAt.Add(authorizationRetention))
		})
		for _, a := range snap.EnrollmentAuthorizations {
			if a.LoginChallenge == loginChallenge {
				if a.ActorUserID != actorID || authorizationStatus(&a) != AuthorizationAwaitingUser {
					return problem(types.KindConflict, "this login request belongs to another browser session; start agentd login again")
				}
				out = a
				return nil
			}
		}
		out = types.EnrollmentAuthorization{ID: randomID(), Status: AuthorizationAwaitingUser, ClientID: clientID, LoginChallenge: loginChallenge, ActorUserID: actorID, CreatedAt: now.UTC(), ExpiresAt: now.Add(AuthorizationTTL).UTC()}
		snap.EnrollmentAuthorizations = append(snap.EnrollmentAuthorizations, out)
		audit(snap, actorID, "device_authorization.begin", out.ID)
		return nil
	})
	return out, err
}

// DeviceChoice is one Workspace the person may register a device in.
type DeviceChoice struct {
	Workspace       types.Workspace        `json:"workspace"`
	Role            string                 `json:"role,omitempty"`
	Self            bool                   `json:"self"`
	SelfRoles       []string               `json:"selfRoles,omitempty"`
	ServiceAccounts []types.ServiceAccount `json:"serviceAccounts"`
}

// DeviceAuthorizationView is what the confirmation page shows.
type DeviceAuthorizationView struct {
	Authorization types.EnrollmentAuthorization `json:"authorization"`
	Choices       []DeviceChoice                `json:"choices"`
}

// DeviceAuthorization lists the selections the actor may make right now.
// The personal Workspace comes first; Service Accounts appear only where the
// actor holds authz.ManageServiceAccounts (an instance admin, in any Workspace).
func (s *State) DeviceAuthorization(actorID, id string) (DeviceAuthorizationView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := &s.snapshot
	a := authorization(snap, id)
	if a == nil || a.ActorUserID != actorID {
		return DeviceAuthorizationView{}, problem(types.KindNotFound, "device authorization not found")
	}
	view := DeviceAuthorizationView{Authorization: *a}
	view.Authorization.Status = authorizationStatus(a)
	view.Authorization.LoginChallenge = ""
	actor, err := requireUser(snap, actorID)
	if err != nil {
		return view, err
	}
	for _, w := range snap.Workspaces {
		m := member(snap, w.ID, actorID)
		if m == nil && !actor.Admin {
			continue
		}
		choice := DeviceChoice{Workspace: w, ServiceAccounts: []types.ServiceAccount{}}
		if m != nil {
			choice.Role, choice.Self, choice.SelfRoles = m.Role, true, allowedRoles(snap, w.ID, userPrincipal(actorID))
		}
		for i := range snap.ServiceAccounts {
			if sa := &snap.ServiceAccounts[i]; sa.WorkspaceID == w.ID && canChooseServiceAccount(snap, actorID, sa) {
				choice.ServiceAccounts = append(choice.ServiceAccounts, *sa)
			}
		}
		if choice.Self || len(choice.ServiceAccounts) > 0 {
			view.Choices = append(view.Choices, choice)
		}
	}
	personal := "personal-" + actorID
	slices.SortStableFunc(view.Choices, func(x, y DeviceChoice) int {
		switch {
		case x.Workspace.Name == personal && y.Workspace.Name != personal:
			return -1
		case y.Workspace.Name == personal && x.Workspace.Name != personal:
			return 1
		}
		return 0
	})
	return view, nil
}

// AuthorizeDevice fixes the person's explicit choice. The record is immutable
// afterwards: Hydra receives its typed subject and consent must match it.
// Repeating the same choice (a retry after Hydra was unreachable) rechecks
// permission and changes nothing; a different choice is a conflict.
func (s *State) AuthorizeDevice(actorID, id, workspaceID string, p api.Principal) (types.EnrollmentAuthorization, error) {
	var out types.EnrollmentAuthorization
	err := s.mutate(func(snap *types.Snapshot) error {
		if a := authorization(snap, id); a != nil && a.ActorUserID == actorID && authorizationStatus(a) == AuthorizationAuthorized && a.WorkspaceID == workspaceID && a.Principal == p {
			out = *a
			return checkSelection(snap, actorID, workspaceID, p)
		}
		a, err := ownAuthorization(snap, actorID, id, AuthorizationAwaitingUser)
		if err != nil {
			return err
		}
		if err := checkSelection(snap, actorID, workspaceID, p); err != nil {
			return err
		}
		a.Status, a.WorkspaceID, a.Principal, a.AllowedRoles, a.AuthorizedAt = AuthorizationAuthorized, workspaceID, p, allowedRoles(snap, workspaceID, p), time.Now().UTC()
		out = *a
		auditDetail(snap, actorID, "device_authorization.authorize", a.ID, p.Subject())
		return nil
	})
	return out, err
}

// CheckDeviceConsent verifies a Hydra consent request against the confirmed
// record and the actor's current permission. It changes nothing. Hydra names
// the login by an internal ID at consent, so the record is found through the
// authorization ID Control put in the login context.
func (s *State) CheckDeviceConsent(actorID, id, subject string) (types.EnrollmentAuthorization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := &s.snapshot
	a, err := ownAuthorization(snap, actorID, id, AuthorizationAuthorized)
	if err != nil {
		return types.EnrollmentAuthorization{}, err
	}
	if a.Principal.Subject() != subject {
		return types.EnrollmentAuthorization{}, problem(types.KindForbidden, "consent does not match the confirmed device authorization")
	}
	if err := checkSelection(snap, actorID, a.WorkspaceID, a.Principal); err != nil {
		return types.EnrollmentAuthorization{}, err
	}
	return *a, nil
}

// RevokeDeviceAuthorization ends an unfinished authorization the person
// declined or abandoned. A consumed one is unaffected: revoke its Agent.
func (s *State) RevokeDeviceAuthorization(actorID, id string) error {
	return s.mutate(func(snap *types.Snapshot) error {
		a := authorization(snap, id)
		if a == nil || a.ActorUserID != actorID {
			return problem(types.KindNotFound, "device authorization not found")
		}
		if status := authorizationStatus(a); status != AuthorizationAwaitingUser && status != AuthorizationAuthorized {
			return nil
		}
		a.Status = AuthorizationRevoked
		audit(snap, actorID, "device_authorization.revoke", a.ID)
		return nil
	})
}

// ExchangeDeviceEnrollment spends a confirmed authorization on exactly one new
// Agent. The caller has already verified the access token with Hydra; the
// permission check is repeated inside the same transaction that consumes the
// record and creates the device, so a concurrent exchange or a demotion since
// consent cannot register a device.
func (s *State) ExchangeDeviceEnrollment(id string, subject api.Principal, request api.AgentEnrollment) (types.EnrollResponse, error) {
	var response types.EnrollResponse
	r, err := newRegistration(request.Name, request.Desired)
	if err != nil {
		return response, err
	}
	err = s.mutate(func(snap *types.Snapshot) error {
		a := authorization(snap, id)
		if a == nil {
			return problem(types.KindUnauthorized, "device authorization not found")
		}
		switch authorizationStatus(a) {
		case AuthorizationAuthorized:
		case AuthorizationConsumed:
			return problem(types.KindConflict, "device authorization was already used; start agentd login again")
		default:
			return problem(types.KindUnauthorized, "device authorization is "+authorizationStatus(a))
		}
		if a.Principal != subject {
			return problem(types.KindUnauthorized, "access token subject does not match the device authorization")
		}
		if err := checkSelection(snap, a.ActorUserID, a.WorkspaceID, a.Principal); err != nil {
			return err
		}
		r.workspace, r.principal, r.roles, r.createdBy = workspace(snap, a.WorkspaceID), a.Principal, a.AllowedRoles, a.ActorUserID
		if request.Workspace != r.workspace.Name {
			return problem(types.KindForbidden, "config.yaml workspace "+request.Workspace+" does not match the authorized workspace "+r.workspace.Name)
		}
		if response, err = s.register(snap, r); err != nil {
			return err
		}
		a = authorization(snap, id)
		a.Status, a.ConsumedAt, a.AgentID = AuthorizationConsumed, time.Now().UTC(), response.Agent.ID
		auditDetail(snap, a.ActorUserID, "agent.enroll", response.Agent.ID, "device authorization "+a.ID+" as "+a.Principal.Subject())
		return nil
	})
	return response, err
}
