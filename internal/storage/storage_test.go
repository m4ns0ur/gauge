package storage_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/storage"
)

func newExperiment(id string) *storage.Experiment {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &storage.Experiment{
		SchemaVersion: storage.SchemaVersion,
		ID:            id,
		Status:        storage.StatusRunning,
		CreatedAt:     now,
		StartedAt:     now,
		Gauge:         storage.GaugeInfo{Version: "test", Commit: "abc"},
		Agent: storage.AgentInfo{
			ID:                "fake",
			ConfigFingerprint: storage.FingerprintJSON(nil),
			Config:            json.RawMessage(`{}`),
		},
		Selection: []storage.TaskSelection{{
			ID:          "task-one",
			Category:    "repository-engineering",
			Fingerprint: "sha256:x",
			Fixture:     strings.Repeat("a", 40),
		}},
		Repetitions: 1,
		Environment: storage.EnvironmentInfo{OS: "test", Arch: "test", Backend: "local"},
	}
}

func newAttempt(expID, id string) *storage.Attempt {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &storage.Attempt{
		SchemaVersion: storage.SchemaVersion,
		ID:            id,
		ExperimentID:  expID,
		TaskID:        "task-one",
		TaskName:      "Task One",
		TaskCategory:  "repository-engineering",
		AgentID:       "fake",
		StartedAt:     start,
		EndedAt:       start.Add(time.Second),
		DurationMS:    1000,
		Outcome:       storage.OutcomePassed,
		Execution:     storage.ExecutionInfo{Completion: adapter.Completed},
		Artifacts:     map[string]storage.Artifact{},
	}
}

func TestExperimentRoundTrip(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
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
	if loaded.Agent.ID != "fake" || loaded.Status != storage.StatusRunning {
		t.Errorf("loaded = %+v", loaded)
	}

	attempt := newAttempt("exp-one", "attempt-one-r01")
	if err := store.SaveAttempt("exp-one", attempt); err != nil {
		t.Fatalf("save attempt: %v", err)
	}
	attempts, err := store.ListAttempts("exp-one")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].ID != attempt.ID {
		t.Fatalf("attempts = %+v", attempts)
	}

	found, err := store.FindAttempt("attempt-one-r01")
	if err != nil {
		t.Fatalf("find attempt: %v", err)
	}
	if found.ExperimentID != "exp-one" {
		t.Errorf("found experiment = %q", found.ExperimentID)
	}
	if _, err := store.FindAttempt("missing"); err == nil {
		t.Fatal("expected missing attempt error")
	}
}

func TestStorageRejectsBadIDsAndPaths(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
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
	store, err := storage.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateExperiment(newExperiment("exp-one")); err != nil {
		t.Fatal(err)
	}
	attempt := newAttempt("exp-one", "attempt-one")
	attempt.ExperimentID = "other"
	if err := store.SaveAttempt("exp-one", attempt); err == nil {
		t.Fatal("expected experiment mismatch error")
	}
}

func TestAttemptValidation(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
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

func TestListExperimentsReportsUnreadableWithoutHidingOthers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store, err := storage.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateExperiment(newExperiment("exp-one")); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "runs", "exp-broken")
	if err := os.MkdirAll(corrupt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, "experiment.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	experiments, listErr := store.ListExperiments()
	if listErr == nil {
		t.Fatal("expected a warning error for the corrupt experiment")
	}
	if len(experiments) != 1 || experiments[0].ID != "exp-one" {
		t.Fatalf("readable experiments = %+v", experiments)
	}
}

func TestSchemaVersionTooNew(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store, err := storage.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	expDir := filepath.Join(dir, "runs", "future")
	if err := os.MkdirAll(expDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"schema_version":99,"id":"future","status":"completed","agent":{"id":"fake"}}`
	if err := os.WriteFile(filepath.Join(expDir, "experiment.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetExperiment("future"); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("expected newer-schema error, got %v", err)
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
