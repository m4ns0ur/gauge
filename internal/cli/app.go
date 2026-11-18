// Package cli implements Gauge's Cobra command surface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/adapters/fake"
	"github.com/m4ns0ur/gauge/internal/config"
	"github.com/m4ns0ur/gauge/internal/storage"
	"github.com/m4ns0ur/gauge/internal/suite"
	"github.com/m4ns0ur/gauge/internal/task"
	"github.com/m4ns0ur/gauge/internal/version"
)

// Application exit codes.
const (
	// ExitOK means everything succeeded.
	ExitOK = 0
	// ExitTaskFailed means at least one task was evaluated and failed.
	ExitTaskFailed = 1
	// ExitUsage means invalid flags, configuration, tasks, or suites.
	ExitUsage = 2
	// ExitInfrastructure means execution, evaluator, or storage failure.
	ExitInfrastructure = 3
	// ExitInterrupted means the run was cancelled.
	ExitInterrupted = 130
)

// ExitError carries a specific process exit code.
type ExitError struct {
	Code int
	Err  error
}

// Error implements error.
func (e *ExitError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying error.
func (e *ExitError) Unwrap() error { return e.Err }

// Exitf builds an ExitError with a formatted message.
func Exitf(code int, format string, args ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}

// ExitCode maps an error to a process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return ExitUsage
}

// App holds process-level dependencies and flag state.
type App struct {
	Out     io.Writer
	Err     io.Writer
	In      io.Reader
	Version version.Info
	// Now overrides the clock in tests.
	Now func() time.Time
	// Context is used by commands instead of context.Background for tests.
	Context func() context.Context

	configPath   string
	dataDirFlag  string
	tasksDirFlag string
	suiteDirFlag string
	verbose      bool
}

// New returns an app wired to the process streams.
func New() *App {
	return &App{
		Out:     os.Stdout,
		Err:     os.Stderr,
		In:      os.Stdin,
		Version: version.Get(),
		Now:     time.Now,
	}
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func (a *App) ctx() context.Context {
	if a.Context != nil {
		return a.Context()
	}
	return context.Background()
}

// Root builds the root command.
func (a *App) Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "gauge",
		Short: "Independent evaluation and benchmarking for AI agents",
		Long: `Gauge measures what agents can actually do.

It runs agent-neutral benchmark tasks against a selected agent harness using a
fresh pinned repository fixture per attempt, evaluates the result independently
of the agent's own claims, and records the evidence needed to compare
configurations.`,
		Version:       a.Version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.SetIn(a.In)

	pf := root.PersistentFlags()
	pf.StringVar(&a.configPath, "config", "", "config file (default: "+config.DefaultPath()+")")
	pf.StringVar(&a.dataDirFlag, "data-dir", "", "override the Gauge data directory")
	pf.StringVar(&a.tasksDirFlag, "tasks-dir", "", "override the task directory")
	pf.StringVar(&a.suiteDirFlag, "suites-dir", "", "override the suite directory")
	pf.BoolVar(&a.verbose, "verbose", false, "print detailed diagnostics")

	root.AddCommand(
		a.versionCommand(),
		a.doctorCommand(),
		a.agentsCommand(),
		a.tasksCommand(),
		a.suitesCommand(),
		a.runCommand(),
		a.runsCommand(),
		a.inspectCommand(),
		a.exportCommand(),
	)
	return root
}

// loadConfig loads configuration and applies flag-level path overrides.
func (a *App) loadConfig() (*config.Config, error) {
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return nil, Exitf(ExitUsage, "%v", err)
	}
	overrides := []struct {
		name  string
		value string
		dst   *string
	}{
		{"data-dir", a.dataDirFlag, &cfg.DataDir},
		{"tasks-dir", a.tasksDirFlag, &cfg.TasksDir},
		{"suites-dir", a.suiteDirFlag, &cfg.SuitesDir},
	}
	for _, o := range overrides {
		if o.value == "" {
			continue
		}
		abs, err := filepath.Abs(o.value)
		if err != nil {
			return nil, Exitf(ExitUsage, "resolve --%s: %v", o.name, err)
		}
		*o.dst = abs
	}
	return cfg, nil
}

func (a *App) registry() *adapter.Registry {
	reg := adapter.NewRegistry()
	reg.Register(fake.New())
	return reg
}

func (a *App) store(cfg *config.Config) (*storage.Store, error) {
	store, err := storage.New(cfg.DataDir)
	if err != nil {
		return nil, Exitf(ExitInfrastructure, "%v", err)
	}
	return store, nil
}

func (a *App) tasks(ctx context.Context, cfg *config.Config) ([]*task.Task, error) {
	dir, err := cfg.ResolveTasksDir()
	if err != nil {
		return nil, Exitf(ExitUsage, "%v", err)
	}
	tasks, err := task.LoadAll(ctx, dir)
	if err != nil {
		return nil, Exitf(ExitUsage, "%v", err)
	}
	return tasks, nil
}

func (a *App) suites(cfg *config.Config) ([]*suite.Suite, error) {
	dir, err := cfg.ResolveSuitesDir()
	if err != nil {
		return nil, Exitf(ExitUsage, "%v", err)
	}
	suites, err := suite.LoadAll(dir)
	if err != nil {
		return nil, Exitf(ExitUsage, "%v", err)
	}
	return suites, nil
}

// adapterConfig looks up one adapter and its validated configuration.
func (a *App) adapterConfig(cfg *config.Config, id string) (adapter.Adapter, json.RawMessage, error) {
	if id == "" {
		return nil, nil, Exitf(ExitUsage, "missing --agent")
	}
	if !cfg.AdapterEnabled(id) {
		return nil, nil, Exitf(ExitUsage, "adapter %q is disabled in configuration", id)
	}
	reg := a.registry()
	impl, ok := reg.Get(id)
	if !ok {
		available := make([]string, 0, 4)
		for _, info := range reg.List() {
			available = append(available, info.ID)
		}
		return nil, nil, Exitf(ExitUsage, "agent %q is not available in this build (available: %v)", id, available)
	}
	options, err := cfg.AdapterOptions(id)
	if err != nil {
		return nil, nil, Exitf(ExitUsage, "%v", err)
	}
	if err := impl.Validate(a.ctx(), options); err != nil {
		return nil, nil, Exitf(ExitUsage, "agent %q configuration is invalid: %v", id, err)
	}
	return impl, options, nil
}

// notFoundError renders a missing record without leaking a raw syscall error and
// points at the most common cause: a different --config/--data-dir store.
func notFoundError(err error, store *storage.Store, kind, id string) error {
	if errors.Is(err, storage.ErrExperimentNotFound) || errors.Is(err, storage.ErrAttemptNotFound) {
		return Exitf(ExitUsage,
			"%s %q not found in %s\nhint: runs recorded with a different --config or --data-dir live in a separate store",
			kind, id, store.Root())
	}
	return Exitf(ExitUsage, "%v", err)
}

func jsonOut(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return Exitf(ExitInfrastructure, "encode JSON: %v", err)
	}
	return nil
}
