package state

import (
	"strings"
	"time"
	"unicode"

	"github.com/bit-precise/agents/control/types"
)

// OIDCUserOptions is supplied only after the Control verifies an ID token.
// VerifiedEmail must come from a strictly true email_verified claim. EmailAdmin
// is the result of matching that email against trusted instance configuration,
// never an admin/roles claim. BootstrapAdmin is a separate, initial-only grant.
type OIDCUserOptions struct {
	BootstrapAdmin bool
	VerifiedEmail  string
	EmailAdmin     bool
}

// OIDCUser binds identity only by issuer/subject, never by email or display name.
func (s *State) OIDCUser(issuer, subject, displayName string, opts OIDCUserOptions) (types.User, error) {
	var result types.User
	if issuer == "" || subject == "" || len(issuer) > 2048 || len(subject) > 255 {
		return result, problem(types.KindInvalid, "invalid OIDC identity")
	}
	opts.VerifiedEmail = strings.ToLower(strings.TrimSpace(opts.VerifiedEmail))
	if len(opts.VerifiedEmail) > 320 || strings.ContainsAny(opts.VerifiedEmail, "\r\n\x00") || (opts.EmailAdmin && opts.VerifiedEmail == "") {
		return result, problem(types.KindInvalid, "invalid verified OIDC email")
	}
	err := s.mutate(func(snap *types.Snapshot) error {
		for i := range snap.OIDCIdentities {
			identity := &snap.OIDCIdentities[i]
			if identity.Issuer == issuer && identity.Subject == subject {
				u, err := requireUser(snap, identity.UserID)
				if err != nil {
					return err
				}
				identity.VerifiedEmail = opts.VerifiedEmail
				wasAdmin := u.Admin
				if identity.AdminFromEmail {
					identity.AdminFromEmail = opts.EmailAdmin
					u.Admin = hasEmailAdmin(snap, u.ID)
				} else if opts.EmailAdmin && !u.Admin {
					identity.AdminFromEmail, u.Admin = true, true
				}
				auditOIDCAdminChange(snap, u.ID, wasAdmin, u.Admin)
				result = *u
				return nil
			}
		}
		name := strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, displayName))
		if name == "" {
			name = "OIDC user"
		}
		for len(name) > 90 {
			runes := []rune(name)
			name = string(runes[:len(runes)-1])
		}
		id := randomID()
		original := name
		for {
			found := false
			for _, u := range snap.Users {
				if u.Name == name {
					found = true
					break
				}
			}
			if !found {
				break
			}
			name = original + " (" + randomID()[:12] + ")"
		}
		result = types.User{ID: id, Name: name, Admin: opts.BootstrapAdmin || opts.EmailAdmin, CreatedAt: time.Now().UTC()}
		snap.Users = append(snap.Users, result)
		if err := createPersonalWorkspace(snap, result, id); err != nil {
			return err
		}
		snap.OIDCIdentities = append(snap.OIDCIdentities, types.OIDCIdentity{
			Issuer: issuer, Subject: subject, UserID: id, CreatedAt: result.CreatedAt,
			VerifiedEmail: opts.VerifiedEmail, AdminFromEmail: opts.EmailAdmin && !opts.BootstrapAdmin,
		})
		// Registration grants only the user's new personal workspace. No API
		// token or access to any existing workspace is issued by OIDC login.
		audit(snap, id, "user.oidc.create", id)
		return nil
	})
	return result, err
}

func hasEmailAdmin(snap *types.Snapshot, userID string) bool {
	for _, identity := range snap.OIDCIdentities {
		if identity.UserID == userID && identity.AdminFromEmail {
			return true
		}
	}
	return false
}

func auditOIDCAdminChange(snap *types.Snapshot, userID string, before, after bool) {
	if before == after {
		return
	}
	action := "user.oidc.admin.revoke"
	if after {
		action = "user.oidc.admin.grant"
	}
	audit(snap, userID, action, userID)
}

// ReconcileOIDCAdminEmails runs before serving requests. Remove revoked policy
// grants so existing browser sessions and user tokens observe the current role.
// It intentionally never promotes from an old stored claim: a newly allowed
// address must be verified again during a fresh login. Empty issuer revokes all
// email-derived grants when OIDC is disabled. Old explicit/subject admins are
// not policy-managed and retain their role.
func (s *State) ReconcileOIDCAdminEmails(issuer string, allowedEmails []string) error {
	allowed := make(map[string]bool, len(allowedEmails))
	for _, email := range allowedEmails {
		if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
			allowed[email] = true
		}
	}
	return s.mutate(func(snap *types.Snapshot) error {
		managed := make(map[string]bool)
		for i := range snap.OIDCIdentities {
			identity := &snap.OIDCIdentities[i]
			if !identity.AdminFromEmail {
				continue
			}
			managed[identity.UserID] = true
			if issuer == "" || identity.Issuer != issuer || !allowed[strings.ToLower(strings.TrimSpace(identity.VerifiedEmail))] {
				identity.AdminFromEmail = false
			}
		}
		for i := range snap.Users {
			u := &snap.Users[i]
			if !managed[u.ID] {
				continue
			}
			wasAdmin := u.Admin
			u.Admin = hasEmailAdmin(snap, u.ID)
			auditOIDCAdminChange(snap, u.ID, wasAdmin, u.Admin)
		}
		return nil
	})
}
