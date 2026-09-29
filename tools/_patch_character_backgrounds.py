# -*- coding: utf-8 -*-
"""Install or restore the local character-background client patch."""

from __future__ import annotations

import argparse
import hashlib
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

import UnityPy


ROOT = Path(__file__).resolve().parent.parent
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
PROJECT = ROOT / "tools" / "client-character-background-patcher" / "ClientCharacterBackgroundPatcher.csproj"
SUFFIX = ".pre-background-only-rebuild-20260824.bak"
BACKGROUND_BASE_SUFFIX = ".pre-character-backgrounds.bak"
PREPARED = ROOT / "assets" / "character-backgrounds" / "prepared"


def asset_bytes(data) -> bytes:
    value = data.m_Script
    return value.encode("utf-8", "surrogateescape") if isinstance(value, str) else bytes(value)


def replace(bundle: Path, dll: bytes, output_dir: Path) -> Path:
    env = UnityPy.load(str(bundle))
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name == "HotfixView.dll":
            data.m_Script = dll.decode("utf-8", "surrogateescape")
            data.save()
            break
    else:
        raise RuntimeError("HotfixView.dll TextAsset not found")
    for item in env.files.values():
        item.mark_changed()
    env.save(pack="lz4", out_path=str(output_dir))
    output = output_dir / BUNDLE_NAME
    if not output.is_file():
        raise RuntimeError("UnityPy did not write patched config bundle")
    return output


def backup_once(path: Path) -> None:
    target = Path(str(path) + SUFFIX)
    if not target.exists():
        shutil.copy2(path, target)
        print("backup", target)


def restore(client: str) -> None:
    root = ROOT / client
    data_root = next(path for path in root.iterdir() if path.name.endswith("_Data"))
    yoo = data_root / "StreamingAssets" / "yoo"
    targets = (
        yoo / "_extracted" / "HotfixView.dll",
        yoo / "aa0" / BUNDLE_NAME,
        yoo / "_dec" / "aa0" / BUNDLE_NAME,
    )
    for target in targets:
        backup = Path(str(target) + SUFFIX)
        if backup.is_file():
            shutil.copy2(backup, target)
            print("restored", target)
    background_dir = data_root / "StreamingAssets" / "CharacterBackgrounds"
    resource_backup = Path(str(background_dir) + SUFFIX)
    if background_dir.exists():
        shutil.rmtree(background_dir)
        print("removed", background_dir)
    if resource_backup.is_dir():
        shutil.copytree(resource_backup, background_dir)
        print("restored", background_dir)


def patch(client: str, background_only: bool = False) -> None:
    if client != "client-test":
        raise RuntimeError("character backgrounds are local-only; refusing to modify the public client")
    root = ROOT / client
    data_root = next(path for path in root.iterdir() if path.name.endswith("_Data"))
    yoo = data_root / "StreamingAssets" / "yoo"
    extracted = yoo / "_extracted" / "HotfixView.dll"
    bundle = yoo / "aa0" / BUNDLE_NAME
    decoded = yoo / "_dec" / "aa0" / BUNDLE_NAME
    background_dir = data_root / "StreamingAssets" / "CharacterBackgrounds"
    if not extracted.is_file() or not bundle.is_file():
        raise FileNotFoundError("local HotfixView.dll/config bundle is missing")
    patch_input = extracted
    if background_only:
        patch_input = Path(str(extracted) + BACKGROUND_BASE_SUFFIX)
        if not patch_input.is_file():
            raise FileNotFoundError(f"background-only baseline is missing: {patch_input}")
    resources = sorted((ROOT / "assets" / "character-backgrounds" / "generated").glob("*.png"))
    if len(resources) != 20:
        raise RuntimeError(f"expected 20 generated backgrounds, found {len(resources)}")
    with tempfile.TemporaryDirectory(prefix="character-backgrounds-") as temp_name:
        temp = Path(temp_name)
        patched_path = temp / "HotfixView.dll"
        subprocess.run(
            ["dotnet", "run", "--project", str(PROJECT), "-c", "Release", "--", str(patch_input), str(patched_path)],
            cwd=ROOT,
            check=True,
        )
        patched = patched_path.read_bytes()
        if background_only:
            forbidden = (b"RefreshCharacterSkin", b"RefreshCharacterSkinFromEvent", b"characterDisplayedSkinId")
            present = [name.decode("ascii") for name in forbidden if name in patched]
            if present:
                raise RuntimeError(f"skin refresh patch remains in background-only DLL: {present}")
        packed = replace(bundle, patched, temp)
        for path in (extracted, bundle):
            backup_once(path)
        if decoded.is_file():
            backup_once(decoded)
        shutil.copy2(patched_path, extracted)
        shutil.copy2(packed, bundle)
        if decoded.is_file():
            shutil.copy2(packed, decoded)
    if not PREPARED.is_dir() or len(list(PREPARED.glob("*.png"))) != 20:
        raise RuntimeError("run tools/_prepare_character_backgrounds.py first")
    if background_dir.exists():
        resource_backup = Path(str(background_dir) + SUFFIX)
        if not resource_backup.exists():
            shutil.copytree(background_dir, resource_backup)
            print("backup", resource_backup)
        shutil.rmtree(background_dir)
    shutil.copytree(PREPARED, background_dir)
    verify(bundle, patched)
    print("installed", client)
    print("HotfixView.dll sha256", hashlib.sha256(patched).hexdigest().upper())
    print("config bundle sha256", hashlib.sha256(bundle.read_bytes()).hexdigest().upper())


def verify(bundle: Path, expected: bytes) -> None:
    env = UnityPy.load(str(bundle))
    for obj in env.objects:
        if obj.type.name == "TextAsset":
            data = obj.read()
            if data.m_Name == "HotfixView.dll":
                if asset_bytes(data) != expected:
                    raise RuntimeError("bundle HotfixView.dll differs from installed DLL")
                return
    raise RuntimeError("bundle HotfixView.dll missing")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--client", default="client-test", choices=("client-test", "client-127.0.0.1"))
    parser.add_argument("--restore", action="store_true")
    parser.add_argument("--background-only", action="store_true",
                        help="rebuild from the pre-background baseline, excluding the skin refresh patch")
    args = parser.parse_args()
    if args.restore:
        restore(args.client)
    else:
        patch(args.client, args.background_only)


if __name__ == "__main__":
    main()
