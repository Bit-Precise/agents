package state

import (
	"strings"
	"time"
	"unicode"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/authz"
	"github.com/bit-precise/agents/control/types"
)

func userPrincipal(id string) api.Principal { return api.Principal{Type: api.PrincipalUser, ID: id} }
func servicePrincipal(id string) api.Principal {
	return api.Principal{Type: api.PrincipalServiceAccount, ID: id}
}

// isUser reports whether p is exactly the given User; a Service Account
// created or operated by that User never is.
func isUser(p api.Principal, userID string) bool {
	return p.Type == api.PrincipalUser && p.ID == userID
}

func serviceAccount(snap *types.Snapshot, id string) *types.ServiceAccount {
	for i := range snap.ServiceAccounts {
		if snap.ServiceAccounts[i].ID == id {
			return &snap.ServiceAccounts[i]
		}
	}
	return nil
}

// principalActive reports whether p may run Agents in workspaceID now: an
// enabled User with a Membership there, or an active Service Account of
// exactly that Workspace.
func principalActive(snap *types.Snapshot, workspaceID string, p api.Principal) bool {
	switch p.Type {
	case api.PrincipalUser:
		u := user(snap, p.ID)
		return u != nil && !u.Disabled && member(snap, workspaceID, p.ID) != nil
	case api.PrincipalServiceAccount:
		sa := serviceAccount(snap, p.ID)
		return sa != nil && sa.Status == api.StatusActive && sa.WorkspaceID == workspaceID
	}
	return false
}

func validDescription(s string) bool {
	return len(s) <= 256 && strings.IndexFunc(s, unicode.IsControl) < 0
}

// CreateServiceAccount adds a machine principal to one Workspace. Managing
// Service Accounts needs authz.ManageServiceAccounts.
func (s *State) CreateServiceAccount(actorID, workspaceID, name, description string) (types.ServiceAccount, error) {
	var out types.ServiceAccount
	name, err := label(name)
	if err != nil {
		return out, err
	}
	if !validDescription(description) {
		return out, problem(types.KindInvalid, "description must be at most 256 printable characters")
	}
	err = s.mutate(func(snap *types.Snapshot) error {
		w := workspace(snap, workspaceID)
		if w == nil {
			return problem(types.KindNotFound, "workspace not found")
		}
		if err := authz.Require(snap, actorID, w.ID, authz.ManageServiceAccounts); err != nil {
			return err
		}
		for _, v := range snap.ServiceAccounts {
			if v.WorkspaceID == w.ID && v.Name == name {
				return problem(types.KindConflict, "service account name already exists")
			}
		}
		out = types.ServiceAccount{ID: randomID(), WorkspaceID: w.ID, Name: name, Description: description, Status: api.StatusActive, CreatedBy: actorID, CreatedAt: time.Now().UTC()}
		snap.ServiceAccounts = append(snap.ServiceAccounts, out)
		audit(snap, actorID, "service_account.create", out.ID)
		return nil
	})
	return out, err
}

// UpdateServiceAccount changes only the description; the Workspace and name
// are fixed.
func (s *State) UpdateServiceAccount(actorID, id, description string) (types.ServiceAccount, error) {
	var out types.ServiceAccount
	if !validDescription(description) {
		return out, problem(types.KindInvalid, "description must be at most 256 printable characters")
	}
	err := s.mutate(func(snap *types.Snapshot) error {
		sa := serviceAccount(snap, id)
		if sa == nil {
			return problem(types.KindNotFound, "service account not found")
		}
		if err := authz.Require(snap, actorID, sa.WorkspaceID, authz.ManageServiceAccounts); err != nil {
			return err
		}
		if sa.Status != api.StatusActive {
			return problem(types.KindConflict, "service account is revoked")
		}
		sa.Description = description
		out = *sa
		audit(snap, actorID, "service_account.update", sa.ID)
		return nil
	})
	return out, err
}

// RevokeServiceAccount is terminal. Every Agent running as it, its unused
// enrollment keys and its credentials stop at once; its grants stop matching.
// The people who created or operated it are not affected, and vice versa.
func (s *State) RevokeServiceAccount(actorID, id string) error {
	return s.mutate(func(snap *types.Snapshot) error {
		sa := serviceAccount(snap, id)
		if sa == nil {
			return problem(types.KindNotFound, "service account not found")
		}
		if err := authz.Require(snap, actorID, sa.WorkspaceID, authz.ManageServiceAccounts); err != nil {
			return err
		}
		if sa.Status == api.StatusRevoked {
			return nil
		}
		sa.Status, sa.RevokedAt = api.StatusRevoked, time.Now().UTC()
		revokePrincipal(snap, actorID, servicePrincipal(sa.ID), "", "service_account.revoke")
		audit(snap, actorID, "service_account.revoke", sa.ID)
		return nil
	})
}

// canChooseServiceAccount reports whether the actor may register a device as
// this Service Account right now.
func canChooseServiceAccount(snap *types.Snapshot, actorID string, sa *types.ServiceAccount) bool {
	return sa != nil && sa.Status == api.StatusActive && authz.Require(snap, actorID, sa.WorkspaceID, authz.ManageServiceAccounts) == nil
}

// allowedRoles is the role ceiling a Device Flow selection grants.
func allowedRoles(snap *types.Snapshot, workspaceID string, p api.Principal) []string {
	return authz.DeviceRoles(p, authz.MemberRole(snap, p.ID, workspaceID), authz.AllDeviceRoles)
}
