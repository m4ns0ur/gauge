// Package config loads Gauge's local TOML configuration.
//
// Configuration is intentionally small: data and content locations, workspace
// retention, experiment defaults, and per-adapter options. Adapter options are
// opaque to Gauge and validated by the adapter itself. Paths in the config file
// resolve relative to that file; environment variables override the data and
// content directories for scripted use.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SchemaVersion is the supported configuration schema version.
const SchemaVersion = 1

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Config is Gauge's local configuration.
type Config struct {
	SchemaVersion int                `toml:"schema_version"`
	DataDir       string             `toml:"data_dir"`
	TasksDir      string             `toml:"tasks_dir"`
	SuitesDir     string             `toml:"suites_dir"`
	Retention     Retention          `toml:"retention"`
	Defaults      Defaults           `toml:"defaults"`
	Adapters      map[string]Adapter `toml:"adapters"`

	// File is the absolute path of the loaded file, empty for defaults.
	File string `toml:"-"`
}

// Retention configures cleanup behavior.
type Retention struct {
	// Workspace is one of keep_failed, keep_all, delete_all.
	Workspace string `toml:"workspace"`
}

// Defaults are experiment defaults.
type Defaults struct {
	Repetitions int `toml:"repetitions"`
}

// Adapter holds one adapter's configuration.
type Adapter struct {
	Enabled *bool          `toml:"enabled"`
	Options map[string]any `toml:"options"`
}

// IsEnabled reports whether the adapter may be used (default true).
func (a Adapter) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
}

// DefaultPath returns the configuration file Gauge uses when none is given.
// Unix uses $XDG_CONFIG_HOME/gauge/config.toml (falling back to
// ~/.config/gauge/config.toml); Windows uses %AppData%\gauge\config.toml.
func DefaultPath() string {
	if p := os.Getenv("GAUGE_CONFIG"); p != "" {
		return expandHome(p)
	}
	if runtime.GOOS == "windows" {
		if dir, err := os.UserConfigDir(); err == nil {
			return filepath.Join(dir, "gauge", "config.toml")
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gauge", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "gauge", "config.toml")
	}
	return filepath.Join(home, ".config", "gauge", "config.toml")
}

// Load reads configuration. An empty path uses DefaultPath; a missing default
// file yields built-in defaults rather than an error. Relative paths resolve
// against the config file's directory (or the working directory for defaults).
func Load(path string) (*Config, error) {
	explicit := path != ""
	if !explicit {
		path = DefaultPath()
	}
	path = expandHome(path)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolve %q: %w", path, err)
	}

	cfg := &Config{}
	baseDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("config: determine working directory: %w", err)
	}
	if data, readErr := os.ReadFile(absPath); readErr == nil {
		if err := checkUnknownKeys(absPath, data); err != nil {
			return nil, err
		}
		dec := toml.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", absPath, err)
		}
		cfg.File = absPath
		baseDir = filepath.Dir(absPath)
	} else if !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("config: read %s: %w", absPath, readErr)
	} else if explicit {
		return nil, fmt.Errorf("config: file %s does not exist", absPath)
	}

	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = SchemaVersion
	}
	if cfg.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("config %s: unsupported schema_version %d (this Gauge supports %d)", cfg.File, cfg.SchemaVersion, SchemaVersion)
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir()
	}
	if cfg.Retention.Workspace == "" {
		cfg.Retention.Workspace = "keep_failed"
	}
	if cfg.Defaults.Repetitions == 0 {
		cfg.Defaults.Repetitions = 1
	}
	for name, value := range map[string]*string{
		"data_dir":   &cfg.DataDir,
		"tasks_dir":  &cfg.TasksDir,
		"suites_dir": &cfg.SuitesDir,
	} {
		if *value == "" {
			continue
		}
		*value = expandHome(*value)
		if !filepath.IsAbs(*value) {
			*value = filepath.Join(baseDir, *value)
		}
		abs, err := filepath.Abs(*value)
		if err != nil {
			return nil, fmt.Errorf("config %s: resolve %s: %w", cfg.File, name, err)
		}
		*value = abs
	}
	if v := os.Getenv("GAUGE_DATA_DIR"); v != "" {
		cfg.DataDir = expandHome(v)
	}
	if v := os.Getenv("GAUGE_TASKS_DIR"); v != "" {
		cfg.TasksDir = expandHome(v)
	}
	if v := os.Getenv("GAUGE_SUITES_DIR"); v != "" {
		cfg.SuitesDir = expandHome(v)
	}
	if cfg.Adapters == nil {
		cfg.Adapters = map[string]Adapter{}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks configuration invariants.
func (c *Config) Validate() error {
	switch c.Retention.Workspace {
	case "keep_failed", "keep_all", "delete_all":
	default:
		return fmt.Errorf("config %s: retention.workspace %q must be keep_failed, keep_all, or delete_all", c.File, c.Retention.Workspace)
	}
	if c.Defaults.Repetitions < 1 || c.Defaults.Repetitions > 100 {
		return fmt.Errorf("config %s: defaults.repetitions %d must be between 1 and 100", c.File, c.Defaults.Repetitions)
	}
	for name, adapter := range c.Adapters {
		if !idRE.MatchString(name) {
			return fmt.Errorf("config %s: adapter name %q must match %s", c.File, name, idRE)
		}
		if !adapter.IsEnabled() && len(adapter.Options) > 0 {
			return fmt.Errorf("config %s: adapter %q is disabled but has options", c.File, name)
		}
		if _, err := json.Marshal(adapter.Options); err != nil {
			return fmt.Errorf("config %s: adapter %q options are not JSON-serializable: %w", c.File, name, err)
		}
	}
	return nil
}

// AdapterOptions returns configuration for one adapter as JSON.
func (c *Config) AdapterOptions(id string) (json.RawMessage, error) {
	adapter, ok := c.Adapters[id]
	if !ok || len(adapter.Options) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !adapter.IsEnabled() {
		return nil, fmt.Errorf("config: adapter %q is disabled", id)
	}
	data, err := json.Marshal(adapter.Options)
	if err != nil {
		return nil, fmt.Errorf("config: adapter %q options: %w", id, err)
	}
	return data, nil
}

// AdapterEnabled reports whether the adapter is enabled (default true).
func (c *Config) AdapterEnabled(id string) bool {
	adapter, ok := c.Adapters[id]
	if !ok {
		return true
	}
	return adapter.IsEnabled()
}

// ResolveTasksDir returns the configured task directory, defaulting to
// <cwd>/tasks when unset.
func (c *Config) ResolveTasksDir() (string, error) {
	return resolveDefaultDir(c.TasksDir, "tasks")
}

// ResolveSuitesDir returns the configured suite directory, defaulting to
// <cwd>/suites when unset.
func (c *Config) ResolveSuitesDir() (string, error) {
	return resolveDefaultDir(c.SuitesDir, "suites")
}

func resolveDefaultDir(configured, name string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("config: determine working directory: %w", err)
	}
	return filepath.Join(cwd, name), nil
}

func defaultDataDir() string {
	if v := os.Getenv("GAUGE_DATA_DIR"); v != "" {
		return expandHome(v)
	}
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "gauge")
		}
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "gauge")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "gauge-data")
	}
	return filepath.Join(home, ".local", "share", "gauge")
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// checkUnknownKeys reports unknown configuration fields with their names. The
// TOML library's strict error is accurate but does not name the field, which
// makes configuration mistakes hard to act on.
func checkUnknownKeys(file string, data []byte) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("config %s: %w", file, err)
	}
	known := map[string]struct{}{
		"schema_version": {}, "data_dir": {}, "tasks_dir": {}, "suites_dir": {},
		"retention": {}, "defaults": {}, "adapters": {},
	}
	for key := range raw {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("config %s: unknown field %q", file, key)
		}
	}
	if retention, ok := raw["retention"].(map[string]any); ok {
		for key := range retention {
			if key != "workspace" {
				return fmt.Errorf("config %s: unknown field %q in [retention]", file, key)
			}
		}
	}
	if defaults, ok := raw["defaults"].(map[string]any); ok {
		for key := range defaults {
			if key != "repetitions" {
				return fmt.Errorf("config %s: unknown field %q in [defaults]", file, key)
			}
		}
	}
	if adapters, ok := raw["adapters"].(map[string]any); ok {
		for name, value := range adapters {
			table, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("config %s: adapter %q must be a table", file, name)
			}
			for key := range table {
				if key != "enabled" && key != "options" {
					return fmt.Errorf("config %s: unknown field %q in [adapters.%s]", file, key, name)
				}
			}
		}
	}
	return nil
}
