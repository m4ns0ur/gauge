# Fix a failing test suite without editing tests

The test suite fails. The tests are correct; the production code in `calc` is
not. Fix the production code so that `Average` returns the exact arithmetic
mean:

- `Average([]int{2, 3})` returns `2.5`, not a truncated value;
- negative values work (`Average([]int{-2, -3})` is `-2.5`);
- an empty slice returns `0`.

Do not edit the test files. Do not add dependencies.
