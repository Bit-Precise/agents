package state

import (
	"strings"
	"time"
	"unicode"

	"github.com/bit-precise/agents/control/types"
)

// OIDCUser is called only after verifying an ID token from the configured
// issuer. bootstrapAdmin is trusted server configuration, never an IdP claim;
// it is applied only when this subject first creates its local account.
func (s *State) OIDCUser(issuer, subject, displayName string, bootstrapAdmin bool) (types.User, error) {
	var result types.User
	if issuer == "" || subject == "" || len(issuer) > 2048 || len(subject) > 255 {
		return result, problem(types.KindInvalid, "invalid OIDC identity")
	}
	err := s.mutate(func(snap *types.Snapshot) error {
		for _, identity := range snap.OIDCIdentities {
			if identity.Issuer == issuer && identity.Subject == subject {
				u, err := requireUser(snap, identity.UserID)
				if err != nil {
					return err
				}
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
		result = types.User{ID: id, Name: name, Admin: bootstrapAdmin, CreatedAt: time.Now().UTC()}
		snap.Users = append(snap.Users, result)
		snap.OIDCIdentities = append(snap.OIDCIdentities, types.OIDCIdentity{Issuer: issuer, Subject: subject, UserID: id, CreatedAt: result.CreatedAt})
		// No API token or membership is issued as a side effect of OIDC login.
		audit(snap, id, "user.oidc.create", id)
		return nil
	})
	return result, err
}
