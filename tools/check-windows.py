#!/usr/bin/env python3
"""Fail when a dcode-owned window is instantiated without a parent window.

A Quickshell `FloatingWindow` with no `parentWindow` is a plain xdg_toplevel. Tiling compositors
(Hyprland, niri, sway) give those a tile, so instead of floating over the window that opened it the
modal comes up as a full-height column — that is exactly how "Add MCP server" behaved before anyone
noticed the property was missing. Quickshell cannot infer the parent, so each instantiation site has
to name one. `FileBrowserModal` spells the same thing `parentModal`.

Types are discovered, not listed: every component whose root is `FloatingWindow` is a window type,
including the ones that live in the component library. Our own files are then scanned for instances,
and the window itself must set one of the two properties — a nested child that names a parent says
nothing about the window containing it.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# Generated or third-party trees: their windows are not ours to fix.
SKIP_DIRS = {
    "dux-qml-common",
    "translations",
    "dist",
    "bin",
    ".git",
    ".github",
}

# The application's own top-level window. It has nothing to be a transient of.
ROOT_WINDOWS = {"ChatWindow"}

WINDOW_ROOT = re.compile(r"^\s*FloatingWindow\s*\{", re.MULTILINE)
INSTANCE = re.compile(r"\b([A-Z][A-Za-z0-9_]*)\s*\{")


def qml_files(root: Path, include_skipped: bool) -> list[Path]:
    out = []
    for path in root.rglob("*.qml"):
        rel = path.relative_to(root)
        if not include_skipped and SKIP_DIRS.intersection(rel.parts):
            continue
        out.append(path)
    return sorted(out)


def strip_noise(text: str) -> str:
    """Blank out comments and string literals so brace counting is not fooled by them."""
    out = []
    i = 0
    n = len(text)
    while i < n:
        ch = text[i]
        if ch == "/" and i + 1 < n and text[i + 1] == "/":
            while i < n and text[i] != "\n":
                out.append(" ")
                i += 1
            continue
        if ch == '"':
            out.append('"')
            i += 1
            while i < n and text[i] != '"':
                if text[i] == "\\":
                    out.append("  ")
                    i += 2
                    continue
                out.append("\n" if text[i] == "\n" else " ")
                i += 1
            out.append('"')
            i += 1
            continue
        out.append(ch)
        i += 1
    return "".join(out)


def block_end(clean: str, open_index: int) -> int:
    """Index of the brace closing the block that starts at `open_index`."""
    depth = 0
    for i in range(open_index, len(clean)):
        if clean[i] == "{":
            depth += 1
        elif clean[i] == "}":
            depth -= 1
            if depth == 0:
                return i
    return len(clean)


ASSIGNMENT = re.compile(r"(parentWindow|parentModal)\s*:\s*([^,;}\n]*)")
NULL_VALUES = {"", "null", "undefined"}
BOUNDARY = "{; \t\n"


def own_parent(body: str) -> tuple[str, str] | None:
    """The block's own `parentWindow`/`parentModal` assignment, if it has one.

    Only properties at depth 1 of the window's block count. A window that contains a picker of its
    own would otherwise pass on the child's parent, which is the shape that let the MCP editor's
    missing parent go unnoticed.
    """
    depth = 0
    for i, ch in enumerate(body):
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
        elif depth == 1 and ch == "p" and body[i - 1] in BOUNDARY:
            match = ASSIGNMENT.match(body, i)
            if match:
                return match.group(1), match.group(2).strip()
    return None


def main() -> int:
    all_files = qml_files(ROOT, include_skipped=True)
    types = {p.stem for p in all_files if WINDOW_ROOT.search(p.read_text(encoding="utf-8", errors="replace"))}
    if not types:
        print("check-windows: no FloatingWindow components found — the scan is broken", file=sys.stderr)
        return 1

    problems = []
    for path in qml_files(ROOT, include_skipped=False):
        text = path.read_text(encoding="utf-8", errors="replace")
        clean = strip_noise(text)
        for match in INSTANCE.finditer(clean):
            name = match.group(1)
            if name not in types or name in ROOT_WINDOWS:
                continue
            open_index = clean.index("{", match.start())
            own = own_parent(clean[open_index : block_end(clean, open_index)])
            if own is None:
                reason = "has no parentWindow"
            elif own[1] in NULL_VALUES:
                reason = f"{own[0]} is {own[1] or 'empty'}"
            else:
                continue
            line = text.count("\n", 0, match.start()) + 1
            problems.append(f"{path.relative_to(ROOT)}:{line}: {name} {reason}")

    if problems:
        print("check-windows: window without a parent (a tiling compositor will tile it):", file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        print("  set `parentWindow:` (or `parentModal:` for FileBrowserModal) to the window that owns it", file=sys.stderr)
        return 1

    print(f"check-windows: ok ({len(types)} window types, every instance has a parent)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
