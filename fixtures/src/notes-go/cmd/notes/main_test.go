package main

import (
	"bytes"
	"testing"

	"example.com/notes/notes"
)

func TestAddCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"add", "buy", "milk"}, notes.NewStore(), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if got := stdout.String(); got != "added n-1\n" {
		t.Fatalf("stdout = %q", got)
	}
}
