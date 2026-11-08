#!/usr/bin/env python3
"""Build the qmldir index qmllint needs, outside the repository.

Quickshell resolves `import qs.Foo` to `<config>/Foo`, but the QML module system wants a directory
with a `qmldir` listing its types. Without one every `qs.*` import fails and the checker reports
unqualified-access warnings for the whole file. The index is generated here rather than committed,
because a `qmldir` in the tree would claim a module name only Quickshell's loader understands.

    tools/qmllint-farm.py [DEST [REPO]]

`DEST` defaults to `$XDG_CACHE_HOME/dcode-qmllint` and holds `qs/`, which is what `-I` points at. It
is a cache, so it is rebuilt each run.
"""

import os
import re
import shutil
import sys
from pathlib import Path

# Not indexed: the component library is a submodule, imported separately as qs.DCommon.
SKIP = ("dux-qml-common", "dist", "core")


def components(directory: Path) -> list[tuple[str, Path, bool]]:
    """(name, absolute file, is_singleton) for the QML files directly in directory."""
    out: list[tuple[str, Path, bool]] = []
    for qml in sorted(directory.glob("*.qml")):
        text = qml.read_text(encoding="utf-8", errors="replace")
        # A qmldir must repeat the `singleton` keyword, or every import of the type fails.
        singleton = re.search(r"^\s*pragma\s+Singleton", text, re.M) is not None
        out.append((qml.stem, qml.resolve(), singleton))
    return out


def module_dirs(repo: Path) -> set[Path]:
    """Every directory reachable as qs.<path>, relative to the module root."""
    dirs: set[Path] = set()
    # Not ours to index, and not a module root either.
    for qml in repo.rglob("*.qml"):
        rel = qml.relative_to(repo)
        if rel.parts[0] in SKIP or len(rel.parts) < 2:
            continue
        dirs.add(rel.parent)
    # The component library is reached as qs.DCommon.<path>, through the DCommon/ symlink.
    for qml in (repo / "DCommon").rglob("*.qml"):
        if len(qml.relative_to(repo / "DCommon").parts) < 2:
            continue
        dirs.add(Path("DCommon") / qml.relative_to(repo / "DCommon").parent)
    return dirs


def build(repo: Path, root: Path) -> int:
    if root.parent.exists():
        shutil.rmtree(root.parent)
    made = 0
    for rel in sorted(module_dirs(repo)):
        items = components(repo / rel)
        if not items:
            continue
        dest: Path = root / rel
        dest.mkdir(parents=True, exist_ok=True)
        lines: list[str] = []
        for name, target, singleton in items:
            Path(dest / target.name).symlink_to(target)
            lines.append(f"{'singleton ' if singleton else ''}{name} 1.0 {target.name}")
        qmldir: Path = dest / "qmldir"
        qmldir.write_text("\n".join(lines) + "\n", encoding="utf-8")
        made += 1
    return made


def default_dest() -> Path:
    cache = os.environ.get("XDG_CACHE_HOME") or str(Path.home() / ".cache")
    return Path(cache) / "dcode-qmllint"


if __name__ == "__main__":
    dest = Path(sys.argv[1]) if len(sys.argv) > 1 else default_dest()
    repo = Path(sys.argv[2] if len(sys.argv) > 2 else ".").resolve()
    print(f"{build(repo, dest / 'qs')} module(s) indexed under {dest}/qs")
