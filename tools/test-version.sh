#!/usr/bin/env bash
# Every shape the version string can take, pinned.
#
# The string reaches the About page, the `dcode status` output and the UI cache directory name, and it
# was wrong in two ways at once: it counted the whole history ("+git40" for a build 12 commits after
# v2.1.2) and appended the suffix unconditionally, so a release built at its own tag was never plain
# "2.1.2". Both are the kind of mistake that only shows up months later in a bug report ("which build
# are you on?"), so they are tested rather than eyeballed.
#
# The four shapes are exercised by feeding scripts/version.sh a describe string; the no-tag case needs
# a real repository, since that is git's answer and not ours.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$ROOT/scripts/version.sh"

fails=0
check() {
	local describe=$1 want=$2 got
	got=$(DCODE_DESCRIBE="$describe" "$VERSION")
	if [ "$got" != "$want" ]; then
		printf 'FAIL  describe=%-30s got %-28s want %s\n' "${describe:-<none>}" "$got" "$want"
		fails=$((fails + 1))
	else
		printf 'ok    %-30s → %s\n' "${describe:-<none>}" "$got"
	fi
}

# At the tag: the tag IS the version. This is the case that made a release carry "+git28.hash".
check "v2.1.2-0-gac0ad2af" "2.1.2"
# Past it: the distance travelled, not the size of the repository.
check "v2.1.2-12-ga20cef2" "2.1.2+git12.a20cef2"
# Uncommitted changes are a different build and have to say so.
check "v2.1.2-12-ga20cef2-dirty" "2.1.2+git12.a20cef2-dirty"
check "v2.1.2-0-gac0ad2af-dirty" "2.1.2-dirty"
# A tag that carries its own suffix must not have it read as the commit count.
check "v2.1.2-rc1-3-g12345678" "2.1.2-rc1+git3.12345678"
# Anything at all (a deliberate VERSION=... still wins in the Makefile, this is the script's own floor).
check "v3.0.0-0-g00000000" "3.0.0"

# No tag reachable: still identify the commit, so two builds are not both "0.0.0".
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(
	cd "$tmp"
	git init -q .
	git config user.email t@example.com
	git config user.name t
	: > file
	git add file
	git commit -qm one
	got=$("$VERSION")
	hash=$(git rev-parse --short=8 HEAD)
	if [ "$got" != "0.0.0+g$hash" ]; then
		printf 'FAIL  no tag                       got %-28s want %s\n' "$got" "0.0.0+g$hash"
		exit 1
	fi
	printf 'ok    %-30s → %s\n' "no tag (real repo)" "$got"
) || fails=$((fails + 1))

if [ "$fails" -gt 0 ]; then
	echo "$fails version check(s) failed"
	exit 1
fi
