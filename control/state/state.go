// Package state coordinates policy, authorization and durable control mutations.
// A committed SQL transaction precedes publication of each immutable snapshot.
package state

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/db"
	"github.com/bit-precise/agents/control/types"
)

const (
	// EffectiveTTL bounds how long agentd may keep its effective configuration
	// without reaching Control. Every successful fetch renews it.
	EffectiveTTL     = 24 * time.Hour
	TokenTTL         = 30 * 24 * time.Hour
	TokenRenewBefore = 7 * 24 * time.Hour
	// DefaultServiceGrantTTL is the default window of the Workspace-member grant
	// created when a service is first published. SetServiceGrantTTL overrides it.
	DefaultServiceGrantTTL = 7 * 24 * time.Hour
	// NameTombstoneTTL keeps a revoked Agent's names from being taken by another
	// principal, so old links cannot be captured immediately.
	NameTombstoneTTL = 7 * 24 * time.Hour
	// PolicyVersion identifies the built-in Workspace policy: the enrollment
	// role ceiling narrowed by the Workspace role (guests run ingress only).
	PolicyVersion = "builtin/1"
	// defaultGrantCreator marks grants created by the Workspace default policy.
	defaultGrantCreator = "policy:default-member-access"
)

type State struct {
	// writeMu serializes mutations; mu only guards publication of snapshot,
	// so readers keep the committed snapshot while a write is persisted.
	writeMu         sync.Mutex
	mu              sync.RWMutex
	store           *db.Store
	snapshot        types.Snapshot
	baseDomain      string
	serviceGrantTTL time.Duration
	subscribers     map[chan struct{}]struct{}
	closed          bool
}

func Open(ctx context.Context, options db.Options, baseDomain string) (*State, error) {
	store, err := db.Open(ctx, options)
	if err != nil {
		return nil, err
	}
	s, err := New(ctx, store, baseDomain)
	if err != nil {
		_ = store.Close()
	}
	return s, err
}
func New(ctx context.Context, store *db.Store, baseDomain string) (*State, error) {
	domain, err := policy.CanonicalDomain(baseDomain)
	if err != nil {
		return nil, err
	}
	snap, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	s := &State{store: store, snapshot: snap, baseDomain: domain, serviceGrantTTL: DefaultServiceGrantTTL, subscribers: make(map[chan struct{}]struct{})}
	// Stored status and effective configuration are facts, not projections.
	// Reconcile them once after load, including data migrated from the removed
	// manual-approval model, and persist only if something changed.
	if err := s.mutate(func(snap *types.Snapshot) error { reconcileAgents(snap); return nil }); err != nil {
		return nil, err
	}
	return s, nil
}

// SetServiceGrantTTL changes the default member-grant window for services
// published from now on. It never changes existing grants.
func (s *State) SetServiceGrantTTL(d time.Duration) error {
	if d <= 0 || d > TokenTTL {
		return problem(types.KindInvalid, "default service grant window must be positive and at most 30 days")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.serviceGrantTTL = d
	return nil
}
func (s *State) Close() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for ch := range s.subscribers {
		close(ch)
	}
	s.subscribers = nil
	return s.store.Close()
}
func (s *State) Subscribe() (<-chan struct{}, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{}, 1)
	if s.closed {
		close(ch)
	} else {
		s.subscribers[ch] = struct{}{}
	}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.subscribers[ch]; ok {
				delete(s.subscribers, ch)
				close(ch)
			}
		})
	}
}

// Revision is the latest committed snapshot revision. Subscribe before reading
// it when implementing a remote watch, so a commit cannot be missed between the
// revision check and notification registration.
func (s *State) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.Revision
}

func problem(kind types.ErrorKind, message string) error {
	return &types.Error{Kind: kind, Message: message}
}

// clone deep-copies a snapshot. The append-only audit log, tokens and OIDC
// bindings hold no references, so they are copied shallowly; Clip makes the
// next append reallocate instead of writing into the committed backing array.
func clone(snap types.Snapshot) (types.Snapshot, error) {
	audit := snap.Audit
	snap.Audit = nil
	data, err := json.Marshal(snap)
	if err != nil {
		return types.Snapshot{}, err
	}
	var out types.Snapshot
	if err = json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	out.Audit = slices.Clip(audit)
	out.Tokens = append([]types.Token(nil), snap.Tokens...)
	out.OIDCIdentities = append([]types.OIDCIdentity(nil), snap.OIDCIdentities...)
	return out, nil
}

// mutate applies fn to a private copy and publishes it only after the SQL
// transaction commits. Readers are never blocked by the database write.
func (s *State) mutate(fn func(*types.Snapshot) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.RLock()
	closed, current := s.closed, s.snapshot
	s.mu.RUnlock()
	if closed {
		return problem(types.KindUnavailable, "control state is closed")
	}
	next, err := clone(current)
	if err != nil {
		return err
	}
	if err = fn(&next); err != nil {
		return err
	}
	if reflect.DeepEqual(next, current) {
		return nil
	}
	next.Revision = current.Revision + 1
	// Mutation arguments and return values can contain caller-owned slices.
	// Publish a second detached copy so callers cannot mutate committed policy.
	next, err = clone(next)
	if err != nil {
		return err
	}
	commitCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = s.store.Save(commitCtx, current, next); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return problem(types.KindUnavailable, "control database commit timed out")
		}
		return err
	}
	s.mu.Lock()
	s.snapshot = next
	for ch := range s.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
	return nil
}
func randomID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}
func randomToken() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(data[:])
}
func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func label(name string) (string, error) {
	canonical, err := policy.Label(strings.ToLower(name))
	if err != nil {
		return "", problem(types.KindInvalid, "name must be one DNS label")
	}
	return canonical, nil
}
func user(snap *types.Snapshot, id string) *types.User {
	for i := range snap.Users {
		if snap.Users[i].ID == id {
			return &snap.Users[i]
		}
	}
	return nil
}
func workspace(snap *types.Snapshot, id string) *types.Workspace {
	for i := range snap.Workspaces {
		if snap.Workspaces[i].ID == id || snap.Workspaces[i].Name == id {
			return &snap.Workspaces[i]
		}
	}
	return nil
}
func agent(snap *types.Snapshot, id string) *types.Agent {
	for i := range snap.Agents {
		if snap.Agents[i].ID == id {
			return &snap.Agents[i]
		}
	}
	return nil
}
func member(snap *types.Snapshot, workspaceID, userID string) *types.Membership {
	for i := range snap.Memberships {
		v := &snap.Memberships[i]
		if v.WorkspaceID == workspaceID && v.UserID == userID {
			return v
		}
	}
	return nil
}

// requireUser authenticates the acting user. requireTargetUser checks a user
// referenced by a request, so a missing target is not a session failure.
func requireUser(snap *types.Snapshot, id string) (*types.User, error) {
	u := user(snap, id)
	if u == nil || u.Disabled {
		return nil, problem(types.KindUnauthorized, "user is missing or disabled")
	}
	return u, nil
}
func requireTargetUser(snap *types.Snapshot, id string) (*types.User, error) {
	u := user(snap, id)
	if u == nil {
		return nil, problem(types.KindNotFound, "user not found")
	}
	if u.Disabled {
		return nil, problem(types.KindForbidden, "user is disabled")
	}
	return u, nil
}
func requireAdmin(snap *types.Snapshot, id string) error {
	u, err := requireUser(snap, id)
	if err != nil {
		return err
	}
	if !u.Admin {
		return problem(types.KindForbidden, "instance administrator required")
	}
	return nil
}
func requireManager(snap *types.Snapshot, actorID, workspaceID string) error {
	u, err := requireUser(snap, actorID)
	if err != nil {
		return err
	}
	if u.Admin {
		return nil
	}
	m := member(snap, workspaceID, actorID)
	if m == nil || (m.Role != "owner" && m.Role != "maintainer") {
		return problem(types.KindForbidden, "workspace manager required")
	}
	return nil
}
func activeAdmins(snap *types.Snapshot) int {
	count := 0
	for _, u := range snap.Users {
		if u.Admin && !u.Disabled {
			count++
		}
	}
	return count
}

// agentAllowed is a defense-in-depth check: cascades already store revocation
// when the principal is disabled, revoked or leaves the Workspace.
func agentAllowed(snap *types.Snapshot, a *types.Agent) bool {
	return a.Status == api.StatusActive && principalActive(snap, a.WorkspaceID, a.Principal)
}
func audit(snap *types.Snapshot, actor, action, resourceID string) {
	auditDetail(snap, actor, action, resourceID, "")
}
func auditDetail(snap *types.Snapshot, actor, action, resourceID, detail string) {
	snap.Audit = append(snap.Audit, types.Audit{ID: randomID(), At: time.Now().UTC(), ActorID: actor, Action: action, ResourceID: resourceID, Detail: detail})
}

// tokenRecord binds a credential to exactly one principal column.
func tokenRecord(kind, plain string, owner api.Principal, workspaceID, agentID string, expires time.Time) types.Token {
	t := types.Token{ID: randomID(), Hash: hash(plain), Kind: kind, WorkspaceID: workspaceID, AgentID: agentID, ExpiresAt: expires}
	if owner.Type == api.PrincipalServiceAccount {
		t.ServiceAccountID = owner.ID
	} else {
		t.UserID = owner.ID
	}
	return t
}
func tokenIn(snap *types.Snapshot, plain string) (*types.Token, error) {
	if plain == "" {
		return nil, problem(types.KindUnauthorized, "token required")
	}
	hashed := hash(plain)
	for i := range snap.Tokens {
		t := &snap.Tokens[i]
		if t.Hash == hashed {
			if t.Revoked {
				return nil, problem(types.KindUnauthorized, "token revoked")
			}
			if !t.ExpiresAt.IsZero() && !t.ExpiresAt.After(time.Now()) {
				return nil, problem(types.KindExpired, "token expired")
			}
			return t, nil
		}
	}
	return nil, problem(types.KindUnauthorized, "invalid token")
}
func identity(snap *types.Snapshot, plain string) (types.Identity, error) {
	t, err := tokenIn(snap, plain)
	if err != nil {
		return types.Identity{}, err
	}
	if t.Kind == "enrollment" {
		return types.Identity{}, problem(types.KindUnauthorized, "enrollment keys cannot authenticate ordinary API sessions")
	}
	owner := userPrincipal(t.UserID)
	if t.ServiceAccountID != "" {
		owner = api.Principal{Type: api.PrincipalServiceAccount, ID: t.ServiceAccountID}
		if sa := serviceAccount(snap, t.ServiceAccountID); sa == nil || sa.Status != api.StatusActive || t.Kind == "user" {
			return types.Identity{}, problem(types.KindUnauthorized, "service account is missing or revoked")
		}
	} else if _, err = requireUser(snap, t.UserID); err != nil {
		return types.Identity{}, err
	}
	if t.AgentID != "" {
		a := agent(snap, t.AgentID)
		if a == nil || !agentAllowed(snap, a) {
			return types.Identity{}, problem(types.KindUnauthorized, "agent is inactive")
		}
		ceiling := roleCeiling(snap, a)
		if t.Kind == "provider" && !slices.Contains(ceiling, api.RoleServe) && !slices.Contains(ceiling, api.RoleEgress) {
			return types.Identity{}, problem(types.KindForbidden, "provider roles are not permitted for this agent")
		}
		if t.Kind == "access" && !a.Effective.Ingress {
			return types.Identity{}, problem(types.KindForbidden, "ingress is not in the effective configuration")
		}
	}
	id := types.Identity{TokenID: t.ID, Kind: t.Kind, UserID: t.UserID, Principal: owner, AgentID: t.AgentID, WorkspaceID: t.WorkspaceID, ExpiresAt: t.ExpiresAt}
	if w := workspace(snap, t.WorkspaceID); w != nil {
		id.Workspace = w.Name
	}
	return id, nil
}
func (s *State) Identity(token string) (types.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return identity(&s.snapshot, token)
}

// KnownToken recognizes platform credentials even after revocation/expiry. It is
// used only at the trusted gateway to avoid forwarding secrets to tenant apps.
func (s *State) KnownToken(plain string) bool {
	if plain == "" {
		return false
	}
	digest := hash(plain)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, token := range s.snapshot.Tokens {
		if token.Hash == digest {
			return true
		}
	}
	return false
}
func (s *State) CheckUser(id string) (types.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, err := requireUser(&s.snapshot, id)
	if err != nil {
		return types.User{}, err
	}
	return *u, nil
}
func (s *State) User(id string) (types.User, error) { return s.CheckUser(id) }

func (s *State) BootstrapAdmin(plain string) (types.User, error) {
	var out types.User
	if plain == "" {
		return out, problem(types.KindInvalid, "bootstrap token is required")
	}
	err := s.mutate(func(snap *types.Snapshot) error {
		// The bootstrap file identifies the initialized database; after a user
		// credential rotation it must not become a mechanism to revive that key.
		for _, token := range snap.Tokens {
			if token.Hash == hash(plain) && token.Kind == "user" {
				if u := user(snap, token.UserID); u != nil && u.Admin {
					out = *u
					return nil
				}
			}
		}
		if len(snap.Users) != 0 {
			return problem(types.KindConflict, "control already bootstrapped with another administrator credential")
		}
		out = types.User{ID: randomID(), Name: "admin", Admin: true, CreatedAt: time.Now().UTC()}
		snap.Users = append(snap.Users, out)
		snap.Tokens = append(snap.Tokens, tokenRecord("user", plain, userPrincipal(out.ID), "", "", time.Time{}))
		audit(snap, out.ID, "bootstrap", out.ID)
		return nil
	})
	return out, err
}
func (s *State) CreateUser(actorID, name string, admin bool) (types.User, string, error) {
	var out types.User
	plain := randomToken()
	err := s.mutate(func(snap *types.Snapshot) error {
		if err := requireAdmin(snap, actorID); err != nil {
			return err
		}
		if strings.TrimSpace(name) != name || name == "" || len(name) > 128 {
			return problem(types.KindInvalid, "invalid user name")
		}
		for _, u := range snap.Users {
			if u.Name == name {
				return problem(types.KindConflict, "user name already exists")
			}
		}
		out = types.User{ID: randomID(), Name: name, Admin: admin, CreatedAt: time.Now().UTC()}
		snap.Users = append(snap.Users, out)
		if err := createPersonalWorkspace(snap, out, actorID); err != nil {
			return err
		}
		snap.Tokens = append(snap.Tokens, tokenRecord("user", plain, userPrincipal(out.ID), "", "", time.Now().Add(TokenTTL)))
		audit(snap, actorID, "user.create", out.ID)
		return nil
	})
	if err != nil {
		plain = ""
	}
	return out, plain, err
}

// createPersonalWorkspace is part of the same mutation as user registration.
// The opaque user ID gives the new owner a stable, valid 41-byte DNS label,
// independent of mutable or non-DNS display names and verified email claims.
func createPersonalWorkspace(snap *types.Snapshot, owner types.User, actorID string) error {
	name, err := label("personal-" + owner.ID)
	if err != nil {
		return err
	}
	if workspace(snap, name) != nil {
		return problem(types.KindConflict, "personal workspace already exists")
	}
	w := types.Workspace{ID: randomID(), Name: name, CreatedAt: owner.CreatedAt}
	snap.Workspaces = append(snap.Workspaces, w)
	snap.Memberships = append(snap.Memberships, types.Membership{WorkspaceID: w.ID, UserID: owner.ID, Role: "owner"})
	audit(snap, actorID, "workspace.personal.create", w.ID)
	return nil
}

// Workspace names cannot be changed, and only an instance administrator can
// explicitly create one. The reserved personal-<userID> name with exactly one
// owner membership identifies an unshared personal space, including an explicit
// administrator-created migration of an older user. Disabling its owner retains
// the data for administrator recovery. Adding any other member makes the usual
// shared-workspace last-owner protection apply again.
func solePersonalWorkspace(snap *types.Snapshot, workspaceID, userID string) bool {
	w := workspace(snap, workspaceID)
	if w == nil || w.Name != "personal-"+userID {
		return false
	}
	count := 0
	for _, m := range snap.Memberships {
		if m.WorkspaceID != w.ID {
			continue
		}
		if m.UserID != userID || m.Role != "owner" {
			return false
		}
		count++
	}
	return count == 1
}

func (s *State) CreateWorkspace(actorID, name string) (types.Workspace, error) {
	var out types.Workspace
	name, err := label(name)
	if err != nil {
		return out, err
	}
	err = s.mutate(func(snap *types.Snapshot) error {
		if err := requireAdmin(snap, actorID); err != nil {
			return err
		}
		if workspace(snap, name) != nil {
			return problem(types.KindConflict, "workspace already exists")
		}
		out = types.Workspace{ID: randomID(), Name: name, CreatedAt: time.Now().UTC()}
		snap.Workspaces = append(snap.Workspaces, out)
		snap.Memberships = append(snap.Memberships, types.Membership{WorkspaceID: out.ID, UserID: actorID, Role: "owner"})
		audit(snap, actorID, "workspace.create", out.ID)
		return nil
	})
	return out, err
}
func validRole(role string) bool {
	return slices.Contains([]string{"owner", "maintainer", "developer", "guest"}, role)
}
func lastOwner(snap *types.Snapshot, workspaceID, userID string) bool {
	for _, m := range snap.Memberships {
		u := user(snap, m.UserID)
		if m.WorkspaceID == workspaceID && m.Role == "owner" && m.UserID != userID && u != nil && !u.Disabled {
			return false
		}
	}
	return true
}
func (s *State) SetMembership(actorID, workspaceID, userID, role string) (types.Membership, error) {
	var out types.Membership
	err := s.mutate(func(snap *types.Snapshot) error {
		w := workspace(snap, workspaceID)
		if w == nil {
			return problem(types.KindNotFound, "workspace not found")
		}
		if err := requireManager(snap, actorID, w.ID); err != nil {
			return err
		}
		if !validRole(role) {
			return problem(types.KindInvalid, "invalid membership role")
		}
		if _, err := requireTargetUser(snap, userID); err != nil {
			return err
		}
		actor := user(snap, actorID)
		current := member(snap, w.ID, userID)
		actorMember := member(snap, w.ID, actorID)
		if !actor.Admin && (role == "owner" || role == "maintainer" || (current != nil && (current.Role == "owner" || current.Role == "maintainer"))) && (actorMember == nil || actorMember.Role != "owner") {
			return problem(types.KindForbidden, "owner required to change privileged membership")
		}
		if current != nil && current.Role == "owner" && role != "owner" && lastOwner(snap, w.ID, userID) {
			return problem(types.KindConflict, "workspace must retain an active owner")
		}
		out = types.Membership{WorkspaceID: w.ID, UserID: userID, Role: role}
		if current == nil {
			snap.Memberships = append(snap.Memberships, out)
		} else {
			*current = out
		}
		// A role change moves the role ceiling of this user's devices here.
		for i := range snap.Agents {
			if a := &snap.Agents[i]; a.WorkspaceID == w.ID && isUser(a.Principal, userID) {
				refreshEffective(snap, a)
			}
		}
		audit(snap, actorID, "membership.set", userID)
		return nil
	})
	return out, err
}
func (s *State) RemoveMembership(actorID, workspaceID, userID string) error {
	return s.mutate(func(snap *types.Snapshot) error {
		w := workspace(snap, workspaceID)
		if w == nil {
			return problem(types.KindNotFound, "workspace not found")
		}
		if err := requireManager(snap, actorID, w.ID); err != nil {
			return err
		}
		m := member(snap, w.ID, userID)
		if m == nil {
			return problem(types.KindNotFound, "membership not found")
		}
		actor := user(snap, actorID)
		am := member(snap, w.ID, actorID)
		if !actor.Admin && (m.Role == "owner" || m.Role == "maintainer") && (am == nil || am.Role != "owner") {
			return problem(types.KindForbidden, "owner required")
		}
		if m.Role == "owner" && lastOwner(snap, w.ID, userID) {
			return problem(types.KindConflict, "workspace must retain an active owner")
		}
		snap.Memberships = slices.DeleteFunc(snap.Memberships, func(v types.Membership) bool { return v.WorkspaceID == w.ID && v.UserID == userID })
		revokePrincipal(snap, actorID, userPrincipal(userID), w.ID, "membership.remove")
		audit(snap, actorID, "membership.remove", userID)
		return nil
	})
}
func (s *State) DisableUser(actorID, userID string) error {
	return s.mutate(func(snap *types.Snapshot) error {
		if err := requireAdmin(snap, actorID); err != nil {
			return err
		}
		u := user(snap, userID)
		if u == nil {
			return problem(types.KindNotFound, "user not found")
		}
		if u.Admin && !u.Disabled && activeAdmins(snap) < 2 {
			return problem(types.KindConflict, "instance must retain an active administrator")
		}
		for _, m := range snap.Memberships {
			if m.UserID == u.ID && m.Role == "owner" && lastOwner(snap, m.WorkspaceID, u.ID) && !solePersonalWorkspace(snap, m.WorkspaceID, u.ID) {
				return problem(types.KindConflict, "transfer workspace ownership before disabling this user")
			}
		}
		u.Disabled = true
		revokePrincipal(snap, actorID, userPrincipal(u.ID), "", "user.disable")
		for i := range snap.Tokens {
			if snap.Tokens[i].Kind == "user" && snap.Tokens[i].UserID == u.ID {
				snap.Tokens[i].Revoked = true
			}
		}
		audit(snap, actorID, "user.disable", u.ID)
		return nil
	})
}

// revokePrincipal is the single cascade for a User or Service Account losing
// its principal status, everywhere (workspaceID == "") or in one Workspace: its
// devices are revoked, and its unused enrollment keys and their tokens can no
// longer enroll. A Service Account's devices never follow a User's lifecycle.
func revokePrincipal(snap *types.Snapshot, actorID string, p api.Principal, workspaceID, reason string) {
	in := func(ws string) bool { return workspaceID == "" || ws == workspaceID }
	sa := p.Type == api.PrincipalServiceAccount
	for i := range snap.Agents {
		if a := &snap.Agents[i]; a.Principal == p && in(a.WorkspaceID) {
			revokeAgent(snap, a, actorID, reason)
		}
	}
	for i := range snap.EnrollmentKeys {
		if k := &snap.EnrollmentKeys[i]; ((sa && k.ServiceAccountID == p.ID) || (!sa && k.UserID == p.ID)) && in(k.WorkspaceID) {
			revokeEnrollmentKey(snap, k)
		}
	}
	for i := range snap.Tokens {
		if t := &snap.Tokens[i]; ((sa && t.ServiceAccountID == p.ID) || (!sa && t.UserID == p.ID)) && t.WorkspaceID != "" && in(t.WorkspaceID) {
			t.Revoked = true
		}
	}
}

// revokeAgent is terminal. The device's tokens stop authenticating, its
// effective configuration empties, and egress routes that exit through it are
// disabled so they cannot shadow other routes. Its names stay reserved until
// claimLabel reclaims them.
func revokeAgent(snap *types.Snapshot, a *types.Agent, actorID, reason string) {
	if a.Status == api.StatusRevoked {
		return
	}
	a.Status = api.StatusRevoked
	a.RevokedAt = time.Now().UTC()
	refreshEffective(snap, a)
	for i := range snap.Tokens {
		if snap.Tokens[i].AgentID == a.ID {
			snap.Tokens[i].Revoked = true
		}
	}
	for i := range snap.EgressRoutes {
		if r := &snap.EgressRoutes[i]; r.AgentID == a.ID && r.Enabled {
			r.Enabled = false
			auditDetail(snap, actorID, "egress_route.enabled", r.ID, "disabled: exit agent revoked")
		}
	}
	auditDetail(snap, actorID, "agent.revoke", a.ID, reason)
}
func revokeEnrollmentKey(snap *types.Snapshot, k *types.EnrollmentKey) {
	k.Revoked = true
	for i := range snap.Tokens {
		if snap.Tokens[i].Kind == "enrollment" && snap.Tokens[i].ID == k.ID {
			snap.Tokens[i].Revoked = true
		}
	}
}

// CreateEnrollmentKey issues an unattended registration key for a Service
// Account. Personal devices register through Device Flow (agentd login); no
// new User-bound keys are created.
func (s *State) CreateEnrollmentKey(actorID string, key types.EnrollmentKey) (types.EnrollmentKey, string, error) {
	plain := randomToken()
	err := s.mutate(func(snap *types.Snapshot) error {
		if key.UserID != "" || key.ServiceAccountID == "" {
			return problem(types.KindInvalid, "enrollment keys bind a service account; register personal devices with agentd login")
		}
		sa := serviceAccount(snap, key.ServiceAccountID)
		if sa == nil || (key.WorkspaceID != "" && sa.WorkspaceID != key.WorkspaceID) {
			return problem(types.KindNotFound, "service account not found")
		}
		if err := requireManager(snap, actorID, sa.WorkspaceID); err != nil {
			return err
		}
		if sa.Status != api.StatusActive {
			return problem(types.KindForbidden, "service account is revoked")
		}
		if len(key.Roles) == 0 {
			return problem(types.KindInvalid, "enrollment roles required")
		}
		seen := map[string]bool{}
		for _, role := range key.Roles {
			if !slices.Contains([]string{api.RoleIngress, api.RoleServe, api.RoleEgress}, role) || seen[role] {
				return problem(types.KindInvalid, "invalid or duplicate enrollment role")
			}
			seen[role] = true
		}
		if key.MaxUses == 0 {
			key.MaxUses = 1
		}
		if key.MaxUses < 1 || key.MaxUses > 10000 {
			return problem(types.KindInvalid, "invalid key usage limit")
		}
		if key.ExpiresAt.IsZero() {
			key.ExpiresAt = time.Now().Add(24 * time.Hour)
		}
		if !key.ExpiresAt.After(time.Now()) || key.ExpiresAt.After(time.Now().Add(TokenTTL)) {
			return problem(types.KindInvalid, "key expiration must be within 30 days")
		}
		key.ID = randomID()
		key.WorkspaceID = sa.WorkspaceID
		key.CreatedBy = actorID
		key.Uses = 0
		key.Revoked = false
		snap.EnrollmentKeys = append(snap.EnrollmentKeys, key)
		token := tokenRecord("enrollment", plain, servicePrincipal(sa.ID), sa.WorkspaceID, "", key.ExpiresAt)
		token.ID = key.ID
		snap.Tokens = append(snap.Tokens, token)
		audit(snap, actorID, "enrollment_key.create", key.ID)
		return nil
	})
	if err != nil {
		plain = ""
	}
	return key, plain, err
}

// claimLabel reserves one DNS label in a Workspace namespace shared by Agents
// and services. A revoked Agent's labels are reclaimable at once by the same
// principal and by anyone else after NameTombstoneTTL; reclaiming purges the revoked
// holder, so stale grants and routes can never attach to the new owner.
func claimLabel(snap *types.Snapshot, workspaceID, name string, owner api.Principal, actorID string) error {
	now := time.Now()
	releasable := func(a *types.Agent) bool {
		return a != nil && a.Status == api.StatusRevoked && (a.Principal == owner || !now.Before(a.RevokedAt.Add(NameTombstoneTTL)))
	}
	for i := range snap.Agents {
		if a := &snap.Agents[i]; a.WorkspaceID == workspaceID && a.Name == name {
			if !releasable(a) {
				return problem(types.KindConflict, "name already reserved")
			}
			purgeAgent(snap, a.ID, actorID)
			break
		}
	}
	for i := range snap.Services {
		if v := snap.Services[i]; v.WorkspaceID == workspaceID && v.Name == name {
			if !releasable(agent(snap, v.AgentID)) {
				return problem(types.KindConflict, "name already reserved")
			}
			purgeService(snap, v.ID)
			auditDetail(snap, actorID, "service.purge", v.ID, "name reclaimed: "+name)
			break
		}
	}
	return nil
}
func purgeAgent(snap *types.Snapshot, id, actorID string) {
	for _, v := range slices.Clone(snap.Services) {
		if v.AgentID == id {
			purgeService(snap, v.ID)
		}
	}
	routes := map[string]bool{}
	for _, r := range snap.EgressRoutes {
		if r.AgentID == id {
			routes[r.ID] = true
		}
	}
	snap.Grants = slices.DeleteFunc(snap.Grants, func(g types.Grant) bool { return g.ResourceType == "egress" && routes[g.ResourceID] })
	snap.EgressRoutes = slices.DeleteFunc(snap.EgressRoutes, func(r types.EgressRoute) bool { return routes[r.ID] })
	snap.Tokens = slices.DeleteFunc(snap.Tokens, func(t types.Token) bool { return t.AgentID == id })
	snap.Agents = slices.DeleteFunc(snap.Agents, func(a types.Agent) bool { return a.ID == id })
	audit(snap, actorID, "agent.purge", id)
}
func purgeService(snap *types.Snapshot, id string) {
	snap.Grants = slices.DeleteFunc(snap.Grants, func(g types.Grant) bool { return g.ResourceType == "service" && g.ResourceID == id })
	snap.Services = slices.DeleteFunc(snap.Services, func(v types.Service) bool { return v.ID == id })
}

// roleCeiling is the enrollment role ceiling. A User principal is further
// narrowed by the current Workspace role (guests may only run ingress); a
// Service Account has no human role, only its registration's ceiling.
func roleCeiling(snap *types.Snapshot, a *types.Agent) []string {
	if a.Principal.Type == api.PrincipalServiceAccount {
		if !principalActive(snap, a.WorkspaceID, a.Principal) {
			return nil
		}
		return slices.Clone(a.Roles)
	}
	m := member(snap, a.WorkspaceID, a.Principal.ID)
	if m == nil {
		return nil
	}
	if m.Role == "guest" {
		if slices.Contains(a.Roles, api.RoleIngress) {
			return []string{api.RoleIngress}
		}
		return nil
	}
	return slices.Clone(a.Roles)
}

// checkDesired is the automatic admission policy for a desired spec. It never
// queues anything for a person: a spec is accepted as a whole or rejected.
func checkDesired(snap *types.Snapshot, a *types.Agent, desired api.AgentSpec) error {
	ceiling := roleCeiling(snap, a)
	for _, role := range desired.Roles() {
		if !slices.Contains(ceiling, role) {
			return problem(types.KindForbidden, "desired role "+role+" exceeds the enrollment key or workspace role")
		}
	}
	return nil
}

// computeEffective = Desired ∩ role ceiling ∩ built-in Workspace policy. A
// revoked or principal-less Agent has an empty effective configuration.
func computeEffective(snap *types.Snapshot, a *types.Agent) api.EffectiveConfig {
	e := api.EffectiveConfig{Policy: PolicyVersion}
	if !agentAllowed(snap, a) {
		return e
	}
	ceiling := roleCeiling(snap, a)
	e.Ingress = a.Desired.Ingress.Enabled && slices.Contains(ceiling, api.RoleIngress)
	if a.Desired.Serve.Enabled && slices.Contains(ceiling, api.RoleServe) {
		for _, desired := range a.Desired.Serve.Services {
			for _, v := range snap.Services {
				if v.AgentID == a.ID && v.Name == desired.Name {
					e.Services = append(e.Services, api.EffectiveService{Name: v.Name, Origin: desired.Origin, Hostname: v.Hostname})
				}
			}
		}
	}
	if a.Desired.Egress.Enabled && slices.Contains(ceiling, api.RoleEgress) {
		e.Egress, e.Deny = api.CloneRules(a.Desired.Egress.Allow), api.CloneRules(a.Desired.Egress.Deny)
	}
	return e
}

const policyRejectionPrefix = "current policy excludes desired roles: "

// refreshEffective recomputes the effective configuration, advancing the
// per-Agent revision only when content changes. Desired roles the current
// ceiling drops (after a demotion, or from a migrated pending request) are
// reported as a rejection instead of waiting for anyone.
func refreshEffective(snap *types.Snapshot, a *types.Agent) {
	next := computeEffective(snap, a)
	if a.Effective.Revision == 0 || !a.Effective.SameContent(next) {
		next.Revision = a.Effective.Revision + 1
		a.Effective = next
	}
	var dropped []string
	if a.Status == api.StatusActive {
		ceiling := roleCeiling(snap, a)
		for _, role := range a.Desired.Roles() {
			if !slices.Contains(ceiling, role) {
				dropped = append(dropped, role)
			}
		}
	}
	message := policyRejectionPrefix + strings.Join(dropped, ", ")
	switch {
	case len(dropped) > 0 && (a.LastRejection == nil || a.LastRejection.Message != message):
		a.LastRejection = &api.ConfigRejection{At: time.Now().UTC(), Kind: string(types.KindForbidden), Message: message}
	case len(dropped) == 0 && a.LastRejection != nil && strings.HasPrefix(a.LastRejection.Message, policyRejectionPrefix):
		a.LastRejection = nil
	}
}

// reconcileAgents makes stored status and effective configuration consistent
// with the current principals, e.g. after migrating the removed approval model.
func reconcileAgents(snap *types.Snapshot) {
	for i := range snap.Agents {
		a := &snap.Agents[i]
		if a.Status != api.StatusRevoked && a.Status != api.StatusActive {
			a.Status = api.StatusActive
		}
		if a.Status == api.StatusActive && !agentAllowed(snap, a) {
			revokeAgent(snap, a, "system", "principal inactive at reconciliation")
			continue
		}
		refreshEffective(snap, a)
	}
}

// registration is the validated input shared by key and Device Flow enrollment.
type registration struct {
	workspace *types.Workspace
	principal api.Principal
	roles     []string
	createdBy string
	name      string
	desired   api.AgentSpec
}

// newRegistration validates the identity-independent parts of a request.
func newRegistration(name string, desired api.AgentSpec) (registration, error) {
	name, err := label(name)
	if err != nil {
		return registration{}, err
	}
	desired = desired.Clone()
	if err := desired.Normalize(name); err != nil {
		return registration{}, problem(types.KindInvalid, "invalid desired configuration: "+err.Error())
	}
	return registration{name: name, desired: desired}, nil
}

// register creates an Agent inside a mutation: the desired spec is checked
// against the principal's role ceiling, names are claimed and every device
// credential is bound to the principal. Nothing waits for approval.
func (s *State) register(snap *types.Snapshot, r registration) (types.EnrollResponse, error) {
	controlToken, providerToken, accessToken := randomToken(), randomToken(), randomToken()
	w := r.workspace
	a := types.Agent{ID: randomID(), WorkspaceID: w.ID, Principal: r.principal, CreatedBy: r.createdBy, Name: r.name, Status: api.StatusActive, Desired: r.desired, Roles: slices.Clone(r.roles), CreatedAt: time.Now().UTC()}
	if err := checkDesired(snap, &a, r.desired); err != nil {
		return types.EnrollResponse{}, err
	}
	if err := claimLabel(snap, w.ID, r.name, r.principal, r.createdBy); err != nil {
		return types.EnrollResponse{}, err
	}
	snap.Agents = append(snap.Agents, a)
	if err := s.ensureServices(snap, a.ID); err != nil {
		return types.EnrollResponse{}, err
	}
	ap := agent(snap, a.ID)
	refreshEffective(snap, ap)
	expiry := time.Now().Add(TokenTTL)
	snap.Tokens = append(snap.Tokens, tokenRecord("control", controlToken, a.Principal, w.ID, a.ID, expiry))
	principal := a.Principal
	creds := api.Credentials{Version: api.CredentialsVersion, Workspace: w.Name, AgentID: a.ID, Principal: &principal, ControlToken: controlToken}
	if slices.Contains(a.Roles, api.RoleServe) || slices.Contains(a.Roles, api.RoleEgress) {
		snap.Tokens = append(snap.Tokens, tokenRecord("provider", providerToken, a.Principal, w.ID, a.ID, expiry))
		creds.ProviderToken = providerToken
	}
	if slices.Contains(a.Roles, api.RoleIngress) {
		snap.Tokens = append(snap.Tokens, tokenRecord("access", accessToken, a.Principal, w.ID, a.ID, expiry))
		creds.AccessToken = accessToken
	}
	view := agentView(snap, *ap)
	creds.Effective = view.Effective
	return types.EnrollResponse{Agent: view, Credentials: creds}, nil
}

// Enroll registers an unattended device with an enrollment key. Service
// Account keys run the device as that Service Account; legacy User keys keep
// working until they expire or are revoked.
func (s *State) Enroll(request types.EnrollRequest) (types.EnrollResponse, error) {
	var response types.EnrollResponse
	r, err := newRegistration(request.Name, request.Desired)
	if err != nil {
		return response, err
	}
	err = s.mutate(func(snap *types.Snapshot) error {
		tk, err := tokenIn(snap, request.EnrollmentKey)
		if err != nil {
			return err
		}
		if tk.Kind != "enrollment" {
			return problem(types.KindUnauthorized, "an enrollment key is required")
		}
		var key *types.EnrollmentKey
		for i := range snap.EnrollmentKeys {
			if snap.EnrollmentKeys[i].ID == tk.ID {
				key = &snap.EnrollmentKeys[i]
			}
		}
		if key == nil || key.Revoked || key.Uses >= key.MaxUses || !key.ExpiresAt.After(time.Now()) {
			return problem(types.KindForbidden, "enrollment key unavailable")
		}
		r.workspace = workspace(snap, key.WorkspaceID)
		if r.workspace == nil || request.Workspace != r.workspace.Name {
			return problem(types.KindForbidden, "enrollment key belongs to a different workspace")
		}
		r.principal, r.roles, r.createdBy = userPrincipal(key.UserID), key.Roles, key.CreatedBy
		if key.ServiceAccountID != "" {
			r.principal = servicePrincipal(key.ServiceAccountID)
		}
		if !principalActive(snap, key.WorkspaceID, r.principal) {
			return problem(types.KindForbidden, "enrollment principal is unavailable or no longer a member")
		}
		if response, err = s.register(snap, r); err != nil {
			return err
		}
		key.Uses++
		auditDetail(snap, r.principal.Subject(), "agent.enroll", response.Agent.ID, "enrollment key "+key.ID)
		return nil
	})
	return response, err
}

// ensureServices reserves a service record for every desired service. A newly
// published service receives the Workspace default member grant once; later
// changes to that grant are explicit grant management, never recreated here.
// Reclaiming a name can purge other Agents, so it takes an ID: callers must not
// hold pointers into snap.Agents across this call.
func (s *State) ensureServices(snap *types.Snapshot, agentID string) error {
	a := *agent(snap, agentID)
	if !a.Desired.Serve.Enabled {
		return nil
	}
	w := workspace(snap, a.WorkspaceID)
	for _, desired := range a.Desired.Serve.Services {
		found := false
		for _, v := range snap.Services {
			if v.AgentID == a.ID && v.Name == desired.Name {
				found = true
			}
		}
		if found {
			continue
		}
		if desired.Name == a.Name {
			return problem(types.KindConflict, "service name already reserved")
		}
		if err := claimLabel(snap, a.WorkspaceID, desired.Name, a.Principal, a.CreatedBy); err != nil {
			return problem(types.KindConflict, "service name already reserved")
		}
		v := types.Service{ID: randomID(), WorkspaceID: a.WorkspaceID, AgentID: a.ID, Name: desired.Name, Hostname: desired.Name + "." + w.Name + "." + s.baseDomain, Enabled: true}
		snap.Services = append(snap.Services, v)
		now := time.Now().UTC()
		snap.Grants = append(snap.Grants, types.Grant{ID: randomID(), WorkspaceID: a.WorkspaceID, ResourceType: "service", ResourceID: v.ID, SubjectType: "workspace", SubjectID: a.WorkspaceID,
			StartsAt: now, ExpiresAt: now.Add(s.serviceGrantTTL), CreatedBy: defaultGrantCreator})
		audit(snap, a.Principal.Subject(), "service.publish", v.ID)
	}
	return nil
}

// agentView is the API projection: workspace name and the validity window of
// the effective configuration are filled in at read time.
func agentView(snap *types.Snapshot, a types.Agent) api.Agent {
	v := api.Agent{ID: a.ID, WorkspaceID: a.WorkspaceID, Principal: a.Principal, CreatedBy: a.CreatedBy, Name: a.Name, Status: a.Status, Roles: slices.Clone(a.Roles),
		Desired: a.Desired.Clone(), Effective: a.Effective.Clone(), CreatedAt: a.CreatedAt, RevokedAt: a.RevokedAt}
	if v.Roles == nil {
		v.Roles = []string{}
	}
	if w := workspace(snap, a.WorkspaceID); w != nil {
		v.Workspace = w.Name
	}
	if a.LastRejection != nil {
		rejection := *a.LastRejection
		v.LastRejection = &rejection
	}
	if agentAllowed(snap, &a) {
		v.Effective.ExpiresAt = time.Now().Add(EffectiveTTL).UTC()
	}
	return v
}
func (s *State) Agent(id string) (api.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a := agent(&s.snapshot, id)
	if a == nil {
		return api.Agent{}, problem(types.KindNotFound, "agent not found")
	}
	return agentView(&s.snapshot, *a), nil
}

// AgentViews projects a bounded set of already-authorized device references
// against one snapshot. Callers must scope IDs before invoking it.
func (s *State) AgentViews(ids []string) (map[string]api.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	out := make(map[string]api.Agent, len(wanted))
	for _, a := range s.snapshot.Agents {
		if wanted[a.ID] {
			out[a.ID] = agentView(&s.snapshot, a)
		}
	}
	return out, nil
}

func (s *State) GetAgentCredentials(controlToken string) (api.Credentials, error) {
	if err := s.renewAgentTokens(controlToken); err != nil {
		return api.Credentials{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, err := identity(&s.snapshot, controlToken)
	if err != nil {
		return api.Credentials{}, err
	}
	if id.Kind != "control" {
		return api.Credentials{}, problem(types.KindUnauthorized, "agent control token required")
	}
	a := agentView(&s.snapshot, *agent(&s.snapshot, id.AgentID))
	return api.Credentials{Version: api.CredentialsVersion, Workspace: a.Workspace, AgentID: a.ID, Effective: a.Effective}, nil
}

func agentTokensNeedRenewal(snap *types.Snapshot, agentID string, now time.Time) bool {
	for _, token := range snap.Tokens {
		if token.AgentID == agentID && !token.Revoked && (token.Kind == "control" || token.Kind == "provider" || token.Kind == "access") && !token.ExpiresAt.IsZero() && token.ExpiresAt.Before(now.Add(TokenRenewBefore)) {
			return true
		}
	}
	return false
}

// Normal polling is read-only. Renewal commits only close to expiry and keeps
// hashes/IDs unchanged so established, independently authorized flows survive.
func (s *State) renewAgentTokens(plain string) error {
	s.mu.RLock()
	id, err := identity(&s.snapshot, plain)
	needed := false
	if err == nil && id.Kind == "control" {
		needed = agentTokensNeedRenewal(&s.snapshot, id.AgentID, time.Now())
	}
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if id.Kind != "control" {
		return problem(types.KindUnauthorized, "agent control token required")
	}
	if !needed {
		return nil
	}
	return s.mutate(func(snap *types.Snapshot) error {
		current, err := identity(snap, plain)
		if err != nil {
			return err
		}
		if current.Kind != "control" {
			return problem(types.KindUnauthorized, "agent control token required")
		}
		now := time.Now()
		if !agentTokensNeedRenewal(snap, current.AgentID, now) {
			return nil
		}
		for i := range snap.Tokens {
			token := &snap.Tokens[i]
			if token.AgentID == current.AgentID && !token.Revoked && (token.Kind == "control" || token.Kind == "provider" || token.Kind == "access") {
				token.ExpiresAt = now.Add(TokenTTL).UTC()
			}
		}
		audit(snap, current.Principal.Subject(), "agent.tokens.renew", current.AgentID)
		return nil
	})
}

// RotateUserToken replaces API/login credentials without changing stable user
// IDs, grants or existing browser sessions. Old bearer tokens stay recognizable
// to the gateway but can no longer authenticate.
func (s *State) RotateUserToken(actorID, userID string) (string, error) {
	plain := randomToken()
	err := s.mutate(func(snap *types.Snapshot) error {
		actor, err := requireUser(snap, actorID)
		if err != nil {
			return err
		}
		if actorID != userID && !actor.Admin {
			return problem(types.KindForbidden, "only the user or an instance administrator can rotate a user token")
		}
		target := user(snap, userID)
		if target == nil {
			return problem(types.KindNotFound, "user not found")
		}
		if target.Disabled {
			return problem(types.KindForbidden, "user is disabled")
		}
		for i := range snap.Tokens {
			if snap.Tokens[i].Kind == "user" && snap.Tokens[i].UserID == userID {
				snap.Tokens[i].Revoked = true
			}
		}
		snap.Tokens = append(snap.Tokens, tokenRecord("user", plain, userPrincipal(userID), "", "", time.Now().Add(TokenTTL).UTC()))
		audit(snap, actorID, "user.token.rotate", userID)
		return nil
	})
	if err != nil {
		return "", err
	}
	return plain, nil
}

// UpdateAgent automatically validates and applies a device's desired spec.
// Invalid (400), forbidden (403) and conflicting (409) specs are rejected as a
// whole; the previous effective configuration keeps running and the rejection
// is recorded for operators. Nothing waits for manual approval.
func (s *State) UpdateAgent(controlToken string, update api.ConfigUpdate) (api.Agent, error) {
	desired := update.Desired.Clone()
	var id string
	err := s.mutate(func(snap *types.Snapshot) error {
		ident, err := identity(snap, controlToken)
		if err != nil {
			return err
		}
		if ident.Kind != "control" {
			return problem(types.KindUnauthorized, "agent control token required")
		}
		a := agent(snap, ident.AgentID)
		if update.Workspace != ident.Workspace || update.Name != a.Name {
			return problem(types.KindForbidden, "cannot switch registered workspace or name")
		}
		id = a.ID
		if err := desired.Normalize(a.Name); err != nil {
			return problem(types.KindInvalid, "invalid desired configuration: "+err.Error())
		}
		if err := checkDesired(snap, a, desired); err != nil {
			return err
		}
		if reflect.DeepEqual(a.Desired, desired) && (a.LastRejection == nil || strings.HasPrefix(a.LastRejection.Message, policyRejectionPrefix)) {
			return nil
		}
		a.Desired, a.LastRejection = desired, nil
		if err := s.ensureServices(snap, a.ID); err != nil {
			return err
		}
		a = agent(snap, id)
		refreshEffective(snap, a)
		audit(snap, a.Principal.Subject(), "agent.desired.apply", a.ID)
		return nil
	})
	if err != nil {
		if kind := types.ErrorKind(types.KindOf(err)); id != "" && (kind == types.KindInvalid || kind == types.KindForbidden || kind == types.KindConflict) {
			s.recordRejection(id, kind, err.Error())
		}
		return api.Agent{}, err
	}
	return s.Agent(id)
}

// recordRejection is best effort: the device already received the error.
func (s *State) recordRejection(agentID string, kind types.ErrorKind, message string) {
	_ = s.mutate(func(snap *types.Snapshot) error {
		a := agent(snap, agentID)
		if a == nil || (a.LastRejection != nil && a.LastRejection.Kind == string(kind) && a.LastRejection.Message == message) {
			return nil
		}
		a.LastRejection = &api.ConfigRejection{At: time.Now().UTC(), Kind: string(kind), Message: message}
		auditDetail(snap, a.Principal.Subject(), "agent.desired.reject", a.ID, message)
		return nil
	})
}

// SnapshotForUser returns a detached, scoped view; token hashes never leave state.
func (s *State) SnapshotForUser(userID string) (types.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, err := requireUser(&s.snapshot, userID)
	if err != nil {
		return types.Snapshot{}, err
	}
	snap, err := clone(s.snapshot)
	if err != nil {
		return snap, err
	}
	snap.Tokens = nil
	snap.OIDCIdentities = nil
	snap.EnrollmentAuthorizations = nil
	if u.Admin {
		return snap, nil
	}
	visible := map[string]bool{}
	managed := map[string]bool{}
	for _, m := range snap.Memberships {
		if m.UserID == userID {
			visible[m.WorkspaceID] = true
			managed[m.WorkspaceID] = m.Role == "owner" || m.Role == "maintainer"
		}
	}
	snap.Workspaces = slices.DeleteFunc(snap.Workspaces, func(w types.Workspace) bool { return !visible[w.ID] })
	snap.Memberships = slices.DeleteFunc(snap.Memberships, func(m types.Membership) bool { return !visible[m.WorkspaceID] })
	users := map[string]bool{userID: true}
	for _, m := range snap.Memberships {
		users[m.UserID] = true
	}
	snap.Users = slices.DeleteFunc(snap.Users, func(v types.User) bool { return !users[v.ID] })
	snap.Agents = slices.DeleteFunc(snap.Agents, func(a types.Agent) bool {
		return !visible[a.WorkspaceID] || (!managed[a.WorkspaceID] && !isUser(a.Principal, userID))
	})
	snap.EnrollmentKeys = slices.DeleteFunc(snap.EnrollmentKeys, func(k types.EnrollmentKey) bool {
		return !visible[k.WorkspaceID] || (!managed[k.WorkspaceID] && k.UserID != userID)
	})
	// Service Accounts are visible and manageable only by maintainer+.
	snap.ServiceAccounts = slices.DeleteFunc(snap.ServiceAccounts, func(v types.ServiceAccount) bool { return !managed[v.WorkspaceID] })
	snap.Services = slices.DeleteFunc(snap.Services, func(v types.Service) bool { return !visible[v.WorkspaceID] })
	snap.EgressRoutes = slices.DeleteFunc(snap.EgressRoutes, func(v types.EgressRoute) bool { return !visible[v.WorkspaceID] })
	// Match the existing service-management authority: developers can manage
	// grants on their own services, while egress and other service grants remain
	// visible only to workspace managers.
	snap.Grants = slices.DeleteFunc(snap.Grants, func(g types.Grant) bool {
		if managed[g.WorkspaceID] {
			return false
		}
		if !visible[g.WorkspaceID] || g.ResourceType != "service" {
			return true
		}
		membership := member(&snap, g.WorkspaceID, userID)
		if membership == nil || membership.Role != "developer" {
			return true
		}
		resource := service(&snap, g.ResourceID)
		if resource == nil {
			return true
		}
		provider := agent(&snap, resource.AgentID)
		return provider == nil || !isUser(provider.Principal, userID)
	})
	snap.Audit = nil
	return snap, nil
}
