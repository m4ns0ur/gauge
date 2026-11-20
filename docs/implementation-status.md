# Implementation status

Tracks delivery against the [implementation plan](gauge-implementation-plan.md).

- **Last updated:** 2026-11-19
- **Verified with:** `make check` (gofmt, vet, build, tests), `make run-smoke`,
  storage tests covering migrations, reconnects, legacy import, corruption, and
  normalized metric/check/artifact rows, comparison tests covering fingerprint
  guards and per-task deltas, a bundled-content test loading every task and suite,
  and the opt-in live integration test passing with the real `iris` (commit
  `d1af31d`) and `pi` 1.5.2 binaries against a local `qwen3.6-35B-Q1` endpoint.
- **Legend:** **Done** = acceptance criteria met and covered by tests; **Partial** =
  usable subset implemented, remaining work listed; **Not started** = design or
  reconnaissance only.

## Snapshot

| Plan phase | Status | Summary |
|---|---|---|
| 0 — Bootstrap and integration recon | **Done** | Go module, CLI, CI, version identity, Iris/Pi recon note. |
| 1 — Domain types and contracts | **Done** | Task/suite/adapter/event/run/evaluation/storage contracts, strict validation, docs, unit tests. |
| 2 — Task and suite discovery | **Done** | Loaders, `tasks list/show`, `suites list/show`, bundled fixture/task/evaluator/smoke suite. |
| 3 — Workspace and local runner | **Done** | Fresh pinned clones, setup, hard timeouts, process-group kill, diff capture, retention, coordinator, fake end-to-end tests. |
| 4 — Independent evaluation | **Done** | Separate evaluator process, versioned JSON, timeouts, infra-vs-task failure split, artifact capture, tests. |
| 5 — Iris and Pi adapters | **Done** | Both adapters built in; recorded-stream, stub-executable, CLI end-to-end, and opt-in live tests against a local model pass. |
| 6 — SQLite persistence and inspection | **Done** | SQLite schema and migrations, normalized checks/artifacts/metrics, legacy import, `runs list/show`, `inspect`, JSON/Markdown export. |
| 7 — Comparison and metrics reporting | **Done** | Aggregates, per-task/category tables, availability rules, repetitions, `gauge compare` with fingerprint guards, tool-call aggregates from event logs. |
| 8 — Initial benchmark suite and docs | **Done** | 17-task Go catalog across four categories, five suites, content test, and a published Iris/Pi baseline (10/17 vs 14/17). |
| 9 — Docker execution backend | **Not started** | Local backend only; `execution.network` is declarative. |

## Phase detail

### Phase 0 — Bootstrap and integration recon: Done

- Module `github.com/m4ns0ur/gauge`, Cobra entry point, `gauge version`,
  `gauge --version` ([cmd/gauge/main.go](../cmd/gauge/main.go),
  [internal/version](../internal/version/version.go)).
- CI runs formatting, vet, build, and tests ([ci.yml](../.github/workflows/ci.yml)).
- Makefile builds, installs via `go install`, checks, and runs the smoke demo
  ([Makefile](../Makefile)).
- Iris and Pi public interfaces inspected and written up, including invocation,
  event streams, state isolation, exit-code quirks, and gaps
  ([adapters.md](adapters.md)).

### Phase 1 — Domain types and contracts: Done

- Task, suite, adapter, event, run result, evaluation, experiment, attempt, and
  metric types are implemented.
- Strict, versioned schemas with file+field errors, path/symlink containment, and
  content fingerprints ([internal/task](../internal/task/load.go),
  [internal/suite](../internal/suite/suite.go),
  [internal/evaluator](../internal/evaluator/result.go)).
- Contract documentation: [task-format.md](task-format.md),
  [adapters.md](adapters.md), [metrics.md](metrics.md),
  [architecture.md](architecture.md).
- Unit tests cover invalid schemas, unsupported versions, missing files, bad
  revisions, duplicate suite tasks, invalid timeouts, and path traversal.

### Phase 2 — Task and suite discovery: Done

- `gauge tasks list/show`, `gauge suites list/show`, category/tag filters, JSON
  output.
- Bundled fixture, task, hidden-test evaluator, and smoke suite:
  [fixtures](../fixtures), [greeting-all](../tasks/repository/greeting-all/task.yaml),
  [smoke suite](../suites/smoke.yaml).
- Missing or duplicated task references fail before any agent starts.

### Phase 3 — Workspace and local runner: Done

- Fresh clone per attempt at the pinned SHA, `--no-local`, origin removed, clean
  checkout verified ([internal/workspace](../internal/workspace/prepare.go)).
- Setup commands, per-attempt agent timeout, process-group termination on Unix,
  diff capture via a temporary index, and retention policies
  ([internal/runner](../internal/runner/runner.go)).
- Sequential experiment coordinator with repetitions, progress persistence, and
  interruption handling ([internal/experiment](../internal/experiment/coordinator.go)).
- Fake adapter proves pass/fail/timeout/evaluator-error/setup-failure/cancellation
  and fresh-workspace isolation without a model
  ([internal/runner/runner_test.go](../internal/runner/runner_test.go)).
- Note: the initial workspace status is verified clean rather than stored as a
  separate field; the baseline commit is recorded with the diff.

### Phase 4 — Independent evaluation: Done

- Evaluator runs after the agent, in its own process, with workspace/assets/scratch
  arguments, a controlled environment, and its own timeout.
- Strict result parsing, individual checks, and consistency validation; non-zero
  exit/timeout/invalid JSON are evaluator errors, never task failures.
- Candidate diffs are captured before evaluation; evaluator scratch and caches are
  cleaned unless `keep_all` ([internal/evaluator](../internal/evaluator/invoke.go)).

### Phase 5 — Iris and Pi adapters: Done

- `internal/adapters/iris` runs `iris jsonl` once per attempt with a fresh
  `--home`, the workspace as `--workdir`, the prompt on stdin, and optional
  `--model`, `--base-url`, and `--debug`
  ([internal/adapters/iris](../internal/adapters/iris/iris.go)).
- `internal/adapters/pi` runs `pi --mode json --no-session` with a fresh
  `PI_CODING_AGENT_DIR`, optional `--provider`/`--model`/`--thinking`/`--tools`,
  copied `models.json`/`auth.json`, and named environment forwarding
  ([internal/adapters/pi](../internal/adapters/pi/pi.go)).
- Both translate their typed event streams to normalized events (messages, tool
  calls/results, usage, errors, lifecycle) and count unknown records rather than
  inventing them. Raw stdout/stderr are kept as artifacts.
- Completion is derived from the stream, not the agent's claim. Pi is the important
  case: it exits 0 after a failed response, so `agent_settled` plus the final
  `stopReason` decide the outcome. Usage, model/provider, and agent version (Pi)
  are recorded only when the agent reports them; Iris has no version flag and
  reports no version.
- Adapter configuration is strict and validated before a run, including missing
  executables (`adapter.UnavailableError`), missing settings/models/auth files, and
  invalid thinking/tool/environment settings.
- Tests cover recorded streams, stub executables (argv, stdin, cwd, artifact
  capture, config copying, version probing, environment forwarding, exit codes,
  timeout, cancellation), and CLI end-to-end runs of the smoke suite for both
  adapters. `gauge agents list`/`gauge doctor` report missing binaries without
  failing the environment check.
- An opt-in live integration test (`internal/cli/live_test.go`) drives both real
  binaries against a running OpenAI-compatible endpoint:
  `GAUGE_LIVE=1 GAUGE_LIVE_MODEL=<model> go test ./internal/cli -run TestLiveAdapters -v -timeout 30m`.
  It passed for both adapters against a local `qwen3.6-35B-Q1` endpoint in the
  initial run (Iris ~9.8s, Pi ~5.8s, both tasks evaluated as passed with usage
  recorded). The task verdict is reported rather than required by default; set
  `GAUGE_LIVE_REQUIRE_PASS=1` to enforce it. `GAUGE_LIVE_REPETITIONS` runs
  multiple attempts and reports per-attempt workload plus a median duration. The
  standard suite skips the test and never calls a model.

### Phase 6 — SQLite persistence and inspection: Done

- SQLite metadata at `<data_dir>/gauge.db` behind the existing `storage.Store`
  operations, with versioned migrations recorded in `schema_migrations`
  ([internal/storage/sqlite.go](../internal/storage/sqlite.go),
  [store.go](../internal/storage/store.go)).
- Normalized `experiments`, `attempts`, `checks`, `artifacts`, and `metrics`
  tables with foreign keys and indexes; events, logs, diffs, and evaluator output
  stay as files with their paths and SHA-256 hashes in the database
  ([rows.go](../internal/storage/rows.go)).
- Typed metric rows carry a unit and provenance (`gauge-observed`,
  `adapter-reported`); unavailable usage is absent rather than zero.
- Fixed-width UTC timestamps keep SQLite ordering chronological, and a database
  written by a newer Gauge is rejected instead of misread.
- A one-time best-effort import reads metadata from the previous file-based store
  when the database is empty; skipped records are surfaced by `gauge doctor`
  ([internal/storage/legacy.go](../internal/storage/legacy.go)).
- `runs list/show`, `inspect`, and JSON/Markdown export are unchanged and read the
  database. Secret redaction still happens before persistence.
- Tests cover round-trips, reopen persistence, normalized child rows, metric
  provenance, child-row replacement, ordering, foreign keys, schema guards,
  corrupt databases, legacy import, and redaction.

### Phase 7 — Comparison and metrics reporting: Done

- Pass rate with explicit numerator/denominator; evaluator errors and
  unevaluated/setup failures reported separately.
- Per-task and per-category tables, median/range duration, usage with coverage,
  reliability counters, and terminal/Markdown/JSON rendering
  ([internal/metrics](../internal/metrics/metrics.go),
  [internal/report](../internal/report/report.go)).
- `gauge compare <experiment-a> <experiment-b>` refuses experiments whose task
  IDs, content fingerprints, or fixture revisions differ, and lists the
  mismatched tasks. Recorded differences in agent, model, provider, adapter
  configuration, suite label, repetitions, Gauge version, and platform are
  displayed rather than normalized away
  ([internal/report/compare.go](../internal/report/compare.go),
  [internal/cli/compare.go](../internal/cli/compare.go)).
- Per-task and per-category comparisons label each task improved, regressed, or
  unchanged with explicit pass-rate denominators, so a regression stays visible
  when the aggregate improves.
- Tool-call totals and failures are reconstructed from normalized event logs,
  with coverage (`event logs for X/Y attempts`), in single-experiment reports and
  comparisons. Missing logs are unavailable, never zero
  ([internal/metrics/behavior.go](../internal/metrics/behavior.go)).
- Holdout-suite guidance and the comparison guards are documented in
  [metrics.md](metrics.md).

### Phase 8 — Initial benchmark suite and docs: Done

- Seventeen deterministic Go tasks across four categories:
  - small coding (8): `text-truncate`, `text-dedupe`, `text-normalize-space`,
    `text-snake-case`, `text-title-case`, `text-join-nonempty`,
    `text-is-palindrome`, `notes-delete`;
  - repository engineering (4): `greeting-all`, `text-reverse-unicode`,
    `text-wordcount`, `notes-cli-exit-codes`;
  - complex engineering (3): `notes-cli-tag-filter`, `notes-json-persistence`,
    `notes-search` (store + CLI work);
  - operational (2): `ops-build-recovery` (fix a repository that does not build),
    `ops-test-recovery` (fix production code behind a correct failing test).
- Five pinned fixtures ([greeting-go](../fixtures/src/greeting-go),
  [text-go](../fixtures/src/text-go), [notes-go](../fixtures/src/notes-go),
  [ops-build-go](../fixtures/src/ops-build-go),
  [ops-tests-go](../fixtures/src/ops-tests-go)). Every task pins a full commit
  SHA, keeps hidden tests in evaluator assets, and records provenance/license.
- `smoke`, `coding`, `repository`, `operational`, and `full` suites
  ([suites](../suites)). `full` contains the entire catalog and is checked
  against it automatically.
- [internal/content](../internal/content/content_test.go) loads every bundled
  task and suite in tests, verifies fixture revisions and evaluator assets, and
  fails if a task is missing from `full`.
- Task evaluators are independent `go test` runs over hidden tests copied into a
  scratch workspace. The sixteen new evaluators were verified to fail on the
  unsolved fixture and pass with reference solutions; representative tasks also
  ran end to end through the runner and evaluator, and the existing greeting
  evaluator is covered by the smoke run.
- The initial baseline is published:
  [2026-11-20 local Qwen3.6-35B-Q1](baselines/2026-11-20-local-qwen3.6-35b-q1.md).
  Iris passed 10/17 (58.8%), Pi 14/17 (82.4%), with one repetition each; three
  Iris and one Pi attempt timed out, and no evaluator errors occurred. The
  report records configuration, per-task and per-category outcomes, telemetry
  caveats, limitations, and task fingerprints.
- Usage, task format, adapter, metrics, architecture, running, and status docs
  are current.

### Phase 9 — Docker execution backend: Not started

- The runner already separates workspace preparation, agent execution, and
  evaluation, which is the intended seam for a backend.
- Next: define the execution-backend interface, enforce network/mount policy, record
  image digests and limits, and document residual risks. Do not claim isolation
  before this exists.

## §17 vertical-slice checklist

- [x] Bootstrap the repository and a minimal Go CLI/CI pipeline.
- [x] Inspect Iris and Pi public interfaces and write the adapter feasibility note
      ([adapters.md](adapters.md)).
- [x] Propose the Task, Suite, Adapter, Event, RunResult, and Evaluator contracts in
      `docs/` ([task-format.md](task-format.md), [adapters.md](adapters.md),
      [metrics.md](metrics.md)).
- [x] Add one tiny fixture repository, one task, one independent evaluator, and one
      smoke suite.
- [x] Implement a fake adapter and prove the full local run/evaluate/report
      lifecycle with automated tests, including fail, timeout, evaluator-error,
      setup-failure, cancellation, and isolation cases.

## Definition of v1 complete (§16)

| Requirement | Status |
|---|---|
| 1. Install/run as a standalone CLI | **Done** — `make build` builds and installs. |
| 2. List and validate agent-neutral tasks/suites | **Done**. |
| 3. Configure Iris and Pi via public interfaces | **Done** — both configurable via TOML, validated, and exercised live. |
| 4. Run the same suite against either agent with pinned fixtures | **Done** — both adapters run the same task end to end with real binaries, fresh fixtures, and a live model. |
| 5. Enforce per-attempt and evaluator timeouts | **Done**. |
| 6. Evaluate independently with hidden checks | **Done**. |
| 7. Capture logs, events, diffs, metadata, evaluation results | **Done** — events/logs where the adapter produces them. |
| 8. Persist and inspect after restart | **Done** — SQLite metadata and file artifacts. |
| 9. Compare two experiments with per-task outcomes and honest metrics | **Done** — `gauge compare` with fingerprint guards and per-task deltas. |
| 10. Reproduce a published baseline | **Done** — [2026-11-20 local Qwen3.6-35B-Q1](baselines/2026-11-20-local-qwen3.6-35b-q1.md); Iris 10/17, Pi 14/17, one repetition. |

## Deliberate deviations

Recorded in [ADR 0001](adr/0001-vertical-slice.md) and
[ADR 0002](adr/0002-sqlite-persistence.md):

| Area | Current choice |
|---|---|
| Persistence | SQLite metadata (`gauge.db`) plus file artifacts; versioned migrations and a one-time legacy import. |
| Fixtures | Bare repositories plus readable sources and `scripts/fixture.sh`. |
| Suites | Single YAML files rather than directories. |
| Evaluator | Positional args + `GAUGE_*` env, strict JSON, candidate failures exit 0. |
| Adapters | `fake`, `iris`, and `pi` are built in. |
| Isolation | Local only; `execution.network` is declarative, not enforced. |
| Diff capture | Temporary index so the candidate's index and worktree are untouched. |

## Next actions, in order

1. Add the Docker execution backend and enforce isolation policy.

## Maintaining this file

- Update **Last updated** and the snapshot when a phase changes status.
- Mark a phase **Done** only when its acceptance criteria are met and covered by
  tests; link the evidence.
- Record contract changes in a new ADR, then update the deviation table here.
- Keep this document a status map, not a duplicate of the task catalog or the
  implementation plan.
