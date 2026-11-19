package main

import (
	"bytes"
	"strings"
	"testing"

	"example.com/notes/notes"
)

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"bogus"}, notes.NewStore(), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("stderr = %q, want usage text", stderr.String())
	}
}

func TestHelpCommand(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{arg}, notes.NewStore(), &stdout, &stderr)
		if code != 0 {
			t.Errorf("%s: exit code = %d, want 0", arg, code)
		}
		if !strings.Contains(stdout.String(), "usage") {
			t.Errorf("%s: stdout = %q, want usage text", arg, stdout.String())
		}
		if stderr.String() != "" {
			t.Errorf("%s: stderr = %q, want empty", arg, stderr.String())
		}
	}
}
