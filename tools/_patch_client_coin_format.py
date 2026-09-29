# -*- coding: utf-8 -*-
"""Patch ordinary-shop tooltips to use the client's gold/silver/copper units.

The server keeps ``ShopBase.Price`` in copper (100 copper = 1 silver and
10,000 copper = 1 gold).  The existing compatibility cache was labelling the
raw integer as gold.  This installer changes only the embedded Hotfix.dll
``ServerShopPriceCache.FormatCoinPrice`` method and leaves HotfixView.dll and
all other client patches untouched.

The first install creates a sibling ``.pre-shop-coin-format.bak`` copy of the
bundle.  Rebuilds always start from that copy, and ``--restore`` puts it back,
so the change is independently reversible.
"""

from __future__ import annotations

import argparse
import hashlib
import shutil
import subprocess
import tempfile
from pathlib import Path

import UnityPy


ROOT = Path(__file__).resolve().parent.parent
CLIENT_ROOT = ROOT / "client-test"
CLIENT = next(CLIENT_ROOT.glob("*_Data"))
YOO = CLIENT / "StreamingAssets" / "yoo"
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
BUNDLE = YOO / "aa0" / BUNDLE_NAME
BACKUP = Path(str(BUNDLE) + ".pre-shop-coin-format.bak")
PATCH_PROJECT = ROOT / "tools" / "client-market-price-patcher" / "ClientMarketPricePatcher.csproj"


def read_text_asset(bundle: Path, name: str) -> bytes:
    env = UnityPy.load(str(bundle))
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if str(data.m_Name) != name:
            continue
        raw = data.m_Script
        return raw.encode("utf-8", errors="surrogateescape") if isinstance(raw, str) else bytes(raw)
    raise RuntimeError(f"bundle TextAsset not found: {name}")


def replace_text_asset(bundle: Path, name: str, replacement: bytes, output_dir: Path) -> Path:
    env = UnityPy.load(str(bundle))
    found = False
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if str(data.m_Name) != name:
            continue
        data.m_Script = replacement.decode("utf-8", errors="surrogateescape")
        data.save()
        found = True
        break
    if not found:
        raise RuntimeError(f"bundle TextAsset not found: {name}")
    for file_item in env.files.values():
        file_item.mark_changed()
    output_dir.mkdir(parents=True, exist_ok=True)
    env.save(pack="lz4", out_path=str(output_dir))
    output = output_dir / bundle.name
    if not output.is_file():
        raise RuntimeError(f"UnityPy did not write {output}")
    return output


def patch_dll(source: bytes, output: Path) -> bytes:
    with tempfile.NamedTemporaryFile(prefix="shop-coin-source-", suffix=".dll", delete=False) as stream:
        source_path = Path(stream.name)
        stream.write(source)
    try:
        subprocess.run(
            [
                "dotnet",
                "run",
                "--project",
                str(PATCH_PROJECT),
                "--configuration",
                "Release",
                "--",
                "coin-format",
                str(source_path),
                str(output),
            ],
            cwd=ROOT,
            check=True,
        )
        patched = output.read_bytes()
        if not patched.startswith(b"MZ"):
            raise RuntimeError("patched Hotfix.dll is not a PE file")
        return patched
    finally:
        source_path.unlink(missing_ok=True)


def verify(bundle: Path, expected: bytes) -> None:
    actual = read_text_asset(bundle, "Hotfix.dll")
    if actual != expected:
        raise RuntimeError("installed Hotfix.dll differs from generated output")


def install(dry_run: bool = False) -> None:
    if not BUNDLE.is_file():
        raise FileNotFoundError(BUNDLE)
    if not PATCH_PROJECT.is_file():
        raise FileNotFoundError(PATCH_PROJECT)

    # The first invocation freezes the exact client state being repaired.  A
    # later invocation rebuilds from that state instead of stacking IL edits.
    source_bundle = BACKUP if BACKUP.is_file() else BUNDLE
    source_hotfix = read_text_asset(source_bundle, "Hotfix.dll")
    with tempfile.TemporaryDirectory(prefix="shop-coin-format-") as temp:
        temp_dir = Path(temp)
        patched_hotfix = patch_dll(source_hotfix, temp_dir / "Hotfix.dll")
        patched_bundle = replace_text_asset(source_bundle, "Hotfix.dll", patched_hotfix, temp_dir / "bundle")
        if dry_run:
            print("dry-run bundle", BUNDLE)
            print("dry-run Hotfix.dll sha256", hashlib.sha256(patched_hotfix).hexdigest().upper())
            return

        if not BACKUP.is_file():
            shutil.copy2(BUNDLE, BACKUP)
        shutil.copy2(patched_bundle, BUNDLE)
        verify(BUNDLE, patched_hotfix)
        print("installed", BUNDLE)
        print("Hotfix.dll sha256", hashlib.sha256(patched_hotfix).hexdigest().upper())
        print("rollback copy", BACKUP)


def restore() -> None:
    if not BACKUP.is_file():
        raise FileNotFoundError(BACKUP)
    shutil.copy2(BACKUP, BUNDLE)
    print("restored", BUNDLE)
    print("source backup", BACKUP)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--restore", action="store_true")
    args = parser.parse_args()
    if args.restore:
        restore()
    else:
        install(dry_run=args.dry_run)
