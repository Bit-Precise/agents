// Package types defines control-plane records shared by persistence, policy and APIs.
package types

import (
	"errors"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/connection"
	"github.com/bit-precise/agentd/policy"
)

type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
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

// Agent is the persistent registration record; api.Agent is its API view.
// Status is stored, not derived: every principal change cascades into it.
// Effective is recomputed by Control whenever Desired or the role ceiling
// changes, and its Revision advances only when its content changes.
type Agent struct {
	ID            string               `json:"id"`
	WorkspaceID   string               `json:"workspaceId"`
	UserID        string               `json:"userId"`
	Name          string               `json:"name"`
	Status        string               `json:"status"`
	Roles         []string             `json:"roles"`
	Desired       api.AgentSpec        `json:"desired"`
	Effective     api.EffectiveConfig  `json:"effective"`
	LastRejection *api.ConfigRejection `json:"lastRejection,omitempty"`
	CreatedAt     time.Time            `json:"createdAt"`
	RevokedAt     time.Time            `json:"revokedAt,omitzero"`
}

// EnrollmentKey.UserID is the User the enrolled device will run as, which a
// manager may set to someone else; it is not the key's creator.
type EnrollmentKey struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	UserID      string    `json:"userId"`
	Roles       []string  `json:"roles"`
	Uses        int       `json:"uses"`
	MaxUses     int       `json:"maxUses"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Revoked     bool      `json:"revoked"`
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
type Token struct {
	ID          string
	Hash        string
	Kind        string
	UserID      string
	AgentID     string
	WorkspaceID string
	ExpiresAt   time.Time
	Revoked     bool
}
type Identity struct {
	TokenID     string
	Kind        string
	UserID      string
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
type Snapshot struct {
	Revision       uint64          `json:"revision"`
	Users          []User          `json:"users"`
	Workspaces     []Workspace     `json:"workspaces"`
	Memberships    []Membership    `json:"memberships"`
	Agents         []Agent         `json:"agents"`
	EnrollmentKeys []EnrollmentKey `json:"enrollmentKeys"`
	Services       []Service       `json:"services"`
	EgressRoutes   []EgressRoute   `json:"egressRoutes"`
	Grants         []Grant         `json:"grants"`
	Audit          []Audit         `json:"audit"`
	Tokens         []Token         `json:"-"`
	OIDCIdentities []OIDCIdentity  `json:"-"`
}
type EnrollRequest = api.EnrollRequest
type EnrollResponse = api.EnrollResponse

// Decision is one data-plane authorization. Revision is the provider Agent's
// effective revision at decision time; flows are rechecked by content, not by it.
type Decision struct {
	ResourceType string
	ResourceID   string
	WorkspaceID  string
	Workspace    string
	AgentID      string
	ServiceName  string
	Hostname     string
	UserID       string
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
