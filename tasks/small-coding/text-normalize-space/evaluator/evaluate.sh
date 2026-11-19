#!/bin/sh
# Independent evaluator for a bundled Go task.
# Usage: evaluate.sh <workspace> <assets> <scratch>
#
# The candidate workspace is copied into the scratch directory, hidden tests
# from the evaluator assets are added there, and each check in checks.txt runs a
# focused `go test`. The hidden tests never enter the agent workspace. The
# script always prints a versioned JSON result; candidate-caused failures exit
# zero, non-zero exits mean the evaluator itself could not run.
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
if [ ! -f "$assets/checks.txt" ]; then
  echo "evaluate.sh: checks.txt is missing from evaluator assets" >&2
  exit 5
fi
if ! find "$assets/testdata" -type f -name '*_test.go' 2>/dev/null | grep -q .; then
  echo "evaluate.sh: hidden tests are missing from evaluator assets" >&2
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
if ! cp -R "$assets/testdata/." "$work/" >/dev/null 2>&1; then
  echo "evaluate.sh: could not install hidden tests" >&2
  exit 7
fi

if [ ! -f "$work/go.mod" ]; then
  printf '%s' '{"schema_version":1,"status":"failed","summary":"The candidate does not contain a Go module.","checks":[{"id":"module-present","status":"failed","message":"go.mod is missing from the candidate workspace."}]}'
  exit 0
fi

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

checks=""
failed=0
# checks.txt lines: id|test regexp|passed message|failed message
while IFS='|' read -r id pattern passed_message failed_message; do
  case "$id" in
    ""|\#*) continue ;;
  esac
  log="$scratch/logs/$id.log"
  if ( cd "$work" && go test -count=1 -v -run "$pattern" ./... ) >"$log" 2>&1; then
    if ! grep -q -- "=== RUN" "$log"; then
      echo "evaluate.sh: hidden test for check $id did not run" >&2
      exit 5
    fi
    status=passed
    message=$passed_message
  else
    status=failed
    message=$failed_message
    failed=1
    cat "$log" >&2
  fi
  if [ -n "$checks" ]; then
    checks="$checks,"
  fi
  checks="$checks{\"id\":\"$id\",\"status\":\"$status\",\"message\":\"$message\"}"
done < "$assets/checks.txt"

if [ "$failed" -eq 0 ]; then
  printf '{"schema_version":1,"status":"passed","summary":"All required checks passed.","checks":[%s]}' "$checks"
else
  printf '{"schema_version":1,"status":"failed","summary":"One or more required checks failed.","checks":[%s]}' "$checks"
fi
