# Task, suite, and evaluator format

This document defines the versioned contracts consumed by Gauge. Schema version 1
is the only supported version in this build. All documents are validated strictly:
unknown fields are errors, and error messages name the file and field.

A task is **agent-neutral**. It may not contain adapter-specific flags, model
names, or harness assumptions. Adapter configuration lives in Gauge's TOML
configuration, never in a task.

## Task layout

```text
tasks/<category>/<task-id>/
├── task.yaml
├── prompt.md
└── evaluator/
    ├── evaluate.sh
    ├── testdata/
    │   └── hidden_test.go     # copied into a scratch candidate copy, never the workspace
    └── README.md              # maintainer notes, also never in the workspace
```

Gauge discovers `task.yaml` files beneath the configured task directory (default
`<cwd>/tasks`, up to three directory levels). Duplicate task IDs are rejected.

## `task.yaml`

```yaml
schema_version: 1
id: greeting-all                     # ^[a-z0-9][a-z0-9-]{0,63}$
name: Add batch greeting support
category: repository-engineering     # ^[a-z][a-z0-9-]{0,31}$
difficulty: easy                     # easy | medium | hard
description: Add GreetAll without changing existing Greet behavior.
tags: [go, library, backward-compatibility]

fixture:
  path: ../../../fixtures/greeting-go.git
  revision: f80f6ef179c10f09dd703aa5760b12e770df9688

prompt: prompt.md

execution:
  timeout: 5m
  network: disabled
  setup:
    - [go, build, ./...]

evaluation:
  path: evaluator
  command: [./evaluate.sh]
  timeout: 2m

provenance:
  origin: Authored for Gauge as a smoke task
  license: MIT
  notes: Maintainer-only context.
```

### Fields and rules

| Field | Rules |
|---|---|
| `schema_version` | Required, must be `1`. |
| `id` | Required, lowercase slug, at most 64 characters. Globally unique. |
| `name` | Required, at most 120 characters. |
| `category` | Required slug. Recommended values: `small-coding`, `repository-engineering`, `complex-engineering`, `operational`. |
| `difficulty` | Required: `easy`, `medium`, or `hard`. |
| `description` | Optional, at most 1000 characters. |
| `tags` | Optional, at most 20 unique tags. |
| `fixture.path` | Required, relative to the task directory. It may point outside the task directory (as in the example) but must resolve to an existing Git repository. Absolute paths are rejected so tasks stay portable. |
| `fixture.revision` | Required full 40-character lowercase commit SHA. Branches, tags, and short SHAs are rejected and the resolved commit is verified. |
| `prompt` | Required path to a non-empty file, relative to the task directory, at most 64 KiB. It may not escape the task directory, including through symlinks. |
| `execution.timeout` | Required positive duration, at most 6h. The agent's hard budget for one attempt. |
| `execution.network` | `disabled` (default) or `allowed`. Declarative in the local backend; the Docker backend is planned to enforce it. |
| `execution.setup` | Optional argv-style commands, at most 32, each with at most 64 non-empty arguments. |
| `evaluation.path` | Required directory inside the task directory. Must not be inside the agent workspace (it is structurally separate). |
| `evaluation.command` | Required argv array. A relative executable resolves inside `evaluation.path`; a bare name resolves via `PATH`. Absolute executable paths are rejected. |
| `evaluation.timeout` | Required positive duration, at most 1h. |
| `provenance` | Optional `origin`, `license`, and `notes` strings. |

### Prompt rules

`prompt.md` is the **only** task instruction the agent receives, together with the
working directory and execution context. Do not include evaluator scripts, hidden
tests, expected output, grading rules, or maintainer-only file paths. Describe the
desired behavior, not the exact implementation steps.

### Fixture conventions

Bundled fixtures are stored as bare Git repositories (`fixtures/<name>.git`) so the
pinned commit can be materialized without a shared worktree. Their readable source
lives in `fixtures/src/<name>` and is rebuilt with:

```bash
./scripts/fixture.sh greeting-go   # prints the new revision
```

Commit metadata is fixed by the script, so regenerating unchanged content yields
the same SHA. Update `fixture.revision` in the task when the fixture changes; the
task fingerprint and experiment identity change with it.

Every attempt clones the fixture with `--no-local`, checks out the exact revision,
removes the `origin` remote, and verifies the checkout is clean. Attempts never
share mutable Git state. Gauge disables system/global Git configuration for these
operations so local aliases, hooks, signing, or filters cannot change behavior.

## Suite format

Suites are single YAML files under the configured suite directory (default
`<cwd>/suites`). Files ending in `.yaml` or `.yml` are loaded; duplicate suite IDs
are rejected.

```yaml
schema_version: 1
id: smoke
name: Gauge smoke suite
description: A short suite for validating an agent configuration.
tasks:
  - id: greeting-all
```

| Field | Rules |
|---|---|
| `schema_version` | Required, must be `1`. |
| `id` | Required lowercase slug, unique. |
| `name` | Required, at most 120 characters. |
| `tasks` | Required, 1–500 entries. Every task must exist when the suite is resolved (before any agent runs). |
| `tasks[].reason` | Required when the same task ID appears more than once; otherwise rejected. Repetition is otherwise an experiment-level property. |

The suite fingerprint is a SHA-256 over the ordered suite definition. The
experiment also records each selected task's content fingerprint, so a task change
is visible even when the suite file did not change.

## Setup commands

Setup runs **before** the agent, sequentially, in the fresh workspace:

- argv form only — no shell is implied (`["go", "test", "./..."]` is not
  `go test ./...` through a shell);
- working directory is the workspace;
- minimal environment: `PATH`, `HOME` (`<attempt>/setup-home`), `TMPDIR`
  (`<attempt>/setup-tmp`), `LANG`, `LC_ALL`, `GAUGE_WORKSPACE`;
- each step gets `execution.timeout`; the first failure stops the sequence;
- stdout/stderr are captured to `setup-<n>/stdout.log` and `stderr.log`.

A setup failure means the task was not evaluated (`not_evaluated`), not that the
agent failed the task.

## Evaluator contract

The evaluator runs only after the agent process has ended or timed out. It is a
separate process with its own timeout and environment, and its assets are never
placed in the candidate workspace or adapter request.

### Invocation

Gauge resolves the first command element (relative to the assets directory or via
`PATH`) and appends three positional arguments:

```bash
./evaluate.sh <workspace> <assets> <scratch>
```

- **workspace** — the candidate workspace (the evaluator should treat it as
  read-only where practical);
- **assets** — the evaluator directory containing scripts and hidden tests;
- **scratch** — a writable directory owned by the evaluator. Gauge sets it as the
  process working directory and creates `tmp/` and `home/` beneath it. It is
  deleted after the attempt unless the retention policy is `keep_all`, so persist
  anything important through stdout/stderr or the result JSON.

Controlled environment:

| Variable | Meaning |
|---|---|
| `GAUGE_SCHEMA_VERSION` | `1` |
| `GAUGE_ATTEMPT_ID` | Current attempt ID |
| `GAUGE_TASK_ID` | Task ID |
| `GAUGE_WORKSPACE` | Same path as argument 1 |
| `GAUGE_EVALUATOR_ASSETS` | Same path as argument 2 |
| `GAUGE_SCRATCH_DIR` | Same path as argument 3 |
| `PATH`, `LANG`, `LC_ALL`, `GOOS`, `GOARCH` | Execution context |
| `HOME`, `TMPDIR` | Point inside the scratch directory |

The caller's `HOME`, credentials, and other environment variables are not
inherited. Evaluators that need a toolchain should find it via `PATH` and place
caches under the scratch directory (see the bundled evaluator for an example).

### Result JSON

The evaluator prints exactly one JSON object to stdout:

```json
{
  "schema_version": 1,
  "status": "passed",
  "score": 1.0,
  "summary": "All required checks passed.",
  "checks": [
    {
      "id": "build-passes",
      "status": "passed",
      "message": "go build ./... succeeded."
    }
  ]
}
```

| Field | Rules |
|---|---|
| `schema_version` | Required, must be `1`. |
| `status` | Required: `passed`, `failed`, or `error`. |
| `summary` | Required, at most 4000 characters. |
| `score` | Optional, 0–1. Not used for cross-task aggregation in v1. |
| `checks[].id` | Required unique identifier (`[A-Za-z0-9][A-Za-z0-9._-]{0,63}`). |
| `checks[].status` | Required: `passed`, `failed`, or `error`. |
| `checks[].message` | Optional, at most 2000 characters. |

Consistency rules:

- `passed` requires at least one check, and no check may be `failed` or `error`.
- `failed` with checks requires at least one failing or errored check.
- `error` may have no checks.

Checks are the source of truth for individual criteria. The primary task result is
pass/fail; a score is optional and is not aggregated across unrelated tasks.

### Failure semantics

| Situation | Recorded as |
|---|---|
| Valid JSON with `status: failed` | Task outcome `failed` (agent did not satisfy the task). |
| Valid JSON with `status: error` | Task outcome `error` (evaluator could not decide). |
| Invalid JSON, unknown fields, schema mismatch | Evaluator error (`error`). |
| Non-zero evaluator exit | Evaluator error (`error`), never a task failure. |
| Evaluator timeout or crash | Evaluator error (`error`). |
| No evaluator run (setup/workspace failure) | `not_evaluated`. |

Candidate-caused failures should exit zero with `status: failed` and a failing
check. Reserve non-zero exits for broken evaluator infrastructure. Include
diagnostics on stderr: Gauge stores them as artifacts.

### Isolation and determinism

- Hidden tests and expected outputs stay in the evaluator directory.
- Copy the candidate to the scratch directory if you need to add files; do not
  mutate the recorded candidate diff.
- No network access is required by default. A task that needs it must declare
  `network: allowed` and justify the exception in provenance/docs.
- Evaluators must be deterministic and independent of agent identity.

## Trusted content warning

Setup commands and evaluator scripts are executable code. Only run task suites from
sources you trust. Local execution is not a sandbox; see
[Running benchmarks](running-benchmarks.md#security-boundary).
