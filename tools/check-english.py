#!/usr/bin/env python3
"""Fail when Chinese prose appears in the source tree.

The rule is that this repository is written in English: comments, log lines, error messages, UI text
and documents. Chinese lives in exactly one place — `translations/`, which is data the runtime looks
up. Daemon strings stay English even though the UI is translated, because a daemon writes one message
for every client, and `dcode send` has no locale at all.

This is not hypothetical. The first release shipped with full-width punctuation inside error messages
(`HTTP 400：...`), an approval dialog title glued together with a full-width colon, and a
summarisation prompt separated by one — all invisible in an English reading and all out of place in a
translated UI. Nothing caught it, because nothing looked.

Only CJK ideographs are flagged. Punctuation alone is not enough evidence: a character class that
strips 「」 from a model's suggested title is data, not prose, and flagging it would train people to
ignore this check. Ideographs never appear for any such reason.

Deliberately not scanned: `dux-qml-common/` (upstream), `translations/` (the data), and build output.
"""

from __future__ import annotations

import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

SKIP_DIRS = {
    "dux-qml-common",
    "translations",
    "dist",
    "bin",
    "uibundle",
    ".git",
    ".github",
    "assets",
    "work",
}

SCANNED_SUFFIXES = {".go", ".qml", ".js", ".sh", ".py", ".md", ".json", ".desktop"}

# CJK Unified Ideographs, Extension A, and the compatibility block. Punctuation is excluded on
# purpose; see the module docstring.
def is_ideograph(ch: str) -> bool:
    code = ord(ch)
    return (
        0x4E00 <= code <= 0x9FFF
        or 0x3400 <= code <= 0x4DBF
        or 0xF900 <= code <= 0xFAFF
    )


def scanned_files() -> list[Path]:
    out = []
    for path in ROOT.rglob("*"):
        if not path.is_file() or path.suffix not in SCANNED_SUFFIXES:
            continue
        rel = path.relative_to(ROOT)
        if SKIP_DIRS.intersection(rel.parts):
            continue
        out.append(path)
    return sorted(out)


def main() -> int:
    problems = []
    files = scanned_files()
    for path in files:
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for number, line in enumerate(text.splitlines(), 1):
            if any(is_ideograph(ch) for ch in line):
                problems.append(f"{path.relative_to(ROOT)}:{number}: {line.strip()[:120]}")

    if problems:
        print("check-english: Chinese prose in the source tree:", file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        print("  UI text goes through I18n.tr(); translations live in translations/.", file=sys.stderr)
        return 1

    print(f"check-english: ok ({len(files)} files, no Chinese prose)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
