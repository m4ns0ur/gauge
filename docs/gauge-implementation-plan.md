# Gauge — Implementation Plan

**Project:** Gauge
**Repository:** `gauge`
**Description:** An independent evaluation and benchmarking framework for AI agents.
**Tagline:** Measure what agents can actually do.

## 1. Purpose

Gauge is a standalone, agent-neutral framework for evaluating AI agents on reproducible software-engineering tasks. It should be able to run the same benchmark tasks against different agent harnesses—initially [Iris](https://github.com/m4ns0ur/iris) and [Pi](https://github.com/earendil-works/pi/tree/main/packages/coding-agent)—and compare their results across models, configurations, and versions.

Gauge evaluates the **complete agent system**, not just the underlying language model. The system under test includes the harness, its prompts, tools, context-management strategy, model configuration, and execution environment. All of these must be recorded so experiment results are interpretable.

The primary purpose is to provide empirical evidence for engineering decisions, for example:

- Does adding `grep` or `glob` improve repository-task completion?
- Does a planning step improve complex-task success enough to justify its token and time overhead?
- Does context compaction improve long-running task reliability?
- Does a subagent architecture improve results, or merely increase cost and coordination failures?
- How much does a harness contribute beyond the model it uses?
- Does a new version improve task success without introducing regressions in reliability or efficiency?

Gauge should be useful as a local developer tool first. It is not intended to become a hosted evaluation service or a general-purpose AI platform in its first release.

## 2. Core principles

1. **Agent-neutral tasks.** Task definitions and evaluators must not depend on Iris, Pi, or any particular harness.
2. **Independent evaluation.** An agent's own completion message or claimed success is never sufficient evidence that a task passed. The evaluator determines correctness.
3. **Reproducibility.** Pin task fixtures and revisions; record exact agent, model, configuration, suite, and environment metadata.
4. **Controlled comparisons.** Run the same tasks under comparable conditions and preserve per-task results, not only aggregate scores.
5. **Observable, not intrusive.** Capture normalized events and raw output when available, but do not require every agent to expose identical telemetry.
6. **Honest metrics.** Missing token, cost, or tool-use data must be represented as unavailable, not estimated or fabricated.
7. **Isolation by default, with explicit limits.** Each attempt gets a fresh workspace and a hard timeout. Stronger process and filesystem isolation should be provided by a container backend. Local execution must not be described as a security sandbox.
8. **Small initial scope.** Build a reliable runner, task format, adapters, evaluator contract, and useful reports before adding dashboards, distributed execution, or broad integrations.
9. **No runtime plugin marketplace.** Initial adapters are built into Gauge. Do not add dynamic third-party plugin loading or a public extension marketplace in v1.

## 3. Non-goals for v1

Do not implement the following in the initial release:

- A web UI or hosted dashboard.
- A public leaderboard or global ranking service.
- Distributed execution, worker pools, Kubernetes, or cloud orchestration.
- An LLM inference server or model-provider abstraction that duplicates the agent's own provider support.
- A plugin marketplace or arbitrary dynamically loaded adapter code.
- A universal benchmark covering every agent category and programming language.
- Advanced statistical significance testing or elaborate scoring formulas.
- Automatic modification of Iris or Pi to expose private internals solely for Gauge.
- A benchmark that rewards the agent for knowing Gauge-specific tasks or prompts.

Keep the architecture open to these possibilities, but do not build them pre-emptively.

## 4. Technology stack

Use the following stack unless repository constraints reveal a strong reason to change it:

| Area | Choice | Notes |
|---|---|---|
| Language | Go | Core runner, CLI, adapters, evaluation orchestration |
| CLI | Cobra | Commands, flags, help and completion |
| Configuration | TOML | Local Gauge configuration |
| Specification | YAML | Task, suite |
| Persistence | SQLite | Experiment metadata, task runs, metrics and evaluations |
| SQLite driver | `modernc.org/sqlite` | Prefer the pure-Go driver to avoid CGO requirements; validate portability before committing |
| Agent integration | Subprocess, HTTP, JSON-RPC where supported | Use supported public interfaces of each agent |
| Workspace | Git clone/worktree or immutable fixture snapshot | A fresh workspace per task attempt |
| Stronger isolation | Docker, optional in early development and recommended for untrusted tasks | Do not require Kubernetes or a remote executor |
| Reports | Markdown and JSON | HTML can be considered later |
| Tests | Go standard testing plus fixture-based integration tests | Avoid unnecessary test frameworks |
| Distribution | Go binaries | macOS and Linux first; Windows should not be blocked by avoidable assumptions |

Use Cobra only for the CLI. Avoid introducing a large dependency-injection framework or a general workflow engine.

## 5. High-level architecture

```text
                         Gauge CLI
                             |
                    Experiment coordinator
                             |
             +---------------+----------------+
             |               |                |
        Task loader     Agent adapter     Run recorder
        Suite loader         |                |
             |         +-----+-----+          |
             |         |           |          |
             |       Iris         Pi          |
             |         |           |          |
             +---------+-----------+----------+
                             |
                     Isolated workspace
                             |
                    Agent execution ends
                             |
                  Collect diff and artifacts
                             |
                   Independent evaluator
                             |
                Metrics + SQLite + reports
```

Gauge owns task selection, workspace lifecycle, timeouts, evaluation, result persistence, and reporting. An agent adapter owns the mechanics of invoking one agent and translating whatever output it exposes into Gauge's common event/result model.

### Suggested repository layout

```text
gauge/
├── cmd/
│   └── gauge/
│       └── main.go
├── internal/
│   ├── adapter/          # Common adapter contract and registry
│   ├── adapters/
│   │   ├── iris/
│   │   └── pi/
│   ├── config/           # User configuration and defaults
│   ├── task/             # Task schema, parsing and validation
│   ├── suite/            # Suite schema, parsing and validation
│   ├── experiment/       # Experiment configuration and orchestration
│   ├── runner/           # Per-attempt lifecycle and timeout handling
│   ├── workspace/        # Fixture preparation and cleanup
│   ├── evaluator/        # Independent evaluator invocation and results
│   ├── events/           # Normalized agent events and JSONL recording
│   ├── metrics/          # Metric types and aggregation
│   ├── storage/          # SQLite schema and repository implementations
│   ├── report/           # Markdown and JSON report generation
│   └── cli/              # Cobra commands and presentation
├── tasks/
│   ├── coding/
│   ├── repository/
│   ├── complex/
│   └── operational/
├── fixtures/             # Small, versioned Git repositories used by tasks
├── suites/               # Named benchmark-suite definitions
├── examples/             # Example Gauge config, task, suite and reports
├── docs/
│   ├── architecture.md
│   ├── task-format.md
│   ├── adapters.md
│   ├── metrics.md
│   └── running-benchmarks.md
├── go.mod
├── README.md
└── LICENSE
```

Adjust package names if the implementation benefits from a simpler layout. Keep packages cohesive and avoid creating empty abstraction layers solely to match this diagram.

## 6. The two foundational contracts

Design and validate these before implementing the full runner:

1. The **task specification**, which defines what the agent is asked to do and how the result is independently evaluated.
2. The **agent adapter contract**, which defines how Gauge invokes different harnesses consistently.

Write both as short design documents and add schema/interface tests before building the rest of the system around them.

### 6.1 Agent adapter contract

The adapter interface should be deliberately small. It must support a single benchmark attempt, pass the task prompt and workspace to the agent, stream observable events, and return a process-level outcome. It must not evaluate task correctness.

A reasonable starting point is:

```go
type Adapter interface {
    ID() string
    Validate(ctx context.Context, config json.RawMessage) error
    Run(ctx context.Context, request RunRequest, emit func(Event)) (RunResult, error)
}
```

Refine names and types during implementation, but preserve these responsibilities.

`RunRequest` should include:

- Unique attempt/run identifier.
- Task prompt as text (not the evaluator or hidden tests).
- Absolute path to the attempt's working directory.
- Per-attempt timeout/deadline.
- Agent-specific configuration as structured JSON or a typed adapter config.
- Requested model/provider overrides only when the adapter can apply them.
- A controlled environment-variable set, with secret values redacted from persisted configuration.

`Event` should support a small normalized vocabulary, such as:

- agent/message output;
- assistant state changes where available;
- tool call and tool result where available;
- usage information where available;
- warning/error;
- process lifecycle events.

Each normalized event should retain a timestamp, source/adapter identifier, and optional raw payload. Write the raw adapter output to a per-attempt JSONL artifact when possible. Do not force an adapter to invent tool calls or token counts when it cannot observe them.

`RunResult` should describe execution, not correctness. It should contain, where available:

- Completion reason: completed, failed, timed out, or cancelled.
- Process exit code and error details.
- Start/end time and elapsed duration.
- Agent name/version/commit if discoverable.
- Actual model/provider identifier if discoverable.
- Input/output/total token counts and cost only when reported by a trustworthy source.
- Adapter-specific metadata, preserved as JSON.

The adapter must not return a `passed` or `success` field for the coding task. Gauge's evaluator owns that result.

#### Adapter implementation requirements

- Implement Iris and Pi as separate built-in adapters.
- Before coding either adapter, inspect its current public CLI, JSONL, JSON-RPC, or SDK integration options. Do not assume flags, event schemas, or package APIs from memory.
- Prefer documented/public process interfaces. Do not import private internals from Iris or Pi.
- Prefer a supported machine-readable event stream when one exists. Otherwise capture stdout/stderr, exit status, and timing; mark unavailable telemetry as unavailable.
- Do not patch the agent repositories as part of Gauge implementation. If an integration gap is found, document it as a separate optional upstream request.
- Adapter configuration must be isolated from the task definition. A task must not contain Iris- or Pi-specific flags.
- Add a fake/test adapter for runner and integration tests. Core tests must not require an installed model or a live inference endpoint.

If one agent cannot be driven reliably without an unsupported private API, document the limitation and implement the best supported subprocess integration rather than coupling Gauge to private code.

### 6.2 Task specification

A task is an immutable, agent-neutral benchmark item. It consists of:

- Metadata and category/difficulty labels.
- A pinned repository fixture and revision.
- A prompt visible to the agent.
- Execution constraints and timeout.
- An evaluator and evaluator assets that are not placed in the agent workspace.

Example task directory:

```text
tasks/repository/go-http-timeout/
├── task.yaml
├── prompt.md
└── evaluator/
    ├── evaluate.sh
    ├── hidden_test.go
    └── README.md             # Optional maintainer-only evaluator notes
```

Example `task.yaml` (final field names may be refined in the schema design):

```yaml
schema_version: 1
id: go-http-timeout
name: Add request timeout configuration
category: repository-engineering
difficulty: medium
description: Add configurable request timeouts without breaking existing behavior.
tags:
  - go
  - configuration
  - backward-compatibility

fixture:
  path: ../../../fixtures/go-http-server
  revision: "<immutable-git-commit>"

prompt: prompt.md

execution:
  timeout: 10m
  network: disabled
  setup: []

evaluation:
  path: evaluator
  command:
    - ./evaluate.sh
  timeout: 2m
```

Use a pinned commit hash, not a moving branch or tag. Validate all relative paths, reject path traversal outside the task root, and account for symlinks when resolving task/evaluator paths.

The example is illustrative. The implementation must create a documented, versioned schema with strict validation and helpful error messages. Avoid allowing arbitrary task YAML to silently override host-level safety controls.

#### Task format rules

- `prompt.md` is the only task instruction passed to the agent, together with the working directory and agreed execution context.
- Do not pass evaluator scripts, hidden tests, expected output, grading rules, or other maintainer-only files to the adapter.
- Tasks must not require internet access by default. Any exception must be explicit, justified, and recorded.
- Task setup and evaluator commands are executable code. Only run task suites from trusted sources; document this clearly.
- The agent should be free to inspect the repository and use its normal tools. Do not bake in file paths or implementation hints unless they are part of the actual issue description.
- Existing tests should remain in the fixture. Hidden tests should check requirements not already covered by visible tests.
- Evaluators should be deterministic, independent of agent identity, and report individual checks.
- Record task origin and license/provenance in metadata or maintainer documentation. Do not copy benchmark datasets wholesale without checking their licenses and terms.

### 6.3 Suite specification

A suite selects a versioned collection of task IDs. It should be possible to run one task, one suite, or all tasks in a category.

Example:

```yaml
schema_version: 1
id: smoke
name: Gauge smoke suite
description: A short suite for validating an agent configuration.
tasks:
  - id: go-small-function-001
  - id: go-http-timeout
```

Support per-task repetition overrides only if there is a clear use case. Otherwise keep repetition count at the experiment/CLI level.

A suite must fail validation if a task is missing or duplicated without an explicit reason. Resolve and record the exact suite/task versions used for each experiment.

## 7. Workspace and execution model

Every task attempt must use a fresh workspace prepared from the fixture's pinned revision. No attempt may reuse a modified workspace from a previous attempt.

### Initial local execution backend

Implement a local backend first so development does not require Docker:

1. Create a unique run directory under Gauge's configurable work root.
2. Clone or materialize the fixture at its pinned revision into a new workspace. Avoid shared mutable Git state between attempts; do not expose a shared fixture worktree or object store to the agent.
3. Run declared setup steps, if any, under the configured timeout and environment.
4. Invoke the selected agent adapter with only the task prompt and workspace path.
5. Enforce a hard timeout and terminate the process tree on timeout/cancellation as reliably as the operating system allows.
6. Record the final Git diff/status, stdout/stderr, normalized events, and execution metadata.
7. Run the evaluator separately from the agent process.
8. Persist the result and either preserve or clean the workspace according to retention settings.

The local backend is a reproducibility aid, not a security boundary. A local agent process may be able to inspect files outside its working directory or use the host network. State this prominently in the documentation.

### Docker backend

Add a Docker execution backend after the local flow is working, but design the runner interfaces so the backend can be added without changing task or adapter contracts.

The Docker backend should:

- Run agent execution in an isolated container with a controlled workspace mount.
- Avoid mounting evaluator assets or the Gauge database into the agent container.
- Disable general network access by default. When a model endpoint is hosted on the machine or elsewhere, allow only the explicitly configured endpoint/network path; record the exception.
- Support configurable CPU, memory, and process/time limits where Docker makes them available.
- Run evaluation in a separate process/container with evaluator assets mounted read-only and the candidate workspace mounted read-only where feasible.
- Clearly document remaining container escape and host-configuration risks; do not claim perfect isolation.

Do not make Docker support block the first usable local release, but do not describe hidden tests as secure against a hostile agent when running locally.

## 8. Evaluation contract

Evaluation runs after the agent has terminated or timed out. It must not be possible for the agent to report its own score or modify the evaluator assets through the workspace mount.

The evaluator receives:

- Path to the candidate workspace.
- Path to evaluator assets.
- A scratch/output directory.
- A controlled environment and timeout.

It returns a versioned JSON result on stdout (or a documented result file). A starting format:

```json
{
  "schema_version": 1,
  "status": "passed",
  "score": 1.0,
  "summary": "All required checks passed.",
  "checks": [
    {
      "id": "request-timeout-applied",
      "status": "passed",
      "message": "Configured timeout is applied to outbound requests."
    },
    {
      "id": "backward-compatibility",
      "status": "passed",
      "message": "Existing behavior remains unchanged when no timeout is configured."
    }
  ]
}
```

Allowed overall statuses: `passed`, `failed`, and `error`. A non-zero evaluator exit, invalid JSON, timeout, or crash should be recorded as an evaluator error, not silently converted into an agent task failure. Keep agent execution failure and evaluator infrastructure failure as separate statuses.

The evaluator's individual checks are the source of truth. `score` may be optional in v1; if included, define how it is calculated and do not use arbitrary weighted scores across unrelated tasks. The primary task result is pass/fail.

The evaluator must not mutate the captured candidate diff. Where practical, run tests against a read-only workspace or a temporary copy. Any generated build/cache files should be placed in a dedicated scratch directory.

## 9. Metrics and experiment model

Do not collapse every result into one opaque leaderboard number. Preserve per-task outcomes and show aggregate metrics with clear denominators.

### 9.1 Correctness — primary

- Task passed/failed/error.
- Suite pass rate: passed tasks divided by tasks with valid evaluator results; show errors and timeouts separately.
- Per-task and per-category pass rate.
- Existing test, hidden test, regression, lint, formatting, and scope checks when supplied by the evaluator.

Do not treat a successful process exit or an agent's final message as task correctness.

### 9.2 Efficiency — secondary

Capture where available:

- Wall-clock duration.
- Input/output/total tokens.
- Estimated or reported cost, with its source and currency.
- Number of tool calls and failed tool calls.
- Model throughput, peak memory, or CPU/GPU utilization only if the source is reliable and collection is practical.

Metrics that an adapter cannot observe must be null/absent, not zero. Do not compare cost values from incompatible provider pricing assumptions without displaying that context.

### 9.3 Reliability

- Timeout, crash, cancellation, and adapter error rates.
- Success rate across repetitions.
- Variance in duration and token usage.
- Repeated failures and flaky evaluator checks.

Keep individual attempts. Report median and range initially; add more statistics only when needed. Do not claim statistical significance from a small number of repetitions.

### 9.4 Behavior and process diagnostics

Where the agent exposes enough events, report:

- Tool calls and tool failures.
- Repeated identical/failed actions where detectable.
- Whether tests were run before completion.
- Whether the agent inspected the repository before editing.
- Whether it made unrelated changes or modified forbidden files.

These are diagnostic signals, not universal correctness criteria. Avoid hard-coding behavior expectations that favor one agent's internal workflow.

### 9.5 Experiment identity and reproducibility

Persist at least:

- Gauge version and Git commit.
- Suite ID/version/fingerprint and exact task revisions.
- Agent adapter ID/version and agent version/commit where available.
- Full non-secret adapter configuration and its fingerprint.
- Model/provider identifier and model revision/hash where available.
- Relevant inference settings when observable/configured.
- Prompt/resource configuration fingerprint where the agent exposes it.
- OS, architecture, Go version, Docker version/image where relevant.
- Execution backend, limits, network policy, and environment metadata.
- Repetition count and random seed if an agent/model exposes a controllable seed.

Never persist API keys or raw secret environment values. Persist variable names and redacted indicators instead.

## 10. Experiment and result storage

Use SQLite for structured metadata and metrics. Store potentially large logs, event streams, diffs, and other artifacts as files beneath a configurable Gauge data directory; keep paths and hashes in SQLite.

Suggested conceptual entities:

- **Experiment:** one invocation/configuration of an agent against a suite, including repetitions.
- **Task attempt:** one execution of one task within an experiment.
- **Event/artifact:** references to JSONL event logs, stdout/stderr, diff, evaluator output, and optional workspace snapshot.
- **Evaluation:** normalized evaluator result and individual checks.
- **Metric:** typed value, unit, and provenance/source.

Use migrations from the beginning. Add foreign keys and indexes for experiment ID, task ID, agent, model, and creation time. Store timestamps in UTC (RFC 3339 or integer epoch consistently).

A possible on-disk layout:

```text
~/.local/share/gauge/
├── gauge.db
└── runs/
    └── <experiment-id>/
        └── <attempt-id>/
            ├── metadata.json
            ├── events.jsonl
            ├── stdout.log
            ├── stderr.log
            ├── diff.patch
            ├── git-status.txt
            ├── evaluation.json
            └── workspace/          # Optional, based on retention policy
```

Allow a user-configured data/work directory. Do not assume that the database and workspaces live in the repository.

Retention settings should support at least:

- Delete workspaces after successful evaluation.
- Preserve failed/error workspaces.
- Preserve all workspaces for debugging.

Do not delete logs or evaluation outputs when a workspace is removed.

## 11. CLI scope

Build a small CLI. Names may be refined, but keep the command surface predictable.

```bash
# Environment and integration checks
gauge version
gauge doctor
gauge agents list

# Discover benchmark content
gauge tasks list
gauge suites list

# Execute
gauge run --suite smoke --agent iris
gauge run --suite repository --agent pi --repetitions 3

# Inspect and compare
gauge runs list
gauge runs show <experiment-id>
gauge inspect <attempt-id>
gauge compare <experiment-id-a> <experiment-id-b>

# Export
gauge export <experiment-id> --format json
gauge export <experiment-id> --format markdown
```

A model-selection flag may be provided when supported by the selected adapter, but do not imply that every agent can accept a universal `--model` override. Adapter-specific configuration should be available through a Gauge config file and/or an explicit config path.

`gauge doctor` should check Gauge configuration, database access, fixture availability, adapter executables/integration prerequisites, and Docker availability when the Docker backend is selected. It should not make inference calls unless explicitly requested.

The CLI should support non-interactive use and return meaningful exit codes for automation. Keep output concise by default; provide `--verbose` for detailed diagnostics.

## 12. Initial benchmark content

Start with approximately 15–25 carefully authored tasks. Quality and determinism are more important than task count.

Suggested first mix:

| Category | Initial target | Examples |
|---|---:|---|
| Small coding | 4–5 | Implement a function, parse a format, add focused unit tests |
| Repository engineering | 8–12 | Fix a bug, add a configuration option, implement an API, update a multi-file feature |
| Complex engineering | 2–4 | Cross-package change, regression investigation, migration or race-condition fix |
| Operational/recovery | 2–3 | Recover from a failed command, handle misleading test output, avoid repeating a failed operation |

Keep the first suite Go-heavy because it is straightforward to fixture and evaluate, but do not make the task schema Go-specific. Add another language only after the execution/evaluator model is stable.

Task design requirements:

- Realistic prompts that describe the desired behavior, not the exact implementation steps.
- Small enough to evaluate repeatably, but not all one-file puzzles.
- A mix of repository exploration, debugging, implementation, testing, refactoring, and multi-step reasoning.
- Existing tests plus independent hidden tests where possible.
- Clear scope constraints and backward-compatibility requirements when relevant.
- Avoid tasks requiring network access, credentials, nondeterministic services, or manual interaction.
- Include some tasks where a naive but incorrect shortcut is caught by hidden/regression tests.

The operational category may depend on an agent exposing tool events or recovery details. Keep those tasks in a separate optional suite if their evaluation is not meaningful across all adapters. Do not let adapter-specific observability determine the main coding score.

## 13. Controlled comparison methodology

Gauge should make it easy to compare configurations such as:

| Experiment | Agent | Model | Configuration |
|---|---|---|---|
| A | Iris | Qwen 3.5 9B | Current baseline |
| B | Iris | Qwen 3.5 9B | Additional repository tools |
| C | Pi | Qwen 3.5 9B | Default supported configuration |
| D | Iris | Model B | Current baseline |
| E | Pi | Model B | Default supported configuration |

The values above are examples, not hard-coded product assumptions.

For fair comparisons:

- Use the same exact suite and task fixture revisions.
- Use the same execution limits and comparable compute resources where practical.
- Record all differences in agent prompts, tools, model settings, and context configuration.
- Use fresh workspaces for every repetition.
- Do not silently retry failed attempts. A retry is a separate attempt and must be visible in the result.
- Start with 1 repetition during development, 3 for routine comparisons, and 5–10 for more meaningful experiments when time/cost permits.
- Report per-task outcomes alongside aggregate results. A suite average can conceal a regression in a particular category.
- Consider a small holdout suite that is not used while tuning an agent configuration. This helps detect overfitting to the public benchmark tasks.

Gauge should not claim that two results are directly comparable when their model revisions, task revisions, or execution environments materially differ. Display these differences in comparison output.

## 14. Implementation phases

Implement in the sequence below. Each phase should leave the repository buildable and tested. Add short architecture decision records when changing a foundational contract.

### Phase 0 — Repository bootstrap and integration reconnaissance

Tasks:

1. Create the standalone Go repository and initialize the module as `gauge` (verify module path and GitHub ownership/name before hard-coding it in docs).
2. Add license, README skeleton, `.gitignore`, formatting/lint/test conventions, and a minimal CI workflow for Go formatting, tests, and build.
3. Add Cobra entry point with `gauge version` and useful root help.
4. Inspect current public integration surfaces for Iris and Pi:
   - supported CLI invocation;
   - non-interactive mode;
   - JSONL/event output or JSON-RPC/SDK support;
   - working-directory selection;
   - model/provider selection;
   - timeout/cancellation behavior;
   - version reporting.
5. Write a short adapter feasibility note for each agent. Identify what telemetry is available and what must remain unavailable.
6. Decide whether the first Iris/Pi adapters can be implemented without changes to either project. If not, document the minimum supported integration needed; do not silently depend on private internals.

Acceptance criteria:

- `go test ./...` and `go build ./...` pass in CI.
- `gauge version` works.
- A written integration note exists for Iris and Pi based on their current public interfaces.
- No agent-specific assumptions are embedded in task definitions.

### Phase 1 — Domain types and foundational specifications

Tasks:

1. Define versioned Task, Suite, Agent Adapter, RunRequest, Event, RunResult, EvaluationResult, Experiment, Attempt, and Metric types.
2. Define task/suite YAML schema and strict validation behavior.
3. Define evaluator JSON output schema.
4. Define normalized event vocabulary and raw-event preservation strategy.
5. Write `docs/task-format.md`, `docs/adapters.md`, and `docs/metrics.md`.
6. Add unit tests for valid/invalid schemas, missing files, bad revisions, duplicate suite tasks, invalid timeouts, path traversal, and unsupported schema versions.

Acceptance criteria:

- Task and suite documents can be loaded and validated without starting an agent.
- Schema errors identify the file and field causing the problem.
- Adapter contract does not expose evaluator details or an agent-controlled success score.
- A fake adapter can satisfy the contract in tests.

### Phase 2 — Task and suite discovery

Tasks:

1. Implement task loader and validator.
2. Implement suite loader and validator.
3. Add `gauge tasks list`, `gauge tasks show <id>`, and `gauge suites list` (or equivalent concise commands).
4. Add one minimal fixture repository and one example task with a deterministic evaluator.
5. Add a smoke suite containing that task.

Acceptance criteria:

- Gauge can discover and display bundled tasks/suites.
- The example task is agent-neutral and includes a pinned fixture revision.
- Invalid/missing references fail before any agent process is launched.
- Task prompt and evaluator assets remain separate in the on-disk layout.

### Phase 3 — Workspace manager and local runner

Tasks:

1. Implement a configurable Gauge data/work root.
2. Materialize a fresh, isolated repository for each attempt from the exact fixture revision.
3. Record repository revision, initial status, and final diff/status.
4. Implement setup command execution with timeout and clear stdout/stderr capture.
5. Implement hard attempt timeout and cancellation propagation to the adapter/process tree as far as supported by the platform.
6. Implement workspace retention policies.
7. Implement the experiment coordinator for sequential task execution and repetitions.
8. Use the fake adapter to test end-to-end orchestration without a real model.

Acceptance criteria:

- Every repetition receives a fresh workspace at the expected fixture commit.
- One attempt cannot modify another attempt's fixture/workspace.
- Timeout, cancellation, setup failure, adapter failure, and successful execution are represented distinctly.
- Logs and diffs are collected even when the agent exits unsuccessfully.
- No task evaluator is invoked by the agent adapter.

### Phase 4 — Independent evaluator execution

Tasks:

1. Implement evaluator invocation outside the agent process lifecycle.
2. Parse and validate versioned evaluator JSON output.
3. Enforce evaluator timeout and capture evaluator stdout/stderr.
4. Keep evaluator infrastructure errors separate from agent execution failures and task failures.
5. Preserve individual check results and evaluator output artifacts.
6. Add integration tests for pass, fail, malformed JSON, missing executable, non-zero exit, and timeout.

Acceptance criteria:

- The example smoke task can pass and fail based on evaluator checks, independent of the agent's final message.
- Evaluator assets are not included in the agent workspace or RunRequest.
- Invalid evaluator output is reported as an evaluator error, not a false pass/fail.
- Candidate diffs are captured before evaluation and are not changed by it.

### Phase 5 — Iris and Pi adapters

Tasks:

1. Implement the Iris adapter using the supported public interface identified in Phase 0.
2. Implement the Pi adapter using its supported public interface identified in Phase 0.
3. Capture process output and normalized events where available.
4. Record agent version, model, provider, and usage only when reliably available.
5. Implement adapter configuration and validation, including useful missing-dependency errors.
6. Add adapter tests using stub executables or recorded sample streams; do not make the standard unit-test suite depend on a live model.
7. Add an opt-in integration test requiring a user-configured local model/endpoint.

Acceptance criteria:

- Both adapters can execute the same smoke task through Gauge, subject to their supported interfaces.
- The task prompt, fixture revision, evaluator, and evaluator criteria are identical across adapters.
- Agent-specific options stay in adapter configuration, not task YAML.
- Missing metrics remain absent/null rather than fabricated.
- No private Iris/Pi package imports or source modifications are required.

If one adapter cannot meet the initial interface without unsupported changes, ship the other adapter and a documented adapter limitation rather than delaying the core framework indefinitely.

### Phase 6 — SQLite persistence and result inspection

Tasks:

1. Add SQLite schema and migrations.
2. Persist experiment metadata, task attempts, evaluator results, checks, and metric values/provenance.
3. Persist artifact paths and hashes; keep large event/log files outside SQLite.
4. Implement `gauge runs list`, `gauge runs show`, and `gauge inspect`.
5. Implement JSON and Markdown export.
6. Add database migration, corruption/error handling, and storage integration tests.

Acceptance criteria:

- Results survive process restart and can be inspected without rerunning the benchmark.
- An experiment records its exact suite/task and agent configuration fingerprints.
- A task attempt can be traced to its logs, events, diff, and evaluation JSON.
- Secret values are redacted and are not stored in the database or artifacts.

### Phase 7 — Comparison and metrics reporting

Tasks:

1. Implement aggregate pass rate with explicit numerator/denominator and separate execution/evaluator errors.
2. Implement per-task and per-category outcome tables.
3. Report duration and token/cost/tool metrics only where available, including source/provenance.
4. For repetitions, report individual outcomes plus median and range for applicable numeric metrics.
5. Implement comparison of two experiments and clearly show configuration/environment differences.
6. Keep the first report readable in a terminal and exportable as Markdown/JSON.

Acceptance criteria:

- A comparison cannot silently compare different task-suite revisions as if they were identical.
- A failed evaluator infrastructure run is not counted as an ordinary task failure.
- Reports show missing metrics as unavailable, not zero.
- Per-task regressions are visible even when aggregate pass rate improves.

### Phase 8 — Initial benchmark suite and documentation

Tasks:

1. Add 15–25 tasks across the categories in Section 12, prioritizing deterministic repository-engineering work.
2. Add `smoke`, `coding`, `repository`, and `full` suites, with a smaller smoke suite suitable for routine checks.
3. Include task provenance/license metadata and maintainer notes for evaluator intent.
4. Add documentation for installing Gauge, configuring adapters, running a suite, interpreting results, adding a task, and comparing experiments.
5. Run the initial benchmark against Iris and Pi with at least one common model where both support it. Use a second model when available.
6. Publish a baseline report with all configuration details and known limitations.

Acceptance criteria:

- The benchmark suite runs end-to-end on a clean supported development machine.
- Every task has a deterministic evaluator and documented expected behavior.
- The same task suite can be run through both initial adapters without changing task files.
- Baseline results include the exact Gauge commit, suite/task revisions, agent versions, model identifiers, and execution environment.
- Known limitations and unavailable metrics are explicitly documented.

### Phase 9 — Docker execution backend (after local MVP)

Tasks:

1. Add an execution-backend interface without changing task or adapter contracts.
2. Implement Docker agent execution with controlled workspace mounts and resource/time limits.
3. Run evaluators separately with evaluator assets read-only and candidate files read-only where practical.
4. Add explicit, narrow configuration for reaching a local/remote model endpoint while otherwise disabling network access.
5. Record Docker image digest, runtime version, resource limits, and network configuration.
6. Add integration tests and documentation for the isolation model and its limitations.

Acceptance criteria:

- A benchmark can run in Docker without exposing evaluator files or Gauge's database to the agent container.
- Network access is denied by default, with any model-endpoint exception explicit and recorded.
- Agent and evaluator execution have separate lifecycles and mounts.
- Documentation accurately describes the security boundary and residual risks.

## 15. Quality requirements

### Correctness and reliability

- Unit-test task/suite parsing, schema validation, metrics, report generation, and storage logic.
- Test runner lifecycle using a fake adapter and fake evaluators.
- Test process timeouts and cancellation on supported operating systems.
- Test interrupted runs and ensure partial logs/artifacts are preserved.
- Do not automatically replay a timed-out or interrupted task; a new attempt must be explicit.

### Security and integrity

- Never pass evaluator paths, hidden tests, expected outputs, or grading rules to an adapter.
- Validate paths and prevent task path traversal outside declared roots.
- Redact secrets from configuration, process arguments where possible, environment dumps, and logs.
- Do not log API keys or authentication headers from model endpoint configuration.
- Treat task setup/evaluator code as trusted executable content and state this in documentation.
- Local mode is not a sandbox. Docker mode should minimize mounts and privileges, but must not be advertised as an absolute security guarantee.
- Capture the diff before evaluation and retain immutable metadata for every attempt.

### Portability

- Avoid assuming Bash, GNU-specific tools, or Linux-only process behavior in the core runner.
- It is acceptable for individual task fixtures/evaluators to specify a Linux/Docker environment, provided this is declared in metadata.
- Validate the chosen SQLite driver and process cancellation behavior on macOS and Linux.

### Maintainability

- Prefer a small number of explicit interfaces over broad generic frameworks.
- Keep task format versioned from its first release.
- Use database migrations.
- Keep raw agent output and normalized telemetry side by side.
- Document design decisions when changing task, adapter, evaluator, or result contracts.

## 16. Definition of v1 complete

Gauge v1 is complete when a user can:

1. Install/run Gauge as a standalone CLI.
2. List and validate agent-neutral benchmark tasks and suites.
3. Configure Iris and Pi adapters through supported public interfaces.
4. Run the same task suite against either agent using fresh pinned repository fixtures.
5. Enforce per-attempt and evaluator timeouts.
6. Evaluate results independently using hidden tests/checks that are not placed in the agent workspace.
7. Capture logs, events where available, diffs, execution metadata, and evaluator results.
8. Persist experiments and inspect them after restarting Gauge.
9. Compare two experiments with per-task outcomes and honest efficiency/reliability metrics.
10. Reproduce a published baseline from documented configuration.

A polished dashboard, distributed execution, a large task catalog, and universal adapter support are not required for v1.

## 17. First task for the implementation agent

Do not immediately implement all phases. Start with the following focused work:

1. Bootstrap the `gauge` repository and a minimal Go CLI/CI pipeline.
2. Inspect the current public interfaces of Iris and Pi and write the adapter feasibility note.
3. Propose the first version of the Task, Suite, Adapter, Event, RunResult, and Evaluator contracts in `docs/`.
4. Add one tiny fixture repository, one task, one independent evaluator, and one smoke suite.
5. Implement a fake adapter and prove the full local run/evaluate/report lifecycle with automated tests.

Only after this vertical slice works should implementation proceed to native Iris/Pi adapters, SQLite persistence, richer metrics, and the initial task catalog. This reduces the risk of building a large framework around untested assumptions about how different agent harnesses can be driven.
