package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
)

func TestSQLiteCompositeForeignKeysAndRollback(t *testing.T) {
	store, err := Open(t.Context(), Options{DSN: filepath.Join(t.TempDir(), "db.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.sql.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO users(id,name,payload) VALUES('u','alice','{}')")
	exec("INSERT INTO workspaces(id,name,payload) VALUES('w1','one','{}'),('w2','two','{}')")
	exec("INSERT INTO agents(id,workspace_id,user_id,name,payload) VALUES('a','w1','u','agent','{}')")
	if _, err := store.sql.Exec("INSERT INTO services(id,workspace_id,agent_id,name,hostname,payload) VALUES('foreign','w2','a','demo','demo.two.test','{}')"); err == nil {
		t.Fatal("cross-workspace service FK accepted")
	}
	exec("INSERT INTO services(id,workspace_id,agent_id,name,hostname,payload) VALUES('svc','w1','a','demo','demo.one.test','{}')")
	if _, err := store.sql.Exec("INSERT INTO grants(id,workspace_id,resource_type,service_id,subject_type,payload) VALUES('g','w2','service','svc','anonymous','{}')"); err == nil {
		t.Fatal("cross-workspace grant FK accepted")
	}
	if _, err := store.sql.Exec("INSERT INTO memberships(workspace_id,user_id,role,payload) VALUES('w1','u','superuser','{}')"); err == nil {
		t.Fatal("undefined membership role accepted")
	}
}

func TestVersionOneMigrationPreservesUsersTokensAndRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range schema {
		if _, err := legacy.ExecContext(t.Context(), statement); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	user := types.User{ID: "existing-user", Name: "existing-admin", Admin: true}
	userJSON, _ := json.Marshal(user)
	token := types.Token{ID: "existing-token", Hash: "existing-private-token-hash", Kind: "user", UserID: user.ID}
	tokenJSON, _ := json.Marshal(token)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO users(id,name,payload) VALUES($1,$2,$3)", []any{user.ID, user.Name, string(userJSON)}},
		{"INSERT INTO tokens(id,hash,kind,user_id,payload) VALUES($1,$2,$3,$4,$5)", []any{token.ID, token.Hash, token.Kind, token.UserID, string(tokenJSON)}},
		{"UPDATE control_meta SET revision=17 WHERE id=1", nil},
	} {
		if _, err := legacy.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), Options{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 17 || len(loaded.Users) != 1 || loaded.Users[0] != user || len(loaded.Tokens) != 1 || loaded.Tokens[0] != token {
		t.Fatalf("migration altered v1 data: %+v", loaded)
	}
	var version int
	if err := store.sql.QueryRow("SELECT schema_version FROM control_meta WHERE id=1").Scan(&version); err != nil || version != 4 {
		t.Fatalf("migration version %d: %v", version, err)
	}
	previous := loaded
	loaded.Revision++
	loaded.OIDCIdentities = []types.OIDCIdentity{{Issuer: "https://idp.example", Subject: "stable-subject", UserID: user.ID}}
	if err := store.Save(t.Context(), previous, loaded); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), Options{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.Load(t.Context())
	if err != nil || len(after.OIDCIdentities) != 1 || after.OIDCIdentities[0] != loaded.OIDCIdentities[0] || len(after.Tokens) != 1 || after.Tokens[0] != token {
		t.Fatalf("OIDC binding did not persist alongside original token: %v", err)
	}
}

func TestSaveRevisionConflictAndTransactionRollback(t *testing.T) {
	store, err := Open(context.Background(), Options{DSN: filepath.Join(t.TempDir(), "db.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := types.Snapshot{Revision: 1, Users: []types.User{{ID: "u", Name: "alice"}}}
	if err = store.Save(t.Context(), types.Snapshot{}, first); err != nil {
		t.Fatal(err)
	}
	bad := types.Snapshot{Revision: 2, Users: []types.User{{ID: "u2", Name: "bad"}}, Agents: []types.Agent{{ID: "a", WorkspaceID: "nonexistent", Principal: api.Principal{Type: api.PrincipalUser, ID: "u2"}, Name: "invalid"}}}
	if err = store.Save(t.Context(), first, bad); err == nil {
		t.Fatal("invalid snapshot committed")
	}
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || len(loaded.Users) != 1 || loaded.Users[0].Name != "alice" {
		t.Fatal("failed transaction partially changed control state")
	}
	if err = store.Save(t.Context(), types.Snapshot{}, types.Snapshot{Revision: 1}); types.KindOf(err) != "conflict" {
		t.Fatalf("stale control writer was not fenced: %v", err)
	}
}

func TestApprovalRemovalMigrationKeepsTraceAndGrantScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := append(append([]string(nil), schema...),
		`CREATE TABLE oidc_identities (issuer TEXT NOT NULL, subject TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), payload TEXT NOT NULL, PRIMARY KEY(issuer,subject))`,
		"UPDATE control_meta SET schema_version=2, revision=40 WHERE id=1",
		`INSERT INTO users(id,name,payload) VALUES('owner','owner','{"id":"owner","name":"owner"}'),('dev','dev','{"id":"dev","name":"dev"}'),('gone','gone','{"id":"gone","name":"gone"}'),('outsider','outsider','{"id":"outsider","name":"outsider"}')`,
		`INSERT INTO workspaces(id,name,payload) VALUES('w','work','{"id":"w","name":"work"}')`,
		`INSERT INTO memberships(workspace_id,user_id,role,payload) VALUES('w','owner','owner','{"workspaceId":"w","userId":"owner","role":"owner"}'),('w','dev','developer','{"workspaceId":"w","userId":"dev","role":"developer"}')`,
		`INSERT INTO agents(id,workspace_id,user_id,name,payload) VALUES('a','w','dev','laptop','{"id":"a","workspaceId":"w","workspace":"work","userId":"dev","name":"laptop","status":"pending","pendingApproval":true,"roles":["serve"],
			"desired":{"apiVersion":"agents/v1alpha1","agent":{"name":"laptop","stateDir":"/home/dev/state"},"serve":{"enabled":true,"services":[{"name":"demo","origin":"http://127.0.0.1:8080"}]}},
			"approval":{"revision":37,"expiresAt":"2026-01-01T00:00:00Z","services":{"demo":"http://127.0.0.1:7070"}}}')`,
		`INSERT INTO services(id,workspace_id,agent_id,name,hostname,payload) VALUES('s','w','a','demo','demo.work.example','{"id":"s","workspaceId":"w","agentId":"a","name":"demo","hostname":"demo.work.example","enabled":true}')`,
		`INSERT INTO enrollment_keys(id,workspace_id,user_id,payload) VALUES('k','w','dev','{"id":"k","workspaceId":"w","userId":"dev","roles":["serve"],"autoApprove":true,"maxUses":1}')`,
		`INSERT INTO grants(id,workspace_id,resource_type,service_id,subject_type,subject_user_id,payload) VALUES
			('by-dev','w','service','s','user','gone','{"id":"by-dev","workspaceId":"w","resourceType":"service","resourceId":"s","subjectType":"user","subjectId":"gone"}'),
			('by-owner','w','service','s','user','outsider','{"id":"by-owner","workspaceId":"w","resourceType":"service","resourceId":"s","subjectType":"user","subjectId":"outsider"}'),
			('member','w','service','s','user','dev','{"id":"member","workspaceId":"w","resourceType":"service","resourceId":"s","subjectType":"user","subjectId":"dev"}')`,
		`INSERT INTO audit(id,payload) VALUES('e1','{"id":"e1","actorId":"dev","action":"grant.create","resourceId":"by-dev"}'),('e2','{"id":"e2","actorId":"owner","action":"grant.create","resourceId":"by-owner"}')`,
	)
	for _, statement := range statements {
		if _, err := legacy.ExecContext(t.Context(), statement); err != nil {
			legacy.Close()
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), Options{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snap, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a := snap.Agents[0]
	if snap.Revision != 40 || a.Status != "active" || !a.Desired.Serve.Enabled || len(a.Desired.Serve.Services) != 1 || a.Effective.Revision != 37 ||
		len(a.Effective.Services) != 1 || a.Effective.Services[0].Origin != "http://127.0.0.1:7070" || a.Effective.Services[0].Hostname != "demo.work.example" {
		t.Fatalf("legacy agent was not converted to its approved effective configuration: %+v", a)
	}
	data, _ := json.Marshal(snap)
	if strings.Contains(string(data), "/home/dev/state") || strings.Contains(string(data), "autoApprove") || strings.Contains(string(data), "pendingApproval\":true,\"approval") {
		t.Fatal("local paths or approval fields survived the migration")
	}
	traced := false
	for _, e := range snap.Audit {
		traced = traced || e.Action == "agent.migrate.approval_removed" && e.ResourceID == "a" && strings.Contains(e.Detail, `"pendingApproval":true`) && strings.Contains(e.Detail, "7070")
	}
	if !traced {
		t.Fatal("legacy approval was not kept in audit")
	}
	external := map[string]bool{}
	for _, g := range snap.Grants {
		external[g.ID] = g.External
	}
	if external["by-dev"] || !external["by-owner"] || external["member"] {
		t.Fatalf("grant scope migrated incorrectly: %v", external)
	}
	if a.Principal != (api.Principal{Type: api.PrincipalUser, ID: "dev"}) || len(snap.EnrollmentKeys) != 1 || snap.EnrollmentKeys[0].UserID != "dev" || snap.EnrollmentKeys[0].ServiceAccountID != "" {
		t.Fatalf("legacy owners did not become user principals: %+v %+v", a.Principal, snap.EnrollmentKeys)
	}
	var version int
	var userID string
	var serviceAccountID sql.NullString
	if err = store.sql.QueryRow("SELECT schema_version FROM control_meta WHERE id=1").Scan(&version); err != nil || version != 4 {
		t.Fatalf("schema version %d, %v", version, err)
	}
	if err = store.sql.QueryRow("SELECT user_id, service_account_id FROM agents WHERE id='a'").Scan(&userID, &serviceAccountID); err != nil || userID != "dev" || serviceAccountID.Valid {
		t.Fatalf("agent principal columns: %q %v %v", userID, serviceAccountID, err)
	}
}

func TestPrincipalColumnsAreExclusiveAndWorkspaceBound(t *testing.T) {
	store, err := Open(t.Context(), Options{DSN: filepath.Join(t.TempDir(), "db.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sa := api.Principal{Type: api.PrincipalServiceAccount, ID: "sa"}
	base := types.Snapshot{Revision: 1, Users: []types.User{{ID: "u", Name: "u"}}, Workspaces: []types.Workspace{{ID: "w1", Name: "one"}, {ID: "w2", Name: "two"}},
		ServiceAccounts: []types.ServiceAccount{{ID: "sa", WorkspaceID: "w1", Name: "robot", Status: api.StatusActive, CreatedBy: "u"}}}
	if err = store.Save(t.Context(), types.Snapshot{}, base); err != nil {
		t.Fatal(err)
	}
	next := base
	next.Revision = 2
	next.Agents = []types.Agent{{ID: "a", WorkspaceID: "w1", Principal: sa, CreatedBy: "u", Name: "runner", Status: api.StatusActive}}
	next.Tokens = []types.Token{{ID: "t", Hash: "h", Kind: "provider", ServiceAccountID: "sa", WorkspaceID: "w1", AgentID: "a"}}
	next.EnrollmentKeys = []types.EnrollmentKey{{ID: "k", WorkspaceID: "w1", ServiceAccountID: "sa", CreatedBy: "u", Roles: []string{"serve"}, MaxUses: 1}}
	next.EnrollmentAuthorizations = []types.EnrollmentAuthorization{{ID: "auth", Status: "consumed", ActorUserID: "u", WorkspaceID: "w1", Principal: sa, AgentID: "a"}}
	if err = store.Save(t.Context(), base, next); err != nil {
		t.Fatal(err)
	}
	cross := next
	cross.Revision = 3
	cross.Agents = append([]types.Agent{{ID: "b", WorkspaceID: "w2", Principal: sa, Name: "elsewhere", Status: api.StatusActive}}, next.Agents...)
	if err = store.Save(t.Context(), next, cross); err == nil {
		t.Fatal("an agent ran as a service account of another workspace")
	}
	if _, err = store.sql.Exec("INSERT INTO agents(id,workspace_id,user_id,service_account_id,name,payload) VALUES('c','w1','u','sa','both','{}')"); err == nil {
		t.Fatal("an agent had two principals")
	}
	if _, err = store.sql.Exec("INSERT INTO agents(id,workspace_id,name,payload) VALUES('d','w1','none','{}')"); err == nil {
		t.Fatal("an agent had no principal")
	}
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 2 || len(loaded.Agents) != 1 || loaded.Agents[0].Principal != sa || loaded.Agents[0].CreatedBy != "u" || loaded.Tokens[0].ServiceAccountID != "sa" ||
		loaded.EnrollmentKeys[0].ServiceAccountID != "sa" || len(loaded.ServiceAccounts) != 1 || len(loaded.EnrollmentAuthorizations) != 1 || loaded.EnrollmentAuthorizations[0].Principal != sa {
		t.Fatalf("service account state did not round-trip: %+v", loaded)
	}
}

func TestIncrementalSaveRoundTripsChangedRowsOnly(t *testing.T) {
	store, err := Open(t.Context(), Options{DSN: filepath.Join(t.TempDir(), "db.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := types.Snapshot{Revision: 1, Users: []types.User{{ID: "u", Name: "alice"}}, Workspaces: []types.Workspace{{ID: "w", Name: "work"}},
		Memberships: []types.Membership{{WorkspaceID: "w", UserID: "u", Role: "developer"}},
		Agents:      []types.Agent{{ID: "old", WorkspaceID: "w", Principal: api.Principal{Type: api.PrincipalUser, ID: "u"}, Name: "laptop", Status: "revoked"}},
		Tokens:      []types.Token{{ID: "t", Hash: "h", Kind: "provider", UserID: "u", WorkspaceID: "w", AgentID: "old"}},
		Audit:       []types.Audit{{ID: "a1", Action: "one"}}}
	if err = store.Save(t.Context(), types.Snapshot{}, first); err != nil {
		t.Fatal(err)
	}
	// One commit changes a constrained column, purges a parent with its child,
	// reuses the freed unique name and appends to the audit log.
	second := first
	second.Revision = 2
	second.Memberships = []types.Membership{{WorkspaceID: "w", UserID: "u", Role: "maintainer"}}
	second.Agents = []types.Agent{{ID: "new", WorkspaceID: "w", Principal: api.Principal{Type: api.PrincipalUser, ID: "u"}, Name: "laptop", Status: "active"}}
	second.Tokens = nil
	second.Audit = append(append([]types.Audit(nil), first.Audit...), types.Audit{ID: "a2", Action: "two"})
	if err = store.Save(t.Context(), first, second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 2 || len(loaded.Agents) != 1 || loaded.Agents[0].ID != "new" || loaded.Memberships[0].Role != "maintainer" || len(loaded.Tokens) != 0 || len(loaded.Audit) != 2 {
		t.Fatalf("incremental save diverged from the snapshot: %+v", loaded)
	}
	var names int
	if err := store.sql.QueryRow("SELECT COUNT(*) FROM names WHERE label='laptop' AND agent_id='new'").Scan(&names); err != nil || names != 1 {
		t.Fatalf("name reservation did not move to the new agent: %d %v", names, err)
	}
}
