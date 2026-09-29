# -*- coding: utf-8 -*-
"""Apply the repeatable client datatable fixes and switch its bundle to plaintext."""

import base64
import hashlib
import json
import os
import re
import shutil
import struct
import sys
import zlib
from datetime import datetime, timezone

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
MANIFEST = os.path.join(YOO_BASE, "aa0", "PackageManifest_aa0_2025-01-05-1027.bytes")
MANIFEST_HASH = os.path.splitext(MANIFEST)[0] + ".hash"
BUNDLE_NAME = "87c13c7e72546d4e4e0960392e451498.bundle"
SOURCE_BUNDLE = os.path.join(YOO_BASE, "_dec", "aa0", BUNDLE_NAME)
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


def find_datatable_load_method(data):
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
        if name == "assets_download_datatable.bundle":
            return index, load_method_pos, load_method
    return None


def parse_lenient(text):
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return json.loads(re.sub(r",(\s*[}\]])", r"\1", text))


def read_rows(obj):
    raw = obj.read().m_Script
    if isinstance(raw, str):
        raw = raw.encode("utf-8")
    compressed = base64.b64decode(raw.decode("utf-8").strip())
    text = zlib.decompress(compressed).decode("utf-8", errors="replace")
    return parse_lenient(text)


def write_rows(obj, rows):
    text = "[\r\n" + ",\r\n".join(
        "[%d, %s]" % (
            key,
            json.dumps(value, ensure_ascii=False, separators=(",", ":")),
        )
        for key, value in rows
    ) + "\r\n]"
    encoded = base64.b64encode(zlib.compress(text.encode("utf-8"))).decode("ascii")
    data = obj.read()
    data.m_Script = encoded
    data.save()


def patch_bundle():
    env = UnityPy.load(SOURCE_BUNDLE)
    assets = {}
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        name = obj.read().m_Name
        if name in ("MapMonsterConfig", "SkinBase", "Sys_Prefab", "SignInRewardMonth"):
            assets[name] = obj

    missing = {"MapMonsterConfig", "SkinBase", "Sys_Prefab", "SignInRewardMonth"} - set(assets)
    if missing:
        raise RuntimeError("datatable TextAsset not found: " + ", ".join(sorted(missing)))

    changed = False
    map_rows = read_rows(assets["MapMonsterConfig"])
    original_count = len(map_rows)
    map_rows = [row for row in map_rows if int(row[0]) != 1017]
    if len(map_rows) != original_count:
        write_rows(assets["MapMonsterConfig"], map_rows)
        changed = True
        print("MapMonsterConfig row 1017 removed")
    else:
        print("MapMonsterConfig row 1017 already absent")

    skin_rows = read_rows(assets["SkinBase"])
    skin_by_id = {int(row[0]): row for row in skin_rows}
    expected_skins = {
        120642: [120642, {"_id": 120642, "PrfabId": 34}],
        120643: [120643, {"_id": 120643, "PrfabId": 34}],
    }
    skin_changed = any(skin_by_id.get(key) != value for key, value in expected_skins.items())
    if skin_changed:
        skin_by_id.update(expected_skins)
        skin_rows = [skin_by_id[key] for key in sorted(skin_by_id)]
        write_rows(assets["SkinBase"], skin_rows)
        changed = True
        print("SkinBase rows 120642/120643 restored -> PrfabId 34")
    else:
        print("SkinBase rows 120642/120643 already point to PrfabId 34")

    prefab_rows = read_rows(assets["Sys_Prefab"])
    prefab_by_id = {int(row[0]): row for row in prefab_rows}
    expected_prefab = [34, {
        "_id": 34,
        "Name": "Skin25",
        "Desc": "1",
        "AssetPath": "Assets/Download/Role/RolePrefab/Skin/Skin25.prefab",
        "PoolId": 1,
        "CullDespawned": True,
        "CullAbove": 0,
        "CullDelay": 30,
        "CullMaxPerPass": 1,
    }]
    if prefab_by_id.get(34) != expected_prefab:
        prefab_by_id[34] = expected_prefab
        prefab_rows = [prefab_by_id[key] for key in sorted(prefab_by_id)]
        write_rows(assets["Sys_Prefab"], prefab_rows)
        changed = True
        print("Sys_Prefab row 34 restored -> Skin25.prefab")
    else:
        print("Sys_Prefab row 34 already points to Skin25.prefab")

    month_rows = read_rows(assets["SignInRewardMonth"])
    current_year = datetime.now(timezone.utc).year
    configured_years = sorted({
        int(row[1].get("Year", 0))
        for row in month_rows
        if isinstance(row, list) and len(row) == 2 and isinstance(row[1], dict)
    })
    if current_year not in configured_years:
        template_years = [year for year in configured_years if 0 < year < current_year]
        if not template_years:
            raise RuntimeError("SignInRewardMonth has no prior-year template")
        template_year = max(template_years)
        patched_count = 0
        for _, value in month_rows:
            if int(value.get("Year", 0)) == template_year:
                value["Year"] = current_year
                patched_count += 1
        if patched_count == 0:
            raise RuntimeError("SignInRewardMonth template year has no rows")
        write_rows(assets["SignInRewardMonth"], month_rows)
        changed = True
        print(
            "SignInRewardMonth %d template rows moved %d -> %d"
            % (patched_count, template_year, current_year)
        )
    else:
        print("SignInRewardMonth already contains year %d" % current_year)

    if not changed:
        print("datatable rows already patched; bundle save skipped")
        return None

    for file_item in env.files.values():
        file_item.mark_changed()
    output_dir = os.path.join(ROOT, "_work", "datatable-blue-mech")
    os.makedirs(output_dir, exist_ok=True)
    output = os.path.join(output_dir, BUNDLE_NAME)
    env.save(pack="lz4", out_path=output_dir)
    print("bundle edited ->", output, os.path.getsize(output))
    return output


def patch_manifest():
    with open(MANIFEST, "rb") as stream:
        data = stream.read()
    hit = find_datatable_load_method(data)
    if hit is None:
        raise RuntimeError("datatable bundle entry not found in manifest")
    index, load_method_pos, load_method = hit
    print("bundle[%d] loadMethod=%d @%d" % (index, load_method, load_method_pos))
    if load_method == TARGET_LOAD_METHOD:
        print("loadMethod already %d" % TARGET_LOAD_METHOD)
        expected_hash = hashlib.md5(data).hexdigest()
        current_hash = ""
        if os.path.exists(MANIFEST_HASH):
            with open(MANIFEST_HASH, "r", encoding="ascii") as stream:
                current_hash = stream.read().strip()
        if current_hash != expected_hash:
            hash_backup = MANIFEST_HASH + ".pre-blue-mech.bak"
            if os.path.exists(MANIFEST_HASH) and not os.path.exists(hash_backup):
                shutil.copy2(MANIFEST_HASH, hash_backup)
            with open(MANIFEST_HASH, "w", encoding="ascii", newline="") as stream:
                stream.write(expected_hash)
            print("manifest hash sidecar updated")
        return

    blue_mech_backup = MANIFEST + ".pre-blue-mech.bak"
    if not os.path.exists(blue_mech_backup):
        shutil.copy2(MANIFEST, blue_mech_backup)
    hash_backup = MANIFEST_HASH + ".pre-blue-mech.bak"
    if os.path.exists(MANIFEST_HASH) and not os.path.exists(hash_backup):
        shutil.copy2(MANIFEST_HASH, hash_backup)
    if not os.path.exists(MANIFEST + ".bak"):
        shutil.copy2(MANIFEST, MANIFEST + ".bak")
    patched = bytearray(data)
    patched[load_method_pos] = TARGET_LOAD_METHOD
    with open(MANIFEST, "wb") as stream:
        stream.write(patched)
    with open(MANIFEST_HASH, "w", encoding="ascii", newline="") as stream:
        stream.write(hashlib.md5(patched).hexdigest())
    print("manifest patched (%d -> %d)" % (load_method, TARGET_LOAD_METHOD))


def install_bundle(edited_bundle):
    if edited_bundle is None:
        print("bundle was not edited this run; replacement skipped")
        return
    for subdir in ("aa0", os.path.join("_dec", "aa0")):
        destination = os.path.join(YOO_BASE, subdir, BUNDLE_NAME)
        blue_mech_backup = destination + ".pre-blue-mech.bak"
        if not os.path.exists(blue_mech_backup):
            shutil.copy2(destination, blue_mech_backup)
        if not os.path.exists(destination + ".bak"):
            shutil.copy2(destination, destination + ".bak")
        shutil.copy2(edited_bundle, destination)
        print("replaced", destination)


def main():
    edited_bundle = patch_bundle()
    patch_manifest()
    install_bundle(edited_bundle)


if __name__ == "__main__":
    main()
