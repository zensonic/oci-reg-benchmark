// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"database/sql"
	"fmt"
)

// Counter kinds in the counters table.
const (
	// CounterKindError rows count failed operations by error type.
	CounterKindError = "error"
	// CounterKindCounter rows hold extra counters such as "pages" or "mountMisses".
	CounterKindCounter = "counter"
)

// runTables are the tables that hold per-run data, in the order they are
// copied by ExportRun.
var runTables = []string{"runs", "intervals", "operations", "histograms", "counters", "error_examples", "samples"}

// migrations bring the schema from one version to the next (RES-11).
// Never edit a migration that has been released; add a new one instead.
var migrations = [][]string{
	// Version 1: the initial schema.
	{
		`CREATE TABLE runs (
			run_uid    TEXT PRIMARY KEY,
			namespace  TEXT NOT NULL,
			name       TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE intervals (
			run_uid            TEXT    NOT NULL,
			scenario           TEXT    NOT NULL,
			interval           INTEGER NOT NULL,
			agent              INTEGER NOT NULL,
			stage              INTEGER NOT NULL,
			warmup             INTEGER NOT NULL,
			workers            INTEGER NOT NULL,
			cpu                REAL    NOT NULL,
			sched_delay_p99_ms REAL    NOT NULL,
			PRIMARY KEY (run_uid, scenario, interval, agent)
		)`,
		`CREATE TABLE operations (
			run_uid   TEXT    NOT NULL,
			scenario  TEXT    NOT NULL,
			interval  INTEGER NOT NULL,
			agent     INTEGER NOT NULL,
			operation TEXT    NOT NULL,
			count     INTEGER NOT NULL,
			errors    INTEGER NOT NULL,
			bytes     INTEGER NOT NULL,
			PRIMARY KEY (run_uid, scenario, interval, agent, operation)
		)`,
		`CREATE TABLE histograms (
			run_uid   TEXT    NOT NULL,
			scenario  TEXT    NOT NULL,
			interval  INTEGER NOT NULL,
			agent     INTEGER NOT NULL,
			operation TEXT    NOT NULL,
			metric    TEXT    NOT NULL,
			data      BLOB    NOT NULL,
			PRIMARY KEY (run_uid, scenario, interval, agent, operation, metric)
		)`,
		`CREATE TABLE counters (
			run_uid   TEXT    NOT NULL,
			scenario  TEXT    NOT NULL,
			interval  INTEGER NOT NULL,
			agent     INTEGER NOT NULL,
			operation TEXT    NOT NULL,
			kind      TEXT    NOT NULL,
			name      TEXT    NOT NULL,
			value     INTEGER NOT NULL,
			PRIMARY KEY (run_uid, scenario, interval, agent, operation, kind, name)
		)`,
		`CREATE TABLE error_examples (
			run_uid   TEXT NOT NULL,
			scenario  TEXT NOT NULL,
			operation TEXT NOT NULL,
			type      TEXT NOT NULL,
			message   TEXT NOT NULL,
			PRIMARY KEY (run_uid, scenario, operation, type)
		)`,
		`CREATE TABLE samples (
			run_uid    TEXT    NOT NULL,
			scenario   TEXT    NOT NULL,
			agent      INTEGER NOT NULL,
			operation  TEXT    NOT NULL,
			offset_us  INTEGER NOT NULL,
			latency_us INTEGER NOT NULL,
			total_us   INTEGER NOT NULL,
			bytes      INTEGER NOT NULL,
			error      TEXT    NOT NULL
		)`,
		`CREATE INDEX samples_by_run ON samples (run_uid, scenario)`,
	},
}

// SchemaVersion is the schema version this code writes.
var SchemaVersion = len(migrations)

// migrate applies every migration the database has not seen yet.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("creating schema_version table: %w", err)
	}

	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("the database has schema version %d, but this controller only knows up to %d; use a newer controller", current, len(migrations))
	}

	for version := current + 1; version <= len(migrations); version++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, statement := range migrations[version-1] {
			if _, err := tx.Exec(statement); err != nil {
				tx.Rollback() //nolint:errcheck // already failing
				return fmt.Errorf("applying schema version %d: %w", version, err)
			}
		}
		if _, err := tx.Exec(`DELETE FROM schema_version`); err != nil {
			tx.Rollback() //nolint:errcheck // already failing
			return err
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
			tx.Rollback() //nolint:errcheck // already failing
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
