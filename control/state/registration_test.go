package state

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/db"
	"github.com/bit-precise/agents/control/types"
)

func registrationState(t *testing.T) (*State, types.User, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(t.Context(), db.Options{DSN: path}, "instance.test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	admin, err := s.BootstrapAdmin("registration-test-bootstrap-credential")
	if err != nil {
		t.Fatal(err)
	}
	return s, admin, path
}

func personalWorkspace(t *testing.T, s *State, owner types.User) types.Workspace {
	t.Helper()
	snap, err := s.SnapshotForUser(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found types.Workspace
	for _, w := range snap.Workspaces {
		if w.Name == "personal-"+owner.ID {
			if found.ID != "" {
				t.Fatal("duplicate personal workspace")
			}
			found = w
		}
	}
	if found.ID == "" || len(found.Name) != 41 {
		t.Fatal("missing stable personal workspace")
	}
	if canonical, err := label(found.Name); err != nil || canonical != found.Name {
		t.Fatal("personal workspace is not a canonical DNS label")
	}
	count := 0
	for _, m := range snap.Memberships {
		if m.WorkspaceID == found.ID {
			count++
			if m.UserID != owner.ID || m.Role != "owner" {
				t.Fatal("personal workspace grants access to another user")
			}
		}
	}
	if count != 1 {
		t.Fatal("personal workspace must have exactly its new owner")
	}
	return found
}

func TestLocalRegistrationPersistsUserPersonalWorkspaceAndOwnerTogether(t *testing.T) {
	s, admin, path := registrationState(t)
	before := s.Revision()
	user, credential, err := s.CreateUser(admin.ID, "Display name with spaces", false)
	if err != nil {
		t.Fatal(err)
	}
	w := personalWorkspace(t, s, user)
	if user.Admin || s.Revision() != before+1 {
		t.Fatal("registration changed global role or committed more than once")
	}
	if _, err = s.CreateWorkspace(admin.ID, w.Name); types.KindOf(err) != "conflict" {
		t.Fatal("personal workspace name is not globally reserved")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(t.Context(), db.Options{DSN: path}, "instance.test")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if personalWorkspace(t, again, user).ID != w.ID {
		t.Fatal("workspace or owner was lost after restart")
	}
	identity, err := again.Identity(credential)
	if err != nil || identity.UserID != user.ID {
		t.Fatal("local user credential was not committed with the workspace")
	}
	for _, candidate := range again.snapshot.Workspaces {
		if candidate.Name == "personal-"+admin.ID {
			t.Fatal("internal bootstrap administrator received a personal workspace")
		}
	}
}

func TestConcurrentOIDCRegistrationCreatesOnePersonalWorkspace(t *testing.T) {
	s, _, _ := registrationState(t)
	before := s.Revision()
	var group sync.WaitGroup
	users := make(chan types.User, 12)
	for range 12 {
		group.Go(func() {
			u, err := s.OIDCUser("https://issuer.test", "stable-subject", "Same display name", OIDCUserOptions{})
			if err != nil {
				t.Error(err)
				return
			}
			users <- u
		})
	}
	group.Wait()
	close(users)
	var first types.User
	for u := range users {
		if first.ID == "" {
			first = u
		} else if first.ID != u.ID {
			t.Fatal("concurrent login created multiple users")
		}
	}
	personalWorkspace(t, s, first)
	if s.Revision() != before+1 || len(s.snapshot.OIDCIdentities) != 1 || len(s.snapshot.Workspaces) != 1 || len(s.snapshot.Tokens) != 1 {
		t.Fatal("repeated login mutated state, duplicated workspace, or issued an API credential")
	}
}

func TestRegistrationRollsBackOnMembershipCommitFailure(t *testing.T) {
	for _, kind := range []string{"local", "oidc"} {
		t.Run(kind, func(t *testing.T) {
			s, admin, path := registrationState(t)
			connection, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if _, err = connection.Exec("CREATE TRIGGER reject_registration_owner BEFORE INSERT ON memberships BEGIN SELECT RAISE(ABORT, 'injected owner failure'); END"); err != nil {
				t.Fatal(err)
			}
			before, err := s.store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			create := func() (types.User, error) {
				if kind == "oidc" {
					return s.OIDCUser("https://issuer.test", "subject", "person", OIDCUserOptions{})
				}
				u, credential, err := s.CreateUser(admin.ID, "person", false)
				if err != nil && credential != "" {
					t.Fatal("failed registration returned a usable credential")
				}
				return u, err
			}
			if _, err = create(); err == nil || !strings.Contains(err.Error(), "injected owner failure") {
				t.Fatal("membership insert failure was not propagated")
			}
			after, err := s.store.Load(t.Context())
			if err != nil || !reflect.DeepEqual(before, after) || s.Revision() != before.Revision {
				t.Fatal("failed owner insert left partially committed registration state")
			}
			if _, err = connection.Exec("DROP TRIGGER reject_registration_owner"); err != nil {
				t.Fatal(err)
			}
			u, err := create()
			if err != nil {
				t.Fatal(err)
			}
			personalWorkspace(t, s, u)
		})
	}
}

func TestExistingUsersAreNotBackfilledOnOpenOrOIDCLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	store, err := db.Open(t.Context(), db.Options{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	legacy := types.User{ID: strings.Repeat("a", 32), Name: "legacy"}
	snap := types.Snapshot{Revision: 1, Users: []types.User{legacy}, OIDCIdentities: []types.OIDCIdentity{{Issuer: "https://issuer.test", Subject: "old-subject", UserID: legacy.ID}}}
	if err = store.Save(t.Context(), types.Snapshot{}, snap); err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), store, "instance.test")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.OIDCUser("https://issuer.test", "old-subject", "Changed display name", OIDCUserOptions{})
	if err != nil || got.ID != legacy.ID || len(s.snapshot.Workspaces) != 0 || len(s.snapshot.Memberships) != 0 || s.Revision() != 1 {
		t.Fatal("existing OIDC user was recreated or received a backfilled workspace")
	}
}

func TestDisableUserRetainsSolePersonalWorkspaceForAdminRecovery(t *testing.T) {
	s, admin, _ := registrationState(t)
	u, _, err := s.CreateUser(admin.ID, "person", false)
	if err != nil {
		t.Fatal(err)
	}
	w := personalWorkspace(t, s, u)
	if err = s.DisableUser(admin.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CheckUser(u.ID); err == nil || workspace(&s.snapshot, w.ID) == nil || member(&s.snapshot, w.ID, u.ID) == nil {
		t.Fatal("disabling personal owner reactivated user or deleted retained workspace data")
	}
	if _, err = s.SetMembership(admin.ID, w.ID, admin.ID, "owner"); err != nil {
		t.Fatal("administrator could not take over retained personal workspace")
	}
}

func TestDisableUserStillProtectsSharedAndOtherWorkspaces(t *testing.T) {
	for _, kind := range []string{"personal-with-disabled-member", "other-workspace"} {
		t.Run(kind, func(t *testing.T) {
			s, admin, _ := registrationState(t)
			u, _, err := s.CreateUser(admin.ID, "person", false)
			if err != nil {
				t.Fatal(err)
			}
			personal := personalWorkspace(t, s, u)
			if kind == "personal-with-disabled-member" {
				other, _, err := s.CreateUser(admin.ID, "other", false)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.SetMembership(admin.ID, personal.ID, other.ID, "guest"); err != nil {
					t.Fatal(err)
				}
				if err = s.DisableUser(admin.ID, other.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				shared, err := s.CreateWorkspace(admin.ID, "shared")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.SetMembership(admin.ID, shared.ID, u.ID, "owner"); err != nil {
					t.Fatal(err)
				}
				if err = s.RemoveMembership(admin.ID, shared.ID, admin.ID); err != nil {
					t.Fatal(err)
				}
			}
			requireKind(t, s.DisableUser(admin.ID, u.ID), types.KindConflict)
			if _, err = s.CheckUser(u.ID); err != nil {
				t.Fatal("rejected disable changed the user's state")
			}
		})
	}
}

// Data migrated from the manual-approval model is recomputed under the current
// policy on startup: valid pending requests take effect, nothing is granted,
// and agents whose principal is gone are revoked instead of activated.
func TestStartupReconcilesMigratedAgentsWithoutGranting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrated.db")
	store, err := db.Open(t.Context(), db.Options{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	spec := api.AgentSpec{Serve: api.ServeSpec{Enabled: true, Services: []api.ServiceSpec{{Name: "demo", Origin: "http://127.0.0.1:8080"}}}}
	legacy := api.EffectiveConfig{Revision: 37, Services: []api.EffectiveService{{Name: "demo", Origin: "http://127.0.0.1:7070", Hostname: "demo.work.instance.test"}}}
	snap := types.Snapshot{Revision: 1, Users: []types.User{{ID: "dev", Name: "dev"}, {ID: "gone", Name: "gone"}}, Workspaces: []types.Workspace{{ID: "w", Name: "work"}},
		Memberships: []types.Membership{{WorkspaceID: "w", UserID: "dev", Role: "developer"}},
		Agents: []types.Agent{{ID: "a", WorkspaceID: "w", UserID: "dev", Name: "laptop", Status: "pending", Roles: []string{"serve"}, Desired: spec, Effective: legacy},
			{ID: "b", WorkspaceID: "w", UserID: "gone", Name: "orphan", Status: api.StatusActive, Roles: []string{"ingress"}, Effective: api.EffectiveConfig{Revision: 3}}},
		Services: []types.Service{{ID: "s", WorkspaceID: "w", AgentID: "a", Name: "demo", Hostname: "demo.work.instance.test", Enabled: true}}}
	if err = store.Save(t.Context(), types.Snapshot{}, snap); err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), store, "instance.test")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := s.Agent("a")
	if a.Status != api.StatusActive || a.Effective.Revision != 38 || a.Effective.Services[0].Origin != "http://127.0.0.1:8080" || a.Effective.Policy != PolicyVersion {
		t.Fatalf("pending request was not recomputed under the current policy: %+v", a)
	}
	if b, _ := s.Agent("b"); b.Status != api.StatusRevoked || b.Effective.Revision != 4 {
		t.Fatalf("agent without a member principal was not revoked: %+v", b)
	}
	if len(s.snapshot.Grants) != 0 {
		t.Fatal("startup reconciliation created grants")
	}
}
