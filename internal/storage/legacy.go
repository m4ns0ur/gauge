package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// importLegacyFileStore imports metadata written by Gauge builds that used the
// file-based store (`runs/<experiment>/experiment.json` and
// `<attempt>/metadata.json`). It runs only when the database has no
// experiments, so an existing or partially used database is never touched.
//
// Import is best-effort: unreadable legacy records are skipped and reported
// through Store.Warnings instead of preventing Gauge from opening the store.
// Legacy files are left in place; artifacts were always files and are unchanged.
func (s *Store) importLegacyFileStore() {
	entries, err := os.ReadDir(s.runsDir)
	if err != nil {
		return
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM experiments`).Scan(&count); err != nil {
		s.warn("legacy import skipped: cannot read database: %v", err)
		return
	}
	if count > 0 {
		return
	}

	experiments := make([]*Experiment, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.runsDir, entry.Name())
		var exp Experiment
		err := readLegacyJSON(filepath.Join(dir, "experiment.json"), &exp)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			s.warn("legacy experiment %s skipped: %v", entry.Name(), err)
			continue
		}
		if exp.SchemaVersion > SchemaVersion {
			s.warn("legacy experiment %s skipped: schema %d is newer than %d", entry.Name(), exp.SchemaVersion, SchemaVersion)
			continue
		}
		if exp.SchemaVersion == 0 {
			exp.SchemaVersion = SchemaVersion
		}
		if err := validateExperiment(&exp); err != nil {
			s.warn("legacy experiment %s skipped: %v", entry.Name(), err)
			continue
		}
		if exp.ID != entry.Name() {
			s.warn("legacy experiment %s skipped: record names %q", entry.Name(), exp.ID)
			continue
		}
		experiments = append(experiments, &exp)
	}
	if len(experiments) == 0 {
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		s.warn("legacy import failed: %v", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	imported := make(map[string]bool, len(experiments))
	for _, exp := range experiments {
		args, err := experimentArgs(exp)
		if err != nil {
			s.warn("legacy experiment %s skipped: %v", exp.ID, err)
			continue
		}
		if _, err := tx.Exec(upsertExperimentSQL, args...); err != nil {
			s.warn("legacy experiment %s skipped: %v", exp.ID, err)
			continue
		}
		imported[exp.ID] = true
	}

	for _, exp := range experiments {
		if !imported[exp.ID] {
			continue
		}
		dir := filepath.Join(s.runsDir, exp.ID)
		attemptEntries, err := os.ReadDir(dir)
		if err != nil {
			s.warn("legacy attempts for %s skipped: %v", exp.ID, err)
			continue
		}
		for _, entry := range attemptEntries {
			if !entry.IsDir() {
				continue
			}
			var attempt Attempt
			err := readLegacyJSON(filepath.Join(dir, entry.Name(), "metadata.json"), &attempt)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				s.warn("legacy attempt %s/%s skipped: %v", exp.ID, entry.Name(), err)
				continue
			}
			if attempt.SchemaVersion > SchemaVersion {
				s.warn("legacy attempt %s/%s skipped: schema %d is newer than %d", exp.ID, entry.Name(), attempt.SchemaVersion, SchemaVersion)
				continue
			}
			if attempt.SchemaVersion == 0 {
				attempt.SchemaVersion = SchemaVersion
			}
			if attempt.ExperimentID != exp.ID || attempt.ID != entry.Name() {
				s.warn("legacy attempt %s/%s skipped: record identifies %q/%q", exp.ID, entry.Name(), attempt.ExperimentID, attempt.ID)
				continue
			}
			if err := validateID("attempt", attempt.ID); err != nil {
				s.warn("legacy attempt %s/%s skipped: %v", exp.ID, entry.Name(), err)
				continue
			}
			if err := validateAttempt(&attempt); err != nil {
				s.warn("legacy attempt %s/%s skipped: %v", exp.ID, entry.Name(), err)
				continue
			}
			if err := saveAttemptRows(tx, &attempt); err != nil {
				s.warn("legacy attempt %s/%s skipped: %v", exp.ID, entry.Name(), err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		s.warn("legacy import failed: %v", err)
		return
	}
}

func readLegacyJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func (s *Store) warn(format string, args ...any) {
	s.warnings = append(s.warnings, fmt.Sprintf(format, args...))
}

// Warnings reports recoverable problems found while opening the store, such as
// legacy records that could not be imported. A non-empty result does not mean
// the store is unusable.
func (s *Store) Warnings() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.warnings...)
}
