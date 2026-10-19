package authz

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
)

// documentedTable returns the cells of the rows between the named markers in
// docs/authorization.md, which presents this package to readers.
func documentedTable(t *testing.T, name string) [][]string {
	t.Helper()
	data, err := os.ReadFile("../../docs/authorization.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	start, end := strings.Index(doc, "<!-- "+name+" -->"), strings.Index(doc, "<!-- /"+name+" -->")
	if start < 0 || end < start {
		t.Fatalf("docs/authorization.md has no %s table", name)
	}
	var rows [][]string
	for _, line := range strings.Split(doc[start:end], "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		cells[0] = strings.Trim(cells[0], "`")
		rows = append(rows, cells)
	}
	return rows
}

func TestDocumentedRBACMatrixMatchesCode(t *testing.T) {
	var documented []Permission
	for _, cells := range documentedTable(t, "rbac-matrix") {
		p := Permission(cells[0])
		documented = append(documented, p)
		for i, role := range MemberRoles {
			if want := cells[2+i] == "✓"; role.Can(p) != want {
				t.Errorf("%s holds %s: code %v, docs %v", role, p, role.Can(p), want)
			}
		}
	}
	if !slices.Equal(documented, AllPermissions) {
		t.Errorf("documented permissions %v, code %v", documented, AllPermissions)
	}
}

func TestDocumentedGrantSubjectsMatchCode(t *testing.T) {
	var documented []string
	for _, cells := range documentedTable(t, "grant-subjects") {
		documented = append(documented, cells[0])
		for i, resource := range []string{ResourceService, ResourceEgress} {
			if want := cells[2+i] == "✓"; Accepts(resource, cells[0]) != want {
				t.Errorf("%s accepts %s: code %v, docs %v", resource, cells[0], !want, want)
			}
		}
	}
	if !slices.Equal(documented, SubjectTypes) {
		t.Errorf("documented subjects %v, code %v", documented, SubjectTypes)
	}
}

func TestRolesAreOrderedByPrivilege(t *testing.T) {
	for i := 1; i < len(MemberRoles); i++ {
		for _, p := range MemberRoles[i].Permissions() {
			if !MemberRoles[i-1].Can(p) {
				t.Errorf("%s holds %s but the more privileged %s does not", MemberRoles[i], p, MemberRoles[i-1])
			}
		}
	}
	if !slices.Equal(Admin.Permissions(), AllPermissions) || len(None.Permissions()) != 0 {
		t.Fatal("admin must hold every permission and a non-member none")
	}
	for _, change := range []struct {
		from, to Role
		want     Permission
	}{
		{None, Developer, ManageMembers}, {Guest, Developer, ManageMembers}, {Developer, None, ManageMembers},
		{None, Maintainer, ManagePrivilegedMembers}, {Maintainer, Developer, ManagePrivilegedMembers}, {Owner, None, ManagePrivilegedMembers},
	} {
		if got := MembershipChange(change.from, change.to); got != change.want {
			t.Errorf("%q -> %q needs %s, got %s", change.from, change.to, change.want, got)
		}
	}
}

func TestDeviceRolesFollowTheRunningPrincipal(t *testing.T) {
	person := api.Principal{Type: api.PrincipalUser, ID: "u"}
	machine := api.Principal{Type: api.PrincipalServiceAccount, ID: "sa"}
	all := AllDeviceRoles
	for _, c := range []struct {
		p          api.Principal
		role       Role
		registered []string
		want       []string
	}{
		{person, Developer, all, all},
		{person, Guest, all, []string{api.RoleIngress}},
		{person, Guest, []string{api.RoleServe}, nil},
		{person, None, all, nil},
		{machine, None, []string{api.RoleEgress}, []string{api.RoleEgress}},
	} {
		if got := DeviceRoles(c.p, c.role, c.registered); !slices.Equal(got, c.want) {
			t.Errorf("%s as %q registered %v: got %v, want %v", c.p.Type, c.role, c.registered, got, c.want)
		}
	}
}

func TestOwnershipNeverComesFromAServiceAccountOrCreator(t *testing.T) {
	mine := &types.Agent{Principal: api.Principal{Type: api.PrincipalUser, ID: "alice"}}
	robot := &types.Agent{Principal: api.Principal{Type: api.PrincipalServiceAccount, ID: "alice"}, CreatedBy: "alice"}
	if !CanManageService(Developer, mine, "alice") || CanManageService(Developer, robot, "alice") || CanManageService(Guest, mine, "alice") {
		t.Fatal("services.manage_own must cover only the person's own device")
	}
	if !CanManageAgent(Guest, mine, "alice") || CanManageAgent(Developer, robot, "alice") || !CanManageAgent(Maintainer, robot, "bob") {
		t.Fatal("devices are managed by their own person or with agents.manage")
	}
	if CanManageGrant(Developer, ResourceEgress, mine, "alice") || !CanManageGrant(Developer, ResourceService, mine, "alice") {
		t.Fatal("egress grants need egress_routes.manage")
	}
}

func TestGrantSubjectsAreEvaluatedNow(t *testing.T) {
	now := time.Now()
	snap := &types.Snapshot{
		Users:           []types.User{{ID: "member"}, {ID: "outsider"}, {ID: "gone", Disabled: true}},
		Memberships:     []types.Membership{{WorkspaceID: "w", UserID: "member", Role: "guest"}, {WorkspaceID: "w", UserID: "gone", Role: "owner"}},
		ServiceAccounts: []types.ServiceAccount{{ID: "sa", WorkspaceID: "w", Status: api.StatusActive}, {ID: "other-sa", WorkspaceID: "x", Status: api.StatusActive}},
	}
	grant := func(resource, subject, id string) types.Grant {
		return types.Grant{WorkspaceID: "w", ResourceType: resource, ResourceID: "r", SubjectType: subject, SubjectID: id, StartsAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	}
	user := func(id string) api.Principal { return api.Principal{Type: api.PrincipalUser, ID: id} }
	machine := func(id string) api.Principal { return api.Principal{Type: api.PrincipalServiceAccount, ID: id} }
	for _, c := range []struct {
		name     string
		grant    types.Grant
		consumer api.Principal
		want     bool
	}{
		{"anonymous lets anyone in", grant(ResourceService, SubjectAnonymous, ""), api.Principal{}, true},
		{"authenticated needs a signed-in person", grant(ResourceService, SubjectAuthenticated, ""), api.Principal{}, false},
		{"authenticated covers a non-member", grant(ResourceService, SubjectAuthenticated, ""), user("outsider"), true},
		{"authenticated skips disabled people", grant(ResourceService, SubjectAuthenticated, ""), user("gone"), false},
		{"authenticated never opens egress", grant(ResourceEgress, SubjectAuthenticated, ""), user("member"), false},
		{"an internal user grant needs membership now", grant(ResourceService, SubjectUser, "outsider"), user("outsider"), false},
		{"a workspace grant covers members of any role", grant(ResourceEgress, SubjectWorkspace, "w"), user("member"), true},
		{"a workspace grant never covers its service accounts", grant(ResourceEgress, SubjectWorkspace, "w"), machine("sa"), false},
		{"a service account grant stays in its workspace", grant(ResourceEgress, SubjectServiceAccount, "other-sa"), machine("other-sa"), false},
		{"a service account cannot use its namesake person's grant", grant(ResourceEgress, SubjectUser, "sa"), machine("sa"), false},
	} {
		snap.Grants = []types.Grant{c.grant}
		if got := !Deadline(snap, c.grant.ResourceType, "r", c.consumer, now).IsZero(); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	external := grant(ResourceService, SubjectUser, "outsider")
	external.External = true
	snap.Grants = []types.Grant{external, grant(ResourceService, SubjectAuthenticated, "")}
	snap.Grants[1].ExpiresAt = now.Add(2 * time.Hour)
	if got := Deadline(snap, ResourceService, "r", user("outsider"), now); !got.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("grants should union to the latest deadline, got %v", got)
	}
}
