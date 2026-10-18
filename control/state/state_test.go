package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/db"
	"github.com/bit-precise/agents/control/types"
)

type fixture struct {
	s           *State
	path        string
	admin, user types.User
	userToken   string
	workspace   types.Workspace
}

func setup(t *testing.T) fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(t.Context(), db.Options{DSN: path}, "instance.test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	admin, err := s.BootstrapAdmin("bootstrap-token-for-state-tests")
	if err != nil {
		t.Fatal(err)
	}
	u, token, err := s.CreateUser(admin.ID, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateWorkspace(admin.ID, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetMembership(admin.ID, w.ID, u.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	return fixture{s, path, admin, u, token, w}
}
func rule(domain string) policy.Rule {
	return policy.Rule{Domain: policy.Domain{Exact: domain}, Ports: []int{443}, Protocol: "tls"}
}
func desired() api.AgentSpec {
	return api.AgentSpec{Ingress: api.IngressSpec{Enabled: true}, Serve: api.ServeSpec{Enabled: true, Services: []api.ServiceSpec{{Name: "demo", Origin: "http://127.0.0.1:8080"}}},
		Egress: api.EgressSpec{Enabled: true, Allow: []policy.Rule{rule("oa.test"), rule("blocked.test")}, Deny: []policy.Rule{{Domain: policy.Domain{Suffix: "blocked.test"}, Protocol: "tls"}}}}
}
func enrollRequest(key, name string, spec api.AgentSpec) types.EnrollRequest {
	return types.EnrollRequest{EnrollmentKey: key, Instance: "https://instance.test", Endpoint: "https://connect.instance.test/agent", Workspace: "w1", Name: name, Desired: spec}
}
func update(spec api.AgentSpec) api.ConfigUpdate {
	return api.ConfigUpdate{Workspace: "w1", Name: "provider", Desired: spec}
}

// enroll registers the fixture user's own device through Device Flow.
func (f fixture) enroll(t *testing.T) types.EnrollResponse {
	t.Helper()
	out, err := f.deviceEnroll(f.user.ID, userPrincipal(f.user.ID), "provider", desired())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// deviceEnroll runs the Control side of Device Flow: the actor confirms the
// principal in the browser, then the device exchanges the access token.
func (f fixture) deviceEnroll(actorID string, p api.Principal, name string, spec api.AgentSpec) (types.EnrollResponse, error) {
	a, err := f.s.BeginDeviceAuthorization(actorID, "agentd", "challenge-"+randomID())
	if err != nil {
		return types.EnrollResponse{}, err
	}
	if _, err = f.s.AuthorizeDevice(actorID, a.ID, f.workspace.ID, p); err != nil {
		return types.EnrollResponse{}, err
	}
	return f.s.ExchangeDeviceEnrollment(a.ID, p, api.AgentEnrollment{Workspace: f.workspace.Name, Name: name, Desired: spec})
}

// legacyUserKey stores a User-bound key as created before Service Accounts;
// such keys keep working until they expire or are revoked.
func (f fixture) legacyUserKey(t *testing.T, userID string, roles []string, maxUses int) (types.EnrollmentKey, string) {
	t.Helper()
	plain := randomToken()
	key := types.EnrollmentKey{ID: randomID(), WorkspaceID: f.workspace.ID, UserID: userID, Roles: roles, MaxUses: maxUses, ExpiresAt: time.Now().Add(time.Hour)}
	if err := f.s.mutate(func(snap *types.Snapshot) error {
		snap.EnrollmentKeys = append(snap.EnrollmentKeys, key)
		token := tokenRecord("enrollment", plain, userPrincipal(userID), f.workspace.ID, "", key.ExpiresAt)
		token.ID = key.ID
		snap.Tokens = append(snap.Tokens, token)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return key, plain
}

// serviceAccountKey creates a Service Account and an unattended key for it.
func (f fixture) serviceAccountKey(t *testing.T, name string, roles []string, maxUses int) (types.ServiceAccount, types.EnrollmentKey, string) {
	t.Helper()
	sa, err := f.s.CreateServiceAccount(f.admin.ID, f.workspace.ID, name, "")
	if err != nil {
		t.Fatal(err)
	}
	key, plain, err := f.s.CreateEnrollmentKey(f.admin.ID, types.EnrollmentKey{ServiceAccountID: sa.ID, Roles: roles, MaxUses: maxUses, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return sa, key, plain
}
func requireKind(t *testing.T, err error, want types.ErrorKind) {
	t.Helper()
	if err == nil || types.KindOf(err) != string(want) {
		t.Fatalf("error=%v kind=%s want=%s", err, types.KindOf(err), want)
	}
}

func TestSQLiteRestartPreservesTokenHashesAndEffectiveConfig(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	first, err := f.s.ResolveServe("demo.w1.instance.test", f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(t.Context(), db.Options{DSN: f.path}, "instance.test")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	for _, token := range []string{f.userToken, enrolled.Credentials.ControlToken, enrolled.Credentials.ProviderToken, enrolled.Credentials.AccessToken} {
		if _, err := again.Identity(token); err != nil {
			t.Fatalf("registered token lost after restart: %v", err)
		}
	}
	second, err := again.ResolveServe("DEMO.w1.instance.test.:443", f.user.ID)
	if err != nil || second.AgentID != first.AgentID || second.Revision != first.Revision {
		t.Fatalf("persisted decision changed: %+v %v", second, err)
	}
	refreshed, err := again.GetAgentCredentials(enrolled.Credentials.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ProviderToken != "" || refreshed.AccessToken != "" || refreshed.AgentID != enrolled.Agent.ID || refreshed.Effective.Revision != enrolled.Credentials.Effective.Revision {
		t.Fatal("refresh recovered secret tokens, lost identity or moved the effective revision")
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{f.userToken, enrolled.Credentials.ControlToken, enrolled.Credentials.ProviderToken, "bootstrap-token-for-state-tests"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("plaintext token persisted to SQLite")
		}
	}
}

func TestEnrollmentIsEffectiveWithoutApproval(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	effective := enrolled.Credentials.Effective
	service, ok := effective.Service("demo")
	if enrolled.Agent.Status != api.StatusActive || !ok || service.Hostname != "demo.w1.instance.test" || !effective.Ingress || effective.Revision != 1 ||
		!effective.ExpiresAt.After(time.Now()) || effective.Policy != PolicyVersion || !policy.Allowed(effective.Egress, effective.Deny, "oa.test", 443, "tls") {
		t.Fatalf("validated enrollment did not become effective: %+v", effective)
	}
	for _, token := range []string{enrolled.Credentials.AccessToken, enrolled.Credentials.ProviderToken} {
		if _, err := f.s.Identity(token); err != nil {
			t.Fatalf("role token unusable right after enrollment: %v", err)
		}
	}
	if _, err := f.s.ResolveServe("demo.w1.instance.test", f.user.ID); err != nil {
		t.Fatalf("default member grant missing for a published service: %v", err)
	}
	// Egress still needs an explicit route and grant; enrollment grants nothing.
	_, err := f.s.ResolveEgress("w1", userPrincipal(f.user.ID), "oa.test", 443)
	requireKind(t, err, types.KindForbidden)
}

func TestEnrollmentKeyConsumedExactlyOnceConcurrently(t *testing.T) {
	f := setup(t)
	_, key, plain := f.serviceAccountKey(t, "ci", []string{"ingress"}, 1)
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			spec := desired()
			spec.Serve.Enabled = false
			spec.Egress.Enabled = false
			if _, err := f.s.Enroll(enrollRequest(plain, fmt.Sprintf("access%d", i), spec)); err == nil {
				success.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("single-use key admitted %d agents", success.Load())
	}
	snap, err := f.s.SnapshotForUser(f.admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 1 {
		t.Fatal("failed enrollment left partial agents")
	}
	for _, k := range snap.EnrollmentKeys {
		if k.ID == key.ID && k.Uses != 1 {
			t.Fatal("key usage not committed atomically")
		}
	}
}

func TestServiceGrantUnionExpirySharingAndAdminNoBypass(t *testing.T) {
	f := setup(t)
	f.enroll(t)
	otherAdmin, _, err := f.s.CreateUser(f.admin.ID, "operator", true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ResolveServe("demo.w1.instance.test", otherAdmin.ID)
	requireKind(t, err, types.KindForbidden)
	bob, _, err := f.s.CreateUser(f.admin.ID, "bob", false)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := f.s.SnapshotForUser(f.admin.ID)
	svc := snap.Services[0]
	direct, err := f.s.CreateGrant(f.admin.ID, types.Grant{WorkspaceID: f.workspace.ID, ResourceType: "service", ResourceID: svc.ID, SubjectType: "user", SubjectID: bob.ID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if !direct.External || direct.CreatedBy != f.admin.ID {
		t.Fatalf("share outside the workspace was not recorded as external: %+v", direct)
	}
	if _, err := f.s.ResolveServe(svc.Hostname, bob.ID); err != nil {
		t.Fatalf("explicit nonmember share denied: %v", err)
	}
	anonymous, err := f.s.CreateGrant(f.admin.ID, types.Grant{WorkspaceID: f.workspace.ID, ResourceType: "service", ResourceID: svc.ID, SubjectType: "anonymous", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ResolveServe(svc.Hostname, ""); err != nil {
		t.Fatal(err)
	}
	if err = f.s.RevokeGrant(f.admin.ID, anonymous.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ResolveServe(svc.Hostname, "")
	requireKind(t, err, types.KindForbidden)
	if _, err = f.s.ResolveServe(svc.Hostname, bob.ID); err != nil {
		t.Fatal("revoking anonymous incorrectly removed explicit user grant")
	}
	if err = f.s.RevokeGrant(f.admin.ID, direct.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ResolveServe(svc.Hostname, bob.ID)
	requireKind(t, err, types.KindForbidden)
	if _, err = f.s.ResolveServe(svc.Hostname, f.user.ID); err != nil {
		t.Fatal("independent workspace grant was removed")
	}
	if err = f.s.mutate(func(snapshot *types.Snapshot) error {
		for i := range snapshot.Grants {
			snapshot.Grants[i].ExpiresAt = time.Now().Add(-time.Second)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ResolveServe(svc.Hostname, f.user.ID)
	requireKind(t, err, types.KindForbidden)
}

func TestInternalUserGrantEndsWhenSubjectLeavesWorkspace(t *testing.T) {
	f := setup(t)
	f.enroll(t)
	bob, _, err := f.s.CreateUser(f.admin.ID, "bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetMembership(f.admin.ID, f.workspace.ID, bob.ID, "guest"); err != nil {
		t.Fatal(err)
	}
	snap, _ := f.s.SnapshotForUser(f.admin.ID)
	svc := snap.Services[0]
	// A developer may grant a current member access to its own service without
	// a manager; that grant must not become an external share later.
	grant, err := f.s.CreateGrant(f.user.ID, types.Grant{WorkspaceID: f.workspace.ID, ResourceType: "service", ResourceID: svc.ID, SubjectType: "user", SubjectID: bob.ID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || grant.External {
		t.Fatalf("internal grant: %+v %v", grant, err)
	}
	if _, err := f.s.ResolveServe(svc.Hostname, bob.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.RemoveMembership(f.admin.ID, f.workspace.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ResolveServe(svc.Hostname, bob.ID)
	requireKind(t, err, types.KindForbidden)
	_, err = f.s.CreateGrant(f.user.ID, types.Grant{WorkspaceID: f.workspace.ID, ResourceType: "service", ResourceID: svc.ID, SubjectType: "user", SubjectID: bob.ID, ExpiresAt: time.Now().Add(time.Hour)})
	requireKind(t, err, types.KindForbidden)
}

func TestEgressRequiresRouteGrantAndBothCapabilitySides(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	route, err := f.s.CreateEgressRoute(f.admin.ID, types.EgressRoute{WorkspaceID: f.workspace.ID, AgentID: enrolled.Agent.ID, Name: "office", Rule: rule("oa.test"), Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ResolveEgress("w1", userPrincipal(f.user.ID), "oa.test", 443)
	requireKind(t, err, types.KindForbidden)
	grant, err := f.s.CreateGrant(f.admin.ID, types.Grant{WorkspaceID: f.workspace.ID, ResourceType: "egress", ResourceID: route.ID, SubjectType: "user", SubjectID: f.user.ID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := f.s.ResolveEgress("w1", userPrincipal(f.user.ID), "OA.TEST.", 443)
	if err != nil || decision.AgentID != enrolled.Agent.ID || decision.Destination.Host != "oa.test" {
		t.Fatalf("wrong routed destination: %+v %v", decision, err)
	}
	spec := desired()
	spec.Egress.Allow = nil
	if _, err = f.s.UpdateAgent(enrolled.Credentials.ControlToken, update(spec)); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ResolveEgress("w1", userPrincipal(f.user.ID), "oa.test", 443)
	requireKind(t, err, types.KindForbidden)
	if _, err = f.s.ResolveServe("demo.w1.instance.test", f.user.ID); err != nil {
		t.Fatal("egress withdrawal interrupted serve")
	}
	if err = f.s.RevokeGrant(f.admin.ID, grant.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTenantScopeAndGuestRoleCannotBeBypassed(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	w2, err := f.s.CreateWorkspace(f.admin.ID, "w2")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CreateEgressRoute(f.admin.ID, types.EgressRoute{WorkspaceID: w2.ID, AgentID: enrolled.Agent.ID, Name: "foreign", Rule: rule("oa.test")})
	requireKind(t, err, types.KindForbidden)
	snap, _ := f.s.SnapshotForUser(f.admin.ID)
	_, err = f.s.CreateGrant(f.admin.ID, types.Grant{WorkspaceID: w2.ID, ResourceType: "service", ResourceID: snap.Services[0].ID, SubjectType: "anonymous", ExpiresAt: time.Now().Add(time.Hour)})
	requireKind(t, err, types.KindForbidden)
	guest, _, err := f.s.CreateUser(f.admin.ID, "guest", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetMembership(f.admin.ID, w2.ID, guest.ID, "guest"); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CreateServiceAccount(guest.ID, w2.ID, "robot", "")
	requireKind(t, err, types.KindForbidden)
	_, _, err = f.s.CreateEnrollmentKey(f.user.ID, types.EnrollmentKey{WorkspaceID: f.workspace.ID, UserID: f.user.ID, Roles: []string{"serve"}})
	requireKind(t, err, types.KindInvalid)
	moved := update(desired())
	moved.Workspace = "w2"
	_, err = f.s.UpdateAgent(enrolled.Credentials.ControlToken, moved)
	requireKind(t, err, types.KindForbidden)
	_, err = f.s.SetMembership(f.admin.ID, f.workspace.ID, "missing-user", "developer")
	requireKind(t, err, types.KindNotFound)
}

func TestRevokeAndMembershipRemovalCascadeAndDoNotResurrect(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	unused, _ := f.legacyUserKey(t, f.user.ID, []string{"ingress"}, 1)
	updates, cancel := f.s.Subscribe()
	defer cancel()
	if err := f.s.RemoveMembership(f.admin.ID, f.workspace.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("committed revocation was not published")
	}
	_, err := f.s.Identity(enrolled.Credentials.ProviderToken)
	requireKind(t, err, types.KindUnauthorized)
	if _, err = f.s.SetMembership(f.admin.ID, f.workspace.ID, f.user.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Identity(enrolled.Credentials.ProviderToken)
	requireKind(t, err, types.KindUnauthorized)
	if _, err = f.s.GetAgentCredentials(enrolled.Credentials.ControlToken); err == nil {
		t.Fatal("removed registration credentials revived")
	}
	snap, _ := f.s.SnapshotForUser(f.admin.ID)
	for _, k := range snap.EnrollmentKeys {
		if k.ID == unused.ID && !k.Revoked {
			t.Fatal("membership removal left the user's enrollment key usable")
		}
	}
	if a := snap.Agents[0]; a.Status != api.StatusRevoked || a.RevokedAt.IsZero() || len(a.Effective.Services) != 0 || a.Effective.Ingress {
		t.Fatalf("stored agent state was not revoked: %+v", a)
	}
	if err = f.s.RemoveMembership(f.admin.ID, f.workspace.ID, f.admin.ID); err == nil {
		t.Fatal("last workspace owner removed")
	}
}

func TestDisableUserRevokesDevicesAndRevokedExitRouteStopsShadowing(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	route, err := f.s.CreateEgressRoute(f.admin.ID, types.EgressRoute{WorkspaceID: f.workspace.ID, AgentID: enrolled.Agent.ID, Name: "office", Rule: rule("oa.test")})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.DisableUser(f.admin.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ := f.s.SnapshotForUser(f.admin.ID)
	if snap.Agents[0].Status != api.StatusRevoked {
		t.Fatal("disabling the principal did not revoke its device")
	}
	for _, r := range snap.EgressRoutes {
		if r.ID == route.ID && r.Enabled {
			t.Fatal("route through a revoked exit stayed enabled")
		}
	}
}

func TestSnapshotAndMutationReturnsCannotModifyPublishedState(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	enrolled.Agent.Desired.Serve.Services[0].Origin = "http://127.0.0.1:9999"
	enrolled.Agent.Effective.Services[0].Origin = "http://127.0.0.1:9999"
	snap, err := f.s.SnapshotForUser(f.admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tokens != nil {
		t.Fatal("token hashes escaped snapshot boundary")
	}
	data, _ := json.Marshal(snap)
	if strings.Contains(string(data), f.userToken) || strings.Contains(string(data), "Hash") {
		t.Fatal("snapshot exposed credentials")
	}
	snap.Agents[0].Desired.Egress.Allow[0].Ports[0] = 1
	a, err := f.s.Agent(enrolled.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Effective.Services[0].Origin != "http://127.0.0.1:8080" || a.Desired.Egress.Allow[0].Ports[0] != 443 {
		t.Fatal("caller changed published immutable snapshot")
	}
}

func TestExpansionAppliesAutomaticallyWithoutRecreatingGrants(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	snap, _ := f.s.SnapshotForUser(f.admin.ID)
	for _, g := range snap.Grants {
		if err := f.s.RevokeGrant(f.admin.ID, g.ID); err != nil {
			t.Fatal(err)
		}
	}
	spec := desired()
	spec.Egress.Allow = append(spec.Egress.Allow, rule("new.test"))
	changed, err := f.s.UpdateAgent(enrolled.Credentials.ControlToken, update(spec))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Effective.Revision != enrolled.Agent.Effective.Revision+1 || !policy.Allowed(changed.Effective.Egress, changed.Effective.Deny, "new.test", 443, "tls") || !policy.Allowed(changed.Effective.Egress, changed.Effective.Deny, "oa.test", 443, "tls") {
		t.Fatalf("validated expansion did not take effect at a new revision: %+v", changed.Effective)
	}
	_, err = f.s.ResolveServe("demo.w1.instance.test", f.user.ID)
	requireKind(t, err, types.KindForbidden)
}

func TestRejectedUpdateKeepsEffectiveConfigAndRecordsReason(t *testing.T) {
	f := setup(t)
	_, key := f.legacyUserKey(t, f.user.ID, []string{"ingress", "serve"}, 1)
	spec := desired()
	spec.Egress.Enabled = false
	enrolled, err := f.s.Enroll(enrollRequest(key, "provider", spec))
	if err != nil {
		t.Fatal(err)
	}
	blocker := f.enrollNamed(t, "blocker")
	for _, tc := range []struct {
		name   string
		change func(*api.AgentSpec)
		kind   types.ErrorKind
	}{
		{"role beyond the key", func(s *api.AgentSpec) { s.Egress.Enabled = true }, types.KindForbidden},
		{"invalid origin", func(s *api.AgentSpec) { s.Serve.Services[0].Origin = "ftp://127.0.0.1" }, types.KindInvalid},
		{"name owned by another agent", func(s *api.AgentSpec) {
			s.Serve.Services = append(s.Serve.Services, api.ServiceSpec{Name: blocker.Agent.Name, Origin: "http://127.0.0.1:1"})
		}, types.KindConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := spec.Clone()
			tc.change(&next)
			_, err := f.s.UpdateAgent(enrolled.Credentials.ControlToken, update(next))
			requireKind(t, err, tc.kind)
			a, _ := f.s.Agent(enrolled.Agent.ID)
			if a.Effective.Revision != enrolled.Agent.Effective.Revision || !a.Effective.SameContent(enrolled.Agent.Effective) || a.LastRejection == nil || a.LastRejection.Kind != string(tc.kind) {
				t.Fatalf("rejection changed the effective configuration or was not recorded: %+v", a)
			}
		})
	}
	spec.Serve.Services[0].Origin = "http://127.0.0.1:8090"
	a, err := f.s.UpdateAgent(enrolled.Credentials.ControlToken, update(spec))
	if err != nil || a.LastRejection != nil || a.Effective.Services[0].Origin != "http://127.0.0.1:8090" {
		t.Fatalf("accepted update did not clear the rejection: %+v %v", a, err)
	}
}

func (f fixture) enrollNamed(t *testing.T, name string) types.EnrollResponse {
	t.Helper()
	spec := desired()
	spec.Serve.Enabled, spec.Egress.Enabled = false, false
	out, err := f.deviceEnroll(f.admin.ID, userPrincipal(f.admin.ID), name, spec)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRevokedAgentNamesAreReclaimedWithoutReviving(t *testing.T) {
	f := setup(t)
	old := f.enroll(t)
	if err := f.s.RevokeAgent(f.user.ID, old.Agent.ID); err != nil {
		t.Fatal(err)
	}
	bob, _, err := f.s.CreateUser(f.admin.ID, "bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetMembership(f.admin.ID, f.workspace.ID, bob.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	_, bobKey := f.legacyUserKey(t, bob.ID, []string{"ingress", "serve", "egress"}, 2)
	_, err = f.s.Enroll(enrollRequest(bobKey, "provider", desired()))
	requireKind(t, err, types.KindConflict)
	// The same principal re-registers immediately; the revoked record, its
	// tokens and its service grants are purged rather than inherited.
	again := f.enroll(t)
	if again.Agent.ID == old.Agent.ID || again.Agent.Effective.Revision != 1 || f.s.KnownToken(old.Credentials.ProviderToken) {
		t.Fatalf("re-enrollment revived or kept the revoked registration: %+v", again.Agent)
	}
	snap, _ := f.s.SnapshotForUser(f.admin.ID)
	if len(snap.Agents) != 1 || len(snap.Services) != 1 || snap.Services[0].AgentID != again.Agent.ID {
		t.Fatalf("revoked agent was not purged: %+v", snap.Agents)
	}
	// Another principal can only reclaim after the tombstone window.
	if err = f.s.RevokeAgent(f.user.ID, again.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.mutate(func(snapshot *types.Snapshot) error {
		snapshot.Agents[0].RevokedAt = time.Now().Add(-NameTombstoneTTL - time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Enroll(enrollRequest(bobKey, "provider", desired())); err != nil {
		t.Fatalf("expired tombstone still blocked the name: %v", err)
	}
}

func TestEgressPriorityDoesNotFallbackAndProviderDenyWins(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	exact, err := f.s.CreateEgressRoute(f.admin.ID, types.EgressRoute{WorkspaceID: f.workspace.ID, AgentID: enrolled.Agent.ID, Name: "restricted", Priority: 0, Rule: rule("oa.test")})
	if err != nil {
		t.Fatal(err)
	}
	broad, err := f.s.CreateEgressRoute(f.admin.ID, types.EgressRoute{WorkspaceID: f.workspace.ID, AgentID: enrolled.Agent.ID, Name: "broad", Priority: 100, Rule: policy.Rule{Domain: policy.Domain{Suffix: "test"}, Ports: []int{443}, Protocol: "tls"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CreateGrant(f.admin.ID, types.Grant{WorkspaceID: f.workspace.ID, ResourceType: "egress", ResourceID: broad.ID, SubjectType: "workspace", SubjectID: f.workspace.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ResolveEgress("w1", userPrincipal(f.user.ID), "oa.test", 443)
	requireKind(t, err, types.KindForbidden)
	if _, err = f.s.SetEgressRouteEnabled(f.admin.ID, exact.ID, false); err != nil {
		t.Fatal(err)
	}
	decision, err := f.s.ResolveEgress("w1", userPrincipal(f.user.ID), "oa.test", 443)
	if err != nil || decision.ResourceID != broad.ID {
		t.Fatalf("broad route not selected after exact disabled: %+v %v", decision, err)
	}
	_, err = f.s.ResolveEgress("w1", userPrincipal(f.user.ID), "blocked.test", 443)
	requireKind(t, err, types.KindForbidden)
}

func TestRejectedEnrollmentRollsBackReservationsAndKeyUse(t *testing.T) {
	f := setup(t)
	f.enroll(t)
	key, plain := f.legacyUserKey(t, f.user.ID, []string{"serve"}, 1)
	spec := desired()
	spec.Ingress.Enabled = false
	spec.Egress.Enabled = false
	spec.Serve.Services = append([]api.ServiceSpec{{Name: "fresh", Origin: "http://127.0.0.1:3000"}}, spec.Serve.Services...)
	_, err := f.s.Enroll(enrollRequest(plain, "second", spec))
	requireKind(t, err, types.KindConflict)
	snap, _ := f.s.SnapshotForUser(f.admin.ID)
	for _, k := range snap.EnrollmentKeys {
		if k.ID == key.ID && k.Uses != 0 {
			t.Fatal("failed enrollment consumed its key")
		}
	}
	for _, v := range snap.Services {
		if v.Name == "fresh" {
			t.Fatal("failed enrollment left a service reservation")
		}
	}
	spec.Serve.Services = spec.Serve.Services[:1]
	if _, err = f.s.Enroll(enrollRequest(plain, "second", spec)); err != nil {
		t.Fatalf("rolled back key or reservation could not be reused: %v", err)
	}
}

func TestExpiredUserTokenAndGuestDowngradeLoseAuthorization(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	if _, err := f.s.SetMembership(f.admin.ID, f.workspace.ID, f.user.ID, "guest"); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Identity(enrolled.Credentials.ProviderToken)
	requireKind(t, err, types.KindForbidden)
	if _, err = f.s.Identity(enrolled.Credentials.AccessToken); err != nil {
		t.Fatal("guest downgrade unexpectedly removed ingress permission")
	}
	_, err = f.s.ResolveServe("demo.w1.instance.test", f.user.ID)
	requireKind(t, err, types.KindUnavailable)
	a, _ := f.s.Agent(enrolled.Agent.ID)
	if a.Effective.Revision <= enrolled.Agent.Effective.Revision || len(a.Effective.Services) != 0 || !a.Effective.Ingress || a.LastRejection == nil || !strings.Contains(a.LastRejection.Message, "serve, egress") {
		t.Fatalf("demotion did not narrow the effective configuration visibly: %+v", a)
	}
	if err = f.s.mutate(func(snapshot *types.Snapshot) error {
		for i := range snapshot.Tokens {
			if snapshot.Tokens[i].Hash == hash(f.userToken) {
				snapshot.Tokens[i].ExpiresAt = time.Now().Add(-time.Second)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Identity(f.userToken)
	requireKind(t, err, types.KindExpired)
}

func TestDesiredRetriesAreIdempotent(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	before, _ := f.s.SnapshotForUser(f.admin.ID)
	updates, cancel := f.s.Subscribe()
	defer cancel()
	for i := 0; i < 2; i++ {
		got, err := f.s.UpdateAgent(enrolled.Credentials.ControlToken, update(desired()))
		if err != nil || got.Effective.Revision != enrolled.Agent.Effective.Revision {
			t.Fatalf("identical desired changed the effective revision: %v", err)
		}
	}
	after, _ := f.s.SnapshotForUser(f.admin.ID)
	if after.Revision != before.Revision || len(after.Audit) != len(before.Audit) {
		t.Fatal("identical desired retry changed revision or audit")
	}
	select {
	case <-updates:
		t.Fatal("no-op desired emitted a policy update")
	default:
	}
	spec := desired()
	spec.Egress.Allow = append(spec.Egress.Allow, rule("new.test"))
	changed, err := f.s.UpdateAgent(enrolled.Credentials.ControlToken, update(spec))
	if err != nil || changed.Effective.Revision != enrolled.Agent.Effective.Revision+1 {
		t.Fatalf("new desired not applied: %v", err)
	}
	before, _ = f.s.SnapshotForUser(f.admin.ID)
	if got, err := f.s.UpdateAgent(enrolled.Credentials.ControlToken, update(spec)); err != nil || got.Effective.Revision != changed.Effective.Revision {
		t.Fatalf("delayed retry moved the effective revision: %v", err)
	}
	if _, err = f.s.BootstrapAdmin("bootstrap-token-for-state-tests"); err != nil {
		t.Fatal(err)
	}
	after, _ = f.s.SnapshotForUser(f.admin.ID)
	if after.Revision != before.Revision || len(after.Audit) != len(before.Audit) {
		t.Fatal("idempotent bootstrap/desired retry changed policy")
	}
	if _, err = f.s.UpdateAgent("invalid", update(spec)); err == nil {
		t.Fatal("idempotency fast path bypassed authentication")
	}
}

func TestConcurrentUpdatesKeepEffectiveConsistentWithDesired(t *testing.T) {
	f := setup(t)
	enrolled := f.enroll(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			spec := desired()
			spec.Serve.Services = slices.Clone(spec.Serve.Services)
			spec.Serve.Services[0].Origin = fmt.Sprintf("http://127.0.0.1:%d", 19000+i)
			if _, err := f.s.UpdateAgent(enrolled.Credentials.ControlToken, update(spec)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	a, err := f.s.Agent(enrolled.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Effective.Services[0].Origin != a.Desired.Serve.Services[0].Origin || a.Effective.Revision != 17 {
		t.Fatalf("effective configuration diverged from the last accepted desired: %+v", a)
	}
}

func TestEnrollmentPreissuesOnlyPermittedRoleTokens(t *testing.T) {
	f := setup(t)
	_, key := f.legacyUserKey(t, f.user.ID, []string{"ingress", "serve"}, 1)
	spec := desired()
	spec.Serve.Enabled = false
	spec.Egress.Enabled = false
	enrolled, err := f.s.Enroll(enrollRequest(key, "provider", spec))
	if err != nil {
		t.Fatal(err)
	}
	if enrolled.Credentials.ProviderToken == "" || enrolled.Credentials.AccessToken == "" {
		t.Fatal("future permitted role token missing")
	}
	spec.Serve.Enabled = true
	if _, err = f.s.UpdateAgent(enrolled.Credentials.ControlToken, update(spec)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Identity(enrolled.Credentials.ProviderToken); err != nil {
		t.Fatal("preissued provider token unusable after enabling serve")
	}
	if !f.s.KnownToken(enrolled.Credentials.AccessToken) {
		t.Fatal("platform token not recognized")
	}
	if err = f.s.RevokeAgent(f.admin.ID, enrolled.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if !f.s.KnownToken(enrolled.Credentials.AccessToken) {
		t.Fatal("revoked platform token forgotten")
	}
}

func TestPeopleCreateBoundedWorkspacesTheyOwn(t *testing.T) {
	f := setup(t)
	w, err := f.s.CreateWorkspace(f.user.ID, "team")
	if err != nil {
		t.Fatal(err)
	}
	if m := member(&f.s.snapshot, w.ID, f.user.ID); m == nil || m.Role != "owner" {
		t.Fatalf("creator is not the owner: %+v", m)
	}
	if _, err = f.s.CreateWorkspace(f.user.ID, "personal-someone"); types.KindOf(err) != string(types.KindInvalid) {
		t.Fatalf("a reserved personal name was taken: %v", err)
	}
	if _, err = f.s.CreateWorkspace(f.user.ID, "team"); types.KindOf(err) != string(types.KindConflict) {
		t.Fatalf("a taken name was accepted: %v", err)
	}
	owned := 0
	for _, m := range f.s.snapshot.Memberships {
		if m.UserID == f.user.ID && m.Role == "owner" {
			owned++
		}
	}
	for i := owned; i < MaxOwnedWorkspaces; i++ {
		if _, err = f.s.CreateWorkspace(f.user.ID, fmt.Sprintf("team-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.s.CreateWorkspace(f.user.ID, "one-too-many"); types.KindOf(err) != string(types.KindForbidden) {
		t.Fatalf("the workspace limit was not enforced: %v", err)
	}
	if _, err = f.s.CreateWorkspace(f.admin.ID, "admin-unbounded"); err != nil {
		t.Fatalf("an administrator was limited: %v", err)
	}
}
