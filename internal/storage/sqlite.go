package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// databaseFile is the SQLite database inside the data directory. Large
// artifacts (events, logs, diffs, evaluator output) stay as files next to it;
// the database stores their paths and hashes.
const databaseFile = "gauge.db"

// migration is one versioned schema change. Migrations are applied in order
// inside a transaction and recorded in schema_migrations.
type migration struct {
	version int
	stmts   []string
}

// migrations is the schema history. Never edit a released migration; append a
// new one instead. Version 1 creates the initial experiment/attempt schema.
var migrations = []migration{
	{
		version: 1,
		stmts: []string{
			`CREATE TABLE experiments (
				id TEXT PRIMARY KEY,
				schema_version INTEGER NOT NULL,
				status TEXT NOT NULL,
				created_at TEXT NOT NULL,
				started_at TEXT NOT NULL,
				ended_at TEXT,
				gauge_version TEXT NOT NULL,
				gauge_commit TEXT NOT NULL,
				agent_id TEXT NOT NULL,
				agent_description TEXT,
				agent_version TEXT,
				agent_provider TEXT,
				agent_model TEXT,
				agent_config TEXT,
				agent_config_fingerprint TEXT NOT NULL,
				agent_config_redacted INTEGER NOT NULL DEFAULT 0,
				suite_id TEXT,
				suite_name TEXT,
				suite_fingerprint TEXT,
				suite_file TEXT,
				suite_tasks TEXT,
				selection TEXT NOT NULL,
				repetitions INTEGER NOT NULL,
				environment TEXT NOT NULL,
				notes TEXT
			)`,
			`CREATE INDEX idx_experiments_created_at ON experiments(created_at)`,
			`CREATE INDEX idx_experiments_agent_id ON experiments(agent_id)`,

			`CREATE TABLE attempts (
				id TEXT PRIMARY KEY,
				experiment_id TEXT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
				schema_version INTEGER NOT NULL,
				task_id TEXT NOT NULL,
				task_name TEXT,
				task_category TEXT,
				task_fingerprint TEXT,
				repetition INTEGER NOT NULL,
				agent_id TEXT NOT NULL,
				agent_version TEXT,
				model TEXT,
				provider TEXT,
				started_at TEXT NOT NULL,
				ended_at TEXT NOT NULL,
				duration_ms INTEGER NOT NULL,
				execution_completion TEXT NOT NULL,
				execution_exit_code INTEGER,
				execution_signal TEXT,
				execution_error TEXT,
				execution_adapter_error TEXT,
				execution_timed_out INTEGER NOT NULL DEFAULT 0,
				execution_cancelled INTEGER NOT NULL DEFAULT 0,
				setup TEXT,
				usage_input_tokens INTEGER,
				usage_output_tokens INTEGER,
				usage_total_tokens INTEGER,
				usage_cost_usd REAL,
				diff TEXT NOT NULL,
				evaluation_status TEXT,
				evaluation_summary TEXT,
				evaluation_score REAL,
				evaluation_error TEXT,
				evaluation_timed_out INTEGER,
				evaluation_exit_code INTEGER,
				evaluation_duration_ms INTEGER,
				adapter_metadata TEXT,
				workspace_kept INTEGER NOT NULL DEFAULT 0,
				error TEXT,
				outcome TEXT NOT NULL
			)`,
			`CREATE INDEX idx_attempts_experiment_id ON attempts(experiment_id)`,
			`CREATE INDEX idx_attempts_task_id ON attempts(task_id)`,
			`CREATE INDEX idx_attempts_outcome ON attempts(outcome)`,
			`CREATE INDEX idx_attempts_agent_id ON attempts(agent_id)`,

			`CREATE TABLE checks (
				attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
				position INTEGER NOT NULL,
				id TEXT NOT NULL,
				status TEXT NOT NULL,
				message TEXT,
				PRIMARY KEY (attempt_id, position)
			)`,

			`CREATE TABLE artifacts (
				attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				path TEXT NOT NULL,
				size INTEGER NOT NULL,
				sha256 TEXT NOT NULL,
				PRIMARY KEY (attempt_id, name)
			)`,

			// Typed metric values with provenance. Gauge never fabricates a
			// value: a metric row exists only when a source reported it.
			`CREATE TABLE metrics (
				attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				value REAL NOT NULL,
				unit TEXT NOT NULL,
				source TEXT NOT NULL,
				PRIMARY KEY (attempt_id, name)
			)`,
			`CREATE INDEX idx_metrics_name ON metrics(name)`,
		},
	},
}

// openDatabase opens the SQLite database at path, applies pragmas and
// migrations, and returns a pool limited to one connection. A single
// connection keeps the per-connection pragmas (notably foreign_keys) in effect
// and serializes writes, which is appropriate for a local sequential runner.
func openDatabase(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("storage: create data directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage: open database %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA synchronous = NORMAL`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("storage: configure database: %w", err)
		}
	}
	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&journal); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: enable write-ahead logging: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrate applies every migration newer than the recorded schema version. A
// database written by a newer Gauge is rejected rather than silently misread.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("storage: create migration table: %w", err)
	}
	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("storage: read database schema version: %w", err)
	}
	latest := 0
	for _, m := range migrations {
		if m.version > latest {
			latest = m.version
		}
	}
	if current > latest {
		return fmt.Errorf("storage: database schema %d is newer than this Gauge supports (%d)", current, latest)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("storage: begin migration %d: %w", m.version, err)
		}
		for _, stmt := range m.stmts {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("storage: apply migration %d: %w", m.version, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storage: record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("storage: commit migration %d: %w", m.version, err)
		}
	}
	return nil
}
