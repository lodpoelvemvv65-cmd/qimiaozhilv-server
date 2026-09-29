# -*- coding: utf-8 -*-
"""Patch the local market tooltip to show fixed 999999 prices for item 110836."""

import hashlib
import os
import shutil
import subprocess
import tempfile
import argparse

import UnityPy


ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CLIENT_ROOT = next(os.path.join(ROOT, "client-test", name)
                   for name in os.listdir(os.path.join(ROOT, "client-test"))
                   if name.endswith("_Data"))
YOO_ROOT = os.path.join(CLIENT_ROOT, "StreamingAssets", "yoo")
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
BUNDLE = os.path.join(YOO_ROOT, "aa0", BUNDLE_NAME)
DECODED = os.path.join(YOO_ROOT, "_dec", "aa0", BUNDLE_NAME)
EXTRACTED = os.path.join(YOO_ROOT, "_extracted", "HotfixView.dll")
PATCH_PROJECT = os.path.join(ROOT, "tools", "client-market-price-patcher",
                             "ClientMarketPricePatcher.csproj")


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
        raise RuntimeError("HotfixView.dll TextAsset not found")
    for file_item in env.files.values():
        file_item.mark_changed()
    env.save(pack="lz4", out_path=output_dir)
    output = os.path.join(output_dir, BUNDLE_NAME)
    if not os.path.isfile(output):
        raise RuntimeError("UnityPy did not create patched config bundle")
    return output


def backup_once(path):
    backup = path + ".pre-strength-coupon.bak"
    if os.path.isfile(path) and not os.path.exists(backup):
        shutil.copy2(path, backup)


def main(source_dll=None):
    if not os.path.isfile(BUNDLE):
        raise FileNotFoundError(BUNDLE)
    if not os.path.isfile(EXTRACTED):
        # Clean client distributions omit analysis DLLs.  Use the archived
        # pre-patch copy as the rebuild source in that case.
        candidates = []
        archive_root = os.path.join(ROOT, "_archive", "client-test-backups")
        for base, _, files in os.walk(archive_root):
            if "HotfixView.dll.pre-server-shop-price.bak" in files:
                candidates.append(os.path.join(base, "HotfixView.dll.pre-server-shop-price.bak"))
        if candidates:
            EXTRACTED_SOURCE = sorted(candidates, key=os.path.getmtime, reverse=True)[0]
        else:
            raise FileNotFoundError(EXTRACTED)
    else:
        EXTRACTED_SOURCE = EXTRACTED
    # Always patch from the untouched DLL when available.  Reusing an already
    # patched file would either duplicate the helper or preserve a bad prior IL
    # body, making rollback/rebuild unsafe.
    if source_dll is None:
        pristine = EXTRACTED + ".pre-strength-coupon.bak"
        source_dll = pristine if os.path.isfile(pristine) else EXTRACTED_SOURCE
    with tempfile.TemporaryDirectory(prefix="market-price-") as temp_dir:
        patched_path = os.path.join(temp_dir, "HotfixView.dll")
        subprocess.run([
            "dotnet", "run", "--project", PATCH_PROJECT, "--configuration", "Release", "--",
            "view", source_dll, patched_path,
        ], cwd=ROOT, check=True)
        with open(patched_path, "rb") as stream:
            patched = stream.read()
        if not patched.startswith(b"MZ"):
            raise RuntimeError("patched HotfixView.dll is not a PE file")
        patched_bundle = replace_hotfix_view(BUNDLE, patched, temp_dir)
        backup_once(BUNDLE)
        if os.path.isfile(EXTRACTED):
            backup_once(EXTRACTED)
            shutil.copy2(patched_path, EXTRACTED)
        shutil.copy2(patched_bundle, BUNDLE)
        if os.path.isfile(DECODED):
            backup_once(DECODED)
            shutil.copy2(patched_bundle, DECODED)

    # Verify that the installed bundle contains exactly the patched TextAsset.
    env = UnityPy.load(BUNDLE)
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name == "HotfixView.dll":
            installed = data.m_Script
            installed = installed.encode("utf-8", errors="surrogateescape") if isinstance(installed, str) else bytes(installed)
            if installed != patched:
                raise RuntimeError("installed HotfixView.dll differs from patched DLL")
            print("installed", BUNDLE)
            print("HotfixView.dll sha256", hashlib.sha256(patched).hexdigest().upper())
            print("rollback copy", BUNDLE + ".pre-strength-coupon.bak")
            return
    raise RuntimeError("installed bundle has no HotfixView.dll")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-dll", help="optional unpatched HotfixView.dll input")
    main(parser.parse_args().source_dll)
