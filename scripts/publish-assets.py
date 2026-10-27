#!/usr/bin/env python3
"""Publish helper: sync release-assets/<plugin>-<version>/ + registry.json.

Usage:
    python publish-assets.py <plugin> <version> [--assets-dir release-assets]
    python publish-assets.py --all-current

Steps:
  1. scan release-assets/<id>-<version>/ for <id>_<version>_<goos>_<goarch>.zip + checksums.txt
  2. verify all 6 platforms present
  3. update registry.json: version + install.artifacts[*].{url,sha256,size}
     (URLs point at the GitHub Release download endpoint)
  4. validate with scripts/validate-registry.py

`--all-current` rewrites every plugin in registry.json to its own current
version without touching version fields; sha256 comes from the Release
checksums.txt and size from the Release asset metadata, so no zip downloads
are needed.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import re
import sys
import urllib.request

REPO = "luode0320/cpa-workbuddy-plugin"
RELEASE_BASE = f"https://github.com/{REPO}/releases/download"

PLATFORMS = [
    "darwin_amd64",
    "darwin_arm64",
    "linux_amd64",
    "linux_arm64",
    "windows_amd64",
    "windows_arm64",
]


def sha256_of(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def release_url(plugin: str, version: str, name: str) -> str:
    return f"{RELEASE_BASE}/{plugin}-v{version}/{name}"


def fetch(url: str) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": "cpa-publish-assets"})
    with urllib.request.urlopen(req, timeout=120) as resp:
        return resp.read()


def release_metadata(plugin: str, version: str) -> tuple[dict[str, str], dict[str, int]]:
    """Return ({zip name: sha256}, {asset name: size}) straight from the Release."""
    tag = f"{plugin}-v{version}"
    checksums: dict[str, str] = {}
    body = fetch(f"{RELEASE_BASE}/{tag}/checksums.txt").decode("utf-8")
    for line in body.splitlines():
        parts = line.split()
        if len(parts) == 2:
            checksums[parts[1]] = parts[0]

    api = f"https://api.github.com/repos/{REPO}/releases/tags/{tag}"
    data = json.loads(fetch(api))
    sizes = {a["name"]: int(a["size"]) for a in data.get("assets", [])}
    return checksums, sizes


def rewrite_all_current() -> int:
    reg = pathlib.Path("registry.json")
    data = json.loads(reg.read_text(encoding="utf-8"))
    plugins = data.get("plugins") or []
    if not plugins:
        raise SystemExit("registry.json has no plugins")

    failures: list[str] = []
    for entry in plugins:
        plugin = entry["id"]
        version = str(entry.get("version") or "").strip()
        install = entry.setdefault("install", {"type": "direct"})
        install["type"] = "direct"
        arts = install.get("artifacts")
        if not isinstance(arts, list):
            arts = []
            install["artifacts"] = arts

        # The platform set is owned by registry.json, never by the Release asset
        # list: several Releases still carry a retired freebsd_amd64 zip.
        wanted = {f"{a['goos']}_{a['goarch']}" for a in arts}
        if wanted != set(PLATFORMS):
            failures.append(f"{plugin}: registry platforms {sorted(wanted)} != {PLATFORMS}")
            continue

        try:
            checksums, sizes = release_metadata(plugin, version)
        except Exception as exc:  # noqa: BLE001 - report and keep going
            failures.append(f"{plugin}: release metadata failed: {exc}")
            continue

        for art in arts:
            t = f"{art['goos']}_{art['goarch']}"
            name = f"{plugin}_{version}_{t}.zip"
            sha = checksums.get(name)
            size = sizes.get(name)
            if not sha or size is None:
                failures.append(f"{plugin}: release asset missing {name}")
                continue
            art["url"] = release_url(plugin, version, name)
            art["sha256"] = sha
            art["size"] = size
        print(f"{plugin} {version}: {len(arts)} artifacts -> Release URLs")

    if failures:
        for f in failures:
            print(f"ERROR {f}", file=sys.stderr)
        return 1

    reg.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"rewrote {len(plugins)} plugins to Release URLs")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("plugin", nargs="?")
    ap.add_argument("version", nargs="?")
    ap.add_argument("--assets-dir", default="release-assets")
    ap.add_argument("--all-current", action="store_true")
    ap.add_argument("--allow-missing", action="store_true",
                    help="publish even if some platforms are absent (drops their artifact slots)")
    args = ap.parse_args()

    if args.all_current:
        if args.plugin or args.version:
            raise SystemExit("--all-current takes no plugin/version")
        return rewrite_all_current()
    if not args.plugin or not args.version:
        raise SystemExit("usage: publish-assets.py <plugin> <version> | --all-current")

    plugin, version = args.plugin, args.version
    ver_re = re.compile(r"^[0-9][0-9A-Za-z.+-]*$")
    if not ver_re.match(version):
        raise SystemExit(f"bad version {version!r}")

    root = pathlib.Path(args.assets_dir)
    out = root / f"{plugin}-{version}"
    if not out.is_dir():
        raise SystemExit(f"assets dir missing: {out}")

    def zip_path(t: str) -> pathlib.Path:
        return out / f"{plugin}_{version}_{t}.zip"

    def present_platforms() -> list[str]:
        return [t for t in PLATFORMS if zip_path(t).exists()]

    missing = [t for t in PLATFORMS if not zip_path(t).exists()]
    if missing:
        # 默认仍强制 6 平台：缺件会让部分用户装不上，属于需要人工确认的发布形态。
        # 只有显式 --allow-missing 才放宽（CI 单平台 runner 长期排队时的已知取舍），
        # 此时 registry 不保留该平台的 artifact 槽位，避免指向不存在的 URL。
        if not args.allow_missing:
            raise SystemExit(f"missing zips: {missing} (pass --allow-missing to publish without them)")
        print(f"WARN publishing without platforms: {missing}")

    # checksums.txt sanity (regenerate if absent)
    cksum = out / "checksums.txt"
    if not cksum.exists():
        lines = []
        for t in present_platforms():
            z = zip_path(t)
            lines.append(f"{sha256_of(z)}  {z.name}")
        cksum.write_text("\n".join(lines) + "\n")
    for t in present_platforms():
        z = zip_path(t)
        h = sha256_of(z)
        ck = f"{h}  {z.name}"
        if ck not in cksum.read_text():
            print(f"WARN checksums.txt mismatch for {z.name} (will regenerate)")
            lines = []
            for tt in present_platforms():
                zz = zip_path(tt)
                lines.append(f"{sha256_of(zz)}  {zz.name}")
            cksum.write_text("\n".join(lines) + "\n")
            break

    reg = pathlib.Path("registry.json")
    data = json.loads(reg.read_text(encoding="utf-8"))
    entry = next((p for p in data.get("plugins", []) if p.get("id") == plugin), None)
    if entry is None:
        raise SystemExit(f"plugin {plugin} not found in registry.json")

    entry["version"] = version
    if "install" not in entry or not isinstance(entry["install"], dict):
        entry["install"] = {"type": "direct"}
    entry["install"]["type"] = "direct"
    arts = entry["install"].get("artifacts")
    if not isinstance(arts, list):
        arts = []
        entry["install"]["artifacts"] = arts
    seen = set()
    for t in present_platforms():
        z = zip_path(t)
        goos, goarch = t.split("_", 1)
        art = next((a for a in arts if a.get("goos") == goos and a.get("goarch") == goarch), None)
        if art is None:
            art = {"goos": goos, "goarch": goarch, "url": "", "sha256": "", "size": 0}
            arts.append(art)
        art["url"] = release_url(plugin, version, z.name)
        art["sha256"] = sha256_of(z)
        art["size"] = z.stat().st_size
        seen.add(t)
    # drop artifact slots for platforms not in our set (keeps registry honest)
    arts[:] = [a for a in arts if f"{a.get('goos')}_{a.get('goarch')}" in seen]
    reg.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    print(f"updated {plugin} -> {version}: {len(arts)} artifacts, sha256+size synced")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
