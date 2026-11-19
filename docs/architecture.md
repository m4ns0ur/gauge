# Architecture

Gauge is a single-binary, local-first benchmark runner. This document describes
the components that exist today, the lifecycle of one attempt, and the boundaries
between agent execution, evaluation, and persistence.

## Goals and non-goals

Gauge owns:

- task and suite discovery,
- workspace preparation from a pinned fixture,
- timeouts and cleanup,
- adapter invocation and normalized event capture,
- independent evaluation,
- persistence and reporting.

Gauge does **not** own correctness claims made by agents, model-provider
abstractions, a hosted service, distributed execution, or a plugin marketplace.
Adapters are built in and registered at startup.

## Components

```text
                       gauge CLI (Cobra)
                              |
             +----------------+-----------------+
             |                |                 |
        task/suite       experiment          storage
         loaders        coordinator        (SQLite + files)
             |                |
             |          runner (one attempt)
             |                |
             |      +---------+----------+
             |      |         |          |
             |  workspace  adapter   evaluator
             |  (fresh    (fake,    (after agent,
             |   clone)    Iris, Pi  outside process)
             |             built in)
             +----------------+-----------------+
                              |
                 artifacts + metrics + reports
```

| Package | Responsibility |
|---|---|
| `internal/task` | Versioned task schema, strict validation, path safety, content fingerprints. |
| `internal/suite` | Versioned suite schema; missing/duplicate task detection. |
| `internal/workspace` | Clone fixture at a pinned SHA, run setup, capture diff, remove workspace. |
| `internal/adapter` + `internal/adapters/*` | Translate one harness's invocation into a normalized run and event stream. |
| `internal/proc` | Put subprocesses in their own process group and kill the tree on timeout (Unix). |
| `internal/evaluator` | Invoke the task evaluator in a controlled environment and validate its JSON result. |
| `internal/runner` | Own the per-attempt sequence and enforce the diff-before-evaluation invariant. |
| `internal/experiment` | Run tasks × repetitions sequentially, persist progress, stop cleanly on cancellation. |
| `internal/storage` | SQLite experiment/attempt metadata, normalized checks/artifacts/metrics, and artifact file paths and hashes. |
| `internal/metrics` | Aggregate outcomes with explicit denominators and availability rules. |
| `internal/report` | Render terminal, Markdown, and JSON views of the same document. |
| `internal/events` | Normalized event vocabulary and a concurrency-safe JSONL recorder. |
| `internal/cli` | Command surface, presentation, exit codes. |

The packages are deliberately concrete and few. There is no dependency-injection
framework, workflow engine, or dynamic plugin loader.

## Attempt lifecycle

Every attempt is a fresh, isolated run of one task.

```text
1. attempt directory created            runs/<experiment>/<attempt>/
2. fixture cloned at the pinned SHA     attempt/workspace/   (no remote)
3. setup commands run                   minimal environment, per-step timeout
4. adapter invoked                      agent gets prompt + workspace only
5. agent process ends or times out      completion recorded separately
6. git diff/status captured             before evaluation, via a temporary index
7. evaluator runs outside agent         workspace + assets + scratch, own timeout
8. artifacts hashed and persisted       events, logs, diff, evaluation JSON
9. retention applied                    workspace deleted only after a pass by default
```

Invariants enforced by the runner:

- The evaluator never runs inside the agent process and never receives agent
  output as evidence.
- The diff is captured **before** the evaluator runs; evaluator mutations cannot
  rewrite the recorded candidate change set.
- A setup or workspace failure produces `not_evaluated`, not a task failure.
- A timed-out agent is still evaluated; partial work can legitimately pass.
- Telemetry write problems are recorded warnings, not fabricated results.

### Outcome model

Two orthogonal facts are recorded for every attempt:

1. **Execution** — how the agent process ended: `completed`, `failed`,
   `timed_out`, or `cancelled`, plus exit code and adapter errors.
2. **Task outcome** — what the independent evaluator concluded: `passed`,
   `failed`, `error` (evaluator infrastructure could not decide), or
   `not_evaluated` (no evaluator verdict exists).

This separation is why a timed-out agent can still pass a task (it finished enough
work), and why an evaluator crash never counts as a failed task. Reports and
metrics preserve both. See [metrics.md](metrics.md).

## Contracts

- **Task** — immutable benchmark content: prompt, pinned fixture, execution
  constraints, evaluator. See [task-format.md](task-format.md).
- **Adapter** — one small interface with `ID`, `Validate`, and `Run`; returns a
  process-level result and normalized events, never a pass/fail score. Validation
  distinguishes invalid configuration from a missing agent binary
  (`adapter.UnavailableError`). See [adapters.md](adapters.md).
- **Evaluator** — a subprocess invoked with workspace, assets, and scratch paths;
  returns versioned JSON with individual checks. A non-zero exit, timeout, or
  invalid JSON is an evaluator error.
- **Storage** — a narrow store backed by SQLite for structured metadata and by
  files for large artifacts. Versioned migrations and a one-time import of the
  previous JSON store keep existing results readable. See
  [ADR 0002](adr/0002-sqlite-persistence.md).

## Persistence layout

```text
<data_dir>/
├── gauge.db                          (SQLite: experiments, attempts, checks,
│                                     metrics, artifact hashes)
└── runs/
    └── <experiment-id>/
        └── <attempt-id>/
            ├── events.jsonl
            ├── stdout.log              (when the adapter produces it)
            ├── stderr.log              (when the adapter produces it)
            ├── diff.patch              (when the workspace changed)
            ├── git-status.txt
            ├── evaluation.json         (valid evaluator verdict)
            ├── evaluator-stdout.log
            ├── evaluator-stderr.log
            ├── setup-<n>/              (setup command logs)
            ├── workspace/              (retained only per policy)
            └── evaluation-scratch/     (keep_all only)
```

Artifact sizes and SHA-256 hashes are stored in SQLite, next to normalized
evaluator checks and typed metric rows with their unit and provenance. Large
files never live in the database. Timestamps are stored as fixed-width UTC text.

Opening the store applies any pending schema migration. A database written by a
newer Gauge is rejected. When the database has no experiments, Gauge imports
metadata from the previous file-based store on first open; unreadable legacy
records are skipped and reported by `gauge doctor`.

Retention policies: `keep_failed` (default; delete the workspace after a passing
evaluation), `keep_all`, and `delete_all`. Logs, diffs, events, and evaluation
output are never deleted with the workspace. Tool caches, isolated agent home
directories (`agent-home/`, `iris-home/`, `pi-home/`), and the evaluator scratch
directory are removed after every attempt unless `keep_all` is selected; they are
not benchmark evidence and can be very large.

## Reproducibility

An experiment records:

- Gauge version and commit,
- suite ID, ordered task IDs, and suite fingerprint,
- per-task content fingerprints (spec + prompt + evaluator assets + pinned
  fixture revision),
- adapter ID and description, redacted configuration and its fingerprint,
- observed agent version/model/provider where available,
- OS, architecture, Go version, execution backend, data directory, Git version,
- repetition count and timestamps.

Secrets are redacted before persistence. Availability is explicit: an adapter that
cannot observe token usage leaves it absent, and reports say `unavailable`.

## Extensibility (planned)

- **Execution backends** wrap workspace preparation and process execution so a
  Docker backend can enforce mount and network isolation without changing task or
  adapter contracts. The `execution.network` field is currently declarative in the
  local backend.
- **Comparison** builds on the persisted per-attempt records; it must refuse to
  compare experiments whose suite or task fingerprints differ.
- **Artifact inventory queries** can build on the normalized `artifacts` table
  without adding columns to the attempt record.

## Security boundary

Local execution is not a sandbox. An agent process can read files outside its
workspace and use the host network. Timeouts and process-group termination are
reliability features, not containment. Evaluator assets are kept out of the agent
workspace and the adapter request, but a hostile agent running locally could still
inspect the host. Task setup commands and evaluators are trusted executable
content. The Docker backend exists in the roadmap specifically to provide a real
isolation boundary.
