# -*- coding: utf-8 -*-
"""Install or restore immediate character-model refresh after worn-item updates."""

from __future__ import annotations

import argparse
import hashlib
import shutil
import subprocess
import tempfile
from pathlib import Path

import UnityPy


ROOT = Path(__file__).resolve().parent.parent
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
PROJECT = ROOT / "tools" / "client-skin-refresh-patcher" / "ClientSkinRefreshPatcher.csproj"
SUFFIX = ".pre-skin-refresh.bak"


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
    result = output_dir / BUNDLE_NAME
    if not result.is_file():
        raise RuntimeError("UnityPy did not write patched config bundle")
    return result


def backup_once(path: Path) -> None:
    target = Path(str(path) + SUFFIX)
    if not target.exists():
        shutil.copy2(path, target)
        print("backup", target)


def restore(client: str) -> None:
    root = ROOT / client
    data_root = next(path for path in root.iterdir() if path.name.endswith("_Data"))
    yoo = data_root / "StreamingAssets" / "yoo"
    for target in (yoo / "_extracted" / "HotfixView.dll", yoo / "aa0" / BUNDLE_NAME,
                   yoo / "_dec" / "aa0" / BUNDLE_NAME):
        backup = Path(str(target) + SUFFIX)
        if backup.is_file():
            shutil.copy2(backup, target)
            print("restored", target)


def patch(client: str) -> None:
    if client != "client-test":
        raise RuntimeError("skin refresh is local-only; refusing to modify the public client")
    root = ROOT / client
    data_root = next(path for path in root.iterdir() if path.name.endswith("_Data"))
    yoo = data_root / "StreamingAssets" / "yoo"
    extracted = yoo / "_extracted" / "HotfixView.dll"
    bundle = yoo / "aa0" / BUNDLE_NAME
    decoded = yoo / "_dec" / "aa0" / BUNDLE_NAME
    with tempfile.TemporaryDirectory(prefix="skin-refresh-") as temp_name:
        temp = Path(temp_name)
        output = temp / "HotfixView.dll"
        subprocess.run(
            ["dotnet", "run", "--project", str(PROJECT), "-c", "Release", "--", str(extracted), str(output)],
            cwd=ROOT,
            check=True,
        )
        patched = output.read_bytes()
        packed = replace(bundle, patched, temp)
        for path in (extracted, bundle):
            backup_once(path)
        if decoded.is_file():
            backup_once(decoded)
        shutil.copy2(output, extracted)
        shutil.copy2(packed, bundle)
        if decoded.is_file():
            shutil.copy2(packed, decoded)
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
    args = parser.parse_args()
    if args.restore:
        restore(args.client)
    else:
        patch(args.client)


if __name__ == "__main__":
    main()
