package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

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
	if err := store.sql.QueryRow("SELECT schema_version FROM control_meta WHERE id=1").Scan(&version); err != nil || version != 2 {
		t.Fatalf("migration version %d: %v", version, err)
	}
	loaded.Revision++
	loaded.OIDCIdentities = []types.OIDCIdentity{{Issuer: "https://idp.example", Subject: "stable-subject", UserID: user.ID}}
	if err := store.Save(t.Context(), loaded, 17); err != nil {
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
	if err = store.Save(t.Context(), first, 0); err != nil {
		t.Fatal(err)
	}
	bad := types.Snapshot{Revision: 2, Users: []types.User{{ID: "u2", Name: "bad"}}, Agents: []types.Agent{{ID: "a", WorkspaceID: "nonexistent", UserID: "u2", Name: "invalid"}}}
	if err = store.Save(t.Context(), bad, 1); err == nil {
		t.Fatal("invalid snapshot committed")
	}
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || len(loaded.Users) != 1 || loaded.Users[0].Name != "alice" {
		t.Fatal("failed transaction partially changed control state")
	}
	if err = store.Save(t.Context(), types.Snapshot{Revision: 2}, 0); types.KindOf(err) != "conflict" {
		t.Fatalf("stale control writer was not fenced: %v", err)
	}
}
