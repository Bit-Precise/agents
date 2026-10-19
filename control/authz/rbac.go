// Package authz is the authorization model of agents. It has two layers that
// answer different questions and do not read each other's inputs.
//
// The control plane (this file) is role-based: a person's Membership role in a
// Workspace, or instance administration, decides which management operations
// they may perform there. The data plane (grants.go) is grant-based: a Grant
// lets one subject use one published resource, a service or an egress route,
// for a bounded window. Data-plane decisions read grants, Workspace membership
// and a device's effective configuration; they never read a Workspace role.
//
// The layers meet in two places, both on the control-plane side: creating and
// revoking grants are management operations, and DeviceRoles bounds what a
// device may provide, which Control materializes into its effective
// configuration before the data plane reads it.
//
// docs/authorization.md presents both layers; tests keep its tables in sync
// with this package.
package authz

import (
	"slices"
	"strings"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
)

// Role is what a person may manage in one Workspace.
type Role string

const (
	Owner      Role = "owner"
	Maintainer Role = "maintainer"
	Developer  Role = "developer"
	Guest      Role = "guest"
	// Admin is the instance administrator, who holds every permission in every
	// Workspace. It is never stored in a Membership.
	Admin Role = "admin"
	// None is a person without a Membership; it holds no permission.
	None Role = ""
)

// MemberRoles are the roles a Membership can hold, most privileged first.
var MemberRoles = []Role{Owner, Maintainer, Developer, Guest}

func ValidMemberRole(s string) bool { return slices.Contains(MemberRoles, Role(s)) }

// Permission names a group of control-plane operations in one Workspace.
type Permission string

const (
	// ViewWorkspace: see the Workspace, its members, services and egress
	// routes; see and revoke one's own devices and flows.
	ViewWorkspace Permission = "workspace.view"
	// ManageMembers: add, change and remove developer and guest members.
	ManageMembers Permission = "members.manage"
	// ManagePrivilegedMembers: any membership change that involves the owner or
	// maintainer role.
	ManagePrivilegedMembers Permission = "members.manage_privileged"
	// ManageServiceAccounts: see and manage Service Accounts and their
	// enrollment keys, and register devices as a Service Account.
	ManageServiceAccounts Permission = "service_accounts.manage"
	// ManageAgents: see and revoke every device of the Workspace, with its
	// connections and flows.
	ManageAgents Permission = "agents.manage"
	// ProvideFromOwnDevices: run serve and egress on one's own devices; without
	// it they run ingress only.
	ProvideFromOwnDevices Permission = "agents.provide"
	// ManageServices: enable and disable every service and manage its grants.
	ManageServices Permission = "services.manage"
	// ManageOwnServices: the same for services published by one's own devices.
	ManageOwnServices Permission = "services.manage_own"
	// ManageEgressRoutes: create, enable and disable egress routes and manage
	// their grants.
	ManageEgressRoutes Permission = "egress_routes.manage"
	// ShareExternally: grant a service beyond its Workspace, to anonymous
	// visitors, every signed-in user, another Workspace or a non-member.
	ShareExternally Permission = "grants.share_external"
)

// AllPermissions lists every permission in display order.
var AllPermissions = []Permission{ViewWorkspace, ManageMembers, ManagePrivilegedMembers, ManageServiceAccounts, ManageAgents, ProvideFromOwnDevices, ManageServices, ManageOwnServices, ManageEgressRoutes, ShareExternally}

// matrix is the control-plane RBAC: the permissions of each Membership role.
var matrix = map[Role][]Permission{
	Owner:      AllPermissions,
	Maintainer: {ViewWorkspace, ManageMembers, ManageServiceAccounts, ManageAgents, ProvideFromOwnDevices, ManageServices, ManageOwnServices, ManageEgressRoutes, ShareExternally},
	Developer:  {ViewWorkspace, ProvideFromOwnDevices, ManageOwnServices},
	Guest:      {ViewWorkspace},
}

func (r Role) Can(p Permission) bool {
	return r == Admin || slices.Contains(matrix[r], p)
}

// Permissions lists what the role holds, in display order.
func (r Role) Permissions() []Permission {
	out := []Permission{}
	for _, p := range AllPermissions {
		if r.Can(p) {
			out = append(out, p)
		}
	}
	return out
}

// MemberRole is the person's Membership role in the Workspace, or None.
func MemberRole(snap *types.Snapshot, userID, workspaceID string) Role {
	if m := membership(snap, workspaceID, userID); m != nil {
		return Role(m.Role)
	}
	return None
}

// RoleOf is the role an enabled person acts with in the Workspace: Admin for
// an instance administrator, otherwise their Membership role.
func RoleOf(snap *types.Snapshot, userID, workspaceID string) Role {
	u := user(snap, userID)
	switch {
	case u == nil || u.Disabled:
		return None
	case u.Admin:
		return Admin
	}
	return MemberRole(snap, userID, workspaceID)
}

// Require checks that an enabled person holds p in the Workspace.
func Require(snap *types.Snapshot, actorID, workspaceID string, p Permission) error {
	if !activeUser(snap, actorID) {
		return unauthenticated()
	}
	if !RoleOf(snap, actorID, workspaceID).Can(p) {
		return Denied(p)
	}
	return nil
}

// RequireAdmin checks that an enabled person is an instance administrator.
func RequireAdmin(snap *types.Snapshot, actorID string) error {
	u := user(snap, actorID)
	if u == nil || u.Disabled {
		return unauthenticated()
	}
	if !u.Admin {
		return &types.Error{Kind: types.KindForbidden, Message: "instance administrator required"}
	}
	return nil
}

// Denied is the error for a missing permission; it names the roles that hold it.
func Denied(p Permission) error {
	var holders []string
	for _, r := range MemberRoles {
		if r.Can(p) {
			holders = append(holders, string(r))
		}
	}
	return &types.Error{Kind: types.KindForbidden, Message: "permission " + string(p) + " required (" + strings.Join(holders, ", ") + ")"}
}

func unauthenticated() error {
	return &types.Error{Kind: types.KindUnauthorized, Message: "user is missing or disabled"}
}

// MembershipChange is the permission needed to move a person from one role to
// another; None stands for no Membership, as when adding or removing.
func MembershipChange(from, to Role) Permission {
	if from == Owner || from == Maintainer || to == Owner || to == Maintainer {
		return ManagePrivilegedMembers
	}
	return ManageMembers
}

// OwnDevice reports whether the Agent runs as exactly this person. A Service
// Account the person created or operates is never their own device, and
// CreatedBy never takes part in authorization.
func OwnDevice(a *types.Agent, userID string) bool {
	return a != nil && a.Principal.Type == api.PrincipalUser && a.Principal.ID == userID
}

// CanManageAgent covers seeing and revoking a device: one's own, or any with
// ManageAgents.
func CanManageAgent(role Role, a *types.Agent, userID string) bool {
	return role.Can(ManageAgents) || OwnDevice(a, userID)
}

// CanManageService covers enabling a service and managing its grants. provider
// is the Agent that publishes it.
func CanManageService(role Role, provider *types.Agent, userID string) bool {
	return role.Can(ManageServices) || role.Can(ManageOwnServices) && OwnDevice(provider, userID)
}

// CanManageGrant covers seeing and revoking a grant; creating one also passes
// the subject rules in grants.go. provider is the Agent of a service resource.
func CanManageGrant(role Role, resourceType string, provider *types.Agent, userID string) bool {
	if resourceType == ResourceService {
		return CanManageService(role, provider, userID)
	}
	return role.Can(ManageEgressRoutes)
}

// CanManageEnrollmentKey covers seeing and revoking a key. Keys bind Service
// Accounts; a legacy key that binds a person stays revocable by that person.
func CanManageEnrollmentKey(role Role, k *types.EnrollmentKey, userID string) bool {
	return role.Can(ManageServiceAccounts) || k.UserID != "" && k.UserID == userID
}

// AllDeviceRoles are the roles a device can run.
var AllDeviceRoles = []string{api.RoleIngress, api.RoleServe, api.RoleEgress}

// DeviceRoles is the ceiling on the roles a device of principal p runs, given
// the roles its registration allows. For a person's own device, role is their
// Membership role: without ProvideFromOwnDevices the device runs ingress only,
// and without a Membership nothing. A Service Account has no Workspace role;
// its manager chose its ceiling at registration. Callers check separately that
// the principal is still active.
func DeviceRoles(p api.Principal, role Role, registered []string) []string {
	switch {
	case p.Type == api.PrincipalServiceAccount || role.Can(ProvideFromOwnDevices):
		return slices.Clone(registered)
	case role != None && slices.Contains(registered, api.RoleIngress):
		return []string{api.RoleIngress}
	}
	return nil
}

func user(snap *types.Snapshot, id string) *types.User {
	for i := range snap.Users {
		if snap.Users[i].ID == id {
			return &snap.Users[i]
		}
	}
	return nil
}
func activeUser(snap *types.Snapshot, id string) bool {
	u := user(snap, id)
	return u != nil && !u.Disabled
}
func membership(snap *types.Snapshot, workspaceID, userID string) *types.Membership {
	for i := range snap.Memberships {
		if m := &snap.Memberships[i]; m.WorkspaceID == workspaceID && m.UserID == userID {
			return m
		}
	}
	return nil
}
func serviceAccount(snap *types.Snapshot, id string) *types.ServiceAccount {
	for i := range snap.ServiceAccounts {
		if snap.ServiceAccounts[i].ID == id {
			return &snap.ServiceAccounts[i]
		}
	}
	return nil
}
