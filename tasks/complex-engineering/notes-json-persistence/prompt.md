# Persist notes to a JSON file

The notes CLI is in-memory today. Add optional JSON persistence:

- A global `--data <file>` flag before the command:
  `notes --data notes.json add "buy milk"` and
  `notes --data notes.json list`.
- With `--data`, load notes from the JSON file if it exists (a missing file
  means an empty store), apply the command, and persist the notes after a
  successful `add`.
- The file must be JSON. Its exact shape is yours, but a later invocation must
  see the notes added earlier, including their text and tags.
- Without `--data`, behavior is unchanged.
- An unreadable or invalid data file must print an error to stderr and exit with
  code 1; do not overwrite it.
- Do not change the visible tests or add dependencies.

The change spans the `cmd/notes` command and may add helpers to the `notes`
package.
