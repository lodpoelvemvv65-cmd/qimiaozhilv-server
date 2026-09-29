"""Build and install the animated Skin25 thunderwing heavy-blade replacement."""

from __future__ import annotations

import argparse
import hashlib
import os
import shutil
import sys
from copy import deepcopy
from pathlib import Path

import numpy as np
import UnityPy
from Crypto.Cipher import AES
from Crypto.Util.Padding import unpad
from PIL import Image, ImageDraw

from _patch_blue_mech_skin import (
    AES_IV,
    AES_KEY,
    encrypted_metadata,
    read_manifest,
    write_manifest,
)


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
AA1 = ROOT / "client-test" / "梦幻奇遇记_Data" / "StreamingAssets" / "yoo" / "aa1"
MANIFEST = AA1 / "PackageManifest_aa1_2025-01-05-1027.bytes"
MANIFEST_HASH = MANIFEST.with_suffix(".hash")
BUILD_DIR = ROOT / "_work" / "thunderwing-bundles"
FRAME_DIR = ROOT / "_work" / "thunderwing-frames"

SOURCE_IDS = {"animator": 149, "prefab": 574, "texture": 822}
SOURCE_CABS = {
    "animator": "CAB-cc6cc007387f21456ae0c630c283dee6",
    "prefab": "CAB-ceb288f5a3249eee3ade9e0a87a7d7cd",
    "texture": "CAB-875666b0093febf765c75aba546ed0cb",
}
TARGET_CABS = {
    kind: "CAB-" + hashlib.md5(("thunderwing-skin25-" + kind).encode()).hexdigest()
    for kind in SOURCE_CABS
}
TARGET_BUNDLE_NAMES = {
    "animator": "assets_download_animators_skin_skin25.bundle",
    "prefab": "assets_download_role_roleprefab_skin_skin25.bundle",
    "texture": "assets_download_texture_animationresource_role_skin_skin25.bundle",
}


def rename_skin(value: str) -> str:
    return value.replace("Skin30", "Skin25").replace("skin30", "skin25")


def decrypt_bundle(path: Path) -> bytes:
    encrypted = path.read_bytes()
    return unpad(AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(encrypted), AES.block_size)


def bundle_parts(raw_or_path):
    if isinstance(raw_or_path, Path):
        raw_or_path = str(raw_or_path)
    environment = UnityPy.load(raw_or_path)
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


def recolor(image: Image.Image) -> Image.Image:
    pixels = np.asarray(image.convert("RGBA"), dtype=np.float32)
    rgb = pixels[..., :3]
    alpha = pixels[..., 3:4]
    maximum = rgb.max(axis=2)
    minimum = rgb.min(axis=2)
    saturation = maximum - minimum
    luminance = rgb[..., 0] * 0.299 + rgb[..., 1] * 0.587 + rgb[..., 2] * 0.114

    visible = alpha[..., 0] > 0
    warm = visible & (rgb[..., 0] > rgb[..., 2] * 1.10) & (rgb[..., 0] > rgb[..., 1] * 1.03) & (saturation > 22)
    cyan = visible & (rgb[..., 2] > rgb[..., 0] * 1.18) & (rgb[..., 1] > rgb[..., 0] * 1.08)
    neutral = visible & (saturation <= 38)
    other = visible & ~warm & ~cyan & ~neutral

    output = rgb.copy()
    # Warm hair, skin and red accents become cobalt armor while retaining the source shading.
    output[..., 0][warm] = luminance[warm] * 0.20
    output[..., 1][warm] = luminance[warm] * 0.48 + 12
    output[..., 2][warm] = luminance[warm] * 1.08 + 28
    # Existing cyan sword effects remain luminous and become the common energy language.
    output[..., 0][cyan] = luminance[cyan] * 0.10
    output[..., 1][cyan] = luminance[cyan] * 0.95 + 18
    output[..., 2][cyan] = luminance[cyan] * 1.22 + 32
    # White wings, blade and grey armor become cool silver instead of flat blue.
    output[..., 0][neutral] = luminance[neutral] * 0.78
    output[..., 1][neutral] = luminance[neutral] * 0.91 + 4
    output[..., 2][neutral] = luminance[neutral] * 1.04 + 10
    # Dark clothing and remaining colors are pulled into a deep navy mechanical base.
    output[..., 0][other] = output[..., 0][other] * 0.42
    output[..., 1][other] = output[..., 1][other] * 0.70
    output[..., 2][other] = np.maximum(output[..., 2][other] * 1.04, luminance[other] * 0.82 + 18)

    output = np.clip(output, 0, 255)
    result = np.concatenate((output, alpha), axis=2).astype(np.uint8)
    return Image.fromarray(result, "RGBA")


def source_bundle(kind: str, bundles) -> bytes:
    record = bundles[SOURCE_IDS[kind]]
    return decrypt_bundle(AA1 / f"{record['hash']}.bundle")


def patch_asset_bundle_object(obj) -> None:
    data = obj.read()
    data.m_Name = rename_skin(data.m_Name)
    data.m_Container = [(rename_skin(path), info) for path, info in data.m_Container]
    data.save()


def patch_bundle(kind: str, bundles, plain_dir: Path) -> Path:
    environment, bundle, serialized = bundle_parts(source_bundle(kind, bundles))
    patch_external_cabs(serialized)
    for obj in environment.objects:
        data = obj.read()
        if obj.type.name == "AssetBundle":
            patch_asset_bundle_object(obj)
            continue
        if hasattr(data, "m_Name") and isinstance(data.m_Name, str):
            data.m_Name = rename_skin(data.m_Name)
        if obj.type.name == "Texture2D":
            data.image = recolor(data.image)
        data.save()

    rename_serialized_file(bundle, serialized, TARGET_CABS[kind])
    output = plain_dir / TARGET_BUNDLE_NAMES[kind]
    output.write_bytes(bundle.save(packer="lz4"))
    return output


def verify_plain_bundles(paths: dict[str, Path]) -> None:
    environment = UnityPy.load(*[str(path) for path in paths.values()])
    types = {}
    names = set()
    prefab_ok = False
    for obj in environment.objects:
        types[obj.type.name] = types.get(obj.type.name, 0) + 1
        data = obj.read()
        name = getattr(data, "m_Name", "")
        if "Skin30" in str(name):
            raise RuntimeError(f"unrenamed Skin30 object: {name}")
        if name:
            names.add(name)
        if obj.type.name == "Texture2D":
            image = data.image.convert("RGBA")
            if image.size != (637, 500) or image.getbbox() is None:
                raise RuntimeError(f"invalid Skin25 texture: {name}")
        if obj.type.name == "GameObject" and name == "Skin25":
            prefab_ok = True

    expected_clips = {"Skin25_Idle", "Skin25_Run", "Skin25_Attack", "Skin25_Hurt"}
    if types.get("Texture2D") != 69 or types.get("Sprite") != 69:
        raise RuntimeError(f"expected 69 textures and sprites, found {types}")
    if not expected_clips.issubset(names):
        raise RuntimeError("Skin25 animation clips are incomplete")
    if not prefab_ok:
        raise RuntimeError("Skin25 prefab root was not found")

    _, _, animator = bundle_parts(paths["animator"])
    _, _, prefab = bundle_parts(paths["prefab"])
    if animator.externals[0].name != TARGET_CABS["texture"]:
        raise RuntimeError("animator does not reference the Skin25 texture CAB")
    prefab_externals = {item.name for item in prefab.externals}
    if not {TARGET_CABS["animator"], TARGET_CABS["texture"]}.issubset(prefab_externals):
        raise RuntimeError("prefab animation dependencies are incomplete")


def export_frame_preview(texture_path: Path) -> None:
    environment = UnityPy.load(str(texture_path))
    wanted = {
        "Skin25_Idle_01", "Skin25_Idle_52",
        "Skin25_Run_01", "Skin25_Run_03", "Skin25_Run_06", "Skin25_Run_08",
        "Skin25_Attack_01", "Skin25_Attack_02", "Skin25_Attack_04", "Skin25_Attack_05",
        "Skin25_Attack_07", "Skin25_Attack_08", "Skin25_Hurt_01",
    }
    frames = []
    FRAME_DIR.mkdir(parents=True, exist_ok=True)
    for obj in environment.objects:
        if obj.type.name != "Texture2D":
            continue
        data = obj.read()
        if data.m_Name in wanted:
            image = data.image.convert("RGBA")
            image.save(FRAME_DIR / f"{data.m_Name}.png")
            frames.append((data.m_Name, image))
    frames.sort()
    tile_w, tile_h, label_h, columns = 637, 500, 24, 3
    rows = (len(frames) + columns - 1) // columns
    sheet = Image.new("RGBA", (columns * tile_w, rows * (tile_h + label_h)), (26, 26, 28, 255))
    draw = ImageDraw.Draw(sheet)
    for index, (name, image) in enumerate(frames):
        x = index % columns * tile_w
        y = index // columns * (tile_h + label_h)
        sheet.alpha_composite(image, (x, y + label_h))
        draw.text((x + 6, y + 5), name, fill="white")
    sheet.save(FRAME_DIR / "thunderwing-contact.png")


def build_bundles():
    raw = MANIFEST.read_bytes()
    header, assets, bundles = read_manifest(raw)
    if write_manifest(header, assets, bundles) != raw:
        raise RuntimeError("manifest round-trip changed unmodified data")
    if BUILD_DIR.exists():
        shutil.rmtree(BUILD_DIR)
    plain_dir = BUILD_DIR / "plain"
    encrypted_dir = BUILD_DIR / "encrypted"
    plain_dir.mkdir(parents=True)
    encrypted_dir.mkdir(parents=True)
    paths = {kind: patch_bundle(kind, bundles, plain_dir) for kind in SOURCE_IDS}
    verify_plain_bundles(paths)
    export_frame_preview(paths["texture"])
    metadata = {}
    for kind, path in paths.items():
        encrypted, digest, crc = encrypted_metadata(path)
        encrypted_path = encrypted_dir / f"{digest}.bundle"
        encrypted_path.write_bytes(encrypted)
        metadata[kind] = {"path": encrypted_path, "hash": digest, "crc": crc, "size": len(encrypted)}
    return metadata


def patch_manifest(metadata) -> bytes:
    header, assets, bundles = read_manifest(MANIFEST.read_bytes())
    source_assets = [asset for asset in assets if "Skin30" in asset["path"]]
    if len(source_assets) != 74:
        raise RuntimeError(f"expected 74 Skin30 assets, found {len(source_assets)}")

    target_bundle_ids = {}
    for kind, name in TARGET_BUNDLE_NAMES.items():
        matches = [index for index, bundle in enumerate(bundles) if bundle["name"] == name]
        if len(matches) != 1:
            raise RuntimeError(f"expected one existing Skin25 {kind} bundle")
        target_bundle_ids[kind] = matches[0]
    id_mapping = {SOURCE_IDS[kind]: target_bundle_ids[kind] for kind in SOURCE_IDS}

    assets = [asset for asset in assets if "Skin25" not in asset["path"]]
    for source in source_assets:
        cloned = deepcopy(source)
        cloned["address"] = rename_skin(cloned["address"])
        cloned["path"] = rename_skin(cloned["path"])
        cloned["bundle"] = id_mapping[cloned["bundle"]]
        cloned["dependencies"] = [id_mapping.get(item, item) for item in cloned["dependencies"]]
        assets.append(cloned)
    if sum("Skin25" in asset["path"] for asset in assets) != 74:
        raise RuntimeError("Skin25 asset replacement is incomplete")

    for kind, target_id in target_bundle_ids.items():
        source = deepcopy(bundles[SOURCE_IDS[kind]])
        source["name"] = TARGET_BUNDLE_NAMES[kind]
        source["hash"] = metadata[kind]["hash"]
        source["crc"] = metadata[kind]["crc"]
        source["size"] = metadata[kind]["size"]
        source["references"] = [id_mapping.get(item, item) for item in source["references"]]
        bundles[target_id] = source
    if target_bundle_ids["prefab"] not in bundles[952]["references"]:
        bundles[952]["references"].append(target_bundle_ids["prefab"])

    paths = [asset["path"] for asset in assets]
    if len(paths) != len(set(paths)):
        raise RuntimeError("manifest contains duplicate asset paths")
    patched = write_manifest(header, assets, bundles)
    check_header, check_assets, check_bundles = read_manifest(patched)
    if check_header != header or len(check_assets) != len(assets) or len(check_bundles) != len(bundles):
        raise RuntimeError("patched manifest failed structural validation")
    return patched


def backup(path: Path) -> Path:
    destination = Path(str(path) + ".pre-thunderwing.bak")
    if not destination.exists():
        shutil.copy2(path, destination)
        print("backup", destination)
    return destination


def atomic_write(path: Path, data: bytes) -> None:
    temporary = Path(str(path) + ".thunderwing.tmp")
    temporary.write_bytes(data)
    os.replace(temporary, path)


def install(metadata, patched_manifest: bytes) -> None:
    backup(MANIFEST)
    backup(MANIFEST_HASH)
    for item in metadata.values():
        destination = AA1 / item["path"].name
        if destination.exists() and destination.read_bytes() != item["path"].read_bytes():
            backup(destination)
        shutil.copy2(item["path"], destination)
        print("installed", destination.name, destination.stat().st_size)
    atomic_write(MANIFEST, patched_manifest)
    atomic_write(MANIFEST_HASH, hashlib.md5(patched_manifest).hexdigest().encode("ascii"))


def verify_installed(metadata) -> None:
    raw = MANIFEST.read_bytes()
    if MANIFEST_HASH.read_text(encoding="ascii").strip() != hashlib.md5(raw).hexdigest():
        raise RuntimeError("manifest hash sidecar does not match")
    _, assets, bundles = read_manifest(raw)
    if sum("Skin25" in asset["path"] for asset in assets) != 74:
        raise RuntimeError("installed manifest does not contain 74 Skin25 assets")
    if sum("skin25" in bundle["name"] for bundle in bundles) != 3:
        raise RuntimeError("installed manifest does not contain three Skin25 bundles")
    for item in metadata.values():
        path = AA1 / f"{item['hash']}.bundle"
        encrypted = path.read_bytes()
        if hashlib.md5(encrypted).hexdigest() != item["hash"]:
            raise RuntimeError(f"installed bundle hash mismatch: {path.name}")
        if not UnityPy.load(decrypt_bundle(path)).objects:
            raise RuntimeError(f"installed bundle is empty: {path.name}")
    print("verified animated Skin25: 52 idle / 8 run / 8 attack / 1 hurt")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--install", action="store_true")
    args = parser.parse_args()
    metadata = build_bundles()
    patched_manifest = patch_manifest(metadata)
    if args.install:
        install(metadata, patched_manifest)
        verify_installed(metadata)
    else:
        print("build verified; inspect thunderwing-contact.png before --install")


if __name__ == "__main__":
    main()
