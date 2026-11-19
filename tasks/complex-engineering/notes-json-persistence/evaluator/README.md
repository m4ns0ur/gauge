# Maintainer notes

Hidden tests drive the in-process CLI twice with the same data file and cover invalid input.

The generic evaluator copies the candidate into scratch, installs the hidden
tests from `testdata/`, and runs each check from `checks.txt` with a focused
`go test`. Hidden tests never enter the agent workspace, and candidate failures
exit zero with a versioned JSON result.
