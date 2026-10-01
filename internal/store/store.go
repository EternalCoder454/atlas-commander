// Package store is Commander's SQLite database: fleets, agents, tasks, the
// approval queue and the append-only audit log.
//
// Unlike Atlas Notes, whose index is a rebuildable cache, this database is the
// source of truth: a restart must not lose the board. So it is never
// quarantined or recreated. Migrations are versioned with PRAGMA user_version,
// run one transaction per step, and a database written by a newer build is
// refused with a plain error instead of being touched.
//
// Goroutines: the pool is a single connection, so every method is safe to call
// from any goroutine and calls are serialised. Inside a transaction code must
// use the tx and never s.db, or it would wait forever for the one connection.
// Platform: none; modernc.org/sqlite is pure Go.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"atlas-commander/internal/paths"
)

// Errors callers may test with errors.Is.
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	// ErrConflict means the change is not allowed from the row's current
	// state, such as finishing a task that was already cancelled.
	ErrConflict = errors.New("not allowed in the current state")
)

// DefaultGatePolicy is the policy JSON given to an agent that has none: the
// tools that can change things need a human's approval.
const DefaultGatePolicy = `{"approve":["Bash","Write","Edit","NotebookEdit"]}`

// Store is the open database.
type Store struct {
	db *sql.DB
}

// migrations[i] takes the schema from version i to i+1. Never edit a step
// that has shipped; add a new one.
var migrations = []string{
	// 1: the initial schema.
	`CREATE TABLE fleets (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		workdir TEXT NOT NULL DEFAULT '',
		budget_usd REAL NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE agents (
		id TEXT PRIMARY KEY,
		fleet_id TEXT NOT NULL REFERENCES fleets(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		backend TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		workdir TEXT NOT NULL DEFAULT '',
		pinned_prompt TEXT NOT NULL DEFAULT '',
		cost_cap_usd REAL NOT NULL DEFAULT 0,
		use_worktree INTEGER NOT NULL DEFAULT 0,
		worktree_path TEXT NOT NULL DEFAULT '',
		branch TEXT NOT NULL DEFAULT '',
		gate_policy TEXT NOT NULL DEFAULT '{"approve":["Bash","Write","Edit","NotebookEdit"]}',
		session_id TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '',
		archived INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT 0,
		UNIQUE (fleet_id, name)
	);
	CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		fleet_id TEXT NOT NULL REFERENCES fleets(id) ON DELETE CASCADE,
		agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
		title TEXT NOT NULL DEFAULT '',
		prompt TEXT NOT NULL DEFAULT '',
		priority INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'queued',
		created_at INTEGER NOT NULL DEFAULT 0,
		started_at INTEGER NOT NULL DEFAULT 0,
		finished_at INTEGER NOT NULL DEFAULT 0,
		result TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX tasks_by_fleet ON tasks (fleet_id, status);
	CREATE TABLE task_deps (
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		depends_on TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		PRIMARY KEY (task_id, depends_on)
	);
	CREATE INDEX task_deps_reverse ON task_deps (depends_on);
	-- No foreign keys on audit: the log must outlive the agents and fleets it
	-- describes, and deleting them must not touch it.
	CREATE TABLE audit (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		at INTEGER NOT NULL,
		fleet_id TEXT NOT NULL DEFAULT '',
		agent_id TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		task_id TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL,
		tool TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		input INTEGER NOT NULL DEFAULT 0,
		output INTEGER NOT NULL DEFAULT 0,
		cache_read INTEGER NOT NULL DEFAULT 0,
		cache_write_5m INTEGER NOT NULL DEFAULT 0,
		cache_write_1h INTEGER NOT NULL DEFAULT 0,
		cost_usd REAL NOT NULL DEFAULT 0,
		is_error INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX audit_by_agent ON audit (agent_id, at);
	CREATE INDEX audit_by_session ON audit (session_id);
	CREATE INDEX audit_by_kind ON audit (kind);
	CREATE INDEX audit_by_fleet ON audit (fleet_id, at);
	CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit
	BEGIN SELECT RAISE(ABORT, 'the audit log is append-only'); END;
	CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit
	BEGIN SELECT RAISE(ABORT, 'the audit log is append-only'); END;
	CREATE TABLE approvals (
		id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		tool TEXT NOT NULL DEFAULT '',
		input TEXT NOT NULL DEFAULT '',
		requested_at INTEGER NOT NULL DEFAULT 0,
		decided_at INTEGER NOT NULL DEFAULT 0,
		decision TEXT,
		reason TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX approvals_pending ON approvals (decision, requested_at);`,
}

// SchemaVersion is the schema this build writes.
func SchemaVersion() int { return len(migrations) }

// Open opens (creating if needed) the database at path, or paths.Database()
// when path is empty, and brings its schema up to date.
func Open(path string) (*Store, error) {
	if path == "" {
		path = paths.Database()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("could not create the database folder: %w", err)
	}
	// synchronous=NORMAL is safe under WAL: it survives an app crash, and only
	// the last transactions can be lost on power failure. That keeps fsync off
	// the path of the supervisor's audit writes.
	db, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		return nil, fmt.Errorf("could not open the database: %w", err)
	}
	// One connection: SQLite serialises writers anyway, and a pool of them
	// only produces "database is locked".
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// dsnFor builds the connection string. The path goes in a file: URI, where
// '?' and '#' would otherwise start the query or fragment, so they and '%' are
// percent-encoded. A Windows drive path needs a slash before the drive letter
// to be read as absolute. _txlock=immediate makes every transaction take the
// write lock up front, so a read-then-write transaction cannot fail midway
// with "database is locked" when another process writes.
func dsnFor(path string) string {
	p := filepath.ToSlash(path)
	if len(p) >= 2 && p[1] == ':' {
		p = "/" + p
	}
	q := url.Values{}
	for _, pr := range []string{"busy_timeout(5000)", "journal_mode(wal)", "synchronous(normal)", "foreign_keys(on)"} {
		q.Add("_pragma", pr)
	}
	q.Set("_txlock", "immediate")
	u := url.URL{
		Scheme:   "file",
		Opaque:   strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(p),
		RawQuery: q.Encode(),
	}
	return u.String()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	for {
		done, err := s.migrateStep()
		if err != nil || done {
			return err
		}
	}
}

// migrateStep applies at most one step. It reads user_version inside the
// transaction (which holds the write lock) so that two processes opening a
// fresh database cannot both apply the same step.
func (s *Store) migrateStep() (done bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return false, fmt.Errorf("could not read the database version: %w", err)
	}
	if v > len(migrations) {
		return false, fmt.Errorf("this database was made by a newer Atlas Commander (schema %d, this one knows %d); update the app", v, len(migrations))
	}
	if v == len(migrations) {
		return true, nil
	}
	// user_version is part of the transaction, so a failed step leaves
	// both the schema and the version where they were.
	_, err = tx.Exec(migrations[v])
	if err == nil {
		_, err = tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1))
	}
	if err != nil {
		return false, fmt.Errorf("could not update the database to version %d: %w", v+1, err)
	}
	return false, tx.Commit()
}

// NewID returns a short random hex id.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// ms stores a time as unix milliseconds; the zero time is 0.
func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// mapErr turns driver errors the caller can act on into the package's errors.
func mapErr(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	case strings.Contains(err.Error(), "UNIQUE constraint failed"):
		return fmt.Errorf("%s: %w", what, ErrExists)
	case isFK(err):
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return err
}

// execer is what both *sql.DB and *sql.Tx offer, so helpers run in or out of a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// needOne reports ErrNotFound when an UPDATE or DELETE touched no row.
func needOne(res sql.Result, err error, what string) error {
	if err != nil {
		return mapErr(err, what)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}

// isFK reports a foreign key violation, which here means a row does not exist.
func isFK(err error) bool { return strings.Contains(err.Error(), "FOREIGN KEY constraint failed") }
