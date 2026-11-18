# Running benchmarks

## Prerequisites

- Go 1.24 or newer
- Git (used to materialize fixture workspaces)
- The toolchain each task's evaluator expects (the bundled task needs `go`)

No model or API key is required for the fake-adapter smoke run.

## Build

```bash
make build      # builds ./bin/gauge and installs `gauge` into GOBIN or $(go env GOPATH)/bin
./bin/gauge version
gauge version   # if $(go env GOPATH)/bin is on PATH
```

`make install` performs only the `go install` step. If `$(go env GOPATH)/bin` is
not on your `PATH`, add it or call `./bin/gauge`.

If the Go module cache is read-only in your environment, `./scripts/go.sh` keeps
`GOPATH`/`GOCACHE` inside the checkout:

```bash
make GO=./scripts/go.sh build
```

## Configuration

Gauge reads TOML. By default it looks for:

| Platform | Default config path |
|---|---|
| Linux/macOS | `${XDG_CONFIG_HOME:-~/.config}/gauge/config.toml` |
| Windows | `%AppData%\gauge\config.toml` |

Data defaults to `~/.local/share/gauge` (or `$XDG_DATA_HOME/gauge`, or
`%LOCALAPPDATA%\gauge` on Windows). A missing default config file is fine; built-in
defaults apply.

```toml
schema_version = 1

data_dir = "~/.local/share/gauge"
tasks_dir = "./tasks"          # relative to this config file
suites_dir = "./suites"

[retention]
workspace = "keep_failed"      # keep_failed | keep_all | delete_all

[defaults]
repetitions = 1

[adapters.fake]
enabled = true

[adapters.fake.options]
mode = "noop"                  # adapter-specific; validated by the adapter
```

Environment overrides for scripted use: `GAUGE_CONFIG`, `GAUGE_DATA_DIR`,
`GAUGE_TASKS_DIR`, `GAUGE_SUITES_DIR`. CLI flags `--config`, `--data-dir`,
`--tasks-dir`, and `--suites-dir` take precedence over the file.

Unknown configuration fields are errors. Invalid retention values, repetition
counts outside 1–100, disabled adapters with options, and non-JSON-serializable
options are rejected before any run.

## Commands

```bash
gauge version [--json]
gauge doctor [--json]
gauge agents list [--json]

gauge tasks list [--category <category>] [--tag <tag>] [--json]
gauge tasks show <task-id> [--json]
gauge suites list [--json]
gauge suites show <suite-id> [--json]

gauge run --agent <id> --suite <suite-id> [--repetitions N] [--model M] [--notes TEXT]
gauge run --agent <id> --task <task-id> [--task <task-id> ...] [--repetitions N]

gauge runs list [--json]
gauge runs show <experiment-id> [--json]
gauge inspect <attempt-id> [--json]
gauge export <experiment-id> [--format json|markdown] [--output FILE]
```

### Run the smoke benchmark

```bash
./bin/gauge run --config examples/gauge.toml --suite smoke --agent fake
```

The example config's fake adapter writes a correct `GreetAll` implementation, so
the run passes without a model:

```text
Experiment: smoke-fake-20261118T192729Z-5a6452 (completed)
Agent:      fake fake-1.0.0   Model: fake/deterministic
Suite:      smoke (sha256:7452…)
Attempts:   1   Passed: 1/1 (100.0%)   Evaluator errors: 0   Not evaluated: 0
...
TASK          CATEGORY                ATTEMPTS  PASS  FAIL  ERROR  NOT EVALUATED  PASS RATE
greeting-all  repository-engineering  1         1     0     0      0              100.0%
```

Progress lines go to stderr; the report goes to stdout. The command prints the
experiment ID for follow-up commands.

### Select tasks and repetitions

```bash
# One task, three fresh attempts
gauge run --agent fake --task greeting-all --repetitions 3

# Everything in a suite
gauge run --agent fake --suite smoke
```

`--suite` and `--task` are mutually exclusive. Duplicate `--task` entries are
rejected. Repetition count comes from `--repetitions` or
`defaults.repetitions`.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Every attempt was evaluated and passed. |
| `1` | At least one task was evaluated and failed. |
| `2` | Usage, configuration, task, or suite error (nothing ran or selection was invalid). |
| `3` | Execution/evaluator infrastructure error, or no attempt produced a task result. |
| `130` | Run interrupted (SIGINT/SIGTERM). Partial results are preserved. |

`--json` commands are intended for automation; their stdout stays machine-readable
while warnings go to stderr.

## Retention and artifacts

| Policy | Behavior |
|---|---|
| `keep_failed` (default) | Delete `workspace/` after a passing evaluation; preserve it for failed, errored, and unevaluated attempts. |
| `keep_all` | Always preserve the workspace. |
| `delete_all` | Always delete the workspace after evaluation. |

Logs, events, diffs, evaluation JSON, and metadata are **never** deleted with the
workspace. Retention only removes the `workspace/` directory.

Tool caches, isolated agent home directories, and the evaluator scratch directory
(`evaluation-scratch/`, `setup-home/`, `setup-tmp/`, `agent-home/`, `agent-tmp/`)
are removed after **every** attempt unless the policy is `keep_all`. They are not
evidence — the evaluator's verdict, logs, checks, and artifacts are captured
separately — and retaining Go build caches alone can consume hundreds of megabytes
per attempt. Use `keep_all` when debugging evaluator scratch contents.

Layout:

```text
<data_dir>/runs/<experiment-id>/
├── experiment.json
└── <attempt-id>/
    ├── metadata.json
    ├── events.jsonl
    ├── stdout.log / stderr.log
    ├── diff.patch
    ├── git-status.txt
    ├── evaluation.json
    ├── evaluator-stdout.log / evaluator-stderr.log
    ├── setup-<n>/
    ├── workspace/            (per retention policy)
    └── evaluation-scratch/   (keep_all only)
```

## Inspecting results

Results are stored in the configured `data_dir`. Commands that read results must
use the same `--config`/`--data-dir` as the run; a different store is a different
directory. `gauge run` prints a copy-pasteable
`gauge --data-dir … runs show …` line for this reason.

```bash
gauge --config examples/gauge.toml runs list
gauge --config examples/gauge.toml runs show <experiment-id>
gauge --config examples/gauge.toml inspect <attempt-id>
```

`inspect` shows the execution outcome separately from the evaluator verdict, the
diff file list, every evaluator check, artifact paths, and whether the workspace
was retained. `--json` variants expose the same records for tooling.

Exports render the same document as JSON or Markdown:

```bash
gauge export <experiment-id> --format markdown --output report.md
gauge export <experiment-id> --format json > report.json
```

## Doctor

`gauge doctor` checks configuration, data-directory writability, task/suite
validity and resolution, Git availability, adapter configuration, and known
adapter gaps. It never makes an inference call. Exit code `3` means at least one
check failed; warnings (including the local-sandbox warning) do not fail the
command.

## Security boundary

> Local execution is **not** a sandbox.

- An agent process can read files outside its workspace and use the host network.
- Timeouts and process-group termination are reliability features, not
  containment.
- Evaluator assets are kept out of the agent workspace and the adapter request, but
  a hostile local agent could still inspect the host.
- Task setup commands and evaluator scripts are trusted executable code. Only run
  suites from sources you trust.
- `execution.network` is declarative in the local backend; it does not enforce
  network isolation. The Docker backend is planned to enforce mounts and network
  policy.
- Gauge redacts secret-looking adapter configuration before persistence, but do not
  put secrets anywhere they are not required.

## Troubleshooting

| Symptom | Check |
|---|---|
| `git executable not found in PATH` | Install Git; `gauge doctor`. |
| Fixture revision rejected | Task revisions must be full 40-character commit SHAs. Regenerate with `scripts/fixture.sh` after editing a fixture. |
| Evaluator error, not a task failure | Read `evaluator-stdout.log`/`evaluator-stderr.log`; confirm the command exits 0 and prints one valid JSON object. |
| `not_evaluated` | Setup or workspace preparation failed; inspect the setup logs and `metadata.json`. |
| `runs show` reports "experiment not found" | You are reading a different store. Use the `--data-dir` value printed by the run, or pass the same `--config`. |
| Workspace missing after a failure | Check `retention.workspace`; the default keeps failed workspaces. |
| Runs are slower than expected | Duration includes workspace clone, setup, agent, and evaluation. The bundled task compiles the fixture in setup and in the evaluator. |
