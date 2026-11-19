# Handle unknown commands and help

The `notes` command currently returns success for unknown commands and has no
help output. Fix the CLI in `cmd/notes`:

- An unknown command must print the usage text to stderr and exit with code 2;
  nothing may be written to stdout.
- `--help` or `-h` as the first argument must print the usage text to stdout
  and exit with code 0; nothing may be written to stderr.
- Existing `add` and `list` behavior must not change.
- Do not change the visible tests or add dependencies.
