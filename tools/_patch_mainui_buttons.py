# -*- coding: utf-8 -*-
"""Legacy analysis helper for patching MainUI bindings.

The active clients intentionally use the original encrypted bundle. Do not run
this helper when preparing a playable client; server behavior must follow the
original client protocol and event bindings.
"""

import hashlib
import os
import shutil
import struct
import subprocess
import sys
import tempfile

import UnityPy


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
YOO_BASE = os.path.join(
    ROOT,
    "client-test",
    "\u68a6\u5e7b\u5947\u9047\u8bb0_Data",
    "StreamingAssets",
    "yoo",
)
EXTRACTED_DLL = os.path.join(YOO_BASE, "_extracted", "HotfixView.dll")
MANIFEST = os.path.join(YOO_BASE, "aa0", "PackageManifest_aa0_2025-01-05-1027.bytes")
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
SOURCE_BUNDLE = os.path.join(YOO_BASE, "_dec", "aa0", BUNDLE_NAME)
PATCH_PROJECT = os.path.join(ROOT, "tools", "client-mainui-patcher", "ClientMainUIPatcher.csproj")
TARGET_LOAD_METHOD = 0


class ManifestReader:
    def __init__(self, data):
        self.data = data
        self.pos = 0

    def u32(self):
        value = struct.unpack_from("<I", self.data, self.pos)[0]
        self.pos += 4
        return value

    def i32(self):
        value = struct.unpack_from("<i", self.data, self.pos)[0]
        self.pos += 4
        return value

    def i64(self):
        value = struct.unpack_from("<q", self.data, self.pos)[0]
        self.pos += 8
        return value

    def u16(self):
        value = struct.unpack_from("<H", self.data, self.pos)[0]
        self.pos += 2
        return value

    def u8(self):
        value = self.data[self.pos]
        self.pos += 1
        return value

    def boolean(self):
        return self.u8() != 0

    def utf8(self):
        length = self.u16()
        value = self.data[self.pos:self.pos + length].decode("utf-8", errors="replace")
        self.pos += length
        return value

    def utf8_array(self):
        return [self.utf8() for _ in range(self.u16())]

    def i32_array(self):
        return [self.i32() for _ in range(self.u16())]


def find_bundle_load_method(data, bundle_name):
    reader = ManifestReader(data)
    if reader.u32() != 5853007:
        raise ValueError("bad manifest magic")
    reader.utf8()
    reader.boolean()
    reader.boolean()
    reader.boolean()
    reader.i32()
    reader.utf8()
    reader.utf8()
    for _ in range(reader.i32()):
        reader.utf8()
        reader.utf8()
        reader.utf8()
        reader.utf8_array()
        reader.i32()
        reader.i32_array()
    for index in range(reader.i32()):
        name = reader.utf8()
        reader.utf8()
        reader.utf8()
        reader.i64()
        reader.boolean()
        load_method_pos = reader.pos
        load_method = reader.u8()
        reader.utf8_array()
        reader.i32_array()
        if name == bundle_name:
            return index, load_method_pos, load_method
    return None


def patch_dll(output_path):
    command = [
        "dotnet",
        "run",
        "--project",
        PATCH_PROJECT,
        "--configuration",
        "Release",
        "--no-restore",
        "--",
        EXTRACTED_DLL,
        output_path,
    ]
    subprocess.run(command, cwd=ROOT, check=True)
    with open(output_path, "rb") as stream:
        patched = stream.read()
    if not patched.startswith(b"MZ"):
        raise RuntimeError("patched HotfixView.dll is not a PE file")
    return patched


def write_bundle(patched_dll, output_dir):
    env = UnityPy.load(SOURCE_BUNDLE)
    target = None
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name == "HotfixView.dll":
            target = obj
            data.m_Script = patched_dll.decode("utf-8", errors="surrogateescape")
            data.save()
            break
    if target is None:
        raise RuntimeError("HotfixView.dll TextAsset not found in config bundle")

    for file_item in env.files.values():
        file_item.mark_changed()
    env.save(pack="lz4", out_path=output_dir)
    output_path = os.path.join(output_dir, BUNDLE_NAME)
    if not os.path.exists(output_path):
        raise RuntimeError("UnityPy did not create the patched config bundle")
    return output_path


def patch_manifest():
    with open(MANIFEST, "rb") as stream:
        data = stream.read()
    hit = find_bundle_load_method(data, "assets_download_config.bundle")
    if hit is None:
        raise RuntimeError("config bundle entry not found in manifest")
    index, position, load_method = hit
    print("bundle[%d] config loadMethod=%d @%d" % (index, load_method, position))
    if load_method == TARGET_LOAD_METHOD:
        return
    if not os.path.exists(MANIFEST + ".bak"):
        shutil.copy2(MANIFEST, MANIFEST + ".bak")
    patched = bytearray(data)
    patched[position] = TARGET_LOAD_METHOD
    with open(MANIFEST, "wb") as stream:
        stream.write(patched)


def install_file(source, destination):
    if not os.path.exists(destination + ".bak"):
        shutil.copy2(destination, destination + ".bak")
    shutil.copy2(source, destination)
    print("installed", destination)


def text_asset_bytes(data):
    script = data.m_Script
    if isinstance(script, str):
        return script.encode("utf-8", errors="surrogateescape")
    return bytes(script)


def verify_install(expected_dll):
    target_bundle = os.path.join(YOO_BASE, "aa0", BUNDLE_NAME)
    with open(target_bundle, "rb") as stream:
        if stream.read(8) != b"UnityFS\0":
            raise RuntimeError("installed config bundle is not plaintext UnityFS")
    env = UnityPy.load(target_bundle)
    installed = None
    for obj in env.objects:
        data = obj.read()
        if getattr(data, "m_Name", "") == "HotfixView.dll":
            installed = text_asset_bytes(data)
            break
    if installed != expected_dll:
        raise RuntimeError("installed HotfixView.dll differs from patched DLL")
    with open(MANIFEST, "rb") as stream:
        hit = find_bundle_load_method(stream.read(), "assets_download_config.bundle")
    if hit is None or hit[2] != TARGET_LOAD_METHOD:
        raise RuntimeError("config bundle manifest load method is not plaintext")
    print("verified HotfixView sha256", hashlib.sha256(installed).hexdigest())


def main():
    os.makedirs(os.path.join(ROOT, "_test_runs"), exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="mainui-patch-", dir=os.path.join(ROOT, "_test_runs")) as temp_dir:
        patched_dll_path = os.path.join(temp_dir, "HotfixView.dll")
        patched_dll = patch_dll(patched_dll_path)
        patched_bundle = write_bundle(patched_dll, temp_dir)

        if not os.path.exists(EXTRACTED_DLL + ".bak"):
            shutil.copy2(EXTRACTED_DLL, EXTRACTED_DLL + ".bak")
        shutil.copy2(patched_dll_path, EXTRACTED_DLL)
        install_file(patched_bundle, os.path.join(YOO_BASE, "aa0", BUNDLE_NAME))
        install_file(patched_bundle, SOURCE_BUNDLE)
        patch_manifest()
        verify_install(patched_dll)


if __name__ == "__main__":
    main()
