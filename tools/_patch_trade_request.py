# -*- coding: utf-8 -*-
"""Install the isolated trade-request compatibility patch in a client."""

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

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
PROJECT = os.path.join(ROOT, "tools", "client-trade-patcher", "ClientTradePatcher.csproj")
BACKUP_SUFFIX = ".pre-trade-store-field-access.bak"
SOURCE_SUFFIX = ".pre-trade-event-refresh.bak"


def asset_bytes(data):
    script = data.m_Script
    return script.encode("utf-8", "surrogateescape") if isinstance(script, str) else bytes(script)


def replace(bundle, hotfix, view, out_dir):
    env = UnityPy.load(bundle)
    found = set()
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        value = {"Hotfix.dll": hotfix, "HotfixView.dll": view}.get(data.m_Name)
        if value is None:
            continue
        data.m_Script = value.decode("utf-8", "surrogateescape")
        data.save()
        found.add(data.m_Name)
    if found != {"Hotfix.dll", "HotfixView.dll"}:
        raise RuntimeError("Hotfix.dll/HotfixView.dll TextAssets not both found")
    for item in env.files.values():
        item.mark_changed()
    env.save(pack="lz4", out_path=out_dir)
    result = os.path.join(out_dir, os.path.basename(bundle))
    if not os.path.isfile(result):
        raise RuntimeError("UnityPy did not write patched bundle")
    return result


def backup(path, yoo):
    target = patch_backup(Path(path), BACKUP_SUFFIX)
    target.parent.mkdir(parents=True, exist_ok=True)
    if not target.exists():
        shutil.copy2(path, target)
        print("backup", target)


def patch_source(path, yoo):
    """Rebuild deterministically from the snapshot before the preceding trade revision."""
    original = patch_backup(Path(path), SOURCE_SUFFIX)
    return str(original) if original.is_file() else path


def verify(bundle, expected):
    env = UnityPy.load(bundle)
    actual = {}
    for obj in env.objects:
        if obj.type.name == "TextAsset":
            data = obj.read()
            if data.m_Name in expected:
                actual[data.m_Name] = asset_bytes(data)
    if actual != expected:
        raise RuntimeError("installed bundle DLLs differ from patched DLLs")


def patch(client):
    root = os.path.join(ROOT, client)
    data_root = next(os.path.join(root, name) for name in os.listdir(root) if name.endswith("_Data"))
    yoo = os.path.join(data_root, "StreamingAssets", "yoo")
    extracted = os.path.join(yoo, "_extracted")
    hotfix_path = os.path.join(extracted, "Hotfix.dll")
    view_path = os.path.join(extracted, "HotfixView.dll")
    bundle = os.path.join(yoo, "aa0", BUNDLE_NAME)
    decoded = os.path.join(yoo, "_dec", "aa0", BUNDLE_NAME)
    with tempfile.TemporaryDirectory(prefix="trade-request-") as temp:
        subprocess.run([
            "dotnet", "run", "--project", PROJECT, "-c", "Release", "--",
            patch_source(hotfix_path, yoo), patch_source(view_path, yoo), temp,
        ], cwd=ROOT, check=True)
        hotfix = open(os.path.join(temp, "Hotfix.dll"), "rb").read()
        view = open(os.path.join(temp, "HotfixView.dll"), "rb").read()
        packed = replace(patch_source(bundle, yoo), hotfix, view, temp)
        for path in (hotfix_path, view_path, bundle):
            backup(path, yoo)
        shutil.copy2(os.path.join(temp, "Hotfix.dll"), hotfix_path)
        shutil.copy2(os.path.join(temp, "HotfixView.dll"), view_path)
        shutil.copy2(packed, bundle)
        if os.path.isfile(decoded):
            backup(decoded, yoo)
            shutil.copy2(packed, decoded)
    verify(bundle, {"Hotfix.dll": hotfix, "HotfixView.dll": view})
    print("installed", client)
    print("Hotfix.dll sha256", hashlib.sha256(hotfix).hexdigest().upper())
    print("HotfixView.dll sha256", hashlib.sha256(view).hexdigest().upper())


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--client", default="client-test", choices=("client-test", "client-127.0.0.1"))
    patch(parser.parse_args().client)
