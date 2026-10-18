package state

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
)

func TestServiceAccountKeyEnrollsMachineDeviceWithTypedCredentials(t *testing.T) {
	f := setup(t)
	if _, err := f.s.CreateServiceAccount(f.user.ID, f.workspace.ID, "robot", ""); types.KindOf(err) != string(types.KindForbidden) {
		t.Fatalf("developer created a service account: %v", err)
	}
	sa, key, plain := f.serviceAccountKey(t, "ci-runner", []string{"ingress", "serve", "egress"}, 2)
	if key.CreatedBy != f.admin.ID || key.UserID != "" {
		t.Fatalf("key provenance: %+v", key)
	}
	enrolled, err := f.s.Enroll(enrollRequest(plain, "runner", desired()))
	if err != nil {
		t.Fatal(err)
	}
	if enrolled.Agent.Principal != servicePrincipal(sa.ID) || enrolled.Agent.CreatedBy != f.admin.ID || *enrolled.Credentials.Principal != servicePrincipal(sa.ID) {
		t.Fatalf("machine device principal: %+v", enrolled.Agent)
	}
	id, err := f.s.Identity(enrolled.Credentials.AccessToken)
	if err != nil || id.UserID != "" || id.Principal != servicePrincipal(sa.ID) {
		t.Fatalf("service account passed as a user or was rejected: %+v %v", id, err)
	}
	for _, token := range f.s.snapshot.Tokens {
		if token.AgentID == enrolled.Agent.ID && (token.UserID != "" || token.ServiceAccountID != sa.ID) {
			t.Fatalf("device token bound to the wrong principal: %+v", token)
		}
	}
	// The operator leaving does not stop the team device.
	if err = f.s.RemoveMembership(f.admin.ID, f.workspace.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Identity(enrolled.Credentials.ProviderToken); err != nil {
		t.Fatalf("service account device followed an unrelated user's lifecycle: %v", err)
	}
	if err = f.s.RevokeServiceAccount(f.admin.ID, sa.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Identity(enrolled.Credentials.ProviderToken); err == nil {
		t.Fatal("revoked service account device still authenticates")
	}
	if _, err = f.s.Enroll(enrollRequest(plain, "second", desired())); err == nil {
		t.Fatal("revoked service account key still enrolls")
	}
	a, _ := f.s.Agent(enrolled.Agent.ID)
	if a.Status != api.StatusRevoked {
		t.Fatal("revocation did not cascade to the stored agent")
	}
}

func TestGrantSubjectsNeverStandInForEachOther(t *testing.T) {
	f := setup(t)
	_, _, plain := f.serviceAccountKey(t, "exit-bot", []string{"ingress", "serve", "egress"}, 1)
	exit, err := f.s.Enroll(enrollRequest(plain, "exit", desired()))
	if err != nil {
		t.Fatal(err)
	}
	sa := exit.Agent.Principal
	route, err := f.s.CreateEgressRoute(f.admin.ID, types.EgressRoute{WorkspaceID: f.workspace.ID, AgentID: exit.Agent.ID, Name: "office", Rule: rule("oa.test")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CreateGrant(f.admin.ID, types.Grant{WorkspaceID: f.workspace.ID, ResourceType: "egress", ResourceID: route.ID, SubjectType: "workspace", SubjectID: f.workspace.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ResolveEgress("w1", userPrincipal(f.user.ID), "oa.test", 443); err != nil {
		t.Fatalf("workspace grant denied a human member: %v", err)
	}
	if _, err = f.s.ResolveEgress("w1", sa, "oa.test", 443); types.KindOf(err) != string(types.KindForbidden) {
		t.Fatalf("workspace member grant covered a service account: %v", err)
	}
	if _, err = f.s.CreateGrant(f.admin.ID, types.Grant{WorkspaceID: f.workspace.ID, ResourceType: "egress", ResourceID: route.ID, SubjectType: "service_account", SubjectID: sa.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if d, err := f.s.ResolveEgress("w1", sa, "oa.test", 443); err != nil || d.Consumer != sa {
		t.Fatalf("service account grant: %+v %v", d, err)
	}
	snap, _ := f.s.SnapshotForUser(f.admin.ID)
	if _, err = f.s.CreateGrant(f.admin.ID, types.Grant{WorkspaceID: f.workspace.ID, ResourceType: "service", ResourceID: snap.Services[0].ID, SubjectType: "service_account", SubjectID: sa.ID, ExpiresAt: time.Now().Add(time.Hour)}); types.KindOf(err) != string(types.KindInvalid) {
		t.Fatalf("service account was granted a browser-only service: %v", err)
	}
}

func TestDeviceAuthorizationChoicesAndSelectionRules(t *testing.T) {
	f := setup(t)
	sa, err := f.s.CreateServiceAccount(f.admin.ID, f.workspace.ID, "builder", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.s.BeginDeviceAuthorization(f.user.ID, "agentd", "challenge-dev")
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.s.DeviceAuthorization(f.user.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Choices) != 2 || view.Choices[0].Workspace.Name != "personal-"+f.user.ID || view.Authorization.LoginChallenge != "" {
		t.Fatalf("choices for a developer: %+v", view)
	}
	for _, c := range view.Choices {
		if len(c.ServiceAccounts) != 0 {
			t.Fatal("developer was offered a service account")
		}
	}
	if _, err = f.s.DeviceAuthorization(f.admin.ID, a.ID); types.KindOf(err) != string(types.KindNotFound) {
		t.Fatal("another person could read the authorization")
	}
	for name, p := range map[string]api.Principal{"another user": userPrincipal(f.admin.ID), "service account": servicePrincipal(sa.ID)} {
		if _, err := f.s.AuthorizeDevice(f.user.ID, a.ID, f.workspace.ID, p); types.KindOf(err) != string(types.KindForbidden) {
			t.Fatalf("%s: developer selection accepted: %v", name, err)
		}
	}
	w2, err := f.s.CreateWorkspace(f.admin.ID, "w2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.AuthorizeDevice(f.user.ID, a.ID, w2.ID, userPrincipal(f.user.ID)); types.KindOf(err) != string(types.KindForbidden) {
		t.Fatalf("self in a workspace without membership: %v", err)
	}
	if _, err = f.s.AuthorizeDevice(f.admin.ID, a.ID, f.workspace.ID, servicePrincipal(sa.ID)); types.KindOf(err) != string(types.KindNotFound) {
		t.Fatalf("another person confirmed the authorization: %v", err)
	}
	authorized, err := f.s.AuthorizeDevice(f.user.ID, a.ID, f.workspace.ID, userPrincipal(f.user.ID))
	if err != nil || authorized.Status != AuthorizationAuthorized || len(authorized.AllowedRoles) != 3 {
		t.Fatalf("authorize self: %+v %v", authorized, err)
	}
	if again, err := f.s.AuthorizeDevice(f.user.ID, a.ID, f.workspace.ID, userPrincipal(f.user.ID)); err != nil || again.AuthorizedAt != authorized.AuthorizedAt {
		t.Fatalf("a repeated confirmation was not idempotent: %v", err)
	}
	if _, err = f.s.AuthorizeDevice(f.user.ID, a.ID, view.Choices[0].Workspace.ID, userPrincipal(f.user.ID)); types.KindOf(err) != string(types.KindConflict) {
		t.Fatal("a confirmed selection was changed")
	}
	if _, err = f.s.CheckDeviceConsent(f.user.ID, a.ID, servicePrincipal(sa.ID).Subject()); types.KindOf(err) != string(types.KindForbidden) {
		t.Fatal("consent with a substituted subject was accepted")
	}
	if _, err = f.s.CheckDeviceConsent(f.admin.ID, a.ID, userPrincipal(f.user.ID).Subject()); types.KindOf(err) != string(types.KindNotFound) {
		t.Fatal("consent in another person's session was accepted")
	}
	if _, err = f.s.CheckDeviceConsent(f.user.ID, a.ID, userPrincipal(f.user.ID).Subject()); err != nil {
		t.Fatal(err)
	}
	// The admin can choose a service account in a workspace it manages.
	b, _ := f.s.BeginDeviceAuthorization(f.admin.ID, "agentd", "challenge-admin")
	if _, err = f.s.AuthorizeDevice(f.admin.ID, b.ID, f.workspace.ID, servicePrincipal(sa.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceExchangeIsOnceAndRechecksPermission(t *testing.T) {
	f := setup(t)
	if _, err := f.s.SetMembership(f.admin.ID, f.workspace.ID, f.user.ID, "maintainer"); err != nil {
		t.Fatal(err)
	}
	sa, err := f.s.CreateServiceAccount(f.user.ID, f.workspace.ID, "fleet", "")
	if err != nil {
		t.Fatal(err)
	}
	authorize := func(challenge string) types.EnrollmentAuthorization {
		a, err := f.s.BeginDeviceAuthorization(f.user.ID, "agentd", challenge)
		if err != nil {
			t.Fatal(err)
		}
		if a, err = f.s.AuthorizeDevice(f.user.ID, a.ID, f.workspace.ID, servicePrincipal(sa.ID)); err != nil {
			t.Fatal(err)
		}
		return a
	}
	request := func(name string) api.AgentEnrollment {
		return api.AgentEnrollment{Workspace: "w1", Name: name, Desired: desired()}
	}
	a := authorize("challenge-1")
	if _, err = f.s.ExchangeDeviceEnrollment(a.ID, userPrincipal(f.user.ID), request("node-1")); types.KindOf(err) != string(types.KindUnauthorized) {
		t.Fatalf("token subject different from the authorization was accepted: %v", err)
	}
	wrongWorkspace := request("node-1")
	wrongWorkspace.Workspace = "elsewhere"
	if _, err = f.s.ExchangeDeviceEnrollment(a.ID, servicePrincipal(sa.ID), wrongWorkspace); types.KindOf(err) != string(types.KindForbidden) {
		t.Fatalf("config.yaml workspace mismatch was accepted: %v", err)
	}
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := f.s.ExchangeDeviceEnrollment(a.ID, servicePrincipal(sa.ID), request(fmt.Sprintf("node-%d", i))); err == nil {
				created.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("one authorization created %d agents", created.Load())
	}
	if _, err = f.s.ExchangeDeviceEnrollment(a.ID, servicePrincipal(sa.ID), request("again")); types.KindOf(err) != string(types.KindConflict) {
		t.Fatalf("consumed authorization exchanged again: %v", err)
	}
	// A demotion between consent and exchange makes the exchange fail.
	b := authorize("challenge-2")
	if _, err = f.s.SetMembership(f.admin.ID, f.workspace.ID, f.user.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ExchangeDeviceEnrollment(b.ID, servicePrincipal(sa.ID), request("node-b")); types.KindOf(err) != string(types.KindForbidden) {
		t.Fatalf("demoted operator still registered a service account device: %v", err)
	}
	// The registered device does not follow its operator's demotion.
	for _, agent := range f.s.snapshot.Agents {
		if agent.Principal == servicePrincipal(sa.ID) && agent.Status != api.StatusActive {
			t.Fatal("service account device was revoked with its operator's role")
		}
	}
}

func TestExpiredOrRevokedAuthorizationCannotBeUsed(t *testing.T) {
	f := setup(t)
	a, _ := f.s.BeginDeviceAuthorization(f.user.ID, "agentd", "challenge-expire")
	if _, err := f.s.AuthorizeDevice(f.user.ID, a.ID, f.workspace.ID, userPrincipal(f.user.ID)); err != nil {
		t.Fatal(err)
	}
	if err := f.s.mutate(func(snap *types.Snapshot) error {
		authorization(snap, a.ID).ExpiresAt = time.Now().Add(-time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ExchangeDeviceEnrollment(a.ID, userPrincipal(f.user.ID), api.AgentEnrollment{Workspace: "w1", Name: "late", Desired: desired()}); types.KindOf(err) != string(types.KindUnauthorized) {
		t.Fatalf("expired authorization exchanged: %v", err)
	}
	b, _ := f.s.BeginDeviceAuthorization(f.user.ID, "agentd", "challenge-revoke")
	if err := f.s.RevokeDeviceAuthorization(f.user.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AuthorizeDevice(f.user.ID, b.ID, f.workspace.ID, userPrincipal(f.user.ID)); types.KindOf(err) != string(types.KindConflict) {
		t.Fatalf("declined authorization was confirmed: %v", err)
	}
	if _, err := f.s.BeginDeviceAuthorization(f.admin.ID, "agentd", "challenge-revoke"); types.KindOf(err) != string(types.KindConflict) {
		t.Fatalf("another person reused a login challenge: %v", err)
	}
}

func TestGuestDeviceFlowIsIngressOnly(t *testing.T) {
	f := setup(t)
	if _, err := f.s.SetMembership(f.admin.ID, f.workspace.ID, f.user.ID, "guest"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.deviceEnroll(f.user.ID, userPrincipal(f.user.ID), "provider", desired()); types.KindOf(err) != string(types.KindForbidden) {
		t.Fatalf("guest registered a provider: %v", err)
	}
	spec := desired()
	spec.Serve.Enabled, spec.Egress.Enabled = false, false
	enrolled, err := f.deviceEnroll(f.user.ID, userPrincipal(f.user.ID), "laptop", spec)
	if err != nil || len(enrolled.Agent.Roles) != 1 || enrolled.Credentials.ProviderToken != "" {
		t.Fatalf("guest ingress device: %+v %v", enrolled.Agent, err)
	}
}
