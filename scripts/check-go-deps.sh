#!/usr/bin/env bash
# Keeps go.mod, go.sum and gomod2nix.toml in sync. Runs from the pre-commit
# hook; exits non-zero when it had to rewrite something so the commit stops
# and the regenerated files can be staged.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

before="$(mktemp -d)"
trap 'rm -rf "$before"' EXIT

cp go.mod go.sum gomod2nix.toml "$before"/

go mod tidy
gomod2nix generate

status=0
for f in go.mod go.sum gomod2nix.toml; do
	if ! cmp -s "$f" "$before/$f"; then
		printf '%s was out of date and has been regenerated; stage it and commit again\n' "$f" >&2
		status=1
	fi
done

exit "$status"
