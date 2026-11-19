# Search notes end to end

Add note search:

- `notes.Store.Search(term)` returns the notes whose text contains `term`,
  case-insensitively, in store order. An empty term returns no notes. The store
  must not be modified.
- The CLI supports `notes search <term>` and prints matches in the same
  `ID<TAB>text` format as `list`.
- A missing term must print the usage text to stderr and exit with code 2.
- Existing `add` and `list` behavior must not change.
- Do not change the visible tests or add dependencies.

The change spans the `notes` package and the `cmd/notes` command.
