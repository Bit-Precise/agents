// Package db owns explicit SQL migrations and atomic control-plane persistence.
package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bit-precise/agentd/api"
	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/types"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Options struct {
	Driver string
	DSN    string
}
type Store struct{ sql *sql.DB }

func Open(ctx context.Context, options Options) (*Store, error) {
	driver := options.Driver
	if driver == "" {
		driver = "sqlite"
	}
	if driver == "postgres" || driver == "postgresql" {
		driver = "pgx"
	}
	if driver != "sqlite" && driver != "pgx" {
		return nil, fmt.Errorf("unsupported SQL driver %q", driver)
	}
	dsn := options.DSN
	if dsn == "" {
		if driver == "pgx" {
			return nil, errors.New("PostgreSQL DSN is required")
		}
		dsn = "agents.db"
	}
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	fail := func(err error) (*Store, error) { _ = conn.Close(); return nil, err }
	if err = conn.PingContext(ctx); err != nil {
		return fail(err)
	}
	if driver == "sqlite" {
		for _, q := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"} {
			if _, err = conn.ExecContext(ctx, q); err != nil {
				return fail(err)
			}
		}
	}
	store := &Store{sql: conn}
	if err = store.migrate(ctx); err != nil {
		return fail(err)
	}
	return store, nil
}
func (s *Store) Close() error { return s.sql.Close() }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range schema {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("control schema migration: %w", err)
		}
	}
	var version int
	if err = tx.QueryRowContext(ctx, "SELECT schema_version FROM control_meta WHERE id=1").Scan(&version); err != nil {
		return err
	}
	if version < 1 || version > 3 {
		return fmt.Errorf("unsupported control schema version %d", version)
	}
	if version == 1 {
		// Migration 2 only adds identity bindings. Existing users, token hashes,
		// memberships, and control revisions are preserved verbatim.
		if _, err = tx.ExecContext(ctx, `CREATE TABLE oidc_identities (issuer TEXT NOT NULL, subject TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), payload TEXT NOT NULL, PRIMARY KEY(issuer,subject))`); err != nil {
			return fmt.Errorf("control migration 2: %w", err)
		}
		if _, err = tx.ExecContext(ctx, "UPDATE control_meta SET schema_version=2 WHERE id=1"); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		if err = migrateApprovalRemoval(ctx, tx); err != nil {
			return fmt.Errorf("control migration 3: %w", err)
		}
		if _, err = tx.ExecContext(ctx, "UPDATE control_meta SET schema_version=3 WHERE id=1"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// legacyAgent is the version-2 payload of the removed manual-approval model,
// where Desired was the whole client config.yaml.
type legacyAgent struct {
	ID              string    `json:"id"`
	WorkspaceID     string    `json:"workspaceId"`
	UserID          string    `json:"userId"`
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	Roles           []string  `json:"roles"`
	CreatedAt       time.Time `json:"createdAt"`
	PendingApproval bool      `json:"pendingApproval"`
	Desired         struct {
		Ingress struct {
			Enabled bool `json:"enabled"`
		} `json:"ingress"`
		Serve struct {
			Enabled  bool              `json:"enabled"`
			Services []api.ServiceSpec `json:"services"`
		} `json:"serve"`
		Egress struct {
			Enabled bool          `json:"enabled"`
			Allow   []policy.Rule `json:"allow"`
			Deny    []policy.Rule `json:"deny"`
		} `json:"egress"`
	} `json:"desired"`
	Approval struct {
		Revision uint64            `json:"revision"`
		Services map[string]string `json:"services"`
		Egress   []policy.Rule     `json:"egress"`
		Deny     []policy.Rule     `json:"deny"`
	} `json:"approval"`
}

// migrateApprovalRemoval rewrites agents into the automatic-validation model.
// The approved snapshot becomes the starting effective configuration with its
// revision, so Control's first reconciliation recomputes it under the current
// policy instead of activating pending requests unconditionally. No grant is
// created or extended. The legacy approval is kept in audit for traceability.
func migrateApprovalRemoval(ctx context.Context, tx *sql.Tx) error {
	now := time.Now().UTC()
	hostnames := map[string]string{}
	if err := eachRow(ctx, tx, "SELECT agent_id||'/'||name, hostname FROM services", func(key, hostname string) error {
		hostnames[key] = hostname
		return nil
	}); err != nil {
		return err
	}
	var agents [][2]string
	var audits []types.Audit
	if err := eachRow(ctx, tx, "SELECT id, payload FROM agents", func(id, payload string) error {
		var old legacyAgent
		if err := json.Unmarshal([]byte(payload), &old); err != nil {
			return fmt.Errorf("decode agent %s: %w", id, err)
		}
		a := types.Agent{ID: old.ID, WorkspaceID: old.WorkspaceID, UserID: old.UserID, Name: old.Name, Status: old.Status, Roles: old.Roles, CreatedAt: old.CreatedAt,
			Desired: api.AgentSpec{Ingress: api.IngressSpec{Enabled: old.Desired.Ingress.Enabled},
				Serve:  api.ServeSpec{Enabled: old.Desired.Serve.Enabled, Services: old.Desired.Serve.Services},
				Egress: api.EgressSpec{Enabled: old.Desired.Egress.Enabled, Allow: old.Desired.Egress.Allow, Deny: old.Desired.Egress.Deny}},
			Effective: api.EffectiveConfig{Revision: max(old.Approval.Revision, 1), Egress: old.Approval.Egress, Deny: old.Approval.Deny}}
		for _, service := range old.Desired.Serve.Services {
			if origin, ok := old.Approval.Services[service.Name]; ok {
				a.Effective.Services = append(a.Effective.Services, api.EffectiveService{Name: service.Name, Origin: origin, Hostname: hostnames[a.ID+"/"+service.Name]})
			}
		}
		if a.Status == api.StatusRevoked {
			a.RevokedAt = now
		} else {
			a.Status = api.StatusActive
		}
		data, err := json.Marshal(a)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(map[string]any{"status": old.Status, "pendingApproval": old.PendingApproval, "approval": old.Approval})
		if err != nil {
			return err
		}
		agents = append(agents, [2]string{a.ID, string(data)})
		audits = append(audits, types.Audit{ID: migrationID(), At: now, ActorID: "system", Action: "agent.migrate.approval_removed", ResourceID: a.ID, Detail: string(detail)})
		return nil
	}); err != nil {
		return err
	}
	for _, v := range agents {
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET payload=$1 WHERE id=$2", v[1], v[0]); err != nil {
			return err
		}
	}
	for _, event := range audits {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO audit(id,payload) VALUES($1,$2)", event.ID, string(data)); err != nil {
			return err
		}
	}
	if err := rewritePayloads[types.EnrollmentKey](ctx, tx, "enrollment_keys", nil); err != nil {
		return err
	}
	return migrateGrantScope(ctx, tx)
}

// migrateGrantScope records which grants were created as external shares.
// Grants for current non-members keep working when a manager (or an unknown
// creator) made them; a grant a non-manager made for a since-removed member
// becomes internal and stops matching.
func migrateGrantScope(ctx context.Context, tx *sql.Tx) error {
	roles := map[string]string{}
	if err := eachRow(ctx, tx, "SELECT workspace_id||'/'||user_id, role FROM memberships", func(key, role string) error {
		roles[key] = role
		return nil
	}); err != nil {
		return err
	}
	admins := map[string]bool{}
	var users []types.User
	if err := loadRows(ctx, tx, "users", &users); err != nil {
		return err
	}
	for _, u := range users {
		admins[u.ID] = u.Admin
	}
	creators := map[string]string{}
	var events []types.Audit
	if err := loadRows(ctx, tx, "audit", &events); err != nil {
		return err
	}
	for _, e := range events {
		if e.Action == "grant.create" {
			creators[e.ResourceID] = e.ActorID
		}
	}
	return rewritePayloads(ctx, tx, "grants", func(g *types.Grant) {
		g.CreatedBy = creators[g.ID]
		switch g.SubjectType {
		case "anonymous":
			g.External = true
		case "workspace":
			g.External = g.SubjectID != g.WorkspaceID
		case "user":
			if _, member := roles[g.WorkspaceID+"/"+g.SubjectID]; !member {
				role := roles[g.WorkspaceID+"/"+g.CreatedBy]
				g.External = g.CreatedBy == "" || admins[g.CreatedBy] || role == "owner" || role == "maintainer"
			}
		}
	})
}

func rewritePayloads[T any](ctx context.Context, tx *sql.Tx, table string, change func(*T)) error {
	var rows [][2]string
	if err := eachRow(ctx, tx, "SELECT id, payload FROM "+table, func(id, payload string) error {
		var value T
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			return fmt.Errorf("decode %s %s: %w", table, id, err)
		}
		if change != nil {
			change(&value)
		}
		data, err := json.Marshal(value)
		rows = append(rows, [2]string{id, string(data)})
		return err
	}); err != nil {
		return err
	}
	for _, v := range rows {
		if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET payload=$1 WHERE id=$2", v[1], v[0]); err != nil {
			return err
		}
	}
	return nil
}

func eachRow(ctx context.Context, tx *sql.Tx, query string, fn func(a, b string) error) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	var pending [][2]string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return err
		}
		pending = append(pending, [2]string{a, b})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, v := range pending {
		if err := fn(v[0], v[1]); err != nil {
			return err
		}
	}
	return nil
}

func migrationID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS control_meta (id INTEGER PRIMARY KEY CHECK(id=1), revision BIGINT NOT NULL, schema_version INTEGER NOT NULL)`,
	`INSERT INTO control_meta(id,revision,schema_version) VALUES(1,0,1) ON CONFLICT(id) DO NOTHING`,
	`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, payload TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS workspaces (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, payload TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS memberships (workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), role TEXT NOT NULL CHECK(role IN ('owner','maintainer','developer','guest')), payload TEXT NOT NULL, PRIMARY KEY(workspace_id,user_id))`,
	`CREATE TABLE IF NOT EXISTS agents (id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), name TEXT NOT NULL,payload TEXT NOT NULL,UNIQUE(workspace_id,id),UNIQUE(workspace_id,name))`,
	`CREATE TABLE IF NOT EXISTS enrollment_keys (id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),payload TEXT NOT NULL,UNIQUE(workspace_id,id))`,
	`CREATE TABLE IF NOT EXISTS services (id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id),agent_id TEXT NOT NULL,name TEXT NOT NULL,hostname TEXT NOT NULL UNIQUE,payload TEXT NOT NULL,UNIQUE(workspace_id,id),UNIQUE(workspace_id,name),FOREIGN KEY(workspace_id,agent_id) REFERENCES agents(workspace_id,id))`,
	`CREATE TABLE IF NOT EXISTS egress_routes (id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id),agent_id TEXT NOT NULL,name TEXT NOT NULL,payload TEXT NOT NULL,UNIQUE(workspace_id,id),UNIQUE(workspace_id,name),FOREIGN KEY(workspace_id,agent_id) REFERENCES agents(workspace_id,id))`,
	`CREATE TABLE IF NOT EXISTS grants (id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id),resource_type TEXT NOT NULL CHECK(resource_type IN ('service','egress')),service_id TEXT,route_id TEXT,subject_type TEXT NOT NULL CHECK(subject_type IN ('user','workspace','anonymous')),subject_user_id TEXT REFERENCES users(id),subject_workspace_id TEXT REFERENCES workspaces(id),payload TEXT NOT NULL,FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id),FOREIGN KEY(workspace_id,route_id) REFERENCES egress_routes(workspace_id,id),CHECK((resource_type='service' AND service_id IS NOT NULL AND route_id IS NULL) OR (resource_type='egress' AND route_id IS NOT NULL AND service_id IS NULL)),CHECK((subject_type='user' AND subject_user_id IS NOT NULL AND subject_workspace_id IS NULL) OR (subject_type='workspace' AND subject_workspace_id IS NOT NULL AND subject_user_id IS NULL) OR (subject_type='anonymous' AND subject_user_id IS NULL AND subject_workspace_id IS NULL AND resource_type='service')))`,
	`CREATE TABLE IF NOT EXISTS names (workspace_id TEXT NOT NULL REFERENCES workspaces(id),label TEXT NOT NULL,agent_id TEXT,service_id TEXT,PRIMARY KEY(workspace_id,label),FOREIGN KEY(workspace_id,agent_id) REFERENCES agents(workspace_id,id),FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id),CHECK((agent_id IS NOT NULL AND service_id IS NULL) OR (agent_id IS NULL AND service_id IS NOT NULL)))`,
	`CREATE TABLE IF NOT EXISTS tokens (id TEXT PRIMARY KEY,hash TEXT NOT NULL UNIQUE,kind TEXT NOT NULL CHECK(kind IN ('user','enrollment','control','provider','access')),user_id TEXT REFERENCES users(id),workspace_id TEXT REFERENCES workspaces(id),agent_id TEXT,enrollment_key_id TEXT,payload TEXT NOT NULL,FOREIGN KEY(workspace_id,agent_id) REFERENCES agents(workspace_id,id),FOREIGN KEY(workspace_id,enrollment_key_id) REFERENCES enrollment_keys(workspace_id,id))`,
	`CREATE TABLE IF NOT EXISTS audit (id TEXT PRIMARY KEY,payload TEXT NOT NULL)`,
}

func loadRows[T any](ctx context.Context, tx *sql.Tx, table string, dst *[]T) error {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM "+table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return err
		}
		var value T
		if err := json.Unmarshal([]byte(data), &value); err != nil {
			return fmt.Errorf("decode %s: %w", table, err)
		}
		*dst = append(*dst, value)
	}
	return rows.Err()
}
func (s *Store) Load(ctx context.Context) (types.Snapshot, error) {
	var snap types.Snapshot
	tx, err := s.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snap, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM control_meta WHERE id=1").Scan(&snap.Revision); err != nil {
		return snap, err
	}
	calls := []func() error{func() error { return loadRows(ctx, tx, "users", &snap.Users) }, func() error { return loadRows(ctx, tx, "workspaces", &snap.Workspaces) }, func() error { return loadRows(ctx, tx, "memberships", &snap.Memberships) }, func() error { return loadRows(ctx, tx, "agents", &snap.Agents) }, func() error { return loadRows(ctx, tx, "enrollment_keys", &snap.EnrollmentKeys) }, func() error { return loadRows(ctx, tx, "services", &snap.Services) }, func() error { return loadRows(ctx, tx, "egress_routes", &snap.EgressRoutes) }, func() error { return loadRows(ctx, tx, "grants", &snap.Grants) }, func() error { return loadRows(ctx, tx, "tokens", &snap.Tokens) }, func() error { return loadRows(ctx, tx, "audit", &snap.Audit) }}
	for _, load := range calls {
		if err = load(); err != nil {
			return snap, err
		}
	}
	if err = loadRows(ctx, tx, "oidc_identities", &snap.OIDCIdentities); err != nil {
		return snap, err
	}
	return snap, tx.Commit()
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// row is one persisted record: key columns, then constrained columns, then an
// optional JSON payload. Rows are diffed by key against the previous snapshot.
type row struct {
	key     string
	columns []any
	payload string
}
type table struct {
	name    string
	keys    []string
	columns []string
	payload bool
	rows    func(types.Snapshot) ([]row, error)
}

func encodedRow(value any, columns ...any) (row, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return row{}, err
	}
	return row{columns: columns, payload: string(data)}, nil
}

// tables lists the schema in parent-to-child order: inserts follow it and
// deletes run in reverse, so foreign keys are satisfied at every statement.
var tables = []table{
	{"workspaces", []string{"id"}, []string{"name"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.Workspaces, func(v types.Workspace) (row, error) { return encodedRow(v, v.ID, v.Name) })
	}},
	{"users", []string{"id"}, []string{"name"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.Users, func(v types.User) (row, error) { return encodedRow(v, v.ID, v.Name) })
	}},
	{"memberships", []string{"workspace_id", "user_id"}, []string{"role"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.Memberships, func(v types.Membership) (row, error) { return encodedRow(v, v.WorkspaceID, v.UserID, v.Role) })
	}},
	{"agents", []string{"id"}, []string{"workspace_id", "user_id", "name"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.Agents, func(v types.Agent) (row, error) { return encodedRow(v, v.ID, v.WorkspaceID, v.UserID, v.Name) })
	}},
	{"enrollment_keys", []string{"id"}, []string{"workspace_id", "user_id"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.EnrollmentKeys, func(v types.EnrollmentKey) (row, error) { return encodedRow(v, v.ID, v.WorkspaceID, v.UserID) })
	}},
	{"services", []string{"id"}, []string{"workspace_id", "agent_id", "name", "hostname"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.Services, func(v types.Service) (row, error) {
			return encodedRow(v, v.ID, v.WorkspaceID, v.AgentID, v.Name, v.Hostname)
		})
	}},
	{"egress_routes", []string{"id"}, []string{"workspace_id", "agent_id", "name"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.EgressRoutes, func(v types.EgressRoute) (row, error) { return encodedRow(v, v.ID, v.WorkspaceID, v.AgentID, v.Name) })
	}},
	{"grants", []string{"id"}, []string{"workspace_id", "resource_type", "service_id", "route_id", "subject_type", "subject_user_id", "subject_workspace_id"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.Grants, func(v types.Grant) (row, error) {
			var service, route, user, workspace any
			if v.ResourceType == "service" {
				service = v.ResourceID
			} else {
				route = v.ResourceID
			}
			if v.SubjectType == "user" {
				user = v.SubjectID
			} else if v.SubjectType == "workspace" {
				workspace = v.SubjectID
			}
			return encodedRow(v, v.ID, v.WorkspaceID, v.ResourceType, service, route, v.SubjectType, user, workspace)
		})
	}},
	{"names", []string{"workspace_id", "label"}, []string{"agent_id", "service_id"}, false, func(s types.Snapshot) ([]row, error) {
		var rows []row
		for _, v := range s.Agents {
			rows = append(rows, row{columns: []any{v.WorkspaceID, v.Name, v.ID, nil}})
		}
		for _, v := range s.Services {
			rows = append(rows, row{columns: []any{v.WorkspaceID, v.Name, nil, v.ID}})
		}
		return rows, nil
	}},
	{"tokens", []string{"id"}, []string{"hash", "kind", "user_id", "workspace_id", "agent_id", "enrollment_key_id"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.Tokens, func(v types.Token) (row, error) {
			var key any
			if v.Kind == "enrollment" {
				key = v.ID
			}
			return encodedRow(v, v.ID, v.Hash, v.Kind, nullable(v.UserID), nullable(v.WorkspaceID), nullable(v.AgentID), key)
		})
	}},
	{"oidc_identities", []string{"issuer", "subject"}, []string{"user_id"}, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.OIDCIdentities, func(v types.OIDCIdentity) (row, error) { return encodedRow(v, v.Issuer, v.Subject, v.UserID) })
	}},
	{"audit", []string{"id"}, nil, true, func(s types.Snapshot) ([]row, error) {
		return encodeAll(s.Audit, func(v types.Audit) (row, error) { return encodedRow(v, v.ID) })
	}},
}

func encodeAll[T any](values []T, encode func(T) (row, error)) ([]row, error) {
	rows := make([]row, 0, len(values))
	for _, v := range values {
		r, err := encode(v)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func (t table) index(snap types.Snapshot) (map[string]row, error) {
	rows, err := t.rows(snap)
	if err != nil {
		return nil, err
	}
	out := make(map[string]row, len(rows))
	for _, r := range rows {
		key, err := json.Marshal(r.columns[:len(t.keys)])
		if err != nil {
			return nil, err
		}
		r.key = string(key)
		out[r.key] = r
	}
	return out, nil
}

func placeholders(from, count int) string {
	out := ""
	for i := 0; i < count; i++ {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("$%d", from+i)
	}
	return out
}
func (t table) where(from int) string {
	out := ""
	for i, key := range t.keys {
		if i > 0 {
			out += " AND "
		}
		out += fmt.Sprintf("%s=$%d", key, from+i)
	}
	return out
}
func (t table) insert(ctx context.Context, tx *sql.Tx, r row) error {
	columns := append(append([]string(nil), t.keys...), t.columns...)
	args := append([]any(nil), r.columns...)
	if t.payload {
		columns = append(columns, "payload")
		args = append(args, r.payload)
	}
	list := ""
	for i, c := range columns {
		if i > 0 {
			list += ","
		}
		list += c
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO "+t.name+"("+list+") VALUES("+placeholders(1, len(columns))+")", args...)
	return err
}

// Save commits next if the stored revision still equals previous.Revision and
// writes only rows that differ from previous. A row whose constrained columns
// change is deleted and reinserted; a payload-only change is updated in place.
// The append-only audit log therefore costs one insert per event.
func (s *Store) Save(ctx context.Context, previous, next types.Snapshot) error {
	expected := previous.Revision
	if next.Revision != expected+1 {
		return &types.Error{Kind: types.KindConflict, Message: "snapshot revision must advance exactly once"}
	}
	before := make([]map[string]row, len(tables))
	after := make([]map[string]row, len(tables))
	for i, t := range tables {
		var err error
		if before[i], err = t.index(previous); err != nil {
			return err
		}
		if after[i], err = t.index(next); err != nil {
			return err
		}
	}
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE control_meta SET revision=$1 WHERE id=1 AND revision=$2", next.Revision, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return &types.Error{Kind: types.KindConflict, Message: "control state changed; reload before writing"}
	}
	sameColumns := func(a, b row) bool {
		x, _ := json.Marshal(a.columns)
		y, _ := json.Marshal(b.columns)
		return string(x) == string(y)
	}
	for i := len(tables) - 1; i >= 0; i-- {
		t := tables[i]
		for key, old := range before[i] {
			if current, ok := after[i][key]; !ok || !sameColumns(old, current) {
				if _, err = tx.ExecContext(ctx, "DELETE FROM "+t.name+" WHERE "+t.where(1), old.columns[:len(t.keys)]...); err != nil {
					return err
				}
			}
		}
	}
	for i, t := range tables {
		for key, current := range after[i] {
			old, ok := before[i][key]
			switch {
			case !ok || !sameColumns(old, current):
				if err = t.insert(ctx, tx, current); err != nil {
					return err
				}
			case t.payload && old.payload != current.payload:
				args := append([]any{current.payload}, current.columns[:len(t.keys)]...)
				if _, err = tx.ExecContext(ctx, "UPDATE "+t.name+" SET payload=$1 WHERE "+t.where(2), args...); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}
