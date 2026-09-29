# -*- coding: utf-8 -*-
"""Make every trade-mode bag drop ask for a quantity.

The compatibility trade workspace reuses StoreUI, but this patch is gated by
StoreUI.CodexTradeMode. Normal warehouse isMulti behavior and Hotfix.dll are
left untouched.
"""

import argparse
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import UnityPy

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "server-mysql", "tools"))
from client_patch_paths import patch_backup

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
PROJECT = os.path.join(
    ROOT,
    "tools",
    "client-trade-quantity-patcher",
    "ClientTradeQuantityPatcher.csproj",
)
BACKUP_SUFFIX = ".pre-trade-quantity.bak"


def asset_bytes(data):
    script = data.m_Script
    return script.encode("utf-8", "surrogateescape") if isinstance(script, str) else bytes(script)


def load_bundle_from_memory(bundle_path):
    with open(bundle_path, "rb") as stream:
        bundle_data = stream.read()
    if bundle_data[:8] != b"UnityFS\0":
        raise RuntimeError(f"config bundle is not plaintext UnityFS: {bundle_path}")
    env = UnityPy.Environment()
    env.load_file(bundle_data, name=os.path.basename(bundle_path))
    return env


def bundle_dlls(bundle_path):
    result = {}
    env = load_bundle_from_memory(bundle_path)
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name in {"Hotfix.dll", "HotfixView.dll"}:
            result[data.m_Name] = asset_bytes(data)
    if result.keys() != {"Hotfix.dll", "HotfixView.dll"}:
        raise RuntimeError(f"config bundle DLL TextAssets are incomplete: {bundle_path}")
    return result


def replace_hotfix_view(bundle_path, patched_dll, output_dir):
    env = load_bundle_from_memory(bundle_path)
    replaced = False
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name != "HotfixView.dll":
            continue
        data.m_Script = patched_dll.decode("utf-8", "surrogateescape")
        data.save()
        replaced = True
        break
    if not replaced:
        raise RuntimeError("HotfixView.dll TextAsset not found in config bundle")
    for file_item in env.files.values():
        file_item.mark_changed()
    env.save(pack="lz4", out_path=output_dir)
    output = os.path.join(output_dir, BUNDLE_NAME)
    if not os.path.isfile(output):
        raise RuntimeError("UnityPy did not create the patched config bundle")
    return output


def backup_once(path):
    backup = patch_backup(Path(path), BACKUP_SUFFIX)
    backup.parent.mkdir(parents=True, exist_ok=True)
    if not backup.exists():
        shutil.copy2(path, backup)
        print("backup", backup)


def client_paths(client_name):
    client_root = os.path.join(ROOT, client_name)
    data_root = next(
        (os.path.join(client_root, name) for name in os.listdir(client_root) if name.endswith("_Data")),
        None,
    )
    if data_root is None:
        raise FileNotFoundError(f"*_Data under {client_root}")
    yoo = os.path.join(data_root, "StreamingAssets", "yoo")
    return (
        os.path.join(yoo, "_extracted", "HotfixView.dll"),
        os.path.join(yoo, "aa0", BUNDLE_NAME),
        os.path.join(yoo, "_dec", "aa0", BUNDLE_NAME),
    )


def restore_client(client_name):
    paths = client_paths(client_name)
    missing = [patch_backup(Path(path), BACKUP_SUFFIX) for path in paths
               if os.path.isfile(path) and not patch_backup(Path(path), BACKUP_SUFFIX).is_file()]
    if missing:
        raise FileNotFoundError("missing trade quantity backups: " + ", ".join(str(path) for path in missing))
    for path in paths:
        backup = patch_backup(Path(path), BACKUP_SUFFIX)
        if backup.is_file():
            shutil.copy2(backup, path)
            print("restored", path)
    extracted, bundle, decoded = paths
    installed = bundle_dlls(bundle)
    with open(extracted, "rb") as stream:
        if stream.read() != installed["HotfixView.dll"]:
            raise RuntimeError("restored bundle and extracted HotfixView.dll differ")
    if os.path.isfile(decoded) and open(decoded, "rb").read() != open(bundle, "rb").read():
        raise RuntimeError("restored runtime and decoded config bundles differ")


def patch_client(client_name):
    extracted, bundle, decoded = client_paths(client_name)
    for path in (extracted, bundle):
        if not os.path.isfile(path):
            raise FileNotFoundError(path)

    with open(extracted, "rb") as stream:
        source_dll = stream.read()
    before_assets = bundle_dlls(bundle)
    if source_dll != before_assets["HotfixView.dll"]:
        raise RuntimeError("current bundle and extracted HotfixView.dll differ before patching")
    if os.path.isfile(decoded) and open(decoded, "rb").read() != open(bundle, "rb").read():
        raise RuntimeError("runtime and decoded config bundles differ before patching")

    with tempfile.TemporaryDirectory(prefix="trade-quantity-") as temp_dir:
        patched_path = os.path.join(temp_dir, "HotfixView.dll")
        subprocess.run(
            ["dotnet", "run", "--project", PROJECT, "-c", "Release", "--", extracted, patched_path],
            cwd=ROOT,
            check=True,
        )
        with open(patched_path, "rb") as stream:
            patched_dll = stream.read()
        if not patched_dll.startswith(b"MZ"):
            raise RuntimeError("patched HotfixView.dll is not a PE file")
        patched_bundle = replace_hotfix_view(bundle, patched_dll, temp_dir)
        after_assets = bundle_dlls(patched_bundle)
        if after_assets["Hotfix.dll"] != before_assets["Hotfix.dll"]:
            raise RuntimeError("Hotfix.dll changed while patching trade quantity")
        if after_assets["HotfixView.dll"] != patched_dll:
            raise RuntimeError("patched bundle HotfixView.dll differs from patch output")

        for path in (extracted, bundle):
            backup_once(path)
        if os.path.isfile(decoded):
            backup_once(decoded)
        shutil.copy2(patched_path, extracted)
        shutil.copy2(patched_bundle, bundle)
        if os.path.isfile(decoded):
            shutil.copy2(patched_bundle, decoded)

    installed = bundle_dlls(bundle)
    if installed["HotfixView.dll"] != patched_dll:
        raise RuntimeError("installed bundle and extracted HotfixView.dll differ")
    if os.path.isfile(decoded) and open(decoded, "rb").read() != open(bundle, "rb").read():
        raise RuntimeError("installed runtime and decoded config bundles differ")
    print("installed", client_name)
    print("HotfixView.dll sha256", hashlib.sha256(patched_dll).hexdigest().upper())
    print("config bundle sha256", hashlib.sha256(open(bundle, "rb").read()).hexdigest().upper())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--client", default="client-test", choices=("client-test", "client-127.0.0.1"))
    parser.add_argument("--restore", action="store_true")
    args = parser.parse_args()
    if args.restore:
        restore_client(args.client)
    else:
        patch_client(args.client)


if __name__ == "__main__":
    main()
