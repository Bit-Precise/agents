package state

import (
	"path/filepath"
	"testing"

	"github.com/bit-precise/agents/control/db"
	"github.com/bit-precise/agents/control/types"
)

func TestOIDCEmailAdminPersistsAndStartupRevokesExistingCredential(t *testing.T) {
	s, admin, path := registrationState(t)
	opts := OIDCUserOptions{VerifiedEmail: "  Operator@Example.Test  "}
	u, err := s.OIDCUser("https://issuer.test", "operator", "Operator", opts)
	if err != nil || u.Admin {
		t.Fatalf("verified email alone granted admin: %v", err)
	}
	opts.EmailAdmin = true
	u, err = s.OIDCUser("https://issuer.test", "operator", "Operator", opts)
	if err != nil || !u.Admin {
		t.Fatalf("fresh allowed login did not grant admin: %v", err)
	}
	credential, err := s.RotateUserToken(admin.ID, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(t.Context(), db.Options{DSN: path}, "instance.test")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	identity := again.snapshot.OIDCIdentities[0]
	if identity.VerifiedEmail != "operator@example.test" || !identity.AdminFromEmail {
		t.Fatal("trusted email and grant source did not persist")
	}
	before := again.Revision()
	if err = again.ReconcileOIDCAdminEmails("https://issuer.test", []string{" OPERATOR@example.test "}); err != nil || again.Revision() != before {
		t.Fatal("unchanged policy rewrote state or rejected a canonical email")
	}
	if err = again.ReconcileOIDCAdminEmails("https://issuer.test", nil); err != nil {
		t.Fatal(err)
	}
	current, err := again.CheckUser(u.ID)
	if err != nil || current.Admin || again.Revision() != before+1 {
		t.Fatal("removed email policy did not immediately change the durable role")
	}
	oldIdentity, err := again.Identity(credential)
	if err != nil || oldIdentity.UserID != u.ID {
		t.Fatal("policy downgrade unnecessarily revoked the ordinary user credential")
	}
	_, _, err = again.CreateUser(oldIdentity.UserID, "must-not-create", false)
	requireKind(t, err, types.KindForbidden)
	personalWorkspace(t, again, current)
	before = again.Revision()
	audits := len(again.snapshot.Audit)
	if err = again.ReconcileOIDCAdminEmails("https://issuer.test", nil); err != nil || again.Revision() != before || len(again.snapshot.Audit) != audits {
		t.Fatal("idempotent revocation wrote an audit or revision")
	}
	if err = again.ReconcileOIDCAdminEmails("https://issuer.test", []string{"operator@example.test"}); err != nil {
		t.Fatal(err)
	}
	current, _ = again.CheckUser(u.ID)
	if current.Admin {
		t.Fatal("startup promoted a user using a stale stored email claim")
	}
	if current, err = again.OIDCUser("https://issuer.test", "operator", "Operator", opts); err != nil || !current.Admin {
		t.Fatal("fresh verified login did not restore the configured grant")
	}
}

func TestExplicitOIDCAndLegacyAdministratorsSurviveEmailPolicyRemoval(t *testing.T) {
	s, admin, _ := registrationState(t)
	bootstrap, err := s.OIDCUser("https://issuer.test", "bootstrap", "Subject admin", OIDCUserOptions{
		BootstrapAdmin: true, EmailAdmin: true, VerifiedEmail: "operator@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy, _, err := s.CreateUser(admin.ID, "Legacy explicit admin", true)
	if err != nil {
		t.Fatal(err)
	}
	// Older OIDC rows contain no source fields. Their existing Admin=true is
	// explicit authority and must not be reclassified by a new matching policy.
	if err = s.mutate(func(snap *types.Snapshot) error {
		snap.OIDCIdentities = append(snap.OIDCIdentities, types.OIDCIdentity{Issuer: "https://issuer.test", Subject: "legacy", UserID: legacy.ID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.OIDCUser("https://issuer.test", "legacy", "Legacy", OIDCUserOptions{EmailAdmin: true, VerifiedEmail: "legacy@example.test"}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileOIDCAdminEmails("", nil); err != nil {
		t.Fatal(err)
	}
	for _, u := range []types.User{admin, bootstrap, legacy} {
		got, err := s.CheckUser(u.ID)
		if err != nil || !got.Admin {
			t.Fatal("email policy removal revoked explicit administrator authority")
		}
	}
	if got, err := s.OIDCUser("https://issuer.test", "bootstrap", "Subject admin", OIDCUserOptions{}); err != nil || !got.Admin {
		t.Fatal("initial subject bootstrap authority was reapplied as revocable email policy")
	}
	for _, identity := range s.snapshot.OIDCIdentities {
		if identity.AdminFromEmail {
			t.Fatal("explicit administrator was incorrectly marked email-derived")
		}
	}
}

func TestLastEmailAdministratorLosesPrivilegesWithoutRevocationRollback(t *testing.T) {
	for _, mode := range []string{"unverified-login", "removed-email", "issuer-change", "oidc-disabled"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "last-admin.db")
			s, err := Open(t.Context(), db.Options{DSN: path}, "instance.test")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			u, err := s.OIDCUser("https://issuer.test", "last", "Last derived admin", OIDCUserOptions{VerifiedEmail: "last@example.test", EmailAdmin: true})
			if err != nil || !u.Admin {
				t.Fatal("failed to create initial derived administrator")
			}
			credential, err := s.RotateUserToken(u.ID, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			// The manual disable operation still retains its independent safety
			// rule; loss of external authorization must never use that exception.
			requireKind(t, s.DisableUser(u.ID, u.ID), types.KindConflict)
			switch mode {
			case "unverified-login":
				_, err = s.OIDCUser("https://issuer.test", "last", "Last derived admin", OIDCUserOptions{})
			case "removed-email":
				err = s.ReconcileOIDCAdminEmails("https://issuer.test", nil)
			case "issuer-change":
				err = s.ReconcileOIDCAdminEmails("https://different-issuer.test", []string{"last@example.test"})
			case "oidc-disabled":
				err = s.ReconcileOIDCAdminEmails("", nil)
			}
			if err != nil {
				t.Fatalf("policy revocation rolled back for last administrator: %v", err)
			}
			identity, err := s.Identity(credential)
			if err != nil {
				t.Fatal("ordinary user credential should remain valid")
			}
			_, _, err = s.CreateUser(identity.UserID, "forbidden-after-downgrade", false)
			requireKind(t, err, types.KindForbidden)
			personalWorkspace(t, s, u)
			stored, err := s.store.Load(t.Context())
			if err != nil || stored.Users[0].Admin || stored.OIDCIdentities[0].AdminFromEmail {
				t.Fatal("revocation was not durably committed")
			}
		})
	}
}

func TestVerifiedEmailNeverMergesSubjectsOrReactivatesDisabledUsers(t *testing.T) {
	s, admin, _ := registrationState(t)
	opts := OIDCUserOptions{VerifiedEmail: "same@example.test", EmailAdmin: true}
	first, err := s.OIDCUser("https://issuer.test", "one", "Same display", opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.OIDCUser("https://issuer.test", "two", "Same display", opts)
	if err != nil || first.ID == second.ID || personalWorkspace(t, s, first).ID == personalWorkspace(t, s, second).ID {
		t.Fatal("matching email merged distinct OIDC identities or personal workspaces")
	}
	if err = s.DisableUser(admin.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	before := s.Revision()
	_, err = s.OIDCUser("https://issuer.test", "one", "Same display", opts)
	requireKind(t, err, types.KindUnauthorized)
	if s.Revision() != before || !user(&s.snapshot, first.ID).Disabled {
		t.Fatal("configured email reactivated a disabled account")
	}
}
