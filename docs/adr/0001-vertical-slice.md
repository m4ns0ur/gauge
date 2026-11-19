# ADR 0001: Vertical-slice implementation decisions

- **Status:** Accepted; the file-store decision is superseded by
  [ADR 0002](0002-sqlite-persistence.md)
- **Date:** 2026-11-18
- **Context:** Implementation plan §17 asks for a narrow vertical slice before
  building the full framework: bootstrap, integration recon, contracts, one
  fixture/task/evaluator/suite, and a proven local run/evaluate/report lifecycle
  with a fake adapter.

This record documents where the slice deliberately differs from the target
architecture and what must change later.

## Decisions

### 1. File-based persistence before SQLite

Experiments and attempts are stored as JSON documents under
`<data_dir>/runs/<experiment>/`, with large artifacts as sibling files and
SHA-256 hashes in `metadata.json`. `internal/storage.Store` is a small concrete
type with the operations the runner and CLI need.

**Why:** the plan sequences SQLite after the runner and evaluator contracts, and the
slice's purpose is to validate those contracts. A file store avoids locking the data
model to a schema before the lifecycle is proven, while preserving the documented
on-disk layout and keeping artifact files out of the database.

**Consequences:**

- No schema migrations yet. Metadata documents carry `schema_version: 1` and readers
  reject documents written by a newer schema.
- `ListExperiments`/`ListAttempts` scan directories and surface unreadable records as
  warnings without hiding readable ones.
- The next persistence phase introduces SQLite and migrations behind the same
  operations; the file layout for artifacts stays valid.

### 2. Fixtures are bare Git repositories with readable sources

Bundled fixtures live as bare repositories (`fixtures/<name>.git`) with their
readable source in `fixtures/src/<name>` and a deterministic rebuild script
(`scripts/fixture.sh`). Tasks pin a full 40-character SHA.

**Why:** every attempt must materialize the exact revision without exposing a shared
worktree or object store; a bare repository is a normal file tree that can be
committed to this repository, unlike a nested `.git` directory. Fixed commit
metadata makes regeneration reproducible.

**Consequences:**

- Reviewers read fixture content from `fixtures/src`, not from Git objects.
- Changing a fixture requires rerunning the script and updating `fixture.revision`.
- Attempts clone with `--no-local`, remove the `origin` remote, and verify HEAD.

### 3. Suites are single YAML files

A suite is `<suite-dir>/<name>.yaml`; discovery loads every `.yaml`/`.yml` beneath
the suite directory.

**Why:** suites have no assets or prompt, so a directory layer adds no value. The
plan's suite example is a single document.

**Consequences:** a non-suite YAML file placed under the suite directory is a
validation error. Suite IDs must be unique across files.

### 4. Evaluator invocation is positional plus a controlled environment

`evaluate.sh <workspace> <assets> <scratch>`, cwd = scratch, explicit `GAUGE_*`
variables, minimal inherited environment. The result is one strict JSON object on
stdout; unknown fields are errors. Candidate-caused failures exit 0 with
`status: failed`; non-zero exits, timeouts, and invalid JSON are evaluator errors.

**Why:** the contract must be language-agnostic, impossible to confuse with agent
output, and must keep infrastructure failure distinct from task failure. Strict JSON
turns evaluator drift into a loud error rather than a silently ignored field.

**Consequences:**

- Evaluators are responsible for copying the workspace if they need to add hidden
  tests.
- The scoring contract is pass/fail; `score` is recorded but not aggregated.
- A malformed evaluator never produces a false pass or a false task failure.

### 5. Only the fake adapter ships in this slice

`internal/adapters/fake` is built in; Iris and Pi are documented from their current
public interfaces in [adapters.md](../adapters.md) but not implemented yet.

**Why:** §17 explicitly defers native adapters until the lifecycle is proven. The
fake adapter exercises the same runner, evaluator, storage, and report paths without
a model or network.

**Consequences:** `gauge agents list` shows one adapter. `gauge doctor` reports Iris
and Pi as warnings. Adding them does not require changes to task, suite, runner, or
evaluator contracts.

### 6. Local backend only; network policy is declarative

The runner executes agents and evaluators as local subprocesses with timeouts and
process-group termination on Unix. `execution.network` is validated and recorded but
not enforced.

**Why:** Docker isolation is a later phase and must not block a usable local tool.
Claiming network isolation that does not exist would violate the plan's honesty
requirements.

**Consequences:** documentation and `gauge doctor` state prominently that local
execution is not a sandbox; the Docker backend must implement real mount/network
policy before untrusted tasks are supported.

### 7. Diff capture uses a temporary index

Before evaluation, Gauge copies the workspace index to a temporary file, stages all
changes there, and diffs against the pinned baseline. The candidate's real index and
worktree are untouched.

**Why:** the recorded change set must include untracked, staged, deleted, and renamed
files, and must be captured before the evaluator can mutate anything.

**Consequences:** untracked files ignored by `.gitignore` are not part of the patch.
Binary changes are captured with `--binary`. Patch size is capped (8 MiB) and
truncation is recorded.

### 8. Duration parse errors defer to schema validation

`task.Duration` records a parse error instead of failing YAML decoding, so validation
can report `execution.timeout` or `evaluation.timeout` with the file name.

**Consequence:** callers must check `Duration.Err()` or rely on `task.Load`, which
always validates before returning a task.

## Follow-ups

1. Native Iris and Pi adapters (plan phase 5) with stub-stream tests and one opt-in
   live integration test.
2. SQLite persistence with migrations, plus `gauge compare` with revision guards
   (phases 6–7).
3. The initial 15–25 task catalog and baseline report (phase 8).
4. Docker execution backend with enforced mounts, network policy, and recorded image
   digests (phase 9).
