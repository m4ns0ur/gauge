# Gauge

**Measure what agents can actually do.**

Gauge is an independent, agent-neutral evaluation and benchmarking framework for AI
agents. It runs reproducible software-engineering tasks against an agent harness,
evaluates the result with hidden checks that never enter the agent's workspace, and
records enough evidence to compare harnesses, models, and configurations honestly.

Gauge evaluates the whole agent system — harness, prompts, tools, context strategy,
model configuration, execution environment — not just the underlying model.

> **Status: early.** This repository implements the vertical slice described in
> [§17 of the implementation plan](docs/gauge-implementation-plan.md): task and
> suite contracts, a pinned-fixture workspace, the runner, an independent
> evaluator, a deterministic fake adapter, storage, reports, and a CLI. It also
> includes the built-in Iris and Pi adapters. SQLite persistence, experiment
> comparison, the full task catalog, and the Docker backend are the next phases.
> See the [implementation tracker](docs/implementation-status.md) for per-phase
> status, [docs/adapters.md](docs/adapters.md) for the adapter contract and Iris/Pi
> configuration, and
> [docs/adr/0001-vertical-slice.md](docs/adr/0001-vertical-slice.md) for the
> decisions made in this slice.

## Quick start

Requirements: Go 1.24 or newer and Git. No model, API key, or network access is
needed for the smoke demo.

```bash
make build    # builds ./bin/gauge and installs `gauge` into GOBIN or $(go env GOPATH)/bin

gauge --config examples/gauge.toml doctor
gauge --config examples/gauge.toml tasks list
gauge --config examples/gauge.toml run --suite smoke --agent fake
gauge --config examples/gauge.toml runs list
gauge --config examples/gauge.toml export <experiment-id> --format markdown
```

If `$(go env GOPATH)/bin` is not on your `PATH`, use `./bin/gauge` instead of
`gauge`, or add that directory to `PATH`.

Results live in the configured `data_dir` (the example config uses
`examples/.gauge-data`). Reading commands must use the same `--config` or
`--data-dir`; every run prints a copy-pasteable
`gauge --data-dir … runs show …` hint.

The example configuration uses the built-in `fake` adapter, which writes a
candidate implementation instead of calling a model. It exercises the full
lifecycle: fresh pinned workspace → agent execution → diff capture → independent
evaluation → persistence → report. Running the same command is how the core test
suite proves the lifecycle works without a live model.

To run the same suite against Iris or Pi, point a config at the agent binary and
its model provider; each attempt gets an isolated agent home, so the model and
policy come from `[adapters.iris.options]` or `[adapters.pi.options]` (see
[Adapters](docs/adapters.md#iris-implemented) and
[Running benchmarks](docs/running-benchmarks.md)).

## What works today

| Area | Status |
|---|---|
| Versioned task schema, strict validation, path-traversal protection | Implemented |
| Versioned suite schema, duplicate/missing task detection | Implemented |
| Fresh workspace per attempt from a pinned Git fixture | Implemented |
| Setup commands, per-attempt hard timeout, process-group termination | Implemented |
| Diff/status capture before evaluation | Implemented |
| Independent evaluator with versioned JSON output and timeout | Implemented |
| Normalized event log, stdout/stderr, evaluator output artifacts | Implemented |
| File-based experiment store, `runs list/show`, `inspect`, `export` | Implemented (SQLite is next) |
| Markdown/JSON reports with explicit denominators | Implemented |
| Deterministic fake adapter for tests | Implemented |
| Iris adapter (`iris jsonl`) with normalized events, usage, and model discovery | Implemented |
| Pi adapter (`pi --mode json`) with normalized events, usage/cost, and version | Implemented |
| Experiment comparison (`gauge compare`) | Planned |
| Docker execution backend | Planned |

## Core ideas

- **Agent-neutral tasks.** A task never contains Iris- or Pi-specific flags.
- **Independent evaluation.** An agent's completion claim is never evidence of
  success. The evaluator decides, after the agent process ends, using assets the
  agent never saw.
- **Reproducibility.** Fixtures are pinned to full commit SHAs; every attempt
  records task, suite, adapter, model, and environment fingerprints.
- **Honest metrics.** Missing token, cost, or tool data is reported as
  `unavailable`, never zero. Pass rate has an explicit numerator and denominator,
  and evaluator crashes stay separate from task failures.
- **Isolation by default, with explicit limits.** Every attempt gets a fresh
  workspace and timeouts. The local backend is a reproducibility aid, **not a
  security sandbox**; do not run untrusted tasks or evaluators locally.

## Repository layout

```text
cmd/gauge/                 CLI entry point
internal/
  adapter/                 Adapter contract and registry
  adapters/fake/           Deterministic test adapter
  adapters/iris/           Iris adapter (iris jsonl subprocess)
  adapters/pi/             Pi adapter (pi --mode json subprocess)
  cli/                     Cobra commands
  config/                  TOML configuration and path resolution
  evaluator/               Evaluator contract and invocation
  events/                  Normalized events and JSONL recorder
  experiment/              Sequential experiment coordinator
  metrics/                 Aggregation with explicit denominators
  proc/                    Process-group supervision
  report/                  Terminal, Markdown, and JSON reports
  runner/                  Per-attempt lifecycle and retention
  storage/                 Experiment/attempt persistence (file-based today)
  suite/                   Suite schema and discovery
  task/                    Task schema, validation, fingerprints
  testutil/                Fixture builders for tests
  version/                 Build identity
tasks/                     Benchmark tasks (prompt + evaluator + task.yaml)
fixtures/                  Pinned Git fixtures (readable source + bare repos)
suites/                    Named benchmark suites
examples/                  Example configuration
docs/                      Design and usage documentation
```

## Documentation

- [Implementation status](docs/implementation-status.md) — phase-by-phase progress,
  deviations, and next actions.
- [Architecture](docs/architecture.md) — components, lifecycle, storage model.
- [Task format](docs/task-format.md) — task/suite/evaluator contracts.
- [Adapters](docs/adapters.md) — adapter contract, fake/Iris/Pi adapters, testing.
- [Metrics](docs/metrics.md) — outcome model, denominators, availability rules.
- [Running benchmarks](docs/running-benchmarks.md) — configuration, commands,
  retention, inspection, security notes.
- [Decision record](docs/adr/0001-vertical-slice.md) — scope decisions for this slice.
- [Implementation plan](docs/gauge-implementation-plan.md) — the full roadmap.

## Development

```bash
make build          # local binary + `go install` (same version metadata)
make install        # install only
make check          # gofmt check, go vet, build, tests
make test           # go test ./...
make run-smoke      # build, install, and run the fake-adapter smoke benchmark
```

Tests use real Git fixtures and real evaluator subprocesses, but no model or
network. If you develop in a sandbox where the Go module cache is read-only, use
`./scripts/go.sh` (a wrapper that keeps `GOPATH`/`GOCACHE` inside the checkout)
instead of `go`:

```bash
./scripts/go.sh test ./...
```

## Safety

Task setup commands and evaluators are executable code and are treated as trusted
content. Only run tasks from sources you trust. Local execution does not isolate
the agent from the host filesystem, network, or credentials. See
[Running benchmarks](docs/running-benchmarks.md#security-boundary) before pointing
Gauge at untrusted content.

## License

MIT — see [LICENSE](LICENSE).
