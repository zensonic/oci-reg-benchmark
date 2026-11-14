// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package store keeps the detailed, full-resolution results of every run in
// a SQLite database owned by the controller (RES-8 to RES-12).
//
// Only the controller reads and writes the database (RES-10). Agents send
// their interval reports to the controller, which stores each one in a
// single transaction.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	// The SQLite driver has a CNCF license exception (see requirements.md, section 15).
	_ "github.com/mattn/go-sqlite3"

	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// Store is the SQLite detail store.
type Store struct {
	db   *sql.DB
	path string

	// exportMu serializes run exports, which attach the database file.
	exportMu sync.Mutex
}

// Open opens (or creates) the database at path and brings its schema up to
// date (RES-11).
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_busy_timeout=10000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("opening SQLite database %s: %w", path, err)
	}
	// One connection: the controller is the only writer, and SQLite allows
	// only one writer at a time anyway.
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// EnsureRun records a run. Calling it again for the same run does nothing.
func (s *Store) EnsureRun(runUID, namespace, name string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO runs (run_uid, namespace, name, created_at) VALUES (?, ?, ?, ?)`,
		runUID, namespace, name, time.Now().UTC().Format(time.RFC3339))
	return err
}

// InsertReport stores one agent's interval report in a single transaction.
func (s *Store) InsertReport(runUID string, agent int, r protocol.IntervalReport) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit

	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO intervals
		 (run_uid, scenario, interval, agent, stage, warmup, workers, cpu, sched_delay_p99_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		runUID, r.ScenarioName, r.Interval, agent, r.Stage, r.Warmup, r.Workers,
		r.CPUUtilization, r.SchedDelayP99Millis); err != nil {
		return fmt.Errorf("storing interval: %w", err)
	}

	for _, op := range r.Operations {
		if err := insertOperation(tx, runUID, agent, r, op); err != nil {
			return err
		}
	}

	for _, sample := range r.Samples {
		if _, err := tx.Exec(
			`INSERT INTO samples (run_uid, scenario, agent, operation, offset_us, latency_us, total_us, bytes, error)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runUID, r.ScenarioName, agent, sample.Operation, sample.OffsetMicros,
			sample.LatencyMicros, sample.TotalMicros, sample.Bytes, sample.Error); err != nil {
			return fmt.Errorf("storing sample: %w", err)
		}
	}

	return tx.Commit()
}

func insertOperation(tx *sql.Tx, runUID string, agent int, r protocol.IntervalReport, op protocol.OperationReport) error {
	var errorCount int64
	for _, n := range op.Errors {
		errorCount += n
	}

	key := []any{runUID, r.ScenarioName, r.Interval, agent, op.Operation}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO operations (run_uid, scenario, interval, agent, operation, count, errors, bytes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		append(key, op.Count, errorCount, op.Bytes)...); err != nil {
		return fmt.Errorf("storing operation: %w", err)
	}

	for metric, data := range op.Histograms {
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO histograms (run_uid, scenario, interval, agent, operation, metric, data)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			append(key, metric, data)...); err != nil {
			return fmt.Errorf("storing histogram: %w", err)
		}
	}

	insertCounter := func(kind, name string, value int64) error {
		_, err := tx.Exec(
			`INSERT OR REPLACE INTO counters (run_uid, scenario, interval, agent, operation, kind, name, value)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			append(key, kind, name, value)...)
		return err
	}
	for errorType, n := range op.Errors {
		if err := insertCounter(CounterKindError, errorType, n); err != nil {
			return fmt.Errorf("storing error count: %w", err)
		}
	}
	for name, n := range op.Counters {
		if err := insertCounter(CounterKindCounter, name, n); err != nil {
			return fmt.Errorf("storing counter: %w", err)
		}
	}

	for errorType, message := range op.ErrorExamples {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO error_examples (run_uid, scenario, operation, type, message)
			 VALUES (?, ?, ?, ?, ?)`,
			runUID, r.ScenarioName, op.Operation, errorType, message); err != nil {
			return fmt.Errorf("storing error example: %w", err)
		}
	}
	return nil
}

// DeleteRun removes all data of a run (RES-12).
func (s *Store) DeleteRun(runUID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit

	for _, table := range runTables {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE run_uid = ?`, runUID); err != nil {
			return fmt.Errorf("deleting run data from %s: %w", table, err)
		}
	}
	return tx.Commit()
}

// ExportRun writes a standalone SQLite database that holds only one run's
// data to path (RES-14). An existing file at path is replaced.
func (s *Store) ExportRun(runUID, path string) error {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	out, err := Open(path)
	if err != nil {
		return err
	}
	if err := s.copyRun(out, runUID); err != nil {
		_ = out.Close()
		return err
	}
	// Closing flushes the new database file, so its error matters.
	return out.Close()
}

// copyRun copies one run's rows from s into out.
func (s *Store) copyRun(out *Store, runUID string) error {

	// Make sure recent writes are in the main database file, which the
	// export attaches.
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		return fmt.Errorf("checkpointing database: %w", err)
	}

	if _, err := out.db.Exec(`ATTACH DATABASE ? AS src`, "file:"+s.path+"?mode=ro"); err != nil {
		return fmt.Errorf("attaching source database: %w", err)
	}
	for _, table := range runTables {
		if _, err := out.db.Exec(`INSERT INTO `+table+` SELECT * FROM src.`+table+` WHERE run_uid = ?`, runUID); err != nil {
			return fmt.Errorf("copying %s: %w", table, err)
		}
	}
	if _, err := out.db.Exec(`DETACH DATABASE src`); err != nil {
		return err
	}
	// A self-contained file: fold the write-ahead log back into it.
	_, err := out.db.Exec(`PRAGMA journal_mode=DELETE`)
	return err
}

// HasRun reports whether the store has any data for the run.
func (s *Store) HasRun(runUID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE run_uid = ?`, runUID).Scan(&n)
	return n > 0, err
}
