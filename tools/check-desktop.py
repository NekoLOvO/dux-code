#!/usr/bin/env python3
"""Validate the .desktop assets, including their localized keys.

    tools/check-desktop.py

`desktop-file-validate` covers most of the spec, and this runs it when it is installed. What it does
not catch is the spelling of the locale suffix: `Name[zh-TW]` is a different key from `Name[zh_TW]`,
and a desktop environment looking for the latter simply never finds it. Nothing reports that — the
field just stays English for every user of that locale, which reads as "we did not translate this"
rather than as a typo. The POSIX locale form is `ll`, `ll_CC` or `ll_CC@modifier`, with an underscore.

It also rejects a locale the repository does not translate: a key for a language with no file under
translations/poexports/ is a claim nobody maintains, and it silently wins over the base value for
everyone whose locale matches it.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ASSETS = ROOT / "assets"
EXPORTS = ROOT / "translations" / "poexports"

# ll, ll_CC, ll_CC@modifier. An underscore between language and country; the hyphen spelling that
# BCP 47 uses is not a desktop entry locale.
LOCALE = re.compile(r"^[a-z]{2,3}(_[A-Z]{2})?(@[A-Za-z0-9]+)?$")
LOCALIZED = re.compile(r"^([A-Za-z0-9-]+)\[([^\]]+)\]$")

REQUIRED = ("Type", "Name", "Exec", "Icon")


def translated_languages() -> set[str]:
    return {p.stem.split("_")[0] for p in EXPORTS.glob("*.json")}


def desktop_files() -> list[Path]:
    return sorted(ASSETS.glob("*.desktop"))


def keys_of(path: Path) -> dict[str, list[tuple[int, str, str]]]:
    """group -> [(line, key, value)], comments and blank lines dropped."""
    groups: dict[str, list[tuple[int, str, str]]] = {}
    group = ""
    for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("[") and line.endswith("]"):
            group = line[1:-1]
            groups.setdefault(group, [])
            continue
        if "=" not in line or not group:
            continue
        key, _, value = line.partition("=")
        groups[group].append((number, key.strip(), value.strip()))
    return groups


def check(path: Path, languages: set[str]) -> list[str]:
    problems: list[str] = []
    groups = keys_of(path)
    entry = dict((key, value) for _, key, value in groups.get("Desktop Entry", []))
    if "Desktop Entry" not in groups:
        return [f"{path.name}: no [Desktop Entry] group"]

    for key in REQUIRED:
        if not entry.get(key):
            problems.append(f"{path.name}: missing {key}")

    for number, key, value in groups.get("Desktop Entry", []):
        match = LOCALIZED.match(key)
        if not match:
            continue
        base, locale = match.groups()
        if base not in entry:
            problems.append(f"{path.name}:{number}: {key} has no unlocalized {base}")
        if not LOCALE.match(locale):
            problems.append(
                f"{path.name}:{number}: {key}: '{locale}' is not a locale "
                f"(ll, ll_CC or ll_CC@modifier, underscore not hyphen)"
            )
        elif locale.split("_")[0].split("@")[0] not in languages:
            problems.append(
                f"{path.name}:{number}: {key}: nothing under translations/poexports/ "
                f"covers '{locale.split('_')[0]}'"
            )
        if not value:
            problems.append(f"{path.name}:{number}: {key} is empty")

    # A localized Keywords replaces the base list rather than adding to it, so the terms only in the
    # base have to be repeated or they stop matching for that locale.
    base_terms = {t for t in entry.get("Keywords", "").split(";") if t}
    for number, key, value in groups.get("Desktop Entry", []):
        if key.split("[")[0] != "Keywords" or "[" not in key:
            continue
        missing = base_terms - {t for t in value.split(";") if t}
        if missing:
            problems.append(
                f"{path.name}:{number}: {key} drops {sorted(missing)}, which a localized "
                f"list replaces rather than extends"
            )
    return problems


def main() -> int:
    files = desktop_files()
    if not files:
        print("check-desktop: no .desktop files in assets/")
        return 1

    languages = translated_languages()
    problems: list[str] = []
    for path in files:
        problems += check(path, languages)
        if (validator := shutil.which("desktop-file-validate")) is not None:
            result = subprocess.run(
                [validator, str(path)], capture_output=True, text=True
            )
            problems += [line for line in result.stdout.splitlines() if line.strip()]

    for problem in problems:
        print(problem)
    print(f"check-desktop: {len(files)} file(s), {len(problems)} problem(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
