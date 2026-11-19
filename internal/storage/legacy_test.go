package storage_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/m4ns0ur/gauge/internal/storage"
)

// writeLegacyRecord writes a JSON document shaped like the metadata files the
// pre-SQLite file store used.
func writeLegacyRecord(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyFileStoreIsImported(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	exp := newExperiment("exp-legacy")
	attempt := newAttempt("exp-legacy", "attempt-legacy")
	writeLegacyRecord(t, filepath.Join(dir, "runs", exp.ID, "experiment.json"), exp)
	writeLegacyRecord(t, filepath.Join(dir, "runs", exp.ID, attempt.ID, "metadata.json"), attempt)

	store := openStore(t, dir)
	if warnings := store.Warnings(); len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	experiments, err := store.ListExperiments()
	if err != nil {
		t.Fatal(err)
	}
	if len(experiments) != 1 || experiments[0].ID != exp.ID {
		t.Fatalf("experiments = %+v", experiments)
	}
	if !reflect.DeepEqual(experiments[0], exp) {
		t.Errorf("imported experiment differs\nimported: %+v\nwant:     %+v", experiments[0], exp)
	}
	loaded, err := store.GetAttempt(exp.ID, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, attempt) {
		t.Errorf("imported attempt differs\nimported: %+v\nwant:     %+v", loaded, attempt)
	}

	// Reopening must not duplicate records or attempt a second import.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	experiments, err = reopened.ListExperiments()
	if err != nil || len(experiments) != 1 {
		t.Fatalf("experiments after reopen = %d, %v", len(experiments), err)
	}
	if len(reopened.Warnings()) != 0 {
		t.Errorf("reopen warnings = %v", reopened.Warnings())
	}
}

func TestLegacyImportSkipsUnreadableRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	runs := filepath.Join(dir, "runs")

	valid := newExperiment("exp-valid")
	writeLegacyRecord(t, filepath.Join(runs, valid.ID, "experiment.json"), valid)

	if err := os.MkdirAll(filepath.Join(runs, "exp-corrupt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs, "exp-corrupt", "experiment.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	future := newExperiment("exp-future")
	future.SchemaVersion = 99
	writeLegacyRecord(t, filepath.Join(runs, future.ID, "experiment.json"), future)

	store := openStore(t, dir)
	experiments, err := store.ListExperiments()
	if err != nil {
		t.Fatal(err)
	}
	if len(experiments) != 1 || experiments[0].ID != "exp-valid" {
		t.Fatalf("experiments = %+v", experiments)
	}
	warnings := strings.Join(store.Warnings(), "\n")
	if !strings.Contains(warnings, "exp-corrupt") {
		t.Errorf("warnings do not mention the corrupt record: %v", store.Warnings())
	}
	if !strings.Contains(warnings, "exp-future") {
		t.Errorf("warnings do not mention the newer-schema record: %v", store.Warnings())
	}
}
