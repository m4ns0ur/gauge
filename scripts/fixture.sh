#!/bin/sh
# Rebuild a bare fixture repository from a readable source tree.
#
#   ./scripts/fixture.sh greeting-go
#
# Fixtures are stored as bare repositories so the pinned commit can be
# materialized without checking out a shared worktree. Commit metadata is fixed
# so regenerating an unchanged fixture produces the same revision.
set -eu
name=${1:?usage: scripts/fixture.sh <fixture-name>}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
src="$root/fixtures/src/$name"
dest="$root/fixtures/$name.git"

if [ ! -d "$src" ]; then
  echo "fixture source not found: $src" >&2
  exit 1
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/gauge-fixture.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/work"
( cd "$src" && tar -cf - --exclude .git . ) | ( cd "$tmp/work" && tar -xf - )
( cd "$tmp/work" && git init --quiet --initial-branch=main ) >/dev/null
git -C "$tmp/work" add -A

GIT_AUTHOR_NAME="Gauge Fixture" \
GIT_AUTHOR_EMAIL="fixture@example.invalid" \
GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" \
GIT_COMMITTER_NAME="Gauge Fixture" \
GIT_COMMITTER_EMAIL="fixture@example.invalid" \
GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
git -C "$tmp/work" -c commit.gpgsign=false commit --quiet -m "Fixture: $name"

rm -rf "$dest"
git clone --quiet --bare "$tmp/work" "$dest"
git -C "$dest" symbolic-ref HEAD refs/heads/main
# A fixture is read-only content, not a working clone: drop the temporary
# source remote and generated hooks so the checked-in repository is portable.
git -C "$dest" remote remove origin 2>/dev/null || true
rm -rf "$dest/hooks" "$dest/description" "$dest/info"
git -C "$dest" config core.ignorecase false
git -C "$dest" config core.filemode true
echo "fixture: $dest"
echo "revision: $(git -C "$dest" rev-parse HEAD)"
