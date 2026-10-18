package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agents/control/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// The test creates a dedicated schema rather than deleting records from a
// caller's database. CI grants its disposable PostgreSQL user CREATE privilege.
// postgresSchema returns Options for a fresh, dedicated schema and a direct
// connection to it for seeding and inspection.
func postgresSchema(t *testing.T) (Options, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("AGENTS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AGENTS_TEST_POSTGRES_DSN to run real PostgreSQL persistence tests")
	}
	connection, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var random [12]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schemaName := "agentd_test_" + hex.EncodeToString(random[:])
	if _, err = connection.ExecContext(t.Context(), "CREATE SCHEMA "+schemaName); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := connection.Exec("DROP SCHEMA " + schemaName + " CASCADE")
		if err != nil {
			t.Errorf("clean PostgreSQL test schema: %v", err)
		}
		_ = connection.Close()
	})
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = map[string]string{}
	}
	config.RuntimeParams["search_path"] = schemaName
	registered := stdlib.RegisterConnConfig(config)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(registered) })
	scoped, err := sql.Open("pgx", registered)
	if err != nil {
		t.Fatal(err)
	}
	scoped.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = scoped.Close() })
	return Options{Driver: "postgres", DSN: registered}, scoped
}

func TestPostgresPersistenceAndConstraints(t *testing.T) {
	options, _ := postgresSchema(t)
	store, err := Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	baseline := types.Snapshot{Revision: 1, Users: []types.User{{ID: "user", Name: "alice"}}, Workspaces: []types.Workspace{{ID: "w1", Name: "one"}, {ID: "w2", Name: "two"}}, Memberships: []types.Membership{{WorkspaceID: "w1", UserID: "user", Role: "owner"}}, Agents: []types.Agent{{ID: "agent", WorkspaceID: "w1", Principal: api.Principal{Type: api.PrincipalUser, ID: "user"}, Name: "provider"}}, Services: []types.Service{{ID: "service", WorkspaceID: "w1", AgentID: "agent", Name: "demo", Hostname: "demo.one.example"}}, Tokens: []types.Token{{ID: "token", Hash: strings.Repeat("a", 64), Kind: "provider", UserID: "user", WorkspaceID: "w1", AgentID: "agent", ExpiresAt: time.Now().Add(time.Hour)}}}
	if err = store.Save(t.Context(), types.Snapshot{}, baseline); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || len(loaded.Services) != 1 || len(loaded.Tokens) != 1 || loaded.Tokens[0].Hash != baseline.Tokens[0].Hash {
		t.Fatal("PostgreSQL restart lost records or token hashes")
	}
	if err = store.Save(t.Context(), types.Snapshot{}, types.Snapshot{Revision: 1}); types.KindOf(err) != "conflict" {
		t.Fatalf("stale PostgreSQL writer not fenced: %v", err)
	}
	for _, resource := range []string{"service", "grant"} {
		t.Run(resource, func(t *testing.T) {
			invalid := baseline
			invalid.Revision = 2
			if resource == "service" {
				invalid.Services = []types.Service{{ID: "foreign", WorkspaceID: "w2", AgentID: "agent", Name: "foreign", Hostname: "foreign.two.example"}}
			} else {
				invalid.Grants = []types.Grant{{ID: "foreign", WorkspaceID: "w2", ResourceType: "service", ResourceID: "service", SubjectType: "anonymous", StartsAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}}
			}
			if err := store.Save(t.Context(), baseline, invalid); err == nil {
				t.Fatal("cross-workspace composite foreign key accepted")
			}
			got, err := store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got.Revision != 1 || len(got.Services) != 1 || got.Services[0].ID != "service" || len(got.Tokens) != 1 {
				t.Fatal("failed PostgreSQL transaction partially replaced state")
			}
		})
	}
}

// TestPostgresPrincipalMigrationOfExistingData migrates populated version-2
// tables, so the ALTERs, the replaced grant subject constraints and the
// payload rewrite run against real rows.
func TestPostgresPrincipalMigrationOfExistingData(t *testing.T) {
	options, scoped := postgresSchema(t)
	statements := append(append([]string(nil), schema...),
		`CREATE TABLE oidc_identities (issuer TEXT NOT NULL, subject TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), payload TEXT NOT NULL, PRIMARY KEY(issuer,subject))`,
		"UPDATE control_meta SET schema_version=2, revision=9 WHERE id=1",
		`INSERT INTO users(id,name,payload) VALUES('dev','dev','{"id":"dev","name":"dev"}'),('other','other','{"id":"other","name":"other"}')`,
		`INSERT INTO workspaces(id,name,payload) VALUES('w','work','{"id":"w","name":"work"}')`,
		`INSERT INTO memberships(workspace_id,user_id,role,payload) VALUES('w','dev','developer','{"workspaceId":"w","userId":"dev","role":"developer"}')`,
		`INSERT INTO agents(id,workspace_id,user_id,name,payload) VALUES('a','w','dev','laptop','{"id":"a","workspaceId":"w","workspace":"work","userId":"dev","name":"laptop","status":"active","roles":["serve"],"desired":{"apiVersion":"agents/v1alpha1","agent":{"name":"laptop"},"serve":{"enabled":true,"services":[{"name":"demo","origin":"http://127.0.0.1:8080"}]}}}')`,
		`INSERT INTO services(id,workspace_id,agent_id,name,hostname,payload) VALUES('s','w','a','demo','demo.work.example','{"id":"s","workspaceId":"w","agentId":"a","name":"demo","hostname":"demo.work.example","enabled":true}')`,
		`INSERT INTO enrollment_keys(id,workspace_id,user_id,payload) VALUES('k','w','dev','{"id":"k","workspaceId":"w","userId":"dev","roles":["serve"],"maxUses":1}')`,
		`INSERT INTO tokens(id,hash,kind,user_id,workspace_id,agent_id,payload) VALUES('t','`+strings.Repeat("b", 64)+`','provider','dev','w','a','{"id":"t","kind":"provider","userId":"dev","workspaceId":"w","agentId":"a"}')`,
		`INSERT INTO grants(id,workspace_id,resource_type,service_id,subject_type,subject_user_id,payload) VALUES('g','w','service','s','user','dev','{"id":"g","workspaceId":"w","resourceType":"service","resourceId":"s","subjectType":"user","subjectId":"dev"}')`,
	)
	for _, statement := range statements {
		if _, err := scoped.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	store, err := Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	snap, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	user := api.Principal{Type: api.PrincipalUser, ID: "dev"}
	if snap.Revision != 9 || len(snap.Agents) != 1 || snap.Agents[0].Principal != user || len(snap.EnrollmentKeys) != 1 || snap.EnrollmentKeys[0].UserID != "dev" ||
		len(snap.Tokens) != 1 || snap.Tokens[0].UserID != "dev" || len(snap.Grants) != 1 {
		t.Fatalf("legacy rows did not migrate to user principals: %+v", snap)
	}
	var version int
	if err = scoped.QueryRow("SELECT schema_version FROM control_meta WHERE id=1").Scan(&version); err != nil || version != 4 {
		t.Fatalf("schema version %d: %v", version, err)
	}
	// The new principal and grant subject are usable on the migrated tables.
	sa := api.Principal{Type: api.PrincipalServiceAccount, ID: "sa"}
	next := snap
	next.Revision = 10
	next.ServiceAccounts = []types.ServiceAccount{{ID: "sa", WorkspaceID: "w", Name: "fleet", Status: api.StatusActive, CreatedBy: "dev"}}
	next.Agents = append(append([]types.Agent(nil), snap.Agents...), types.Agent{ID: "runner", WorkspaceID: "w", Principal: sa, CreatedBy: "dev", Name: "runner", Status: api.StatusActive})
	next.EgressRoutes = []types.EgressRoute{{ID: "r", WorkspaceID: "w", AgentID: "runner", Name: "out", Enabled: true}}
	next.Grants = append(append([]types.Grant(nil), snap.Grants...), types.Grant{ID: "sa-grant", WorkspaceID: "w", ResourceType: "egress", ResourceID: "r", SubjectType: "service_account", SubjectID: "sa", StartsAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	if err = store.Save(t.Context(), snap, next); err != nil {
		t.Fatal(err)
	}
	// With the Service Account present, only the CHECK constraints can refuse these.
	for _, statement := range []string{
		"INSERT INTO agents(id,workspace_id,name,payload) VALUES('none','w','none','{}')",
		"INSERT INTO agents(id,workspace_id,user_id,service_account_id,name,payload) VALUES('both','w','dev','sa','both','{}')",
		"INSERT INTO enrollment_keys(id,workspace_id,user_id,service_account_id,payload) VALUES('k2','w','dev','sa','{}')",
		"INSERT INTO grants(id,workspace_id,resource_type,route_id,subject_type,subject_user_id,subject_service_account_id,payload) VALUES('mixed','w','egress','r','service_account','dev','sa','{}')",
	} {
		if _, err := scoped.Exec(statement); err == nil {
			t.Fatalf("migrated constraints accepted %s", statement)
		}
	}
	if _, err := scoped.Exec("INSERT INTO grants(id,workspace_id,resource_type,route_id,subject_type,subject_service_account_id,payload) VALUES('valid','w','egress','r','service_account','sa','{}')"); err != nil {
		t.Fatalf("a service account grant row was refused: %v", err)
	}
	cross := next
	cross.Revision = 11
	cross.Workspaces = append(append([]types.Workspace(nil), next.Workspaces...), types.Workspace{ID: "w2", Name: "two"})
	cross.Agents = append(append([]types.Agent(nil), next.Agents...), types.Agent{ID: "elsewhere", WorkspaceID: "w2", Principal: sa, Name: "elsewhere", Status: api.StatusActive})
	if err = store.Save(t.Context(), next, cross); err == nil {
		t.Fatal("PostgreSQL accepted a service account device in another workspace")
	}
}
