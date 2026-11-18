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

## Iris feasibility

Observed on this machine (2026-11-18) from the local Iris checkout at commit
`d1af31d` and the installed binary. Re-verify before implementing, as the project
is under active development.

### Supported public interface

- One-shot JSONL: `iris jsonl "<prompt>"` (prompt may come from stdin).
  `iris print` returns only the final text; `iris rpc` serves JSON-RPC 2.0 over
  stdin/stdout.
- Global flags: `-w/--workdir` (must exist; defaults to cwd), `-m/--model`,
  `-b/--base-url`, `--home` (agent state directory), `-d/--debug`.
- Events are NDJSON lines of the form `{"type":"<event>","event":{…}}`, e.g.:

```json
{"type":"assistant.delta","event":{"at":"…","text":"Hello"}}
{"type":"usage","event":{"at":"…","prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}
{"type":"agent.finished","event":{"at":"…","summary":{"duration_ms":14,"llm_requests":1,"tool_executions":0,"usage":{…}}}}
{"type":"result","event":{"content":"…","session_id":"…","usage":{…},"summary":{…}}}
```

- Event names include `agent.started`, `agent.turn.started`,
  `llm.request.started`, `llm.response.started`, `assistant.delta`,
  `reasoning.delta`, `tool.started`, `tool.progress`, `tool.completed`,
  `tool.failed`, `usage`, `agent.turn.finished`, `agent.turn.retried`,
  `context.compaction.started/finished`, `agent.error`, and `agent.finished`.
  A successful run ends with `agent.finished` then a final `result`. On failure an
  additional `{"type":"error", …}` follows and the process exits non-zero.
- Tool telemetry: `tool.started` carries `tool_call_id`, `tool_name`, and `args`;
  `tool.completed` carries the result and duration.
- Cancellation: Iris installs a SIGINT/SIGTERM context; partial events are flushed
  and the run exits non-zero.

### Gaps and caveats

- No `--version` flag or `version` subcommand. Version is only discoverable via
  `go version -m <binary>` (`mod github.com/m4ns0ur/iris <pseudo-version>`).
- The model/provider is not in the event stream; it is recorded in the session file
  header under `--home`.
- No per-run timeout flag; Gauge must enforce it.
- State always lands under the Iris home directory (`~/.iris` by default):
  settings, sessions, logs. A fresh `--home` per attempt is required for isolation;
  session persistence cannot be disabled.
- `iris rpc` exits 0 even when the run failed, and stdin EOF cancels the active
  run. `iris jsonl` is the better one-shot interface.
- Headless shell commands fail closed unless configured; that is Iris behavior, not
  something Gauge should patch around.

### Recommended adapter strategy

Run `iris jsonl` once per attempt as a subprocess:

- `--home <attempt>/iris-home`, `--workdir <attempt>/workspace`,
  `--model` / `--base-url` when configured;
- stream stdout line by line, map typed events to Gauge events;
- treat success as: exit 0, a `result` record present, and no `error` record;
- capture raw stdout to `stdout.log`;
- read model/provider (if needed) from the session header under the isolated home;
- report agent version as unknown unless a supported discovery mechanism exists.

Telemetry available: token usage per turn and aggregate, LLM request count, tool
execution count, durations, per-tool args/results/duration, compaction events,
session ID. Unavailable: agent version, in-stream model/provider, cost, reliable
exit-code success for RPC.

## Pi feasibility

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

## Adapter testing strategy (next phase)

- Unit-test event translation against recorded sample streams (no live model).
- Use stub executables (or a fake HTTP server) to test invocation, flags, working
  directory, and process termination.
- Keep the standard test suite independent of installed agents and inference.
- Add an opt-in integration test gated by explicit environment configuration for a
  local model endpoint.
- Document any upstream gap as a separate optional request; do not patch Iris or Pi
  inside Gauge.

Both adapters are expected to satisfy the existing contract without private imports
or source changes. Neither is implemented in this build; `gauge agents list` shows
only `fake` until the next phase.
