package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/notes/notes"
)

func TestDataPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.json")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--data", path, "add", "buy milk"}, notes.NewStore(), &stdout, &stderr); code != 0 {
		t.Fatalf("add exit code = %d (stderr %q)", code, stderr.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("data file was not written: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--data", path, "list"}, notes.NewStore(), &stdout, &stderr); code != 0 {
		t.Fatalf("list exit code = %d (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "buy milk") {
		t.Fatalf("list output = %q, want the persisted note", stdout.String())
	}
}

func TestInvalidDataFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--data", path, "list"}, notes.NewStore(), &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.String() == "" {
		t.Error("stderr is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{" {
		t.Errorf("invalid data file was overwritten: %q", data)
	}
}
