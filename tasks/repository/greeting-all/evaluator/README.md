# Maintainer notes

This evaluator copies the candidate workspace into the scratch directory, adds
`testdata/greeting_all_test.go` as `greeting_all_test.go`, and runs three independent checks:

1. `build-passes` — `go build ./...` succeeds.
2. `greet-all-order` — `GreetAll` preserves order and format.
3. `greet-all-empty` — `GreetAll(nil)` returns an empty, non-nil slice.
4. `backward-compatibility` — `Greet` behaves as before.

The tests are never placed in the candidate workspace. The evaluator always
prints a versioned JSON result and exits zero for candidate-caused failures;
non-zero exits are reserved for evaluator infrastructure problems.
