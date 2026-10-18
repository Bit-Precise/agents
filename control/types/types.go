// Package types defines control-plane records shared by persistence, policy and APIs.
package types

import (
	"errors"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/connection"
	"github.com/bit-precise/agentd/policy"
)

// User is a person. Email and Picture are display data refreshed from the
// upstream IdP at each SSO login (email only when verified); identity is the
// OIDC issuer and subject, never these fields.
type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email,omitempty"`
	Picture   string    `json:"picture,omitempty"`
	Admin     bool      `json:"admin"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"createdAt"`
}
type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}
type Membership struct {
	WorkspaceID string `json:"workspaceId"`
	UserID      string `json:"userId"`
	Role        string `json:"role"`
}

// ServiceAccount is a machine principal owned by exactly one Workspace. It is
// not a User: it has no Membership, no admin flag and never a Console session.
// Revocation is terminal and cascades to its Agents, keys and tokens.
type ServiceAccount struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	CreatedBy   string    `json:"createdBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	RevokedAt   time.Time `json:"revokedAt,omitzero"`
}

// Agent is the persistent registration record; api.Agent is its API view.
// Principal is exactly one User or one same-Workspace Service Account and
// never changes. CreatedBy is the registering person, kept for audit only.
// Status is stored, not derived: every principal change cascades into it.
// Effective is recomputed by Control whenever Desired or the role ceiling
// changes, and its Revision advances only when its content changes.
type Agent struct {
	ID            string               `json:"id"`
	WorkspaceID   string               `json:"workspaceId"`
	Principal     api.Principal        `json:"principal"`
	CreatedBy     string               `json:"createdBy,omitempty"`
	Name          string               `json:"name"`
	Status        string               `json:"status"`
	Roles         []string             `json:"roles"`
	Desired       api.AgentSpec        `json:"desired"`
	Effective     api.EffectiveConfig  `json:"effective"`
	LastRejection *api.ConfigRejection `json:"lastRejection,omitempty"`
	CreatedAt     time.Time            `json:"createdAt"`
	RevokedAt     time.Time            `json:"revokedAt,omitzero"`
}

// EnrollmentKey bootstraps unattended registration as a Service Account. Keys
// created before Service Accounts bind a User instead (UserID, legacy) and stay
// usable until they expire or are revoked; no new User keys are created.
type EnrollmentKey struct {
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspaceId"`
	ServiceAccountID string    `json:"serviceAccountId,omitempty"`
	UserID           string    `json:"userId,omitempty"`
	CreatedBy        string    `json:"createdBy,omitempty"`
	Roles            []string  `json:"roles"`
	Uses             int       `json:"uses"`
	MaxUses          int       `json:"maxUses"`
	ExpiresAt        time.Time `json:"expiresAt"`
	Revoked          bool      `json:"revoked"`
}
type Service struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	AgentID     string `json:"agentId"`
	Name        string `json:"name"`
	Hostname    string `json:"hostname"`
	Enabled     bool   `json:"enabled"`
}
type EgressRoute struct {
	ID          string      `json:"id"`
	WorkspaceID string      `json:"workspaceId"`
	AgentID     string      `json:"agentId"`
	Name        string      `json:"name"`
	Rule        policy.Rule `json:"rule"`
	Priority    int         `json:"priority"`
	Enabled     bool        `json:"enabled"`
}

// Grant.External records whether a manager authorized sharing beyond the
// resource Workspace. A user grant created without it applies only while the
// subject remains a member, so leaving the Workspace cannot turn an internal
// grant into an unapproved external share.
type Grant struct {
	ID           string    `json:"id"`
	WorkspaceID  string    `json:"workspaceId"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	SubjectType  string    `json:"subjectType"`
	SubjectID    string    `json:"subjectId,omitempty"`
	StartsAt     time.Time `json:"startsAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Revoked      bool      `json:"revoked"`
	External     bool      `json:"external"`
	CreatedBy    string    `json:"createdBy,omitempty"`
}
type Audit struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	ActorID    string    `json:"actorId"`
	Action     string    `json:"action"`
	ResourceID string    `json:"resourceId"`
	Detail     string    `json:"detail,omitempty"`
}

// Token hashes are persistence-only and are never returned in API snapshots.
// A device token belongs to its Agent's principal: UserID for a User Agent,
// ServiceAccountID for a Service Account Agent, never both.
type Token struct {
	ID               string
	Hash             string
	Kind             string
	UserID           string
	ServiceAccountID string
	AgentID          string
	WorkspaceID      string
	ExpiresAt        time.Time
	Revoked          bool
}

// Identity is an authenticated credential. Principal is typed; UserID is set
// only when the principal is a User, so a Service Account never passes as one.
type Identity struct {
	TokenID     string
	Kind        string
	UserID      string
	Principal   api.Principal
	AgentID     string
	WorkspaceID string
	Workspace   string
	ExpiresAt   time.Time
}

// OIDCIdentity is a persistence-only login binding. Email and display name are
// never identity keys, and IdP claims never grant workspace membership.
type OIDCIdentity struct {
	Issuer         string    `json:"issuer"`
	Subject        string    `json:"subject"`
	UserID         string    `json:"userId"`
	CreatedAt      time.Time `json:"createdAt"`
	VerifiedEmail  string    `json:"verifiedEmail,omitempty"`
	AdminFromEmail bool      `json:"adminFromEmail,omitempty"`
}

// EnrollmentAuthorization is Control's record of one Device Flow approval by a
// person. It binds the Hydra login challenge, the human actor and, once the
// person confirms, the Workspace, principal and role ceiling. It is consumed by
// exactly one Agent registration and is never reusable.
type EnrollmentAuthorization struct {
	ID             string        `json:"id"`
	Status         string        `json:"status"`
	ClientID       string        `json:"clientId"`
	LoginChallenge string        `json:"loginChallenge,omitempty"`
	ActorUserID    string        `json:"actorUserId"`
	WorkspaceID    string        `json:"workspaceId,omitempty"`
	Principal      api.Principal `json:"principal,omitzero"`
	AllowedRoles   []string      `json:"allowedRoles,omitempty"`
	CreatedAt      time.Time     `json:"createdAt"`
	ExpiresAt      time.Time     `json:"expiresAt"`
	AuthorizedAt   time.Time     `json:"authorizedAt,omitzero"`
	ConsumedAt     time.Time     `json:"consumedAt,omitzero"`
	AgentID        string        `json:"agentId,omitempty"`
}

type Snapshot struct {
	Revision                 uint64                    `json:"revision"`
	Users                    []User                    `json:"users"`
	Workspaces               []Workspace               `json:"workspaces"`
	Memberships              []Membership              `json:"memberships"`
	ServiceAccounts          []ServiceAccount          `json:"serviceAccounts"`
	EnrollmentAuthorizations []EnrollmentAuthorization `json:"enrollmentAuthorizations"`
	Agents                   []Agent                   `json:"agents"`
	EnrollmentKeys           []EnrollmentKey           `json:"enrollmentKeys"`
	Services                 []Service                 `json:"services"`
	EgressRoutes             []EgressRoute             `json:"egressRoutes"`
	Grants                   []Grant                   `json:"grants"`
	Audit                    []Audit                   `json:"audit"`
	Tokens                   []Token                   `json:"-"`
	OIDCIdentities           []OIDCIdentity            `json:"-"`
}
type EnrollRequest = api.EnrollRequest
type EnrollResponse = api.EnrollResponse

// Decision is one data-plane authorization. Consumer is who uses the resource:
// the browser User (empty for anonymous Serve) or the ingress Agent's principal.
// Revision is the provider Agent's effective revision at decision time; flows
// are rechecked by content, not by it.
type Decision struct {
	ResourceType string
	ResourceID   string
	WorkspaceID  string
	Workspace    string
	AgentID      string
	ServiceName  string
	Hostname     string
	Consumer     api.Principal
	Destination  connection.Destination
	Revision     uint64
	ExpiresAt    time.Time
}

type ErrorKind string

const (
	KindInvalid      ErrorKind = "invalid"
	KindUnauthorized ErrorKind = "unauthorized"
	KindForbidden    ErrorKind = "forbidden"
	KindNotFound     ErrorKind = "not_found"
	KindConflict     ErrorKind = "conflict"
	KindExpired      ErrorKind = "expired"
	KindUnavailable  ErrorKind = "unavailable"
)

type Error struct {
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
}

func (e *Error) Error() string { return e.Message }
func KindOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return string(e.Kind)
	}
	return "internal"
}
