# Implement stable de-duplication

`text.Dedupe` is not implemented. Implement it so that it returns the unique
values from `values` in first-occurrence order.

Requirements:

- Comparison is exact (case-sensitive).
- The returned slice must be non-nil and empty for empty or nil input.
- The input slice must not be modified.
- Do not change the visible tests or add dependencies.
