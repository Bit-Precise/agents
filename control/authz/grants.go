package authz

import (
	"slices"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
)

// Resource types a Grant opens on the data plane.
const (
	ResourceService = "service" // a published Serve service, visited by browsers
	ResourceEgress  = "egress"  // an egress route, used by ingress devices
)

// Subject types a Grant can name.
const (
	// SubjectAnonymous is anyone, signed in or not.
	SubjectAnonymous = "anonymous"
	// SubjectAuthenticated is every signed-in, enabled user of the instance.
	SubjectAuthenticated = "authenticated"
	// SubjectUser is one person.
	SubjectUser = "user"
	// SubjectWorkspace is the people who are members of a Workspace, never its
	// Service Accounts.
	SubjectWorkspace = "workspace"
	// SubjectServiceAccount is one Service Account of the resource Workspace.
	SubjectServiceAccount = "service_account"
)

// SubjectTypes lists every subject type in display order.
var SubjectTypes = []string{SubjectAnonymous, SubjectAuthenticated, SubjectUser, SubjectWorkspace, SubjectServiceAccount}

// subjects lists what each resource type accepts. Browsers visit services, so
// they accept people and the public; a Service Account has no browser
// credential. Egress is used by ingress devices of the resource Workspace, so
// it accepts that Workspace's people and Service Accounts only.
var subjects = map[string][]string{
	ResourceService: {SubjectAnonymous, SubjectAuthenticated, SubjectUser, SubjectWorkspace},
	ResourceEgress:  {SubjectUser, SubjectWorkspace, SubjectServiceAccount},
}

// Accepts reports whether a grant on resourceType may name subjectType.
func Accepts(resourceType, subjectType string) bool {
	return slices.Contains(subjects[resourceType], subjectType)
}

// NamesSubject reports whether the subject type carries a subject ID.
func NamesSubject(subjectType string) bool {
	return subjectType != SubjectAnonymous && subjectType != SubjectAuthenticated
}

// External reports whether a grant reaches beyond its resource Workspace:
// anonymous and authenticated grants always do, as do a person who is not a
// member and another Workspace. Egress is never external, and creating an
// external service grant needs ShareExternally.
func External(snap *types.Snapshot, g types.Grant) bool {
	switch g.SubjectType {
	case SubjectAnonymous, SubjectAuthenticated:
		return true
	case SubjectUser:
		return membership(snap, g.WorkspaceID, g.SubjectID) == nil
	case SubjectWorkspace:
		return g.SubjectID != g.WorkspaceID
	}
	return false
}

// Deadline returns when the latest active grant that lets consumer use the
// resource ends, or zero if none does. consumer is the browser User for a
// service (the zero Principal when anonymous) and the ingress device's
// principal for egress; it is never replaced by the provider or an operator.
func Deadline(snap *types.Snapshot, resourceType, resourceID string, consumer api.Principal, now time.Time) time.Time {
	var until time.Time
	for _, g := range snap.Grants {
		if g.ResourceType != resourceType || g.ResourceID != resourceID || g.Revoked || now.Before(g.StartsAt) || !now.Before(g.ExpiresAt) {
			continue
		}
		if covers(snap, g, consumer) && g.ExpiresAt.After(until) {
			until = g.ExpiresAt
		}
	}
	return until
}

// covers evaluates a grant's subject now, not when it was made: an internal
// user grant matches only while its subject is still a member, so leaving the
// Workspace cannot turn it into an external share. No subject type stands in
// for another.
func covers(snap *types.Snapshot, g types.Grant, c api.Principal) bool {
	if !Accepts(g.ResourceType, g.SubjectType) {
		return false
	}
	person := c.Type == api.PrincipalUser && activeUser(snap, c.ID)
	switch g.SubjectType {
	case SubjectAnonymous:
		return true
	case SubjectAuthenticated:
		return person
	case SubjectUser:
		return person && g.SubjectID == c.ID && (g.External || membership(snap, g.WorkspaceID, c.ID) != nil)
	case SubjectWorkspace:
		return person && membership(snap, g.SubjectID, c.ID) != nil
	case SubjectServiceAccount:
		sa := serviceAccount(snap, c.ID)
		return c.Type == api.PrincipalServiceAccount && g.SubjectID == c.ID && sa != nil && sa.Status == api.StatusActive && sa.WorkspaceID == g.WorkspaceID
	}
	return false
}

// DefaultGrantCreator marks the grant of the Workspace default policy.
const DefaultGrantCreator = "policy:default-member-access"

// DefaultServiceGrant is the default data-plane policy for a newly published
// service: the members of its Workspace may use it for ttl. It is created once;
// changing it afterwards is ordinary grant management. The caller assigns ID.
func DefaultServiceGrant(workspaceID, serviceID string, now time.Time, ttl time.Duration) types.Grant {
	return types.Grant{WorkspaceID: workspaceID, ResourceType: ResourceService, ResourceID: serviceID, SubjectType: SubjectWorkspace, SubjectID: workspaceID,
		StartsAt: now, ExpiresAt: now.Add(ttl), CreatedBy: DefaultGrantCreator}
}
