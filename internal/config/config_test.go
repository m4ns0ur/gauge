package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m4ns0ur/gauge/internal/config"
	"github.com/m4ns0ur/gauge/internal/testutil"
)

func TestDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv("GAUGE_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.DataDir == "" {
		t.Error("DataDir is empty")
	}
	if cfg.Retention.Workspace != "keep_failed" {
		t.Errorf("retention = %q", cfg.Retention.Workspace)
	}
	if cfg.Defaults.Repetitions != 1 {
		t.Errorf("repetitions = %d", cfg.Defaults.Repetitions)
	}
	if cfg.File != "" {
		t.Errorf("File = %q for built-in defaults", cfg.File)
	}
}

func TestExplicitMissingFileFails(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil {
		t.Fatal("expected error for explicit missing config")
	}
}

func TestRelativePathsResolveAgainstConfigFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "config.toml")
	testutil.WriteFile(t, file, `data_dir = "./data"
tasks_dir = "./content/tasks"
suites_dir = "../shared/suites"

[retention]
workspace = "keep_all"

[defaults]
repetitions = 3

[adapters.fake.options]
mode = "noop"
`, 0o644)
	cfg, err := config.Load(file)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != filepath.Join(root, "data") {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.TasksDir != filepath.Join(root, "content", "tasks") {
		t.Errorf("TasksDir = %q", cfg.TasksDir)
	}
	wantSuites := filepath.Join(filepath.Dir(root), "shared", "suites")
	if cfg.SuitesDir != wantSuites {
		t.Errorf("SuitesDir = %q, want %q", cfg.SuitesDir, wantSuites)
	}
	if cfg.Retention.Workspace != "keep_all" || cfg.Defaults.Repetitions != 3 {
		t.Errorf("retention=%q repetitions=%d", cfg.Retention.Workspace, cfg.Defaults.Repetitions)
	}

	options, err := cfg.AdapterOptions("fake")
	if err != nil {
		t.Fatalf("adapter options: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(options, &decoded); err != nil {
		t.Fatalf("options are not JSON: %v", err)
	}
	if decoded["mode"] != "noop" {
		t.Errorf("options = %s", options)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "config.toml")
	testutil.WriteFile(t, file, "data_dir = \"./data\"\n", 0o644)
	dataDir := filepath.Join(root, "override-data")
	t.Setenv("GAUGE_DATA_DIR", dataDir)
	t.Setenv("GAUGE_TASKS_DIR", filepath.Join(root, "override-tasks"))
	cfg, err := config.Load(file)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != dataDir {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.TasksDir != filepath.Join(root, "override-tasks") {
		t.Errorf("TasksDir = %q", cfg.TasksDir)
	}
}

func TestUnknownFieldFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.toml")
	testutil.WriteFile(t, file, "surprise = true\n", 0o644)
	if _, err := config.Load(file); err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"bad retention": `[retention]
workspace = "sometimes"
`,
		"bad repetitions": `[defaults]
repetitions = 1000
`,
		"disabled adapter with options": `[adapters.fake]
enabled = false

[adapters.fake.options]
mode = "noop"
`,
		"bad schema": `schema_version = 9
`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.toml")
			testutil.WriteFile(t, file, content, 0o644)
			if _, err := config.Load(file); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestHomeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	file := filepath.Join(t.TempDir(), "config.toml")
	testutil.WriteFile(t, file, "data_dir = \"~/gauge-data\"\n", 0o644)
	cfg, err := config.Load(file)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != filepath.Join(home, "gauge-data") {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
}

func TestAdapterOptionsMissingIsEmptyObject(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.toml")
	testutil.WriteFile(t, file, "", 0o644)
	cfg, err := config.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	options, err := cfg.AdapterOptions("fake")
	if err != nil {
		t.Fatal(err)
	}
	if string(options) != "{}" {
		t.Errorf("options = %s", options)
	}
	if !cfg.AdapterEnabled("fake") {
		t.Error("unknown adapter should default to enabled")
	}
}
