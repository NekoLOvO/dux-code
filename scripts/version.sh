#!/bin/sh
# The version string, in one place: `make version` prints this, `make build` bakes it into the binary
# through ldflags, and scripts/build-release.sh asks it instead of computing a second copy.
#
# Shape:
#
#	HEAD is the release tag, tree clean     2.1.2
#	12 commits past it                     2.1.2+git12.a20cef2
#	... with uncommitted changes           2.1.2+git12.a20cef2-dirty
#	the tag itself, with changes           2.1.2-dirty
#	no release tag reachable               0.0.0+ga20cef2
#
# Two things this fixes, both of which came from counting commits with `git rev-list --count HEAD`:
#
#   - That counts the whole history. A build 12 commits after v2.1.2 read as "+git40" because the
#     repository happened to hold 40 commits, which looks like "40 commits after the release". What
#     belongs there is the distance travelled, which is what `git describe --long` reports.
#   - It says nothing about whether the tag is exactly here, so the suffix was appended
#     unconditionally: even a release built at its own tag came out as "2.1.2+git28.<hash>", and a
#     clean version never appeared anywhere. `--long` gives "-0-g" at the tag, which is the branch
#     that drops the suffix.
#
# --match keeps backup markers and notes-to-self out: with a plain describe the nearest tag of any
# kind wins, and the About page shows whatever it returns verbatim.
#
# DCODE_DESCRIBE overrides the describe output, so tools/test-version.sh can cover every shape without
# building four git repositories.
set -eu

describe=${DCODE_DESCRIBE-}
if [ -z "$describe" ]; then
	describe=$(git describe --tags --long --dirty --match 'v[0-9]*' 2>/dev/null || true)
fi

if [ -z "$describe" ]; then
	# No tag reachable (a fresh clone of a shallow tree, or no tags at all). Still say which commit
	# this is: two different builds must not both call themselves 0.0.0.
	hash=$(git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)
	printf '0.0.0+g%s\n' "$hash"
	exit 0
fi

# v2.1.2-12-ga20cef2-dirty → base=2.1.2  ahead=12  hash=a20cef2  dirty=dirty
#
# The base is greedy on purpose: a tag that carries its own suffix (v2.1.2-rc1) must not have that
# suffix read as the commit count.
printf '%s\n' "$describe" |
	sed -E 's/^v//; s/^(.*)-([0-9]+)-g([0-9a-f]+)(-dirty)?$/\1 \2 \3 \4/' |
	awk 'NF < 3 { print; next }
	     {
		base = $1; ahead = $2 + 0; hash = $3; dirty = ($4 != "")
		suffix = dirty ? "-dirty" : ""
		if (ahead == 0)
			printf "%s%s\n", base, suffix
		else
			printf "%s+git%d.%s%s\n", base, ahead, hash, suffix
	     }'
