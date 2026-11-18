package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/gauge/internal/adapter"
	"github.com/m4ns0ur/gauge/internal/config"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail
	Detail string `json:"detail"`
}

func (a *App) doctorCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check configuration, content, storage, and agent prerequisites",
		Long: `Check that Gauge can run benchmarks on this machine.

Doctor validates configuration, data directory access, task and suite content,
Git availability, and adapter prerequisites. It never makes an inference call.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := a.runDoctor()
			failed := 0
			for _, check := range checks {
				if check.Status == "fail" {
					failed++
				}
			}
			if asJSON {
				if err := jsonOut(cmd.OutOrStdout(), checks); err != nil {
					return err
				}
			} else {
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
				for _, check := range checks {
					fmt.Fprintf(tw, "%-10s %-5s %s\n", check.Name, strings.ToUpper(check.Status), check.Detail)
				}
				if err := tw.Flush(); err != nil {
					return Exitf(ExitInfrastructure, "write output: %v", err)
				}
			}
			if failed > 0 {
				return Exitf(ExitInfrastructure, "doctor found %d failing check(s)", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func (a *App) runDoctor() []doctorCheck {
	var checks []doctorCheck
	add := func(name, status, detail string) {
		checks = append(checks, doctorCheck{Name: name, Status: status, Detail: detail})
	}

	cfg, err := a.loadConfig()
	if err != nil {
		add("config", "fail", err.Error())
		return checks
	}
	if cfg.File != "" {
		add("config", "ok", cfg.File)
	} else {
		add("config", "ok", "built-in defaults (no config file at "+config.DefaultPath()+")")
	}

	store, err := a.store(cfg)
	if err != nil {
		add("data", "fail", err.Error())
	} else if err := probeWritable(store.Root()); err != nil {
		add("data", "fail", fmt.Sprintf("%s is not writable: %v", store.Root(), err))
	} else {
		add("data", "ok", store.Root())
	}

	ctx := a.ctx()
	tasks, taskErr := a.tasks(ctx, cfg)
	dir, _ := cfg.ResolveTasksDir()
	if taskErr != nil {
		add("tasks", "fail", taskErr.Error())
	} else {
		add("tasks", "ok", fmt.Sprintf("%d task(s) in %s", len(tasks), dir))
	}

	suites, suiteErr := a.suites(cfg)
	if suiteErr != nil {
		add("suites", "fail", suiteErr.Error())
	} else if taskErr != nil {
		add("suites", "warn", "not checked because tasks failed validation")
	} else {
		bad := 0
		for _, s := range suites {
			if _, err := s.Resolve(tasks); err != nil {
				bad++
				add("suites", "fail", err.Error())
			}
		}
		if bad == 0 {
			sdir, _ := cfg.ResolveSuitesDir()
			add("suites", "ok", fmt.Sprintf("%d suite(s) in %s, all tasks resolve", len(suites), sdir))
		}
	}

	if path, err := exec.LookPath("git"); err != nil {
		add("git", "fail", "git not found in PATH; fixtures cannot be materialized")
	} else if out, err := exec.Command(path, "version").Output(); err != nil {
		add("git", "fail", fmt.Sprintf("%s: %v", path, err))
	} else {
		add("git", "ok", strings.TrimSpace(string(out)))
	}

	reg := a.registry()
	for _, info := range reg.List() {
		impl, ok := reg.Get(info.ID)
		if !ok {
			continue
		}
		options, err := cfg.AdapterOptions(info.ID)
		if err != nil {
			add("agent:"+info.ID, "fail", err.Error())
			continue
		}
		if err := impl.Validate(ctx, options); err != nil {
			var unavailable *adapter.UnavailableError
			if errors.As(err, &unavailable) {
				// A correctly configured adapter whose agent is not installed
				// must not fail the whole environment check.
				add("agent:"+info.ID, "warn", err.Error())
			} else {
				add("agent:"+info.ID, "fail", err.Error())
			}
			continue
		}
		add("agent:"+info.ID, "ok", "configured")
	}
	add("agent:pi", "warn", "not implemented in this build; see docs/adapters.md")
	add("sandbox", "warn", "local execution is not a security sandbox; tasks and evaluators are trusted code")
	return checks
}

func probeWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
