package main

import (
	"bytes"
	"strings"
	"testing"

	"example.com/notes/notes"
)

func TestSearchCommand(t *testing.T) {
	store := notes.NewStore()
	if _, err := store.Add("Buy Milk", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("read book", nil); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"search", "milk"}, store, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Buy Milk") {
		t.Errorf("stdout = %q, want the matching note", stdout.String())
	}
	if strings.Contains(stdout.String(), "read book") {
		t.Errorf("stdout = %q, want only the matching note", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}

	var missingOut, missingErr bytes.Buffer
	code = run([]string{"search"}, notes.NewStore(), &missingOut, &missingErr)
	if code != 2 {
		t.Errorf("missing term: exit code = %d, want 2", code)
	}
	if !strings.Contains(missingErr.String(), "usage") {
		t.Errorf("missing term: stderr = %q, want usage", missingErr.String())
	}
}
