# Maintainer notes

Hidden test checks order, case sensitivity, and non-nil empty output.

The generic evaluator copies the candidate into scratch, installs the hidden
tests from `testdata/`, and runs each check from `checks.txt` with a focused
`go test`. Hidden tests never enter the agent workspace, and candidate failures
exit zero with a versioned JSON result.
