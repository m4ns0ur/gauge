# Implement note deletion

`notes.Store.Delete` is not implemented. Implement it:

- Deleting an existing note removes it; later `List` and `Find` must not return
  it.
- Deleting an unknown ID returns an error and leaves the store unchanged.
- Removing one note must preserve the order of the remaining notes.
- Do not change the visible tests or add dependencies.
