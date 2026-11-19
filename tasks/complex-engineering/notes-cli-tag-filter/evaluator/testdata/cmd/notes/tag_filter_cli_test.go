package main

import (
	"bytes"
	"strings"
	"testing"

	"example.com/notes/notes"
)

func TestListByTag(t *testing.T) {
	store := notes.NewStore()
	if _, err := store.Add("buy milk", []string{"errand"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("read book", []string{"study"}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "--tag", "errand"}, store, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "buy milk") {
		t.Errorf("stdout = %q, want the matching note", stdout.String())
	}
	if strings.Contains(stdout.String(), "read book") {
		t.Errorf("stdout = %q, want only the tagged note", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}

	var missingOut, missingErr bytes.Buffer
	code = run([]string{"list", "--tag"}, notes.NewStore(), &missingOut, &missingErr)
	if code != 2 {
		t.Errorf("missing tag value: exit code = %d, want 2", code)
	}
	if !strings.Contains(missingErr.String(), "usage") {
		t.Errorf("missing tag value: stderr = %q, want usage", missingErr.String())
	}
}
