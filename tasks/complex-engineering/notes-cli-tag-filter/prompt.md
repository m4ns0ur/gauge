# Filter notes by tag end to end

Add tag filtering to the notes tool:

- `notes.Store.FilterByTag(tag)` returns the notes whose tags contain `tag`, in
  store order, matching tags case-insensitively. An empty tag matches nothing.
  The store must not be modified.
- The CLI supports `notes list --tag <tag>`, which lists only matching notes
  using the same `ID<TAB>text` format as `list`. A missing tag value must print
  the usage text to stderr and exit with code 2.
- `notes list` without arguments keeps its current behavior.
- Do not change the visible tests or add dependencies.

The change spans the `notes` package and the `cmd/notes` command.
