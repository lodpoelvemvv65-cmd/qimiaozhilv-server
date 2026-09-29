# -*- coding: utf-8 -*-
"""Install or restore the local client-assisted launcher login patch."""

import argparse
import gc
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

import UnityPy

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from client_patch_paths import patch_backup

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

REPOSITORY_ROOT = Path(__file__).resolve().parents[4]
CLIENT_ROOT = REPOSITORY_ROOT / "client-test"
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
BACKUP_SUFFIX = ".pre-launcher-direct-login.bak"
PATCH_PROJECT = Path(__file__).with_name("ClientLauncherLoginPatcher.csproj")


def client_paths():
    data_roots = sorted(CLIENT_ROOT.glob("*_Data"))
    if len(data_roots) != 1:
        raise RuntimeError(f"expected exactly one *_Data directory under {CLIENT_ROOT}")
    yoo = data_roots[0] / "StreamingAssets" / "yoo"
    return (
        yoo / "_extracted" / "HotfixView.dll",
        yoo / "aa0" / BUNDLE_NAME,
        yoo / "_dec" / "aa0" / BUNDLE_NAME,
    )


def text_asset_bytes(data):
    script = data.m_Script
    if isinstance(script, str):
        return script.encode("utf-8", errors="surrogateescape")
    return bytes(script)


def dispose_environment(environment):
    readers = {}
    for obj in environment.objects:
        reader = getattr(obj.assets_file, "reader", None)
        if reader is not None:
            readers[id(reader)] = reader
    for reader in readers.values():
        reader.dispose()
    del readers
    gc.collect()


def extract_hotfix_view(bundle_path):
    environment = UnityPy.load(str(bundle_path))
    try:
        for obj in environment.objects:
            if obj.type.name != "TextAsset":
                continue
            data = obj.read()
            if data.m_Name == "HotfixView.dll":
                return text_asset_bytes(data)
        raise RuntimeError(f"HotfixView.dll TextAsset not found in {bundle_path}")
    finally:
        dispose_environment(environment)


def replace_hotfix_view(bundle_path, patched_dll, output_dir):
    environment = UnityPy.load(str(bundle_path))
    try:
        replaced = False
        for obj in environment.objects:
            if obj.type.name != "TextAsset":
                continue
            data = obj.read()
            if data.m_Name != "HotfixView.dll":
                continue
            data.m_Script = patched_dll.decode("utf-8", errors="surrogateescape")
            data.save()
            replaced = True
            break
        if not replaced:
            raise RuntimeError("HotfixView.dll TextAsset not found in current config bundle")
        for file_item in environment.files.values():
            file_item.mark_changed()
        environment.save(pack="lz4", out_path=str(output_dir))
    finally:
        dispose_environment(environment)
    output = output_dir / BUNDLE_NAME
    if not output.is_file():
        raise RuntimeError("UnityPy did not create the patched config bundle")
    return output


def ensure_plain_bundle(path):
    with path.open("rb") as stream:
        if stream.read(8) != b"UnityFS\0":
            raise RuntimeError(f"config bundle is not plaintext UnityFS: {path}")


def backup_once(path):
    backup = patch_backup(Path(path), BACKUP_SUFFIX)
    backup.parent.mkdir(parents=True, exist_ok=True)
    if not backup.exists():
        shutil.copy2(path, backup)
        print("backup", backup)


def atomic_write(path, content):
    temporary = path.with_name(path.name + ".launcher-direct-login.tmp")
    try:
        temporary.write_bytes(content)
        os.replace(temporary, path)
    finally:
        if temporary.exists():
            temporary.unlink()


def verify_consistent(extracted, bundle, decoded=None):
    installed_dll = extracted.read_bytes()
    if extract_hotfix_view(bundle) != installed_dll:
        raise RuntimeError("runtime bundle and extracted HotfixView.dll differ")
    if decoded is not None and decoded.is_file():
        if extract_hotfix_view(decoded) != installed_dll:
            raise RuntimeError("decoded bundle and extracted HotfixView.dll differ")


def install():
    extracted, bundle, decoded = client_paths()
    for path in (extracted, bundle):
        if not path.is_file():
            raise FileNotFoundError(path)
    ensure_plain_bundle(bundle)
    source_dll = extract_hotfix_view(bundle)
    if not source_dll.startswith(b"MZ"):
        raise RuntimeError("current bundle HotfixView.dll is not a PE file")
    if extracted.read_bytes() != source_dll:
        raise RuntimeError("current bundle and extracted HotfixView.dll differ before patching")
    if decoded.is_file() and extract_hotfix_view(decoded) != source_dll:
        raise RuntimeError("current bundle and decoded bundle differ before patching")

    with tempfile.TemporaryDirectory(prefix="launcher-direct-login-") as temp_name:
        temp_dir = Path(temp_name)
        input_dll = temp_dir / "HotfixView.input.dll"
        patched_path = temp_dir / "HotfixView.dll"
        input_dll.write_bytes(source_dll)
        subprocess.run(
            [
                "dotnet",
                "run",
                "--project",
                str(PATCH_PROJECT),
                "--configuration",
                "Release",
                "--",
                str(input_dll),
                str(patched_path),
            ],
            cwd=REPOSITORY_ROOT,
            check=True,
        )
        patched_dll = patched_path.read_bytes()
        if not patched_dll.startswith(b"MZ"):
            raise RuntimeError("patched HotfixView.dll is not a PE file")
        if patched_dll == source_dll:
            verify_consistent(extracted, bundle, decoded)
            print("launcher direct-login patch is already installed")
            return

        patched_bundle_path = replace_hotfix_view(bundle, patched_dll, temp_dir)
        ensure_plain_bundle(patched_bundle_path)
        if extract_hotfix_view(patched_bundle_path) != patched_dll:
            raise RuntimeError("generated bundle does not contain the patched HotfixView.dll")
        patched_bundle = patched_bundle_path.read_bytes()

    backup_once(extracted)
    backup_once(bundle)
    if decoded.is_file():
        backup_once(decoded)
    atomic_write(extracted, patched_dll)
    atomic_write(bundle, patched_bundle)
    if decoded.is_file():
        atomic_write(decoded, patched_bundle)

    verify_consistent(extracted, bundle, decoded)
    print("installed client-test launcher direct-login patch")
    print("HotfixView.dll sha256", hashlib.sha256(patched_dll).hexdigest().upper())
    print("config bundle sha256", hashlib.sha256(patched_bundle).hexdigest().upper())


def restore():
    extracted, bundle, decoded = client_paths()
    required = (extracted, bundle)
    missing = [patch_backup(path, BACKUP_SUFFIX) for path in required
               if not patch_backup(path, BACKUP_SUFFIX).is_file()]
    if missing:
        raise FileNotFoundError("missing launcher patch backup: " + ", ".join(map(str, missing)))

    targets = [extracted, bundle]
    if patch_backup(decoded, BACKUP_SUFFIX).is_file():
        targets.append(decoded)
    for path in targets:
        backup = patch_backup(path, BACKUP_SUFFIX)
        atomic_write(path, backup.read_bytes())
        print("restored", path)
    verify_consistent(extracted, bundle, decoded if decoded in targets else None)
    print("restored client-test state from launcher direct-login backups")


def main():
    parser = argparse.ArgumentParser()
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--install", action="store_true")
    action.add_argument("--restore", action="store_true")
    args = parser.parse_args()
    if args.install:
        install()
    else:
        restore()


if __name__ == "__main__":
    main()
