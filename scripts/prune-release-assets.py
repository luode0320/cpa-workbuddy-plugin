#!/usr/bin/env python3
"""Prune old release-assets directories, keeping only registry current versions.

Usage:
    python scripts/prune-release-assets.py --dry-run   # report only
    python scripts/prune-release-assets.py --apply     # git rm old dirs + remove empty dirs

Keep set = for each plugin in registry.json: "<id>-<version>".
Delete set = every directory under release-assets/ not in the keep set.

Tracked dirs are removed with `git rm -r` (staged for commit); empty /
untracked dirs are removed with rmdir (non-empty dirs are skipped).
"""
from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys


def repo_root() -> pathlib.Path:
    out = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        capture_output=True, text=True, check=True,
    )
    return pathlib.Path(out.stdout.strip())


def load_keep(registry: pathlib.Path) -> set[str]:
    data = json.loads(registry.read_text(encoding="utf-8"))
    return {f"{p['id']}-{p['version']}" for p in data["plugins"]}


def dir_size(path: pathlib.Path) -> int:
    total = 0
    for f in path.rglob("*"):
        if f.is_file():
            try:
                total += f.stat().st_size
            except OSError:
                pass
    return total


def is_tracked(root: pathlib.Path, rel: str) -> bool:
    out = subprocess.run(
        ["git", "ls-files", "--", rel],
        cwd=root, capture_output=True, text=True, check=True,
    )
    return bool(out.stdout.strip())


def main() -> int:
    ap = argparse.ArgumentParser()
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--dry-run", action="store_true")
    g.add_argument("--apply", action="store_true")
    ap.add_argument("--assets-dir", default="release-assets")
    args = ap.parse_args()

    root = repo_root()
    assets = root / args.assets_dir
    if not assets.is_dir():
        print(f"assets dir not found: {assets}")
        return 1

    keep = load_keep(root / "registry.json")
    dirs = sorted(d.name for d in assets.iterdir() if d.is_dir())
    old = [d for d in dirs if d not in keep]

    # safety: keep dirs must never appear in delete set
    overlap = set(old) & keep
    if overlap:
        print(f"REFUSING: keep dirs in delete set: {sorted(overlap)}")
        return 2

    old_size = sum(dir_size(assets / d) for d in old)
    print(f"keep set ({len(keep)}): {sorted(keep)}")
    print(f"dirs total: {len(dirs)}, keep present: {len(keep & set(dirs))}, to delete: {len(old)}")
    print(f"to delete size: {old_size / (1 << 30):.2f} GB")
    for d in old:
        print(f"  - {d}  ({dir_size(assets / d) / (1 << 20):.1f} MB)")

    if args.dry_run:
        return 0

    # tracked -> git rm; empty/untracked -> rmdir
    tracked = [d for d in old if is_tracked(root, f"{args.assets_dir}/{d}")]
    untracked = [d for d in old if d not in tracked]
    if tracked:
        subprocess.run(["git", "rm", "-r", "-q", "--"] + [f"{args.assets_dir}/{d}" for d in tracked], cwd=root, check=True)
    for d in untracked:
        target = assets / d
        if any(target.iterdir()):
            print(f"SKIP non-empty untracked dir: {d}")
            continue
        target.rmdir()
    print(f"removed: tracked={len(tracked)} untracked_empty={len(untracked)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
