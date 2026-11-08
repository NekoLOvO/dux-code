#!/usr/bin/env python3
"""Fail when a QML signal is called with fewer arguments than it declares.

    tools/check-signal-arity.py [ROOT]

Qt 6 throws `TypeError: Insufficient arguments` at the call site, and nothing checks it statically:
qmllint has a category for signal *handler* parameters, not for calling a signal, so a wrong call is
silent until the code path runs. When that path is the one that ends a turn, the failure is a UI left
saying "running" while the daemon has already stopped, which reads as a broken button rather than as
a broken call.

Only calls to a signal declared in the same file are checked, and only unqualified ones: those are
the ones where the identifier can only mean the signal. More arguments than declared is legal in QML
and is not reported.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# Generated or third-party trees, same set qmllint uses.
SKIP_DIRS = {"dux-qml-common", "dist", "bin", ".git", "node_modules"}

SIGNAL = re.compile(r"^\s*signal\s+(\w+)\s*\(", re.M)
STRING_QUOTES = "\"'`"


def split_top_level(text: str) -> list[str]:
    """Split an argument list on the commas that are not inside brackets or a string."""
    parts: list[str] = []
    current = ""
    depth = 0
    quote: str | None = None
    for ch in text:
        if quote:
            current += ch
            if ch == quote:
                quote = None
            continue
        if ch in STRING_QUOTES:
            quote = ch
            current += ch
            continue
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            depth -= 1
        if ch == "," and depth == 0:
            parts.append(current.strip())
            current = ""
        else:
            current += ch
    if current.strip():
        parts.append(current.strip())
    return parts


def argument_lists(text: str, name: str):
    """Argument lists of every unqualified call to `name`, skipping its declaration."""
    for match in re.finditer(r"(?<![\w.])" + name + r"\s*\(", text):
        line_start = text.rfind("\n", 0, match.start()) + 1
        if text[line_start : match.start()].strip().startswith("signal"):
            continue
        body, depth, index, quote = "", 1, match.end(), None
        while index < len(text) and depth:
            ch = text[index]
            if quote:
                body += ch
                if ch == quote:
                    quote = None
            elif ch in STRING_QUOTES:
                quote = ch
                body += ch
            elif ch in "([{":
                depth += 1
                body += ch
            elif ch in ")]}":
                depth -= 1
                if depth:
                    body += ch
            else:
                body += ch
            index += 1
        yield text.count("\n", 0, match.start()) + 1, body


def declarations(text: str) -> dict[str, int]:
    """Signal name -> declared parameter count, for one file."""
    found: dict[str, int] = {}
    for match in SIGNAL.finditer(text):
        # The parameter list runs to the matching parenthesis; a signal's is always on one line.
        close = text.find(")", match.end())
        if close < 0:
            continue
        params = [p for p in split_top_level(text[match.end() : close]) if p]
        found[match.group(1)] = len(params)
    return found


def main(argv: list[str]) -> int:
    root = Path(argv[1]) if len(argv) > 1 else ROOT
    problems: list[str] = []
    files = sorted(
        p
        for p in root.rglob("*.qml")
        if not (set(p.relative_to(root).parts) & SKIP_DIRS)
    )
    for path in files:
        text = path.read_text(encoding="utf-8", errors="replace")
        for name, arity in declarations(text).items():
            for line, body in argument_lists(text, name):
                args = [a for a in split_top_level(body) if a]
                if any(a.startswith("...") for a in args):
                    continue
                if len(args) < arity:
                    problems.append(
                        f"{path.relative_to(root)}:{line}: {name}() takes {arity} "
                        f"argument(s), called with {len(args)}"
                    )

    for problem in problems:
        print(problem)
    print(f"check-signal-arity: {len(files)} files, {len(problems)} problem(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
