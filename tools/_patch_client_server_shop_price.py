# -*- coding: utf-8 -*-
"""Install the server-driven shop price patch into the local client bundle.

Only ``client-test`` is touched. The installer keeps one-time
``.pre-server-shop-price.bak`` rollback copies. Rebuilds always start from
the currently installed bundle, so repairs applied by another installer
(including the warehouse take-out repair) remain intact.  The generated DLL
copies and all rollback files are stored under ``server-mysql/client-patches``;
the playable client only retains the runtime bundle and its analysis junction.
"""

from __future__ import annotations

import argparse
import gc
import hashlib
import shutil
import subprocess
import tempfile
from pathlib import Path

import UnityPy

import sys


ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "server-mysql" / "tools"))
from client_patch_paths import BACKUPS, CURRENT, current_dll, patch_backup, runtime_bundle, runtime_decoded_bundle


BUNDLE = runtime_bundle()
DECODED = runtime_decoded_bundle()
BUNDLE_NAME = BUNDLE.name
PATCH_PROJECT = ROOT / "tools" / "client-market-price-patcher" / "ClientMarketPricePatcher.csproj"
BACKUP_SUFFIX = ".pre-server-shop-price.bak"


def backup_once(path: Path) -> None:
    if not path.is_file():
        return
    backup = patch_backup(path, BACKUP_SUFFIX)
    backup.parent.mkdir(parents=True, exist_ok=True)
    if backup.exists():
        return
    shutil.copy2(path, backup)
    print("backup", backup)


def asset_bytes(data) -> bytes:
    raw = data.m_Script
    return raw.encode("utf-8", errors="surrogateescape") if isinstance(raw, str) else bytes(raw)


def bundle_assets(path: Path) -> dict[str, bytes]:
    env = UnityPy.load(str(path))
    result: dict[str, bytes] = {}
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if str(data.m_Name) in {"Hotfix.dll", "HotfixView.dll"}:
            result[str(data.m_Name)] = asset_bytes(data)
    del env
    gc.collect()
    if set(result) != {"Hotfix.dll", "HotfixView.dll"}:
        raise RuntimeError(f"config bundle DLL TextAssets are incomplete: {path}")
    return result


def patch_dll(mode: str, source: bytes, destination: Path) -> bytes:
    with tempfile.TemporaryDirectory(prefix="server-shop-price-source-") as temp:
        source_path = Path(temp) / ("Hotfix.dll" if mode == "hotfix" else "HotfixView.dll")
        source_path.write_bytes(source)
        subprocess.run(
            [
                "dotnet", "run", "--project", str(PATCH_PROJECT),
                "--configuration", "Release", "--no-restore", "--",
                mode, str(source_path), str(destination),
            ],
            cwd=ROOT,
            check=True,
        )
    data = destination.read_bytes()
    if not data.startswith(b"MZ"):
        raise RuntimeError(f"{destination} is not a PE file")
    return data


def replace_text_assets(bundle: Path, replacements: dict[str, bytes], output_dir: Path) -> Path:
    env = UnityPy.load(str(bundle))
    found: set[str] = set()
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        name = str(data.m_Name)
        if name not in replacements:
            continue
        data.m_Script = replacements[name].decode("utf-8", errors="surrogateescape")
        data.save()
        found.add(name)
    missing = set(replacements) - found
    if missing:
        raise RuntimeError(f"bundle TextAssets missing: {sorted(missing)}")
    for file_item in env.files.values():
        file_item.mark_changed()
    output_dir.mkdir(parents=True, exist_ok=True)
    env.save(pack="lz4", out_path=str(output_dir))
    output = output_dir / bundle.name
    if not output.is_file():
        raise RuntimeError(f"UnityPy did not write {output}")
    return output


def verify_bundle(bundle: Path, replacements: dict[str, bytes]) -> None:
    env = UnityPy.load(str(bundle))
    found: set[str] = set()
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        name = str(data.m_Name)
        if name not in replacements:
            continue
        raw = data.m_Script
        raw = raw.encode("utf-8", errors="surrogateescape") if isinstance(raw, str) else bytes(raw)
        if raw != replacements[name]:
            raise RuntimeError(f"{name} in bundle differs from patched DLL")
        found.add(name)
    if found != set(replacements):
        raise RuntimeError(f"bundle verification missing: {sorted(set(replacements) - found)}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dry-run", action="store_true", help="patch temporary files without installing")
    parser.add_argument("--restore", action="store_true", help="restore this patch's centralized pre-install snapshot")
    args = parser.parse_args()

    if not BUNDLE.is_file():
        raise FileNotFoundError(BUNDLE)
    if args.restore:
        bundle_backup = patch_backup(BUNDLE, BACKUP_SUFFIX)
        if not bundle_backup.is_file():
            raise FileNotFoundError(bundle_backup)
        shutil.copy2(bundle_backup, BUNDLE)
        if DECODED.is_file() or patch_backup(DECODED, BACKUP_SUFFIX).is_file():
            DECODED.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(bundle_backup, DECODED)
        for name in ("Hotfix.dll", "HotfixView.dll"):
            target = current_dll(name)
            backup = patch_backup(target, BACKUP_SUFFIX)
            if not backup.is_file():
                raise FileNotFoundError(backup)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(backup, target)
        current_bundle = CURRENT / BUNDLE_NAME
        current_bundle.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(bundle_backup, current_bundle)
        verify_bundle(BUNDLE, bundle_assets(BUNDLE))
        print("restored", BUNDLE)
        print("source backup", bundle_backup)
        return
    before = bundle_assets(BUNDLE)

    with tempfile.TemporaryDirectory(prefix="server-shop-price-") as temp:
        temp_dir = Path(temp)
        # Rebuild from the currently installed bundle so later client repairs
        # (for example warehouse take-out) are retained in the new DLL.
        patched_hotfix = patch_dll("hotfix", before["Hotfix.dll"], temp_dir / "Hotfix.dll")
        patched_view = patch_dll("view", before["HotfixView.dll"], temp_dir / "HotfixView.dll")
        patched_bundle = replace_text_assets(
            BUNDLE,
            {"Hotfix.dll": patched_hotfix, "HotfixView.dll": patched_view},
            temp_dir / "bundle",
        )
        if args.dry_run:
            print("dry-run Hotfix.dll", hashlib.sha256(patched_hotfix).hexdigest().upper())
            print("dry-run HotfixView.dll", hashlib.sha256(patched_view).hexdigest().upper())
            return

        backup_once(BUNDLE)
        backup_once(DECODED)
        for name, data in (("Hotfix.dll", patched_hotfix), ("HotfixView.dll", patched_view)):
            target = current_dll(name)
            backup_once(target)
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        shutil.copy2(patched_bundle, BUNDLE)
        if DECODED.is_file() or patch_backup(DECODED, BACKUP_SUFFIX).is_file():
            DECODED.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(patched_bundle, DECODED)
        current_bundle = CURRENT / BUNDLE_NAME
        backup_once(current_bundle)
        current_bundle.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(patched_bundle, current_bundle)
        verify_bundle(BUNDLE, {"Hotfix.dll": patched_hotfix, "HotfixView.dll": patched_view})
        print("installed", BUNDLE)
        print("Hotfix.dll sha256", hashlib.sha256(patched_hotfix).hexdigest().upper())
        print("HotfixView.dll sha256", hashlib.sha256(patched_view).hexdigest().upper())
        print("rollback copies: *.pre-server-shop-price.bak")


if __name__ == "__main__":
    main()
