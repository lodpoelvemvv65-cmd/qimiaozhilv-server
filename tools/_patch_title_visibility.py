# -*- coding: utf-8 -*-
"""Patch the original title initialization omission in Hotfix.dll.

The original client creates other-player HUDs from UnitCharacter, but
NumHelper.FillNum does not copy UnitCharacter.Title into NumericType 1038.
HudCharacter.Init consequently reads zero until a later numeric update. This
patch adds only that missing assignment and preserves every existing client
patch in the config bundle.
"""

import argparse
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
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
MANIFEST_NAME = "PackageManifest_aa0_2025-01-05-1027.bytes"
PATCH_PROJECT = os.path.join(ROOT, "tools", "client-title-patcher", "ClientTitlePatcher.csproj")
BACKUP_SUFFIX = ".pre-title-visibility.bak"


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


def config_load_method(path):
    with open(path, "rb") as stream:
        reader = ManifestReader(stream.read())
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
    for _ in range(reader.i32()):
        name = reader.utf8()
        reader.utf8()
        reader.utf8()
        reader.i64()
        reader.boolean()
        load_method = reader.u8()
        reader.utf8_array()
        reader.i32_array()
        if name == "assets_download_config.bundle":
            return load_method
    raise RuntimeError("config bundle entry not found in manifest")


def text_asset_bytes(data):
    script = data.m_Script
    if isinstance(script, str):
        return script.encode("utf-8", errors="surrogateescape")
    return bytes(script)


def replace_hotfix(bundle_path, patched_dll, output_dir):
    env = UnityPy.load(bundle_path)
    replaced = False
    for obj in env.objects:
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
        if data.m_Name == "Hotfix.dll":
            actual = text_asset_bytes(data)
            if actual != expected_dll:
                raise RuntimeError("installed bundle Hotfix.dll differs from patched DLL")
            return
    raise RuntimeError("installed bundle does not contain Hotfix.dll")


def patch_client(client_name):
    yoo = os.path.join(ROOT, client_name, "梦幻奇遇记_Data", "StreamingAssets", "yoo")
    extracted = os.path.join(yoo, "_extracted", "Hotfix.dll")
    bundle = os.path.join(yoo, "aa0", BUNDLE_NAME)
    decoded_bundle = os.path.join(yoo, "_dec", "aa0", BUNDLE_NAME)
    manifest = os.path.join(yoo, "aa0", MANIFEST_NAME)
    for path in (extracted, bundle, manifest):
        if not os.path.isfile(path):
            raise FileNotFoundError(path)
    if config_load_method(manifest) != 0:
        raise RuntimeError(
            "%s config bundle is still encrypted; decrypt it and switch the manifest before patching" % client_name
        )

    with tempfile.TemporaryDirectory(prefix="title-visibility-") as temp_dir:
        patched_path = os.path.join(temp_dir, "Hotfix.dll")
        subprocess.run(
            [
                "dotnet", "run", "--project", PATCH_PROJECT,
                "--configuration", "Release", "--", extracted, patched_path,
            ],
            cwd=ROOT,
            check=True,
        )
        with open(patched_path, "rb") as stream:
            patched = stream.read()
        if not patched.startswith(b"MZ"):
            raise RuntimeError("patched Hotfix.dll is not a PE file")
        patched_bundle = replace_hotfix(bundle, patched, temp_dir)

        backup_once(extracted)
        backup_once(bundle)
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
    parser.add_argument("--client", default="client-test", choices=("client-test", "client-127.0.0.1"))
    args = parser.parse_args()
    patch_client(args.client)


if __name__ == "__main__":
    main()
