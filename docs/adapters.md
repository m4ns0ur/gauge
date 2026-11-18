# Adapter contract and agent integration

An adapter is the only component that knows how a particular agent harness is
invoked. It translates one harness into Gauge's common run/event model. It never
decides whether a task passed.

## The interface

```go
type Adapter interface {
    ID() string
    Validate(ctx context.Context, config json.RawMessage) error
    Run(ctx context.Context, request RunRequest, emit func(events.Event)) (RunResult, error)
}
```

Adapters are built in and registered at startup (`internal/cli`). Gauge does not
load adapter code dynamically and has no plugin marketplace.

`Validate` returns a normal error for invalid configuration and an
`adapter.UnavailableError` when the configuration is fine but the agent
executable is missing. Gauge lists the adapter as `unavailable` in
`gauge agents list`, warns instead of failing in `gauge doctor`, and refuses to
start a run rather than recording a misleading task failure.

### `RunRequest`

| Field | Meaning |
|---|---|
| `RunID` | Unique attempt identifier. |
| `TaskID` | Task identifier, for logs and telemetry. |
| `Prompt` | The task prompt text — the only task instruction the agent may see. |
| `WorkDir` | Absolute path to the prepared candidate workspace. |
| `ArtifactsDir` | Where the adapter may write raw output (`stdout.log`, `stderr.log`, …). |
| `Timeout` | Per-attempt budget; `ctx` carries the same deadline. |
| `Config` | Validated adapter-specific JSON from Gauge's TOML config. |
| `Model` | Optional model override; applied only when the adapter supports it. |
| `Environment` | Controlled environment for the agent process (PATH, HOME, TMPDIR, `GAUGE_*`). |

The request deliberately contains no evaluator paths, hidden tests, expected
outputs, or scoring rules.

### Normalized events

Adapters emit a small vocabulary and omit signals they cannot observe:

| Type | Payload |
|---|---|
| `agent.message` | Assistant/agent text. |
| `agent.state` | State change, when exposed. |
| `tool.call` | Tool name, call ID, input (raw JSON). |
| `tool.result` | Output, failure flag. |
| `usage` | Input/output/total tokens and/or cost, only from a trustworthy source. |
| `warning` / `error` | Non-fatal problem / error surfaced by the agent. |
| `lifecycle` | Process lifecycle transitions. |

Every event is stamped with schema version, sequence number, UTC timestamp, and
source adapter. Unrecognized or unknown raw payloads can be preserved in `raw`.
Process-based adapters should write raw stdout/stderr to `ArtifactsDir`; Gauge
records normalized events in `events.jsonl` alongside them.

### `RunResult`

Execution facts, not correctness:

- completion reason: `completed`, `failed`, `timed_out`, or `cancelled`;
- exit code, signal, error text;
- start/end time and duration;
- agent name/version/commit when discoverable;
- actual model/provider when discoverable;
- usage only when reported by a trustworthy source;
- adapter-specific metadata as JSON.

`RunResult` has no `passed` or `success` field. Task correctness belongs to the
evaluator.

### Invariants

- Honor `ctx` cancellation and the deadline; kill the whole process tree where the
  platform allows.
- Never evaluate the task, read evaluator assets, or accept the agent's completion
  claim as success.
- Never fabricate tool calls, token counts, or cost.
- Keep agent-specific flags in adapter configuration, not task YAML.
- Do not import private agent internals; use supported public interfaces.

### Configuration and secrets

The TOML config exposes opaque per-adapter options:

```toml
[adapters.fake.options]
mode = "write"
```

Gauge marshals options to JSON, asks the adapter to validate them before any run,
and records the redacted configuration plus its fingerprint in the experiment.
Credential-like keys (`api_key`, `token`, `secret`, `password`, `credential`,
`authorization`, `bearer`, `private_key`) are redacted recursively; obvious bearer
tokens and prefixed keys are redacted even under innocuous key names. Token
*counts* such as `input_tokens`, `output_tokens`, and `total_tokens` are metrics,
not credentials, and are preserved for reproducibility.
Redaction applies before persistence, and the fingerprint is computed over the
redacted document.

## The fake adapter

`internal/adapters/fake` is a built-in deterministic adapter used by tests and the
example configuration. It has no model and no network:

```toml
[adapters.fake.options]
mode = "write"          # noop | write | fail | sleep
sleep = "10ms"          # optional, for timeout tests
exit_code = 0
agent_version = "fake-1.0.0"
model = "fake/deterministic"
provider = "fake"

[adapters.fake.options.files]
"answer.txt" = "42"

[adapters.fake.options.usage]
input_tokens = 420
output_tokens = 96
total_tokens = 516
```

It can also emit canned events (`events = [{ type = "agent.message", text = "…" }]`),
which lets tests prove that an agent's claim of success does not affect the
evaluator's verdict.

## Iris (implemented)

`internal/adapters/iris` drives the installed `iris` binary through its
documented one-shot JSONL interface. It was implemented against Iris commit
`d1af31d` (verified 2026-11-18) and depends only on that public CLI and the
stream schema described in Iris's own CLI documentation.

### Invocation

```text
iris jsonl --home <attempt>/iris-home --workdir <attempt>/workspace \
           [--model <m>] [--base-url <u>] [--debug]     # prompt arrives on stdin
```

- The prompt is written to stdin, so it never appears in the process list and is
  not subject to argument-size limits.
- `--home` is always a fresh directory inside the attempt directory. Iris
  persists settings, sessions, and logs under its home; a shared home would let
  attempts observe each other. The directory is removed as scratch after the
  attempt unless retention is `keep_all`.
- The process runs with the controlled environment from `RunRequest`, its
  working directory set to the workspace, and Gauge's process-group supervision.
  A timeout or cancellation kills the whole group.
- Raw JSONL is written to `stdout.log`; diagnostics go to `stderr.log`.

### Configuration

```toml
[adapters.iris]
enabled = true

[adapters.iris.options]
command = "iris"                               # executable name or path
model = "qwen3.6-35B-Q1"                       # optional; a run's --model wins
base_url = "http://127.0.0.1:8080/v1"          # optional; passed as --base-url
settings_file = "examples/iris-settings.toml"  # optional; see below
debug = false                                  # passes --debug
```

`settings_file` is copied to `<attempt>/iris-home/settings.toml` before the run.
It is how an attempt gets Iris security policy, model settings, and context
limits: Gauge never reads or shares the user's `~/.iris`, so nothing from that
directory applies. Iris gives its CLI flags precedence, so `model` and `base_url`
override the matching keys in the file. A relative path resolves against Gauge's
working directory (like `command`), not against the config file, so scripts
should use an absolute path. [examples/iris-settings.toml](../examples/iris-settings.toml)
is a starting point.

> **Model and security are not inherited from `~/.iris`.** A fresh Iris home has
> no `default_model`, and its shell policy defaults to `approval`, which fails
> closed in headless mode. Set `model` (or `--model`), and use `settings_file`
> when a task needs shell commands, an allowlist, or a larger context window.

### Event mapping

| Iris record | Gauge event |
|---|---|
| `agent.started` | lifecycle `agent_started` (emitted by the adapter once the process starts) |
| `assistant.delta` | buffered and emitted as one `agent.message` per turn |
| `agent.turn.started` / `agent.turn.finished` | `agent.state` (`turn_started`, `turn_finished`) |
| `agent.turn.retried` | `warning`; buffered assistant text is discarded |
| `tool.started` | `tool.call` (call ID, tool name, arguments) |
| `tool.completed` | `tool.result` (output, failure flag) |
| `tool.failed` | `tool.result` with `failed: true` |
| `usage` | `usage` (input = prompt tokens, output = completion tokens) |
| `context.compaction.started` / `.finished` | `agent.state` |
| `agent.error` | `error` |
| `agent.finished` | lifecycle `agent_settled`; its summary becomes adapter metadata |
| `result` | terminal result: session ID, usage, truncation |
| `error` | terminal failure |
| `reasoning.delta`, `tool.progress`, `llm.*`, `agent.queue.*`, `agent.instruction.*` | omitted; raw output remains in `stdout.log` |

Tool inputs and outputs are truncated in normalized events (4 KiB and 8 KiB); the
complete payloads stay in `stdout.log`. Unknown record types are counted in
metadata rather than fabricated into events.

### Outcome

A run is `completed` only when the process exits 0, a `result` record is
present, and no `error` record, `agent.error`, or reported cancellation is
present. A non-zero exit, a missing result, or a reported error is `failed`; a
deadline is `timed_out`; an interrupt through Gauge is `cancelled`. The adapter
never decides whether the task passed.

### Telemetry and artifacts

- `stdout.log` — the raw JSONL stream, unchanged.
- `stderr.log` — Iris diagnostics, including startup failures that produce no
  JSON records at all.
- `result.Metadata` — command, session ID, record counts, unrecognized event
  types, LLM request count, tool executions, and working-tree change count.
- Usage — Iris's run summary when available (it includes compaction and retries),
  otherwise the result record, otherwise the sum of per-call usage events.
  Missing usage stays unavailable.
- Model/provider — neither appears in the event stream. The adapter reads the
  session header from the isolated home and records `model` plus
  `model_metadata.provider` and `model_metadata.base_url`. A configured or
  requested `--model` is the fallback when no session header exists.
- Agent version — unavailable: Iris has no supported version flag. Gauge reports
  no version rather than guessing from the binary.

### Known limitations

- Iris writes a session file per run, but the isolated home is deleted as
  scratch, so session transcripts are not kept as attempt artifacts.
- Headless shell commands need an allowlist or approval hook; that is Iris
  policy, and Gauge does not patch around it.
- `iris rpc` is not used: it exits 0 on failed runs and treats stdin EOF as
  cancellation, so `jsonl` is the better one-shot interface.
- A run that fails before the model responds has no usage and usually no model
  information; those fields are reported as unavailable.

## Pi feasibility (next)

Observed from the installed `@earendil-works/pi-coding-agent` 1.5.2 package and
its documented JSON mode.

### Supported public interface

- One-shot JSON: `pi --mode json --no-session "<prompt>"`, with `--provider` and
  `--model`; prompt may also arrive on stdin. Bare `pi` starts the TUI.
- The stream is strict JSONL. The first record is a session header
  (`{"type":"session","version":3,"id":…,"timestamp":…,"cwd":…}`), followed by
  `agent_start`, `turn_start`, `message_start/update/end`,
  `tool_execution_start/update/end`, `turn_end`, `agent_end`, and the terminal
  `agent_settled`.
- `message_end` is the authoritative final message and carries:

```json
{
  "role": "assistant",
  "content": [{"type": "text", "text": "…"}],
  "provider": "fake",
  "model": "fake-model",
  "usage": {"input": 11, "output": 3, "totalTokens": 14, "cost": {…}},
  "stopReason": "stop",
  "responseId": "…",
  "durationMs": 16
}
```

- `-v/--version` prints the version (for example `1.5.2`).
- Cancellation: print/JSON modes handle SIGTERM and SIGHUP gracefully and flush
  output; SIGINT is not handled and exits 130. Gauge's process-group termination
  covers the timeout path.
- State is stored under an agent directory (`~/.pi/agent` by default), relocatable
  with `PI_CODING_AGENT_DIR`. `--no-session` avoids session persistence.

### Gaps and caveats

- No `--base-url` CLI flag. A custom endpoint is configured through
  `<agent-dir>/models.json` (`providers.<id>.baseUrl`), so Gauge must generate that
  file per attempt when a local/remote endpoint is used.
- The JSON-mode exit code is **not** a run-success signal: failed or aborted
  responses still exit 0. Success must come from `message_end.stopReason`
  (`stop` vs `error`/`aborted`) and `agent_settled`.
- Startup failures are stderr-only with no JSON records.
- All prompts are supplied at startup in JSON mode; steering or cost statistics
  need RPC (`--mode rpc`), which has no official Go client (the JSONL protocol is
  documented).
- Agent version is not in the stream; run `pi --version` once as a probe.
- Even with a fake provider, Pi may create `models-store.json`/`auth.json` in the
  agent directory. Isolate that directory per attempt.
- Sessions are written unless `--no-session` is passed.

### Recommended adapter strategy

Run `pi --mode json --no-session` once per attempt:

- set `PI_CODING_AGENT_DIR=<attempt>/pi-home`, generate `models.json` from adapter
  configuration when an endpoint is configured, and set `PI_OFFLINE=1`,
  `PI_SKIP_VERSION_CHECK=1`, `PI_TELEMETRY=0`;
- pass `--provider`/`--model`, run with cwd = workspace;
- parse `message_end` for final text, provider/model, usage/cost, and `stopReason`;
  stop at `agent_settled`;
- map `stopReason` to completion; do not trust the exit code alone;
- report `pi --version` as the agent version.

Telemetry available: token usage and computed cost (from the model cost table),
provider/model/response ID, per-tool duration and error flags, per-message duration,
session ID, retry/compaction events. Unavailable/reconstructable-only: run-level
duration, tool counters, git diff summary, reliable run-outcome exit codes, a
base-URL CLI override.

## Adapter testing

- `internal/adapters/iris` unit tests feed recorded JSONL streams through the
  translator: a successful run, a retried turn, partial output before a timeout,
  unknown and malformed records, and usage precedence. They need no agent and no
  network.
- Stub executables exercise the real subprocess path: argv, prompt on stdin,
  working directory, raw stdout capture, session-header discovery, settings-file
  copying, non-zero exit, missing executable, timeout, and cancellation. These
  tests skip on Windows, which has no POSIX shell.
- The CLI test suite runs the smoke suite end-to-end with an Iris stub, proving
  that the adapter satisfies the same runner/evaluator/storage lifecycle as
  `fake`.
- A live run against a local model endpoint is manual for now: point
  `[adapters.iris.options] settings_file` at an Iris settings file (for the model
  and shell policy) and run the smoke suite. The standard test suite never calls
  a model or the network.
- Pi will follow the same pattern once it is implemented: recorded streams plus a
  stub executable, with any live check kept opt-in.

Both adapters are expected to satisfy the existing contract without private
imports or source changes. Iris is implemented; Pi remains a documented plan and
does not appear in `gauge agents list` yet.
