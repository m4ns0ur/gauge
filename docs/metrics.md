# Metrics and result interpretation

Gauge does not collapse an experiment into one leaderboard number. It preserves
per-attempt facts and reports aggregates with explicit denominators and explicit
availability.

## Two independent facts per attempt

| Fact | Values | Meaning |
|---|---|---|
| **Execution** | `completed`, `failed`, `timed_out`, `cancelled` | How the agent process ended. Says nothing about task correctness. |
| **Task outcome** | `passed`, `failed`, `error`, `not_evaluated` | What the independent evaluator concluded. |

A timed-out agent may still pass: it may have finished enough work before the
deadline. An evaluator crash is `error`, never a failed task. A setup or workspace
failure is `not_evaluated`, never a weak-agent signal.

## Aggregate summary

`gauge run` and `gauge runs show` compute:

| Field | Definition |
|---|---|
| `attempts` | Total attempts recorded. |
| `evaluated` | Attempts with a valid evaluator verdict: `passed + failed`. This is the pass-rate denominator. |
| `passed` / `failed` | Task outcomes from the evaluator. |
| `pass_rate` | `passed / evaluated`, or unavailable when nothing was evaluated. |
| `evaluator_errors` | Attempts where evaluation infrastructure failed (invalid JSON, non-zero exit, timeout, crash) or the evaluator reported `error`. |
| `not_evaluated` | Attempts with no evaluator verdict (workspace or setup failure). |
| `timeouts` | Attempts whose agent or setup timed out, regardless of the task outcome. |
| `cancelled` | Attempts cancelled by the user or runner. |
| `execution_failures` | Agent processes that failed, excluding setup failures. |
| `setup_failures` | Attempts whose setup commands failed. |
| `duration` | Count, median, min, max of attempt wall-clock time (workspace + setup + agent + evaluation). |
| `usage` | Sum of values adapters actually reported, with coverage. |

Every report shows the fraction, not just a percentage:

```text
Attempts: 3   Passed: 2/3 (66.7%)   Evaluator errors: 0   Not evaluated: 0
```

Per-task and per-category rows preserve the same distinction: attempts, evaluated,
passed, failed, evaluator errors, not evaluated, pass rate, and median duration. A
per-task regression remains visible even if the aggregate improves.

## Missing data is unavailable, never zero

Adapters may be unable to observe token counts, cost, tool calls, or model
identity. Gauge represents that as absent:

- token and cost fields are optional pointers, omitted from JSON when unreported;
- reports print `unavailable`;
- usage sums have a coverage count, e.g. `input 420 [reported for 1/2 attempts]`;
- aggregate usage is `nil` rather than zero when no attempt reported any value.

Cost is only shown when the adapter reports it, and it is labeled
`adapter-reported`. Provider pricing assumptions are not applied by Gauge. Do not
compare cost values from incompatible sources as if they were equivalent.

## Repetitions

`--repetitions N` produces N independent attempts per task with fresh workspaces.
Reports show each attempt plus median and range for duration and usage. Gauge does
not claim statistical significance from a handful of repetitions; start with one
for development, three for routine comparisons, and five to ten when time and cost
allow.

Retries are never silent. A new attempt is explicit and visible; an interrupted or
timed-out task is not replayed automatically.

## Reliability and behavior diagnostics

Reliability counters (timeouts, cancellations, execution failures, setup failures)
are reported alongside task outcomes so a faster or cheaper configuration is not
mistaken for a better one.

The store also persists typed metric rows (`duration_ms`, `usage.*`,
`evaluation.duration_ms`) with a unit and a provenance (`gauge-observed` or
`adapter-reported`). A row exists only for a value a source actually reported,
so SQL queries over metrics inherit the same availability rule as the reports.

The event vocabulary records tool calls, tool results, and warnings when an adapter
exposes them; the Iris and Pi adapters do, and `fake` emits canned events.
Behavioral aggregation (repeated failed actions, whether tests were run, whether
the repository was inspected) is diagnostic and not part of the task score. It is
planned on top of the stored event streams; Gauge does not fabricate those
signals for agents that do not expose them.

## Comparing experiments

`gauge compare` uses the persisted identity to decide whether two experiments may
be compared:

- task IDs, per-task content fingerprints (spec + prompt + evaluator assets +
  fixture revision) must match; otherwise the comparison is refused with the
  mismatched tasks listed;
- adapter configuration fingerprint (over the redacted config), observed agent
  version, model/provider, suite label, repetitions, Gauge version, backend, and
  platform are displayed when they differ;
- per-task and per-category pass-rate deltas are reported alongside the aggregate,
  so a regression in one task remains visible when the total improves;
- evaluator errors, unevaluated attempts, timeouts, duration, token usage, and
  tool-call counts stay separate and carry their coverage; unavailable data is
  reported as unavailable.

`--format markdown` and `--format json` produce the same document for reports and
automation.

## Holdout suites

Keep a small holdout suite out of the tuning loop. Use the public suites while
developing or configuring an agent, then run the holdout suite and compare its
results separately for a less biased view.
Gauge records the exact suite and task fingerprints, and `gauge compare` refuses
mismatched selections, so a holdout comparison is explicit in the recorded
metadata rather than an implicit convention.

## Current boundaries

- No statistical significance testing.
- No weighted cross-task score; `score` from an evaluator is recorded but not
  aggregated.
- No peak memory, CPU/GPU utilization, or throughput collection.
- Tool-call aggregation is limited to counts and failures from normalized events;
  behavioral judgments such as "tests were run" are not inferred.
