# -*- coding: utf-8 -*-
"""Install the confirmation-based ForceOffLine handler in a client."""

import argparse
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

import UnityPy


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
PROJECT = os.path.join(
    ROOT,
    "tools",
    "client-force-offline-patcher",
    "ClientForceOfflinePatcher.csproj",
)
BACKUP_SUFFIX = ".pre-force-offline.bak"


def text_asset_bytes(data):
    script = data.m_Script
    if isinstance(script, str):
        return script.encode("utf-8", errors="surrogateescape")
    return bytes(script)


def replace_hotfix(bundle_path, patched_dll, output_dir):
    environment = UnityPy.load(bundle_path)
    replaced = False
    for obj in environment.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name != "Hotfix.dll":
            continue
        data.m_Script = patched_dll.decode("utf-8", errors="surrogateescape")
        data.save()
        replaced = True
        break
    if not replaced:
        raise RuntimeError("Hotfix.dll TextAsset not found in config bundle")
    for file_item in environment.files.values():
        file_item.mark_changed()
    environment.save(pack="lz4", out_path=output_dir)
    output = os.path.join(output_dir, BUNDLE_NAME)
    if not os.path.isfile(output):
        raise RuntimeError("UnityPy did not create the patched bundle")
    return output


def backup_once(path):
    backup_path = path + BACKUP_SUFFIX
    if not os.path.exists(backup_path):
        shutil.copy2(path, backup_path)
        print("backup", backup_path)


def verify_bundle(bundle_path, expected_dll):
    environment = UnityPy.load(bundle_path)
    for obj in environment.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name == "Hotfix.dll":
            if text_asset_bytes(data) != expected_dll:
                raise RuntimeError("installed bundle Hotfix.dll differs from patched DLL")
            return
    raise RuntimeError("installed bundle does not contain Hotfix.dll")


def patch_client(client_name):
    client_root = os.path.join(ROOT, client_name)
    data_root = next(
        os.path.join(client_root, name)
        for name in os.listdir(client_root)
        if name.endswith("_Data")
    )
    yoo_root = os.path.join(data_root, "StreamingAssets", "yoo")
    extracted = os.path.join(yoo_root, "_extracted", "Hotfix.dll")
    bundle = os.path.join(yoo_root, "aa0", BUNDLE_NAME)
    decoded_bundle = os.path.join(yoo_root, "_dec", "aa0", BUNDLE_NAME)
    for path in (extracted, bundle):
        if not os.path.isfile(path):
            raise FileNotFoundError(path)

    with tempfile.TemporaryDirectory(prefix="force-offline-") as temp_dir:
        patched_path = os.path.join(temp_dir, "Hotfix.dll")
        subprocess.run(
            [
                "dotnet",
                "run",
                "--project",
                PROJECT,
                "--configuration",
                "Release",
                "--",
                extracted,
                patched_path,
            ],
            cwd=ROOT,
            check=True,
        )
        with open(patched_path, "rb") as stream:
            patched = stream.read()
        if not patched.startswith(b"MZ"):
            raise RuntimeError("patched Hotfix.dll is not a PE file")
        patched_bundle = replace_hotfix(bundle, patched, temp_dir)

        for path in (extracted, bundle):
            backup_once(path)
        shutil.copy2(patched_path, extracted)
        shutil.copy2(patched_bundle, bundle)
        if os.path.isfile(decoded_bundle):
            backup_once(decoded_bundle)
            shutil.copy2(patched_bundle, decoded_bundle)

    verify_bundle(bundle, patched)
    print("installed", client_name)
    print("Hotfix.dll sha256", hashlib.sha256(patched).hexdigest().upper())
    print("config bundle sha256", hashlib.sha256(open(bundle, "rb").read()).hexdigest().upper())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--client",
        default="client-test",
        choices=("client-test", "client-127.0.0.1"),
    )
    patch_client(parser.parse_args().client)


if __name__ == "__main__":
    main()
