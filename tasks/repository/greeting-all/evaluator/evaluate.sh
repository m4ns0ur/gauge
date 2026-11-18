#!/bin/sh
# Independent evaluator for the greeting-all task.
# Usage: evaluate.sh <workspace> <assets> <scratch>
#
# This script runs outside the agent process. It copies the candidate workspace
# into the scratch directory, adds the hidden test, and reports versioned JSON.
# Candidate-caused failures exit 0; non-zero exits mean the evaluator itself
# could not run.
set -u

workspace=${1:-}
assets=${2:-}
scratch=${3:-}

if [ -z "$workspace" ] || [ -z "$assets" ] || [ -z "$scratch" ]; then
  echo "evaluate.sh: workspace, assets, and scratch directory are required" >&2
  exit 2
fi
if ! command -v go >/dev/null 2>&1; then
  echo "evaluate.sh: go toolchain not found in PATH" >&2
  exit 4
fi
if [ ! -f "$assets/testdata/greeting_all_test.go" ]; then
  echo "evaluate.sh: hidden test missing from evaluator assets" >&2
  exit 5
fi

work="$scratch/candidate"
rm -rf "$work"
mkdir -p "$work"
if ! cp -R "$workspace/." "$work/" >/dev/null 2>&1; then
  echo "evaluate.sh: could not copy candidate workspace" >&2
  exit 6
fi
rm -rf "$work/.git"

json_check() {
  printf '{"id":"%s","status":"%s","message":"%s"}' "$1" "$2" "$3"
}

if [ ! -f "$work/go.mod" ]; then
  printf '%s' '{"schema_version":1,"status":"failed","summary":"The candidate does not contain a Go module.","checks":[{"id":"module-present","status":"failed","message":"go.mod is missing from the candidate workspace."}]}'
  exit 0
fi

cp "$assets/testdata/greeting_all_test.go" "$work/greeting_all_test.go" || exit 7

export GOPATH="$scratch/gopath"
export GOCACHE="$scratch/gocache"
export GOMODCACHE="$scratch/gopath/pkg/mod"
export GO111MODULE=on
export GOFLAGS=-mod=mod
export GOPROXY=off
export CGO_ENABLED=0
mkdir -p "$GOPATH" "$GOCACHE" "$scratch/logs"

build_log="$scratch/logs/build.log"
if ! ( cd "$work" && go build ./... ) >"$build_log" 2>&1; then
  cat "$build_log" >&2
  printf '%s' '{"schema_version":1,"status":"failed","summary":"The candidate does not build.","checks":[{"id":"build-passes","status":"failed","message":"go build ./... failed; details are on evaluator stderr."}]}'
  exit 0
fi

run_test() { # $1 regexp, $2 log file
  ( cd "$work" && go test -count=1 -run "$1" ./... ) >"$2" 2>&1
}

failed=0

greet_all_status=passed
greet_all_message="GreetAll returns one greeting per name, in order."
if ! run_test '^TestGreetAllOrder$' "$scratch/logs/greet-all.log"; then
  greet_all_status=failed
  greet_all_message="GreetAll does not return the expected greetings in order."
  failed=1
  cat "$scratch/logs/greet-all.log" >&2
fi

empty_status=passed
empty_message="GreetAll returns an empty, non-nil slice for no names."
if ! run_test '^TestGreetAllEmpty$' "$scratch/logs/greet-all-empty.log"; then
  empty_status=failed
  empty_message="GreetAll(nil) did not return an empty, non-nil slice."
  failed=1
  cat "$scratch/logs/greet-all-empty.log" >&2
fi

backward_status=passed
backward_message="Greet keeps its existing behavior."
if ! run_test '^TestGreetBackwardCompatible$' "$scratch/logs/backward.log"; then
  backward_status=failed
  backward_message="Existing Greet behavior changed."
  failed=1
  cat "$scratch/logs/backward.log" >&2
fi

checks="$(json_check build-passes passed 'The candidate builds.'),$(json_check greet-all-order "$greet_all_status" "$greet_all_message"),$(json_check greet-all-empty "$empty_status" "$empty_message"),$(json_check backward-compatibility "$backward_status" "$backward_message")"

if [ "$failed" -eq 0 ]; then
  printf '{"schema_version":1,"status":"passed","summary":"All required checks passed.","checks":[%s]}' "$checks"
else
  printf '{"schema_version":1,"status":"failed","summary":"One or more required checks failed.","checks":[%s]}' "$checks"
fi
