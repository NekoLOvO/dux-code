#!/usr/bin/env python3
"""Fail when app QML reads a Theme token that Common/Theme.qml does not define.

`check-upstream.sh` compares the *contract* — which tokens upstream has and whether a value drifted. It
never asks whether a name written in our own QML exists at all, so `Theme.secondaryContainerText` (which
was invented, not renamed) passed every check and failed at run time as
"Unable to assign [undefined] to QColor" — a warning in a terminal, and a widget with no colour.

Only the app layer is scanned: DCommon is the library and has its own Theme, and the extracted UI under
core/internal/uibundle/dist is a copy of what is scanned here.
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
THEME = ROOT / "Common" / "Theme.qml"

# property [type] name / readonly property [type] name / function name.
#
# The type is any identifier, not a list of built-ins: `readonly property FontSource typography` is a
# token like any other, and a built-in-only list reported it as missing.
DECL = re.compile(
    r"^\s*(?:readonly\s+)?property\s+[\w.]+(?:\s*<[^>]*>)?\s+(\w+)"
    r"|^\s*function\s+(\w+)",
    re.M,
)

# `Theme.foo`. A file may bind another object to the name Theme (a local alias), so a name that does
# not resolve is reported with its file and line rather than assumed wrong.
USE = re.compile(r"\bTheme\.(\w+)")

SKIP_DIRS = {"dux-qml-common", "uibundle", ".git", "translations", "node_modules", "dist", "build"}


def declared() -> set[str]:
    if not THEME.is_file():
        print(f"check-theme-tokens: {THEME} is missing")
        sys.exit(1)
    names = set()
    for prop, func in DECL.findall(THEME.read_text(encoding="utf-8")):
        names.add(prop or func)
    return names


def qml_files():
    for path in ROOT.rglob("*.qml"):
        if any(part in SKIP_DIRS for part in path.relative_to(ROOT).parts):
            continue
        yield path


def main() -> int:
    known = declared()
    problems = 0
    checked = 0
    for path in qml_files():
        text = path.read_text(encoding="utf-8", errors="replace")
        if "Theme." not in text:
            continue
        checked += 1
        for number, line in enumerate(text.splitlines(), start=1):
            if "Theme." not in line:
                continue
            # A comment mentioning a token is documentation, not a read.
            if line.lstrip().startswith("//"):
                continue
            for name in USE.findall(line):
                if name not in known:
                    rel = path.relative_to(ROOT)
                    print(f"✗ {rel}:{number}: Theme.{name} is not defined in Common/Theme.qml")
                    problems += 1

    if problems:
        print(f"check-theme-tokens: {problems} unknown token(s) across {checked} files")
        return 1
    print(f"✓ every Theme token read in {checked} QML files exists")
    return 0


if __name__ == "__main__":
    sys.exit(main())
