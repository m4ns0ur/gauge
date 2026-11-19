package storage_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/evaluator"
	"github.com/m4ns0ur/gauge/internal/events"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/workspace"
)

func newExperiment(id string) *storage.Experiment {
	now := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	return &storage.Experiment{
		SchemaVersion: storage.SchemaVersion,
		ID:            id,
		Status:        storage.StatusRunning,
		CreatedAt:     now,
		StartedAt:     now,
		Gauge:         storage.GaugeInfo{Version: "test", Commit: "abc"},
		Agent: storage.AgentInfo{
			ID:                "fake",
			Description:       "deterministic test agent",
			ConfigFingerprint: storage.FingerprintJSON(nil),
			Config:            json.RawMessage(`{}`),
		},
		Suite: &storage.SuiteInfo{
			ID:          "smoke",
			Name:        "Smoke suite",
			Fingerprint: "sha256:suite",
			File:        "/suites/smoke.yaml",
			Tasks:       []string{"task-one"},
		},
		Selection: []storage.TaskSelection{{
			ID:          "task-one",
			Category:    "repository-engineering",
			Fingerprint: "sha256:x",
			Fixture:     strings.Repeat("a", 40),
		}},
		Repetitions: 1,
		Environment: storage.EnvironmentInfo{OS: "test", Arch: "test", Backend: "local", DataDir: "/tmp/data"},
		Notes:       "initial",
	}
}

func newAttempt(expID, id string) *storage.Attempt {
	start := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	code := 0
	input, output, total := int64(100), int64(20), int64(120)
	cost := 0.0125
	score := 1.0
	return &storage.Attempt{
		SchemaVersion:   storage.SchemaVersion,
		ID:              id,
		ExperimentID:    expID,
		TaskID:          "task-one",
		TaskName:        "Task One",
		TaskCategory:    "repository-engineering",
		TaskFingerprint: "sha256:task",
		Repetition:      0,
		AgentID:         "fake",
		AgentVersion:    "fake-1.0.0",
		Model:           "fake/deterministic",
		Provider:        "fake",
		StartedAt:       start,
		EndedAt:         start.Add(time.Second),
		DurationMS:      1000,
		Execution: storage.ExecutionInfo{
			Completion: adapter.Completed,
			ExitCode:   &code,
			Error:      "",
		},
		Setup: &storage.SetupInfo{Ran: true, Steps: 1},
		Usage: &events.Usage{
			InputTokens:  &input,
			OutputTokens: &output,
			TotalTokens:  &total,
			CostUSD:      &cost,
		},
		Diff: storage.DiffInfo{
			Baseline: strings.Repeat("b", 40),
			Files: []workspace.FileStatus{
				{Index: "M", Worktree: "M", Path: "greeting.go"},
			},
			Changed:   true,
			Truncated: false,
		},
		Evaluation: &storage.EvaluationInfo{
			Status:  evaluator.Passed,
			Summary: "everything passed",
			Score:   &score,
			Checks: []evaluator.Check{
				{ID: "check-one", Status: evaluator.Passed, Message: "first"},
				{ID: "check-two", Status: evaluator.Passed, Message: "second"},
			},
			ExitCode:   &code,
			DurationMS: 250,
		},
		Outcome: storage.OutcomePassed,
		Artifacts: map[string]storage.Artifact{
			"events.jsonl": {Path: "events.jsonl", Size: 12, SHA256: strings.Repeat("a", 64)},
			"diff.patch":   {Path: "diff.patch", Size: 34, SHA256: strings.Repeat("b", 64)},
		},
		AdapterMetadata: json.RawMessage(`{"tool_calls":3}`),
		WorkspaceKept:   false,
		Error:           "",
	}
}

func openStore(t *testing.T, dir string) *storage.Store {
	t.Helper()
	store, err := storage.New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func TestExperimentRoundTrip(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "data"))
	exp := newExperiment("exp-one")
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.CreateExperiment(exp); err == nil {
		t.Fatal("expected duplicate experiment error")
	}

	loaded, err := store.GetExperiment("exp-one")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(loaded, exp) {
		t.Errorf("loaded experiment differs from the created one\nloaded: %+v\nwant:   %+v", loaded, exp)
	}

	ended := exp.StartedAt.Add(time.Minute)
	exp.Status = storage.StatusCompleted
	exp.EndedAt = &ended
	exp.Notes = "updated"
	if err := store.SaveExperiment(exp); err != nil {
		t.Fatalf("save: %v", err)
	}
	updated, err := store.GetExperiment("exp-one")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != storage.StatusCompleted || updated.EndedAt == nil || !updated.EndedAt.Equal(ended) || updated.Notes != "updated" {
		t.Errorf("updated experiment = %+v", updated)
	}
}

func TestAttemptRoundTripAndChildren(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "data"))
	if err := store.CreateExperiment(newExperiment("exp-one")); err != nil {
		t.Fatal(err)
	}
	attempt := newAttempt("exp-one", "attempt-one")
	if err := store.SaveAttempt("exp-one", attempt); err != nil {
		t.Fatalf("save attempt: %v", err)
	}

	attempts, err := store.ListAttempts("exp-one")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %+v", attempts)
	}
	if !reflect.DeepEqual(attempts[0], attempt) {
		t.Errorf("listed attempt differs\nlisted: %+v\nwant:   %+v", attempts[0], attempt)
	}

	found, err := store.FindAttempt("attempt-one")
	if err != nil {
		t.Fatalf("find attempt: %v", err)
	}
	if !reflect.DeepEqual(found, attempt) {
		t.Errorf("found attempt differs")
	}
	if _, err := store.FindAttempt("missing"); err == nil {
		t.Fatal("expected missing attempt error")
	}
	if _, err := store.GetAttempt("exp-other", "attempt-one"); err == nil {
		t.Fatal("expected cross-experiment lookup to fail")
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store, err := storage.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	exp := newExperiment("exp-one")
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatal(err)
	}
	attempt := newAttempt("exp-one", "attempt-one")
	if err := store.SaveAttempt("exp-one", attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}

	reopened, err := storage.New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	loaded, err := reopened.GetAttempt("exp-one", "attempt-one")
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if !reflect.DeepEqual(loaded, attempt) {
		t.Errorf("attempt after reopen differs\nloaded: %+v\nwant:   %+v", loaded, attempt)
	}
	experiments, err := reopened.ListExperiments()
	if err != nil || len(experiments) != 1 || experiments[0].ID != "exp-one" {
		t.Fatalf("experiments after reopen = %+v, %v", experiments, err)
	}
}

func TestDatabaseLayoutAndMetrics(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store := openStore(t, dir)
	if err := store.CreateExperiment(newExperiment("exp-one")); err != nil {
		t.Fatal(err)
	}
	attempt := newAttempt("exp-one", "attempt-one")
	if err := store.SaveAttempt("exp-one", attempt); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "gauge.db")); err != nil {
		t.Errorf("database file missing: %v", err)
	}
	attemptDir, err := store.AttemptDir("exp-one", "attempt-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(attemptDir); err != nil {
		t.Errorf("attempt directory missing: %v", err)
	}
	// The SQLite backend must not resurrect the old JSON metadata files.
	for _, name := range []string{"experiment.json", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(attemptDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should not be written by the SQLite store", name)
		}
	}

	db := openRaw(t, store.DatabasePath())
	defer db.Close()

	rows, err := db.Query(`SELECT name, value, unit, source FROM metrics WHERE attempt_id = ? ORDER BY name`, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	metrics := map[string]struct {
		value  float64
		unit   string
		source string
	}{}
	for rows.Next() {
		var name, unit, source string
		var value float64
		if err := rows.Scan(&name, &value, &unit, &source); err != nil {
			t.Fatal(err)
		}
		metrics[name] = struct {
			value  float64
			unit   string
			source string
		}{value, unit, source}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	expected := map[string]struct {
		value  float64
		unit   string
		source string
	}{
		"duration_ms":            {1000, "ms", "gauge-observed"},
		"usage.input_tokens":     {100, "tokens", "adapter-reported"},
		"usage.output_tokens":    {20, "tokens", "adapter-reported"},
		"usage.total_tokens":     {120, "tokens", "adapter-reported"},
		"usage.cost_usd":         {0.0125, "usd", "adapter-reported"},
		"evaluation.duration_ms": {250, "ms", "gauge-observed"},
	}
	if !reflect.DeepEqual(metrics, expected) {
		t.Errorf("metrics = %+v\nwant %+v", metrics, expected)
	}

	var checks, artifacts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM checks WHERE attempt_id = ?`, attempt.ID).Scan(&checks); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE attempt_id = ?`, attempt.ID).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if checks != 2 || artifacts != 2 {
		t.Errorf("child rows = %d checks, %d artifacts", checks, artifacts)
	}
}

func TestResavingAttemptReplacesChildRows(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "data"))
	if err := store.CreateExperiment(newExperiment("exp-one")); err != nil {
		t.Fatal(err)
	}
	attempt := newAttempt("exp-one", "attempt-one")
	if err := store.SaveAttempt("exp-one", attempt); err != nil {
		t.Fatal(err)
	}

	attempt.Evaluation.Checks = []evaluator.Check{{ID: "only-check", Status: evaluator.Passed}}
	attempt.Artifacts = map[string]storage.Artifact{
		"events.jsonl": {Path: "events.jsonl", Size: 1, SHA256: strings.Repeat("c", 64)},
	}
	attempt.Usage = nil
	if err := store.SaveAttempt("exp-one", attempt); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.GetAttempt("exp-one", "attempt-one")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Evaluation == nil || len(loaded.Evaluation.Checks) != 1 || loaded.Evaluation.Checks[0].ID != "only-check" {
		t.Errorf("checks after resave = %+v", loaded.Evaluation)
	}
	if len(loaded.Artifacts) != 1 {
		t.Errorf("artifacts after resave = %+v", loaded.Artifacts)
	}
	if loaded.Usage != nil {
		t.Errorf("usage after resave = %+v", loaded.Usage)
	}

	db := openRaw(t, store.DatabasePath())
	defer db.Close()
	var missingUsageMetrics int
	if err := db.QueryRow(`SELECT COUNT(*) FROM metrics WHERE attempt_id = ? AND name LIKE 'usage.%'`, attempt.ID).Scan(&missingUsageMetrics); err != nil {
		t.Fatal(err)
	}
	if missingUsageMetrics != 0 {
		t.Errorf("stale usage metrics = %d", missingUsageMetrics)
	}
}

func TestListOrdering(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "data"))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"exp-a", "exp-b", "exp-c"} {
		exp := newExperiment(id)
		exp.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		exp.StartedAt = exp.CreatedAt
		if err := store.CreateExperiment(exp); err != nil {
			t.Fatal(err)
		}
	}
	experiments, err := store.ListExperiments()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, exp := range experiments {
		ids = append(ids, exp.ID)
	}
	if strings.Join(ids, ",") != "exp-a,exp-b,exp-c" {
		t.Errorf("experiment order = %v", ids)
	}

	exp := newExperiment("exp-attempts")
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"attempt-late", "attempt-early", "attempt-mid"} {
		attempt := newAttempt("exp-attempts", id)
		attempt.StartedAt = base.Add(time.Duration(3-i) * time.Minute)
		attempt.EndedAt = attempt.StartedAt.Add(time.Second)
		if err := store.SaveAttempt("exp-attempts", attempt); err != nil {
			t.Fatal(err)
		}
	}
	attempts, err := store.ListAttempts("exp-attempts")
	if err != nil {
		t.Fatal(err)
	}
	var attemptIDs []string
	for _, attempt := range attempts {
		attemptIDs = append(attemptIDs, attempt.ID)
	}
	if strings.Join(attemptIDs, ",") != "attempt-mid,attempt-early,attempt-late" {
		t.Errorf("attempt order = %v", attemptIDs)
	}
}

func TestSecretsAreNotPersisted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store := openStore(t, dir)
	raw := json.RawMessage(`{"mode":"invoke","api_key":"sk-live-secret-value","endpoint":"http://localhost:1234"}`)
	redacted, changed := storage.RedactJSON(raw)
	if !changed {
		t.Fatal("expected the config to be redacted")
	}
	exp := newExperiment("exp-secret")
	exp.Agent.Config = redacted
	exp.Agent.ConfigRedacted = true
	exp.Agent.ConfigFingerprint = storage.FingerprintJSON(redacted)
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatal(err)
	}

	db := openRaw(t, store.DatabasePath())
	defer db.Close()
	var config string
	if err := db.QueryRow(`SELECT agent_config FROM experiments WHERE id = ?`, "exp-secret").Scan(&config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, "sk-live-secret-value") {
		t.Errorf("secret was persisted in the database: %s", config)
	}
	if !strings.Contains(config, "localhost:1234") {
		t.Errorf("non-secret config was lost: %s", config)
	}
}

func TestStorageRejectsBadIDsAndPaths(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "data"))
	exp := newExperiment("../escape")
	if err := store.CreateExperiment(exp); err == nil {
		t.Fatal("expected invalid experiment id error")
	}
	if _, err := store.GetExperiment("../escape"); err == nil {
		t.Fatal("expected invalid experiment id on read")
	}
	if _, err := store.GetAttempt("exp", "../../etc/passwd"); err == nil {
		t.Fatal("expected invalid attempt id on read")
	}
}

func TestAttemptMustBelongToExperiment(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "data"))
	if err := store.CreateExperiment(newExperiment("exp-one")); err != nil {
		t.Fatal(err)
	}
	attempt := newAttempt("exp-one", "attempt-one")
	attempt.ExperimentID = "other"
	if err := store.SaveAttempt("exp-one", attempt); err == nil {
		t.Fatal("expected experiment mismatch error")
	}
}

func TestAttemptRequiresExistingExperiment(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "data"))
	attempt := newAttempt("exp-missing", "attempt-one")
	err := store.SaveAttempt("exp-missing", attempt)
	if err == nil {
		t.Fatal("expected missing experiment error")
	}
	if !errors.Is(err, storage.ErrExperimentNotFound) {
		t.Errorf("error = %v, want ErrExperimentNotFound", err)
	}
	if _, err := store.CreateAttemptDir("exp-missing", "attempt-two"); err == nil {
		t.Fatal("expected CreateAttemptDir to require an experiment")
	}
}

func TestAttemptValidation(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "data"))
	if err := store.CreateExperiment(newExperiment("exp-one")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*storage.Attempt)
	}{
		{"bad outcome", func(a *storage.Attempt) { a.Outcome = "maybe" }},
		{"missing completion", func(a *storage.Attempt) { a.Execution.Completion = "" }},
		{"missing times", func(a *storage.Attempt) { a.StartedAt = time.Time{} }},
		{"ends before start", func(a *storage.Attempt) { a.EndedAt = a.StartedAt.Add(-time.Second) }},
		{"missing task", func(a *storage.Attempt) { a.TaskID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempt := newAttempt("exp-one", "attempt-"+strings.ReplaceAll(tc.name, " ", "-"))
			tc.mutate(attempt)
			if err := store.SaveAttempt("exp-one", attempt); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSchemaVersionGuards(t *testing.T) {
	t.Run("record schema newer", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		store := openStore(t, dir)
		if err := store.CreateExperiment(newExperiment("exp-future")); err != nil {
			t.Fatal(err)
		}
		db := openRaw(t, store.DatabasePath())
		if _, err := db.Exec(`UPDATE experiments SET schema_version = 99 WHERE id = ?`, "exp-future"); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if _, err := store.GetExperiment("exp-future"); err == nil || !strings.Contains(err.Error(), "newer") {
			t.Fatalf("expected newer-schema error, got %v", err)
		}
	})

	t.Run("database schema newer", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		store, err := storage.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		db := openRaw(t, filepath.Join(dir, "gauge.db"))
		if _, err := db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (99, ?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if _, err := storage.New(dir); err == nil || !strings.Contains(err.Error(), "newer") {
			t.Fatalf("expected newer database schema error, got %v", err)
		}
	})
}

func TestCorruptDatabaseIsRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gauge.db"), []byte("this is not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.New(dir); err == nil {
		t.Fatal("expected corrupt database error")
	}
}

func TestNewRequiresDataDir(t *testing.T) {
	if _, err := storage.New("  "); err == nil {
		t.Fatal("expected empty data directory error")
	}
}

func TestArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts, err := storage.CollectArtifacts(dir, "events.jsonl", "missing.log")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	artifact, ok := artifacts["events.jsonl"]
	if !ok {
		t.Fatal("events artifact missing")
	}
	if artifact.Size != 8 || len(artifact.SHA256) != 64 {
		t.Errorf("artifact = %+v", artifact)
	}
	if _, ok := artifacts["missing.log"]; ok {
		t.Error("missing artifact should be skipped, not recorded")
	}
}

func TestRedaction(t *testing.T) {
	raw := json.RawMessage(`{
		"mode": "invoke",
		"api_key": "sk-live-123456789",
		"nested": {"password": "hunter2", "endpoint": "http://localhost:1234"},
		"headers": {"Authorization": "Bearer abcdef"},
		"list": [{"token": "abc"}, "plain"],
		"usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
		"note": "sk-abcdefghijklmnop"
	}`)
	redacted, changed := storage.RedactJSON(raw)
	if !changed {
		t.Fatal("expected redaction to report a change")
	}
	text := string(redacted)
	for _, secret := range []string{"sk-live-123456789", "hunter2", "Bearer abcdef", "sk-abcdefghijklmnop"} {
		if strings.Contains(text, secret) {
			t.Errorf("redacted config still contains %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "localhost:1234") || !strings.Contains(text, "plain") {
		t.Errorf("non-secret values were dropped: %s", text)
	}
	for _, want := range []string{`"input_tokens":10`, `"output_tokens":5`, `"total_tokens":15`} {
		if !strings.Contains(text, want) {
			t.Errorf("usage counts were redacted or lost (%s missing): %s", want, text)
		}
	}

	clean := json.RawMessage(`{"mode":"x","endpoint":"http://localhost"}`)
	if _, changed := storage.RedactJSON(clean); changed {
		t.Error("clean config reported a change")
	}
}

func TestFingerprintJSONStable(t *testing.T) {
	a := storage.FingerprintJSON(json.RawMessage(`{"b":2,"a":1}`))
	b := storage.FingerprintJSON(json.RawMessage(`{"a":1,"b":2}`))
	if a != b {
		t.Errorf("fingerprint depends on key order: %s vs %s", a, b)
	}
	if a == storage.FingerprintJSON(json.RawMessage(`{"a":1}`)) {
		t.Error("different configs share a fingerprint")
	}
}

func TestTokenCountsAreNotSecrets(t *testing.T) {
	raw := json.RawMessage(`{"tokens":{"input_tokens":1,"total_tokens":2},"token":"credential-value","auth_token":"another-value","max_tokens":4096}`)
	redacted, changed := storage.RedactJSON(raw)
	if !changed {
		t.Fatal("expected credentials to be redacted")
	}
	text := string(redacted)
	for _, want := range []string{`"input_tokens":1`, `"total_tokens":2`, `"max_tokens":4096`} {
		if !strings.Contains(text, want) {
			t.Errorf("token count lost (%s missing): %s", want, text)
		}
	}
	for _, secret := range []string{"credential-value", "another-value"} {
		if strings.Contains(text, secret) {
			t.Errorf("credential %q was persisted: %s", secret, text)
		}
	}
}

// openRaw opens the SQLite database directly for assertions about normalized
// rows. The driver is registered by the storage package import.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping raw database: %v", err)
	}
	return db
}
