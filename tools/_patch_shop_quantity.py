# -*- coding: utf-8 -*-
"""Make ordinary and yuanbao-market purchases ask for a quantity.

The original client only opens the quantity tip while Ctrl is held. The
server already validates and applies C2M_BuyInShop/Market.Count, so this patch
changes only the two HotfixView conditional branches. Existing Hotfix.dll
patches are left untouched.
"""

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
PATCH_PROJECT = os.path.join(ROOT, "tools", "client-shop-quantity-patcher", "ClientShopQuantityPatcher.csproj")
BACKUP_SUFFIX = ".pre-shop-quantity.bak"


def text_asset_bytes(data):
    script = data.m_Script
    if isinstance(script, str):
        return script.encode("utf-8", errors="surrogateescape")
    return bytes(script)


def replace_hotfix_view(bundle_path, patched_dll, output_dir):
    env = UnityPy.load(bundle_path)
    replaced = False
    for obj in env.objects:
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
        raise RuntimeError("HotfixView.dll TextAsset not found in config bundle")
    for file_item in env.files.values():
        file_item.mark_changed()
    env.save(pack="lz4", out_path=output_dir)
    output = os.path.join(output_dir, BUNDLE_NAME)
    if not os.path.exists(output):
        raise RuntimeError("UnityPy did not create the patched config bundle")
    return output


def backup_once(path):
    backup = path + BACKUP_SUFFIX
    if not os.path.exists(backup):
        shutil.copy2(path, backup)
        print("backup", backup)


def verify_bundle(bundle_path, expected_dll):
    with open(bundle_path, "rb") as stream:
        if stream.read(8) != b"UnityFS\0":
            raise RuntimeError("patched config bundle is not plaintext UnityFS")
    env = UnityPy.load(bundle_path)
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name == "HotfixView.dll":
            if text_asset_bytes(data) != expected_dll:
                raise RuntimeError("installed bundle HotfixView.dll differs from patched DLL")
            return
    raise RuntimeError("installed bundle does not contain HotfixView.dll")


def patch_client(client_name, source_dll=None):
    client_root = os.path.join(ROOT, client_name)
    data_root = next(
        (os.path.join(client_root, name) for name in os.listdir(client_root) if name.endswith("_Data")),
        None,
    )
    if data_root is None:
        raise FileNotFoundError(f"*_Data under {client_root}")
    yoo = os.path.join(data_root, "StreamingAssets", "yoo")
    extracted = os.path.join(yoo, "_extracted", "HotfixView.dll")
    source_dll = os.path.abspath(source_dll) if source_dll else extracted
    bundle = os.path.join(yoo, "aa0", BUNDLE_NAME)
    decoded_bundle = os.path.join(yoo, "_dec", "aa0", BUNDLE_NAME)
    for path in (extracted, bundle):
        if not os.path.isfile(path):
            raise FileNotFoundError(path)

    with tempfile.TemporaryDirectory(prefix="shop-quantity-") as temp_dir:
        patched_path = os.path.join(temp_dir, "HotfixView.dll")
        subprocess.run(
            ["dotnet", "run", "--project", PATCH_PROJECT, "--configuration", "Release", "--", source_dll, patched_path],
            cwd=ROOT,
            check=True,
        )
        with open(patched_path, "rb") as stream:
            patched = stream.read()
        if not patched.startswith(b"MZ"):
            raise RuntimeError("patched HotfixView.dll is not a PE file")
        patched_bundle = replace_hotfix_view(bundle, patched, temp_dir)

        backup_once(extracted)
        backup_once(bundle)
        shutil.copy2(patched_path, extracted)
        shutil.copy2(patched_bundle, bundle)
        if os.path.isfile(decoded_bundle):
            backup_once(decoded_bundle)
            shutil.copy2(patched_bundle, decoded_bundle)

    verify_bundle(bundle, patched)
    print("installed", client_name)
    print("HotfixView.dll sha256", hashlib.sha256(patched).hexdigest().upper())
    print("config bundle sha256", hashlib.sha256(open(bundle, "rb").read()).hexdigest().upper())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--client", default="client-test", choices=("client-test", "client-127.0.0.1"))
    parser.add_argument("--source-dll", help="use an unpatched HotfixView.dll as the patch input")
    args = parser.parse_args()
    patch_client(args.client, args.source_dll)


if __name__ == "__main__":
    main()
