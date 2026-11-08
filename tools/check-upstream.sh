#!/usr/bin/env bash
# Upstream contract check.
#
# The project's model:
#   dux-qml-common/  = the submodule, the component library itself (pinned to the recorded commit)
#   DCommon/         = a symlink to dux-qml-common/DCommon (same approach as upstream dux-calendar)
#   Common/          = the app layer (ours), which has to satisfy the library's contract
#
# The library is no longer compared file by file: it *is* that commit, so "has it drifted" means "is the
# submodule clean".
#
# Two layers of checks:
#   library layer (1/2/3)  submodule state, the Theme tokens the library reads, and whether any value
#                          was silently changed
#   app layer (4)          against dux-calendar's Common/Theme.qml: which token is missing, which value
#                          differs
#
# The authority for 2/3 is the library's own Common/ (its minimal app-layer implementation), not
# Style.qml's read list — some `theme?.X` reads have no upstream counterpart either, left for fallback on
# purpose.
#
# The authority for 4 is dux-calendar (this shell was carved out of it). Without a dcal checkout the
# layer is skipped, so the script still runs on machines without it.
#
# Usage:
#   tools/check-upstream.sh          # check (exit 1 on problems)
#   DUX_UPSTREAM=/path tools/...     # use another component-library clone
#   DUX_DCAL=/path tools/...         # use another dux-calendar clone
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUB="$ROOT/dux-qml-common"
UP="${DUX_UPSTREAM:-$SUB}"
fail=0

# --- 1. submodule: initialised, unmodified, commit tracked ---
if [ ! -d "$SUB/DCommon" ]; then
  echo "✗ submodule not initialised: git submodule update --init"
  echo "  DCommon is a link to dux-qml-common/DCommon; without the submodule the whole library is gone"
  fail=1
else
  if [ ! -d "$ROOT/DCommon/Common" ]; then
    echo "✗ the DCommon link is broken (expected -> dux-qml-common/DCommon)"
    fail=1
  fi

  dirty="$(git -C "$SUB" status --porcelain 2>/dev/null)"
  if [ -n "$dirty" ]; then
    echo "✗ the submodule has uncommitted changes; the library is no longer upstream:"
    printf '%s\n' "$dirty" | sed 's/^/    /'
    echo "    To restore: git -C dux-qml-common checkout -- ."
    echo "    If the library really must change, fork it and change it there; this only points at it"
    fail=1
  else
    echo "✓ library = upstream $(git -C "$SUB" rev-parse --short HEAD) (submodule clean)"
  fi

  # The gitlink in the index must equal the submodule's current commit, or after committing
  # someone else checks out another version while everything looks fine locally.
  staged="$(git -C "$ROOT" ls-files -s dux-qml-common 2>/dev/null | awk '{print $2}')"
  if [ -n "$staged" ] && [ "$staged" != "$(git -C "$SUB" rev-parse HEAD)" ]; then
    echo "✗ the submodule commit is not tracked: git add dux-qml-common"
    fail=1
  fi
fi

# --- 2/3. library Theme tokens: missing locally, or same name with a different value ---
if [ -f "$UP/Common/Theme.qml" ] && command -v python3 >/dev/null; then
  report=$(python3 - "$UP/Common/Theme.qml" "$ROOT/Common/Theme.qml" <<'PY'
import re, sys

# Deliberate overrides (must match the list in UPSTREAM.md; change both together)
ALLOW = {
    "clockFaceSize", "clockHandleSize",
    "sliderHandleHeightM", "sliderHandleHeightS",
    "stateLayerFocus", "stateLayerPressed",
    "tooltipMaxWidth",
}

DECL = re.compile(r'^\s*(?:readonly\s+)?(?:property\s+[A-Za-z0-9_.]+\s+|function\s+)([A-Za-z0-9_]+)')
LITERAL = re.compile(r'^\s*(?:readonly\s+)?property\s+(?:real|int|double|bool)\s+(\w+)\s*:\s*([-\d.]+)\s*$')

def names(path):
    return {m.group(1) for m in map(DECL.match, open(path)) if m}

def literals(path):
    out = {}
    for line in open(path):
        m = LITERAL.match(line)
        if m:
            out[m.group(1)] = m.group(2)
    return out

up_path, lc_path = sys.argv[1], sys.argv[2]
missing = sorted(names(up_path) - names(lc_path))
drift = []
up_lit, lc_lit = literals(up_path), literals(lc_path)
for k in sorted(set(up_lit) & set(lc_lit)):
    if up_lit[k] != lc_lit[k] and k not in ALLOW:
        drift.append(f"{k}: upstream {up_lit[k]} / local {lc_lit[k]}")

if missing:
    print("MISSING")
    for k in missing:
        print(f"    {k}")
if drift:
    print("DRIFT")
    for d in drift:
        print(f"    {d}")
PY
)
  if printf '%s' "$report" | grep -q '^MISSING'; then
    echo "✗ Common/Theme.qml is missing tokens upstream has (Style falls back):"
    printf '%s\n' "$report" | sed -n '/^MISSING/,/^DRIFT\|^$/p' | grep '^    '
    fail=1
  else
    echo "✓ every upstream Theme token is present"
  fi
  if printf '%s' "$report" | grep -q '^DRIFT'; then
    echo "✗ token values differ from upstream without being listed as overrides:"
    printf '%s\n' "$report" | sed -n '/^DRIFT/,$p' | grep '^    '
    fail=1
  else
    echo "✓ no literal token deviates outside the list"
  fi
else
  echo "– upstream Theme.qml or python3 not found; skipping the token comparison"
fi

# --- 4. app layer: compare against dux-calendar's Common/Theme.qml ---
# 3 only looks at bare literals like `: 123` and cannot see expressions (`withAlpha(...)`,
# `BlurService.enabled`).
# So a changed value or a new token in dcal would never wake us up; this layer closes that hole.
# An explicit DUX_DCAL is authoritative: a wrong path is reported instead of silently searching on.
DCAL=""
SKIP=""
if [ -n "${DUX_DCAL:-}" ]; then
  if [ -f "${DUX_DCAL}/quickshell/Common/Theme.qml" ]; then
    DCAL="$DUX_DCAL"
  else
    SKIP="– DUX_DCAL=$DUX_DCAL has no quickshell/Common/Theme.qml; skipping the app-layer comparison"
  fi
else
  for c in "$ROOT/../dux-calendar" \
           "$HOME/dux-calendar" \
           "$HOME/src/dux-calendar"; do
    if [ -f "$c/quickshell/Common/Theme.qml" ]; then
      DCAL="$c"
      break
    fi
  done
  [ -z "$DCAL" ] && SKIP="– no dux-calendar checkout found (DUX_DCAL=<path> to point at one); skipping the app-layer comparison"
fi

if [ -n "$DCAL" ] && command -v python3 >/dev/null; then
  report=$(python3 - "$DCAL/quickshell/Common/Theme.qml" "$ROOT/Common/Theme.qml" <<'PY'
import re, sys

# Deliberately different from dcal (must match "Deliberate divergences" in UPSTREAM.md; change both).
# Every entry needs a reason; "upstream changed and we could not be bothered" is not one.
ALLOW = {
    "blurLayersActive",   # no BlurService in this project, so no compositor blur on windows
    "dmsTypographyPath",  # Dux Shell 2.0 typography comes from ~/.config/dms/settings.json
    "outlineMedium",      # like dfiles, reads blurLayerOutlineOpacity (same 0.12 by default)
}

PROP = re.compile(r'^\s*(?:readonly\s+)?property\s+([\w.]+|var)\s+(\w+)\s*:\s*(.*)$')
DECL = re.compile(r'^\s*(?:readonly\s+)?(?:property\s+[A-Za-z0-9_.]+\s+|function\s+)([A-Za-z0-9_]+)')

def props(path):
    out = {}
    with open(path, encoding='utf-8') as f:
        for line in f:
            m = PROP.match(line)
            if m:
                value = re.sub(r'\s+', ' ', m.group(3)).strip()
                value = re.sub(r'\s*//.*$', '', value).strip()  # a trailing comment is not a value
                out[m.group(2)] = value
    return out

def decls(path):
    with open(path, encoding='utf-8') as f:
        return {m.group(1) for m in map(DECL.match, f) if m}

up_path, lc_path = sys.argv[1], sys.argv[2]
up, lc = props(up_path), props(lc_path)
missing = sorted(decls(up_path) - decls(lc_path))
drift = [f"{k}: dcal {up[k]} / local {lc[k]}"
         for k in sorted(set(up) & set(lc))
         if up[k] != lc[k] and k not in ALLOW]

if missing:
    print("MISSING")
    for k in missing:
        print(f"    {k}")
if drift:
    print("DRIFT")
    for d in drift:
        print(f"    {d}")
PY
)
  dcal_rev="$(git -C "$DCAL" rev-parse --short HEAD 2>/dev/null || echo '?')"
  if printf '%s' "$report" | grep -q '^MISSING'; then
    echo "✗ Common/Theme.qml is missing tokens dcal ($dcal_rev) has:"
    printf '%s\n' "$report" | sed -n '/^MISSING/,/^DRIFT/p' | grep '^    '
    fail=1
  else
    echo "✓ every Theme token from dcal ($dcal_rev) is present"
  fi
  if printf '%s' "$report" | grep -q '^DRIFT'; then
    echo "✗ Theme expressions differ from dcal ($dcal_rev) without being listed as overrides:"
    printf '%s\n' "$report" | sed -n '/^DRIFT/,$p' | grep '^    '
    fail=1
  else
    echo "✓ Theme expressions match dcal ($dcal_rev)"
  fi
else
  echo "${SKIP:-– python3 missing; skipping the app-layer comparison}"
fi

exit $fail
