#!/usr/bin/env bash
# Probes a running mnemo instance: the login page, the authenticated API, and a
# CardDAV PROPFIND. Redirects are followed, so an http:// base that upgrades to
# https reports the final result.
#
# Usage: scripts/check-server.sh [base-url]
# Credentials come from MNEMO_USER and MNEMO_PASS. The fallback matches the old
# fixed bootstrap; new installs log a generated password on first boot.
set -uo pipefail

base="${1:-https://mnemo.electrolit.biz}"
base="${base%/}"
user="${MNEMO_USER:-admin}"
pass="${MNEMO_PASS:-password}"

body="$(mktemp)"
trap 'rm -f "$body"' EXIT
fail=0

status() { # status URL
	local code
	code=$(curl -sS -o /dev/null --max-time 15 -w '%{http_code}' "$@") || code=000
	printf '%s' "$code"
}

check() { # check LABEL URL [curl args...]
	local label="$1" url="$2"
	shift 2
	local code
	code=$(curl -sSL -o "$body" --max-time 15 -w '%{http_code}' "$@" "$url") || code=000
	printf '%-34s %s\n' "$label" "$code"
	case "$code" in
	2*) ;;
	*)
		fail=$((fail + 1))
		printf '  %s\n' "$(tr '\n' ' ' <"$body" | cut -c1-200)"
		;;
	esac
}

printf 'base: %s\n' "$base"
printf 'root: %s\n' "$(status "$base/")"
loc=$(curl -sS -o /dev/null --max-time 15 -w '%{redirect_url}' "$base/" || true)
[ -n "$loc" ] && printf 'root redirect: %s\n' "$loc"

# An http base that upgrades to https needs its own probe target. curl drops
# credentials on a scheme change, so following the redirect per request would
# report 401s for a server that works fine over https.
target="$base"
origin() { printf '%s' "$1" | sed -E 's#^(https?://[^/]+).*#\1#'; }
if [ -n "$loc" ] && [ "$(origin "$loc")" != "$(origin "$base")" ]; then
	target="$(origin "$loc")"
	printf 'checks against: %s\n' "$target"
fi
printf '\n'

check 'GET /login' "$target/login"
if grep -qi 'sign in' "$body"; then
	printf '%s\n' '  login page rendered'
else
	printf '%s\n' '  login page did not contain "Sign in"'
	fail=$((fail + 1))
fi

check 'GET /static/app.js' "$target/static/app.js"

check 'GET /api/v1/me' "$target/api/v1/me" -u "$user:$pass"
printf '  %s\n' "$(tr '\n' ' ' <"$body" | cut -c1-200)"

check 'GET /api/v1/books' "$target/api/v1/books" -u "$user:$pass"
check 'GET /api/v1/contacts' "$target/api/v1/contacts" -u "$user:$pass"

check 'PROPFIND /carddav/' "$target/carddav/" -u "$user:$pass" \
	-X PROPFIND -H 'Depth: 0' -H 'Content-Type: application/xml' \
	--data '<?xml version="1.0"?><propfind xmlns="DAV:"><prop><displayname/></prop></propfind>'

code=$(status "$target/api/v1/me")
printf '%-34s %s\n' 'GET /api/v1/me (no auth)' "$code"
if [ "$code" != 401 ]; then
	printf '%s\n' '  expected 401'
	fail=$((fail + 1))
fi

printf '\n'
if [ "$fail" -eq 0 ]; then
	printf '%s\n' 'all checks passed'
else
	printf '%s\n' "$fail check(s) failed"
fi
[ "$fail" -eq 0 ]
