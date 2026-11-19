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
  types, LLM request count, tool executions, working-tree change count, and the
  SHA-256 of the copied `settings_file` so a later settings change is visible in
  the experiment record.
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

## Pi (implemented)

`internal/adapters/pi` drives the installed `pi` binary through its documented
one-shot JSON mode. It was implemented against Pi 1.5.2 (verified 2026-11-19) and
Pi's published JSON event-stream documentation, with no private package imports.

### Invocation

```text
pi --mode json --no-session [--provider <p>] [--model <m>] \
   [--thinking <level>] [--tools <list>]        # prompt arrives on stdin
```

- The prompt is written to stdin, so it never appears in the process list and is
  not subject to argument-size limits. Pi reserves stdout for its JSONL stream;
  diagnostics go to stderr.
- `PI_CODING_AGENT_DIR` is always a fresh directory inside the attempt directory.
  Pi stores model catalogs, credentials, and trust state there, so a shared
  directory would let attempts observe each other.
- `PI_OFFLINE=1`, `PI_SKIP_VERSION_CHECK=1`, and `PI_TELEMETRY=0` are pinned for
  the agent process; the agent directory is removed as scratch after the attempt
  unless retention is `keep_all`.
- The agent version is probed with `pi --version` (a supported public flag) using
  the same isolated environment. A failed probe records no version.
- The process runs with the controlled attempt environment, its working
  directory set to the workspace, and Gauge's process-group supervision.

### Configuration

```toml
[adapters.pi]
enabled = true

[adapters.pi.options]
command = "pi"                             # executable name or path
provider = "gauge-local"                   # --provider (requires a model)
model = "qwen3.6-35B-Q1"                   # --model; a run's --model wins
thinking = "off"                           # off|minimal|low|medium|high|xhigh|max
tools = ["read", "bash", "edit", "write"]  # --tools allowlist
models_file = "examples/pi-models.json"    # copied to <pi-home>/models.json
auth_file = "/path/to/auth.json"           # optional; copied to <pi-home>/auth.json
forward_env = ["OPENAI_API_KEY"]           # names only; values are never persisted
```

Pi has no `--base-url` flag, so a custom OpenAI-compatible endpoint is configured
in `models.json` (`providers.<id>.baseUrl`, `api`, `apiKey`, `models`).
`models_file` is copied per attempt, so Gauge never reads or shares `~/.pi`;
[examples/pi-models.json](../examples/pi-models.json) is a starting point.
`auth_file` supports provider credentials stored in Pi's `auth.json`.
`forward_env` forwards named environment variables from Gauge's process into the
agent process, which is the way to pass API keys without writing them to disk;
only the names appear in the recorded configuration.

Adapter option paths, including `models_file` and `auth_file`, resolve against
Gauge's working directory rather than the config file; use absolute paths in
scripts.

### Event mapping

| Pi record | Gauge event |
|---|---|
| `session` | session ID and format version, recorded as metadata |
| `agent_start` / `agent_end` | omitted; raw output remains in `stdout.log` |
| `turn_start` / `turn_end` | `agent.state` (`turn_started`, `turn_finished`) |
| `message_end` (assistant) | `agent.message` for text blocks; `usage`; provider/model/stop-reason tracking |
| `message_end` (system/user/tool result) | omitted |
| `message_start` / `message_update` | omitted (`message_end` is authoritative) |
| `tool_execution_start` | `tool.call` (call ID, tool name, arguments) |
| `tool_execution_end` | `tool.result` (text output, failure flag from `isError`) |
| `tool_execution_update` | omitted; raw output remains in `stdout.log` |
| `auto_retry_start` | `warning` with attempt/max and error text |
| `auto_retry_end` | unsuccessful final error captured for the run failure |
| `compaction_start` / `compaction_end` | `agent.state` |
| `agent_settled` | lifecycle `agent_settled`; settlement flag for the outcome |
| `extension_error` | `warning` |
| `queue_update`, `entry_appended`, `session_info_changed`, `thinking_level_changed`, `summarization_retry_*` | omitted; raw output remains in `stdout.log` |

Tool inputs and outputs are truncated in normalized events (4 KiB and 8 KiB); the
complete payloads stay in `stdout.log`. Unknown record types are counted in
metadata rather than fabricated into events.

### Outcome

Pi exits **0** even when the assistant response failed, so the adapter derives the
run outcome from the stream:

- `completed` — `agent_settled` was observed with `aborted: false`, and the final
  assistant message's `stopReason` is `stop`. A `length` stop is also completed,
  and is recorded as truncated with a warning.
- `failed` — an `error`, `aborted`, `deferred`, `toolUse`, or `pending` stop
  reason, an aborted settlement, a missing `agent_settled`, a non-zero exit, or
  no final assistant message.
- `timed_out` / `cancelled` — from the attempt context.

The evaluator still decides whether the task passed.

### Telemetry and artifacts

- `stdout.log` — the raw JSONL stream, unchanged.
- `stderr.log` — Pi diagnostics. Startup failures such as missing credentials
  appear only here, and the first line is included in the recorded failure.
- Usage — summed from assistant `message_end` records: input, output, and Pi's
  `totalTokens`. Pi's total includes cache reads and writes, so it can exceed
  input + output. Cost is recorded only when Pi computes a non-zero estimate;
  zero can mean the model has no pricing configured, so it is treated as
  unavailable.
- Model/provider — taken from the final assistant message, preferring
  `responseModel` (the concrete model reported by the provider).
- Agent version — `pi --version`.
- `result.Metadata` — session ID/version, stop reason, settlement, retries,
  compactions, tool counters, response ID, record counts, unrecognized event
  types, raw-output errors, the SHA-256 of the copied `models_file`, and whether
  an `auth_file` was installed.

### Known limitations

- Pi retries failed requests automatically (three attempts with backoff by
  default). Those retries are recorded as warnings, but they can consume much of a
  short attempt timeout when an endpoint is down.
- Deferred provider responses cannot be resumed by Gauge; they are reported as a
  failed run.
- `--no-session` is always passed, so sessions are ephemeral. The session file is
  not kept as an attempt artifact.
- Project-local Pi resources (`AGENTS.md`, extensions, skills, prompt templates)
  follow Pi's own discovery and trust behavior. Global `~/.pi` resources never
  apply because the agent directory is isolated.
- Headless Pi uses its normal tool approval behavior. As with every local adapter,
  this is not a security sandbox.
## Adapter testing

- `internal/adapters/iris` and `internal/adapters/pi` unit tests feed recorded
  JSONL streams through their translators: successful runs, retried turns,
  partial output, error and aborted terminals, truncation, unknown and malformed
  records, usage precedence, and raw-copy failures. They need no agent and no
  network.
- Stub executables exercise the real subprocess path: argv, prompt on stdin,
  working directory, raw stdout capture, configuration-file copying, version
  probing (Pi), environment forwarding (Pi), non-zero exit and missing
  executables, timeout, and cancellation. These tests skip on Windows, which has
  no POSIX shell.
- A Pi-specific regression test pins the important quirk that a failed assistant
  response produces exit code 0: the adapter must still record `failed`.
- The CLI test suite runs the smoke suite end-to-end with an Iris stub and a Pi
  stub, proving that both adapters satisfy the same runner/evaluator/storage
  lifecycle as `fake`.
- The opt-in live integration test in `internal/cli/live_test.go` runs both real
  binaries against an OpenAI-compatible endpoint and asserts a completed,
  independently passed task with recorded usage:

  ```bash
  GAUGE_LIVE=1 GAUGE_LIVE_MODEL=qwen3.6-35B-Q1 \
    go test ./internal/cli -run TestLiveAdapters -v -timeout 30m
  ```

  It generates isolated Iris/Pi configuration, so `~/.iris` and `~/.pi` are never
  read. Optional variables: `GAUGE_LIVE_ENDPOINT`, `GAUGE_LIVE_AGENTS`,
  `GAUGE_LIVE_TIMEOUT`. The standard suite skips the test and never calls a model
  or the network. It passed for both adapters against a local
  `qwen3.6-35B-Q1` endpoint (Iris 9.8s, Pi 5.8s).

Both adapters are built into Gauge, are configurable only through adapter
options, and use public interfaces without private imports or source changes.
