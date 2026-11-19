# ADR 0002: SQLite for experiment metadata

- **Status:** accepted
- **Date:** 2026-11-19
- **Supersedes:** the file-store decision in [ADR 0001](0001-vertical-slice.md)

## Context

The vertical slice stored experiment and attempt metadata as JSON files under
`<data_dir>/runs/`. That was enough to prove the lifecycle, but it had clear
limits: listing experiments walked directories, child records (evaluator checks,
artifacts, usage) were embedded in one JSON document, no index or query could
answer "show failed attempts for this agent", and there was no schema migration
mechanism. The implementation plan calls for SQLite from the beginning and
explicit migrations.

## Decision

- Use SQLite through `modernc.org/sqlite`, the pure-Go driver, so no CGO
  toolchain is required on supported platforms.
- Keep the database at `<data_dir>/gauge.db`. Large artifacts (events, logs,
  diffs, evaluator output) remain files under
  `<data_dir>/runs/<experiment>/<attempt>/`; the database stores their paths,
  sizes, and SHA-256 hashes.
- Normalize the structured data into `experiments`, `attempts`, `checks`,
  `artifacts`, and `metrics` tables with foreign keys and indexes. Nested
  configuration (selection, suite, environment, adapter config, diff, setup) is
  stored as JSON columns because it is read as a unit and never queried by field.
- Store typed metric rows (`duration_ms`, `usage.*`, `evaluation.duration_ms`)
  with unit and provenance (`gauge-observed`, `adapter-reported`). A metric row
  exists only when a source reported it; unavailable values are absent.
- Version the schema with a `schema_migrations` table and apply migrations in a
  transaction when the store is opened. A database written by a newer Gauge is
  rejected rather than misread.
- Limit the pool to one connection and enable `foreign_keys`, WAL, and a busy
  timeout so per-connection pragmas hold and the local sequential runner never
  contends with itself.
- Store timestamps as fixed-width UTC text so lexical ordering in SQLite matches
  chronological ordering.
- Import metadata written by the previous file-based store once, when the
  database has no experiments. The import is best-effort: unreadable records are
  skipped and surfaced as warnings through `gauge doctor`. Legacy JSON files are
  left in place; artifact files were always unchanged.
- Keep the `Store` API (`CreateExperiment`, `SaveExperiment`, `GetExperiment`,
  `ListExperiments`, `CreateAttemptDir`, `SaveAttempt`, `GetAttempt`,
  `ListAttempts`, `FindAttempt`) so the runner, CLI, and reports are unaffected.

## Consequences

- Existing results are preserved through the one-time import; new results are
  queryable and inspectable without walking directories.
- Gauge gains a pure-Go SQLite dependency (and its transitive modules). The
  binary grows; CGO stays unnecessary.
- The database is a local file with SQLite locking. Gauge remains a
  single-process, sequential runner; distributed or concurrent writers are still
  out of scope.
- Artifacts remain ordinary files, so logs and diffs can be inspected, copied,
  and hashed without database access. Deleting `gauge.db` loses the index but not
  the artifact files.
