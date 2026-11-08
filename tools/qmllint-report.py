#!/usr/bin/env python3
"""Run qmllint and report every diagnostic it finds.

    tools/qmllint-report.py [-I DIR] [--ignore-settings] [--all] [--max-warnings N] FILES...

Warnings are printed one per line and the exit status is 1 when there are more than `--max-warnings`
(0 by default, so a warning fails the run). Info-level diagnostics are counted per category, with one
example location, unless `--all` prints them individually.

Why not read qmllint's own output: its text form drops most of the diagnostics it counted. On one
file it printed 20 of 68, on another 0 of 4, while `--max-warnings` refused the run for the ones it
never showed. `--json` is the complete list, so that is what this parses.
"""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path


def parse_args(argv: list[str]) -> tuple[str | None, list[str], bool, bool, int]:
    farm: str | None = None
    files: list[str] = []
    ignore_settings = False
    show_all = False
    max_warnings = 0
    i = 0
    while i < len(argv):
        arg = argv[i]
        if arg == "-I":
            i += 1
            farm = argv[i]
        elif arg.startswith("-I"):
            farm = arg[2:]
        elif arg == "--ignore-settings":
            ignore_settings = True
        elif arg == "--all":
            show_all = True
        elif arg == "--max-warnings":
            i += 1
            max_warnings = int(argv[i])
        elif arg.startswith("--max-warnings="):
            max_warnings = int(arg.split("=", 1)[1])
        elif arg.startswith("-"):
            sys.exit(f"qmllint-report: unknown option {arg}")
        else:
            files.append(arg)
        i += 1
    if not files:
        sys.exit("qmllint-report: no QML files given")
    return farm, files, ignore_settings, show_all, max_warnings


Diagnostic = tuple[str, int, int, str, str]


def collect(
    farm: str | None, files: list[str], ignore_settings: bool
) -> tuple[list[Diagnostic], list[Diagnostic], subprocess.CompletedProcess[str]]:
    """(warnings, infos, proc) as (file, line, column, id, message) tuples.

    A file that does not parse shows up as a `syntax` warning, so it needs no separate handling;
    stderr is only for a qmllint that produced nothing at all.
    """
    cmd = [os.environ.get("QMLLINT") or "qmllint"]
    if farm:
        cmd += ["-I", farm]
    if ignore_settings:
        cmd += ["--ignore-settings"]
    with tempfile.TemporaryDirectory() as tmp:
        report = Path(tmp) / "qmllint.json"
        proc = subprocess.run(
            cmd + ["--json", str(report)] + files, capture_output=True, text=True
        )
        if not report.exists():
            # No JSON at all: qmllint itself failed (bad options, a crash).
            sys.exit(
                (proc.stderr or proc.stdout or "qmllint produced no report").strip()
            )
        data = json.loads(report.read_text(encoding="utf-8"))

    warnings: list[Diagnostic] = []
    infos: list[Diagnostic] = []
    for entry in data.get("files", []):
        name = entry.get("filename", "?")
        for w in entry.get("warnings", []):
            item: Diagnostic = (
                name,
                w.get("line", 0),
                w.get("column", 0),
                w.get("id", ""),
                w.get("message", "").splitlines()[0],
            )
            if w.get("type") == "warning":
                warnings.append(item)
            else:
                infos.append(item)
    return warnings, infos, proc


def main(argv: list[str]) -> int:
    farm, files, ignore_settings, show_all, max_warnings = parse_args(argv)
    warnings, infos, proc = collect(farm, files, ignore_settings)

    for name, line, col, ident, message in warnings:
        print(f"{name}:{line}:{col}: [{ident}] {message}")

    if infos:
        if show_all:
            for name, line, col, ident, message in infos:
                print(f"{name}:{line}:{col}: [{ident}] {message}")
        else:
            seen: dict[tuple[str, str], list[tuple[str, int, int, str, str]]] = {}
            for item in infos:
                seen.setdefault((item[3], item[4]), []).append(item)
            for (ident, message), items in seen.items():
                name, line, col = items[0][0], items[0][1], items[0][2]
                more = f" (+{len(items) - 1} more)" if len(items) > 1 else ""
                print(f"info: {name}:{line}:{col}: [{ident}] {message}{more}")

    print(
        f"qmllint: {len(warnings)} warning(s), {len(infos)} info(s) in {len(files)} file(s)"
        + ("" if ignore_settings else ", settings applied")
    )

    if not warnings and not infos and proc.returncode != 0:
        # Nonzero with nothing to show means qmllint failed rather than linted.
        sys.stderr.write((proc.stderr or proc.stdout).strip() + "\n")
        return 2
    if max_warnings >= 0 and len(warnings) > max_warnings:
        print(
            f"qmllint-report: {len(warnings)} > {max_warnings} allowed", file=sys.stderr
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
