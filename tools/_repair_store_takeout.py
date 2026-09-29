# -*- coding: utf-8 -*-
"""Repair the local warehouse take-out entry point.

The old trade patch added a guard directly to the async ``SendPutOutProto``
wrapper.  Mono validates that wrapper before executing its branch, so the
invalid body prevents ordinary warehouse take-out from ever sending opcode
20194.  This installer removes only that known prefix from the embedded
``HotfixView.dll`` and leaves ``Hotfix.dll`` and the server untouched.
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
from client_patch_paths import (
    CURRENT,
    current_dll,
    patch_backup,
    runtime_bundle,
    runtime_decoded_bundle,
)


BUNDLE = runtime_bundle()
DECODED = runtime_decoded_bundle()
PATCH_PROJECT = ROOT / "tools" / "client-trade-patcher" / "ClientTradePatcher.csproj"
BACKUP_SUFFIX = ".pre-store-takeout-fix.bak"


def asset_bytes(data) -> bytes:
    raw = data.m_Script
    return raw.encode("utf-8", errors="surrogateescape") if isinstance(raw, str) else bytes(raw)


def bundle_assets(path: Path) -> dict[str, bytes]:
    if path.read_bytes()[:8] != b"UnityFS\0":
        raise RuntimeError(f"config bundle is not plaintext UnityFS: {path}")
    env = UnityPy.load(str(path))
    result: dict[str, bytes] = {}
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name in {"Hotfix.dll", "HotfixView.dll"}:
            result[str(data.m_Name)] = asset_bytes(data)
    if set(result) != {"Hotfix.dll", "HotfixView.dll"}:
        raise RuntimeError(f"config bundle DLL TextAssets are incomplete: {path}")
    del env
    gc.collect()
    return result


def replace_view(path: Path, view: bytes, output_dir: Path) -> Path:
    env = UnityPy.load(str(path))
    replaced = False
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name != "HotfixView.dll":
            continue
        data.m_Script = view.decode("utf-8", errors="surrogateescape")
        data.save()
        replaced = True
        break
    if not replaced:
        raise RuntimeError("HotfixView.dll TextAsset not found")
    for file_item in env.files.values():
        file_item.mark_changed()
    output_dir.mkdir(parents=True, exist_ok=True)
    env.save(pack="lz4", out_path=str(output_dir))
    output = output_dir / path.name
    if not output.is_file():
        raise RuntimeError(f"UnityPy did not write {output}")
    del env
    gc.collect()
    return output


def backup_once(path: Path) -> None:
    if not path.is_file():
        return
    backup = patch_backup(path, BACKUP_SUFFIX)
    backup.parent.mkdir(parents=True, exist_ok=True)
    if not backup.exists():
        shutil.copy2(path, backup)
        print("backup", backup)


def repair_dll(view: bytes, output: Path) -> bytes:
    with tempfile.TemporaryDirectory(prefix="store-takeout-repair-") as temp:
        source = Path(temp) / "HotfixView.dll"
        source.write_bytes(view)
        subprocess.run(
            [
                "dotnet",
                "run",
                "--project",
                str(PATCH_PROJECT),
                "--configuration",
                "Release",
                "--no-restore",
                "--",
                "--repair-store-takeout",
                str(source),
                str(output),
            ],
            cwd=ROOT,
            check=True,
        )
    repaired = output.read_bytes()
    if not repaired.startswith(b"MZ"):
        raise RuntimeError("repaired HotfixView.dll is not a PE file")
    return repaired


def verify(path: Path, expected_hotfix: bytes, expected_view: bytes) -> None:
    actual = bundle_assets(path)
    if actual["Hotfix.dll"] != expected_hotfix:
        raise RuntimeError("Hotfix.dll changed while repairing store take-out")
    if actual["HotfixView.dll"] != expected_view:
        raise RuntimeError("bundle HotfixView.dll differs from repaired DLL")


def install(dry_run: bool) -> None:
    before = bundle_assets(BUNDLE)
    with tempfile.TemporaryDirectory(prefix="store-takeout-bundle-") as temp:
        temp_dir = Path(temp)
        patched_view = repair_dll(before["HotfixView.dll"], temp_dir / "HotfixView.dll")
        patched_bundle = replace_view(BUNDLE, patched_view, temp_dir / "bundle")
        verify(patched_bundle, before["Hotfix.dll"], patched_view)
        if dry_run:
            print("dry-run HotfixView.dll sha256", hashlib.sha256(patched_view).hexdigest().upper())
            print("dry-run config bundle sha256", hashlib.sha256(patched_bundle.read_bytes()).hexdigest().upper())
            return

        backup_once(BUNDLE)
        shutil.copy2(patched_bundle, BUNDLE)
        if DECODED.is_file():
            backup_once(DECODED)
            shutil.copy2(patched_bundle, DECODED)

        # Keep the central analysis copies synchronized.  They are deliberately
        # outside the playable client tree.
        for name, data in (("Hotfix.dll", before["Hotfix.dll"]), ("HotfixView.dll", patched_view)):
            target = current_dll(name)
            target.parent.mkdir(parents=True, exist_ok=True)
            if target.is_file():
                backup_once(target)
            target.write_bytes(data)
        current_bundle = CURRENT / BUNDLE.name
        backup_once(current_bundle)
        current_bundle.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(patched_bundle, current_bundle)
        verify(BUNDLE, before["Hotfix.dll"], patched_view)

    print("installed", BUNDLE)
    print("HotfixView.dll sha256", hashlib.sha256(patched_view).hexdigest().upper())
    print("config bundle sha256", hashlib.sha256(BUNDLE.read_bytes()).hexdigest().upper())
    print("rollback copy:", BUNDLE.name + BACKUP_SUFFIX)


def restore() -> None:
    for path in (BUNDLE, DECODED, CURRENT / BUNDLE.name,
                 current_dll("Hotfix.dll"), current_dll("HotfixView.dll")):
        backup = patch_backup(path, BACKUP_SUFFIX)
        if not backup.is_file():
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(backup, path)
        print("restored", path)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--restore", action="store_true")
    args = parser.parse_args()
    if args.restore:
        restore()
    else:
        install(args.dry_run)


if __name__ == "__main__":
    main()
