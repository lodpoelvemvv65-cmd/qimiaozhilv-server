# -*- coding: utf-8 -*-
"""Use CharacterUI.id when opening another player's equipment tooltip."""

import argparse
import hashlib
import os
import shutil
import subprocess
import tempfile

import UnityPy

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
PROJECT = os.path.join(ROOT, "tools", "client-equip-tooltip-patcher", "ClientEquipTooltipPatcher.csproj")
SUFFIX = ".pre-equip-tooltip.bak"


def asset_bytes(data):
    script = data.m_Script
    return script.encode("utf-8", "surrogateescape") if isinstance(script, str) else bytes(script)


def replace(bundle, dll, out_dir):
    env = UnityPy.load(bundle)
    for obj in env.objects:
        if obj.type.name == "TextAsset":
            data = obj.read()
            if data.m_Name == "HotfixView.dll":
                data.m_Script = dll.decode("utf-8", "surrogateescape")
                data.save()
                break
    else:
        raise RuntimeError("HotfixView.dll TextAsset not found")
    for item in env.files.values():
        item.mark_changed()
    env.save(pack="lz4", out_path=out_dir)
    path = os.path.join(out_dir, BUNDLE_NAME)
    if not os.path.isfile(path):
        raise RuntimeError("UnityPy did not write patched bundle")
    return path


def backup(path):
    target = path + SUFFIX
    if not os.path.exists(target):
        shutil.copy2(path, target)
        print("backup", target)


def verify(bundle, expected):
    env = UnityPy.load(bundle)
    for obj in env.objects:
        if obj.type.name == "TextAsset":
            data = obj.read()
            if data.m_Name == "HotfixView.dll":
                if asset_bytes(data) != expected:
                    raise RuntimeError("bundle HotfixView.dll differs from installed DLL")
                return
    raise RuntimeError("bundle HotfixView.dll missing")


def patch(client):
    root = os.path.join(ROOT, client)
    data_root = next(os.path.join(root, name) for name in os.listdir(root) if name.endswith("_Data"))
    yoo = os.path.join(data_root, "StreamingAssets", "yoo")
    dll_path = os.path.join(yoo, "_extracted", "HotfixView.dll")
    bundle = os.path.join(yoo, "aa0", BUNDLE_NAME)
    decoded = os.path.join(yoo, "_dec", "aa0", BUNDLE_NAME)
    with tempfile.TemporaryDirectory(prefix="equip-tooltip-") as temp:
        out = os.path.join(temp, "HotfixView.dll")
        subprocess.run(["dotnet", "run", "--project", PROJECT, "-c", "Release", "--", dll_path, out], cwd=ROOT, check=True)
        dll = open(out, "rb").read()
        packed = replace(bundle, dll, temp)
        backup(dll_path)
        backup(bundle)
        shutil.copy2(out, dll_path)
        shutil.copy2(packed, bundle)
        if os.path.isfile(decoded):
            backup(decoded)
            shutil.copy2(packed, decoded)
    verify(bundle, dll)
    print("installed", client)
    print("HotfixView.dll sha256", hashlib.sha256(dll).hexdigest().upper())


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--client", default="client-test", choices=("client-test", "client-127.0.0.1"))
    patch(parser.parse_args().client)
