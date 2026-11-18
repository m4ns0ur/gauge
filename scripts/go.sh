#!/bin/sh
# Run the Go toolchain with a workspace-local module/build cache.
#
# This keeps the module cache inside the checkout, which is required in
# sandboxed environments where $GOPATH/pkg/mod is read-only. It is a
# convenience wrapper for development, not something users need.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
export GOPATH="$root/.gopath"
export GOMODCACHE="$root/.gopath/pkg/mod"
export GOCACHE="$root/.gocache"
export GOSUMDB=off
export GOFLAGS=-mod=mod
exec go "$@"
