"""Build and install the independent Skin25 blue lightning mech asset chain."""

from __future__ import annotations

import argparse
import hashlib
import os
import shutil
import struct
import sys
import zlib
from copy import deepcopy
from pathlib import Path

import UnityPy
from Crypto.Cipher import AES
from Crypto.Util.Padding import pad, unpad
from PIL import Image


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
CLIENT_AA1 = (
    ROOT
    / "client-test"
    / "梦幻奇遇记_Data"
    / "StreamingAssets"
    / "yoo"
    / "aa1"
)
SOURCE_DIR = ROOT / "_work" / "skin34-decrypted"
FRAME_DIR = ROOT / "_work" / "blue-mech-frames"
BUILD_DIR = ROOT / "_work" / "blue-mech-bundles"
MANIFEST = CLIENT_AA1 / "PackageManifest_aa1_2025-01-05-1027.bytes"
MANIFEST_HASH = MANIFEST.with_suffix(".hash")

AES_KEY = bytes.fromhex("4d7944595d4b656472776043585a6469")
AES_IV = bytes.fromhex("63485d77426e71567a6554667673434f")

SOURCE_BUNDLES = {
    "animator": "b92868d76e4db5fd6fa6aeed11456a41.bundle",
    "prefab": "d6f01688370cfd0def4d2041c1982ee9.bundle",
    "texture": "b1f6664275818fc7d628d87d31de65a8.bundle",
}
SOURCE_IDS = {"animator": 153, "prefab": 578, "texture": 826}
SOURCE_CABS = {
    "animator": "CAB-db6e7ee5029c3dcbe7404eda174ceade",
    "prefab": "CAB-6702b23df3345c65acbf79fd9fdcf98d",
    "texture": "CAB-2da59438d872d252cac9473a90bbc00a",
}
TARGET_CABS = {
    kind: "CAB-" + hashlib.md5(("blue-mech-skin25-" + kind).encode()).hexdigest()
    for kind in SOURCE_CABS
}
TARGET_BUNDLE_NAMES = {
    "animator": "assets_download_animators_skin_skin25.bundle",
    "prefab": "assets_download_role_roleprefab_skin_skin25.bundle",
    "texture": "assets_download_texture_animationresource_role_skin_skin25.bundle",
}


def rename_skin(value: str) -> str:
    return value.replace("Skin34", "Skin25").replace("skin34", "skin25")


def bundle_parts(path: Path):
    environment = UnityPy.load(str(path))
    bundle = next(iter(environment.files.values()))
    serialized = next(item for item in bundle.files.values() if hasattr(item, "externals"))
    return environment, bundle, serialized


def rename_serialized_file(bundle, serialized, target_cab: str) -> None:
    old_name = serialized.name
    rebuilt = {}
    for name, item in bundle.files.items():
        if name == old_name:
            rebuilt[target_cab] = item
        elif name == old_name + ".resS":
            # Replaced textures are embedded by UnityPy; retaining this file
            # would duplicate the original Skin34 pixels in the new bundle.
            continue
        else:
            rebuilt[rename_skin(name)] = item
    bundle.files = rebuilt
    serialized.name = target_cab
    bundle.cab_file = target_cab + ".resS"


def patch_external_cabs(serialized) -> None:
    for external in serialized.externals:
        for kind, source_cab in SOURCE_CABS.items():
            if source_cab in external.path:
                external.path = external.path.replace(source_cab, TARGET_CABS[kind])


def patch_asset_bundle_object(obj) -> None:
    data = obj.read()
    data.m_Name = rename_skin(data.m_Name)
    data.m_Container = [(rename_skin(path), info) for path, info in data.m_Container]
    data.save()


def patch_bundle(kind: str, plain_dir: Path) -> Path:
    source = SOURCE_DIR / SOURCE_BUNDLES[kind]
    environment, bundle, serialized = bundle_parts(source)
    patch_external_cabs(serialized)

    expected_textures = set()
    for obj in environment.objects:
        data = obj.read()
        if obj.type.name == "AssetBundle":
            patch_asset_bundle_object(obj)
            continue
        if hasattr(data, "m_Name") and isinstance(data.m_Name, str):
            data.m_Name = rename_skin(data.m_Name)
        if obj.type.name == "Texture2D":
            expected_textures.add(data.m_Name)
            frame = FRAME_DIR / (data.m_Name + ".png")
            if not frame.is_file():
                raise RuntimeError(f"missing generated frame: {frame}")
            with Image.open(frame) as image:
                data.image = image.convert("RGBA")
        data.save()

    if kind == "texture" and len(expected_textures) != 31:
        raise RuntimeError(f"expected 31 textures, found {len(expected_textures)}")
    rename_serialized_file(bundle, serialized, TARGET_CABS[kind])
    output = plain_dir / TARGET_BUNDLE_NAMES[kind]
    raw = bundle.save(packer="lz4")
    output.write_bytes(raw)
    return output


class ManifestReader:
    def __init__(self, data: bytes):
        self.data = data
        self.pos = 0

    def unpack(self, pattern: str):
        value = struct.unpack_from(pattern, self.data, self.pos)[0]
        self.pos += struct.calcsize(pattern)
        return value

    def u8(self):
        return self.unpack("<B")

    def u16(self):
        return self.unpack("<H")

    def u32(self):
        return self.unpack("<I")

    def i32(self):
        return self.unpack("<i")

    def i64(self):
        return self.unpack("<q")

    def boolean(self):
        return self.u8() != 0

    def text(self):
        size = self.u16()
        value = self.data[self.pos:self.pos + size].decode("utf-8")
        self.pos += size
        return value

    def texts(self):
        return [self.text() for _ in range(self.u16())]

    def integers(self):
        return [self.i32() for _ in range(self.u16())]


class ManifestWriter:
    def __init__(self):
        self.data = bytearray()

    def pack(self, pattern: str, value):
        self.data.extend(struct.pack(pattern, value))

    def u8(self, value):
        self.pack("<B", value)

    def u16(self, value):
        self.pack("<H", value)

    def u32(self, value):
        self.pack("<I", value)

    def i32(self, value):
        self.pack("<i", value)

    def i64(self, value):
        self.pack("<q", value)

    def boolean(self, value):
        self.u8(1 if value else 0)

    def text(self, value):
        encoded = value.encode("utf-8")
        self.u16(len(encoded))
        self.data.extend(encoded)

    def texts(self, values):
        self.u16(len(values))
        for value in values:
            self.text(value)

    def integers(self, values):
        self.u16(len(values))
        for value in values:
            self.i32(value)


def read_manifest(raw: bytes):
    reader = ManifestReader(raw)
    header = {
        "magic": reader.u32(),
        "version": reader.text(),
        "addressable": reader.boolean(),
        "lower": reader.boolean(),
        "guid": reader.boolean(),
        "style": reader.i32(),
        "package": reader.text(),
        "package_version": reader.text(),
    }
    if header["magic"] != 5853007:
        raise RuntimeError("invalid YooAsset manifest magic")
    assets = []
    for _ in range(reader.i32()):
        assets.append({
            "address": reader.text(),
            "path": reader.text(),
            "guid": reader.text(),
            "tags": reader.texts(),
            "bundle": reader.i32(),
            "dependencies": reader.integers(),
        })
    bundles = []
    for _ in range(reader.i32()):
        bundles.append({
            "name": reader.text(),
            "hash": reader.text(),
            "crc": reader.text(),
            "size": reader.i64(),
            "raw": reader.boolean(),
            "load_method": reader.u8(),
            "tags": reader.texts(),
            "references": reader.integers(),
        })
    if reader.pos != len(raw):
        raise RuntimeError("manifest parser did not consume the complete file")
    return header, assets, bundles


def write_manifest(header, assets, bundles) -> bytes:
    writer = ManifestWriter()
    writer.u32(header["magic"])
    writer.text(header["version"])
    writer.boolean(header["addressable"])
    writer.boolean(header["lower"])
    writer.boolean(header["guid"])
    writer.i32(header["style"])
    writer.text(header["package"])
    writer.text(header["package_version"])
    writer.i32(len(assets))
    for asset in assets:
        writer.text(asset["address"])
        writer.text(asset["path"])
        writer.text(asset["guid"])
        writer.texts(asset["tags"])
        writer.i32(asset["bundle"])
        writer.integers(asset["dependencies"])
    writer.i32(len(bundles))
    for bundle in bundles:
        writer.text(bundle["name"])
        writer.text(bundle["hash"])
        writer.text(bundle["crc"])
        writer.i64(bundle["size"])
        writer.boolean(bundle["raw"])
        writer.u8(bundle["load_method"])
        writer.texts(bundle["tags"])
        writer.integers(bundle["references"])
    return bytes(writer.data)


def encrypted_metadata(path: Path):
    raw = path.read_bytes()
    encrypted = AES.new(AES_KEY, AES.MODE_CBC, AES_IV).encrypt(pad(raw, AES.block_size))
    digest = hashlib.md5(encrypted).hexdigest()
    crc = struct.pack("<I", zlib.crc32(encrypted) & 0xFFFFFFFF).hex()
    return encrypted, digest, crc


def verify_plain_bundles(plain_paths: dict[str, Path]) -> None:
    environment = UnityPy.load(*[str(path) for path in plain_paths.values()])
    serialized_names = {
        obj.assets_file.name
        for obj in environment.objects
    }
    if not set(TARGET_CABS.values()).issubset(serialized_names):
        raise RuntimeError("new CAB identities are missing after bundle serialization")

    textures = []
    prefab_ok = False
    for obj in environment.objects:
        data = obj.read()
        if hasattr(data, "m_Name") and "Skin34" in str(data.m_Name):
            raise RuntimeError(f"unrenamed Skin34 object: {data.m_Name}")
        if obj.type.name == "Texture2D":
            image = data.image.convert("RGBA")
            if image.size != (330, 330) or image.getbbox() is None:
                raise RuntimeError(f"invalid generated texture: {data.m_Name}")
            textures.append(data.m_Name)
        if obj.type.name == "GameObject" and data.m_Name == "Skin25":
            prefab_ok = True
    if len(textures) != 31 or len(set(textures)) != 31:
        raise RuntimeError(f"expected 31 unique Skin25 textures, found {len(textures)}")
    if not prefab_ok:
        raise RuntimeError("Skin25 prefab root was not found")

    _, _, animator_file = bundle_parts(plain_paths["animator"])
    _, _, prefab_file = bundle_parts(plain_paths["prefab"])
    if animator_file.externals[0].name != TARGET_CABS["texture"]:
        raise RuntimeError("animator bundle does not reference the Skin25 texture CAB")
    prefab_external_names = {item.name for item in prefab_file.externals}
    for kind in ("animator", "texture"):
        if TARGET_CABS[kind] not in prefab_external_names:
            raise RuntimeError(f"prefab bundle does not reference the Skin25 {kind} CAB")


def build_bundles():
    plain_dir = BUILD_DIR / "plain"
    encrypted_dir = BUILD_DIR / "encrypted"
    if BUILD_DIR.exists():
        shutil.rmtree(BUILD_DIR)
    plain_dir.mkdir(parents=True)
    encrypted_dir.mkdir(parents=True)

    plain_paths = {kind: patch_bundle(kind, plain_dir) for kind in SOURCE_BUNDLES}
    verify_plain_bundles(plain_paths)
    metadata = {}
    for kind, path in plain_paths.items():
        encrypted, digest, crc = encrypted_metadata(path)
        encrypted_path = encrypted_dir / (digest + ".bundle")
        encrypted_path.write_bytes(encrypted)
        metadata[kind] = {
            "path": encrypted_path,
            "hash": digest,
            "crc": crc,
            "size": len(encrypted),
        }
        decrypted = unpad(AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(encrypted), AES.block_size)
        if decrypted[:7] != b"UnityFS":
            raise RuntimeError(f"encrypted {kind} bundle failed round-trip validation")
    return plain_paths, metadata


def patch_manifest(metadata) -> bytes | None:
    raw = MANIFEST.read_bytes()
    header, assets, bundles = read_manifest(raw)
    if write_manifest(header, assets, bundles) != raw:
        raise RuntimeError("manifest round-trip changed unmodified data")

    existing = [asset for asset in assets if "Skin25" in asset["path"]]
    if existing:
        if len(existing) != 36:
            raise RuntimeError(f"partial Skin25 manifest already exists ({len(existing)} assets)")
        changed = False
        for kind in ("animator", "prefab", "texture"):
            matching = [bundle for bundle in bundles if bundle["name"] == TARGET_BUNDLE_NAMES[kind]]
            if len(matching) != 1:
                raise RuntimeError(f"expected one existing Skin25 {kind} bundle record")
            bundle = matching[0]
            for field in ("hash", "crc", "size"):
                value = metadata[kind][field]
                if bundle[field] != value:
                    bundle[field] = value
                    changed = True
        if not changed:
            print("Skin25 manifest records already match the generated bundles")
            return None
        return write_manifest(header, assets, bundles)

    target_ids = {kind: len(bundles) + offset for offset, kind in enumerate(("animator", "prefab", "texture"))}
    id_mapping = {SOURCE_IDS[kind]: target_ids[kind] for kind in SOURCE_IDS}
    for asset in list(assets):
        if "Skin34" not in asset["path"]:
            continue
        cloned = deepcopy(asset)
        cloned["address"] = rename_skin(cloned["address"])
        cloned["path"] = rename_skin(cloned["path"])
        cloned["bundle"] = id_mapping[cloned["bundle"]]
        cloned["dependencies"] = [id_mapping.get(item, item) for item in cloned["dependencies"]]
        assets.append(cloned)

    if sum("Skin25" in asset["path"] for asset in assets) != 36:
        raise RuntimeError("did not clone the expected 36 Skin25 manifest assets")

    bundles[952]["references"].append(target_ids["prefab"])
    for kind in ("animator", "prefab", "texture"):
        cloned = deepcopy(bundles[SOURCE_IDS[kind]])
        cloned["name"] = TARGET_BUNDLE_NAMES[kind]
        cloned["hash"] = metadata[kind]["hash"]
        cloned["crc"] = metadata[kind]["crc"]
        cloned["size"] = metadata[kind]["size"]
        cloned["references"] = [id_mapping.get(item, item) for item in cloned["references"]]
        bundles.append(cloned)

    patched = write_manifest(header, assets, bundles)
    check_header, check_assets, check_bundles = read_manifest(patched)
    if len(check_assets) != len(assets) or len(check_bundles) != len(bundles):
        raise RuntimeError("patched manifest failed structural validation")
    if check_header != header:
        raise RuntimeError("patched manifest header changed unexpectedly")
    return patched


def backup(path: Path) -> Path:
    destination = Path(str(path) + ".pre-blue-mech.bak")
    if not destination.exists():
        shutil.copy2(path, destination)
        print("backup", destination)
    return destination


def atomic_write(path: Path, data: bytes) -> None:
    temporary = Path(str(path) + ".blue-mech.tmp")
    temporary.write_bytes(data)
    os.replace(temporary, path)


def install(metadata, patched_manifest: bytes | None) -> None:
    if patched_manifest is None:
        return
    backup(MANIFEST)
    backup(MANIFEST_HASH)
    for item in metadata.values():
        destination = CLIENT_AA1 / item["path"].name
        if destination.exists() and destination.read_bytes() != item["path"].read_bytes():
            backup(destination)
        shutil.copy2(item["path"], destination)
        print("installed", destination.name, destination.stat().st_size)
    atomic_write(MANIFEST, patched_manifest)
    atomic_write(MANIFEST_HASH, hashlib.md5(patched_manifest).hexdigest().encode("ascii"))
    print("installed manifest assets=36 bundles=3")


def verify_installed(metadata) -> None:
    raw = MANIFEST.read_bytes()
    expected_hash = hashlib.md5(raw).hexdigest()
    if MANIFEST_HASH.read_text(encoding="ascii").strip() != expected_hash:
        raise RuntimeError("manifest hash sidecar does not match")
    _, assets, bundles = read_manifest(raw)
    skin_assets = [asset for asset in assets if "Skin25" in asset["path"]]
    skin_bundles = [bundle for bundle in bundles if "skin25" in bundle["name"]]
    if len(skin_assets) != 36 or len(skin_bundles) != 3:
        raise RuntimeError("installed manifest is missing Skin25 records")
    for item in metadata.values():
        path = CLIENT_AA1 / (item["hash"] + ".bundle")
        encrypted = path.read_bytes()
        if hashlib.md5(encrypted).hexdigest() != item["hash"]:
            raise RuntimeError(f"installed bundle hash mismatch: {path.name}")
        decrypted = unpad(AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(encrypted), AES.block_size)
        environment = UnityPy.load(decrypted)
        if not environment.objects:
            raise RuntimeError(f"installed bundle is empty: {path.name}")
    print("verified installed Skin25 manifest, hashes and encrypted bundles")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--install", action="store_true", help="install verified output into client-test")
    args = parser.parse_args()
    _, metadata = build_bundles()
    patched_manifest = patch_manifest(metadata)
    if args.install:
        install(metadata, patched_manifest)
        verify_installed(metadata)
    else:
        print("build verified; run again with --install to modify client-test")


if __name__ == "__main__":
    main()
