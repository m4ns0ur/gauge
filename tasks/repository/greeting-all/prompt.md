# Add batch greeting support

The `greeting` package formats one name at a time. The CLI that uses this module now needs to print several greetings from a single call, and formatting them in a loop at every call site duplicates logic.

Add a `GreetAll` function to the `greeting` package:

- It takes a slice of names and returns one greeting per name, in the same order.
- It uses the same greeting format as `Greet`.
- Given no names, it returns an empty, non-nil slice.
- `Greet` must keep its current behavior; existing callers must not need changes.
- The module must continue to build and pass its tests.

Work in the current repository, keep the change focused, and do not add dependencies.
