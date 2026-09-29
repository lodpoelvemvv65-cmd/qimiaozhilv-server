"""Replace local Skin25 art with Death4 (Byakuya) frames, with rollback."""

from __future__ import annotations

import argparse
import hashlib
import os
import shutil
from copy import deepcopy
from pathlib import Path

import UnityPy
from Crypto.Cipher import AES
from Crypto.Util.Padding import unpad
from PIL import Image, ImageDraw

from _patch_blue_mech_skin import AES_IV, AES_KEY, encrypted_metadata, read_manifest, write_manifest


ROOT = Path(__file__).resolve().parent.parent
AA1 = ROOT / "client-test" / "梦幻奇遇记_Data" / "StreamingAssets" / "yoo" / "aa1"
MANIFEST = AA1 / "PackageManifest_aa1_2025-01-05-1027.bytes"
MANIFEST_HASH = MANIFEST.with_suffix(".hash")
BUILD_DIR = ROOT / "_work" / "byakuya-skin25-bundles"
PREVIEW = BUILD_DIR / "byakuya-skin25-preview.png"
BACKUP_SUFFIX = ".pre-byakuya-skin25.bak"

SOURCE_TEXTURE_ID = 673  # assets_download_texture_animationresource_role_monster_death4.bundle
TARGET_ANIMATOR_ID = 953  # assets_download_animators_skin_skin25.bundle
TARGET_PREFAB_ID = 954  # assets_download_role_roleprefab_skin_skin25.bundle
TARGET_TEXTURE_ID = 955  # assets_download_texture_animationresource_role_skin_skin25.bundle
TARGET_BUNDLE_IDS = (TARGET_ANIMATOR_ID, TARGET_PREFAB_ID, TARGET_TEXTURE_ID)
TARGET_TEXTURE_NAME = "assets_download_texture_animationresource_role_skin_skin25.bundle"
TARGET_TEXTURE_CANVAS = (637, 500)
TARGET_BASELINE = 462
TARGET_IDLE_HEIGHT = 246
TARGET_ATTACK_HEIGHT = 260


def decrypt_bundle(path: Path) -> bytes:
    encrypted = path.read_bytes()
    if hashlib.md5(encrypted).hexdigest() != path.stem:
        raise RuntimeError(f"bundle hash mismatch: {path.name}")
    return unpad(AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(encrypted), AES.block_size)


def bundle_parts(raw: bytes):
    environment = UnityPy.load(raw)
    bundle = next(iter(environment.files.values()))
    serialized = next(item for item in bundle.files.values() if hasattr(item, "externals"))
    return environment, bundle, serialized


def target_texture_path(manifest_bundles: list[dict]) -> Path:
    record = manifest_bundles[TARGET_TEXTURE_ID]
    if record["name"] != TARGET_TEXTURE_NAME:
        raise RuntimeError(f"unexpected Skin25 texture bundle: {record['name']}")
    return AA1 / f"{record['hash']}.bundle"


def target_bundle_paths(manifest_bundles: list[dict]) -> list[Path]:
    paths = []
    for bundle_id in TARGET_BUNDLE_IDS:
        record = manifest_bundles[bundle_id]
        paths.append(AA1 / f"{record['hash']}.bundle")
    return paths


def source_texture_path(manifest_bundles: list[dict]) -> Path:
    record = manifest_bundles[SOURCE_TEXTURE_ID]
    expected = "assets_download_texture_animationresource_role_monster_death4.bundle"
    if record["name"] != expected:
        raise RuntimeError(f"unexpected Death4 texture bundle: {record['name']}")
    return AA1 / f"{record['hash']}.bundle"


def source_images(manifest_bundles: list[dict]) -> dict[str, Image.Image]:
    path = source_texture_path(manifest_bundles)
    environment = UnityPy.load(decrypt_bundle(path))
    images = {}
    for obj in environment.objects:
        if obj.type.name != "Texture2D":
            continue
        data = obj.read()
        images[data.m_Name] = data.image.convert("RGBA")
    required = (
        [f"Death4_Idle_{index:02d}" for index in range(1, 11)]
        + [f"Death4_Attack_{index:02d}" for index in range(1, 9)]
        + ["Death4_Hurt_01"]
    )
    missing = [name for name in required if name not in images]
    if missing:
        raise RuntimeError("Death4 source frames missing: " + ", ".join(missing))
    return images


def frame_source_name(target_name: str) -> str:
    action, number = target_name[len("Skin25_"):].rsplit("_", 1)
    index = int(number)
    if action == "Idle":
        return f"Death4_Idle_{(index - 1) % 10 + 1:02d}"
    if action == "Run":
        return f"Death4_Idle_{(index - 1) % 8 + 1:02d}"
    if action == "Attack":
        return f"Death4_Attack_{(index - 1) % 8 + 1:02d}"
    if action == "Hurt":
        return "Death4_Hurt_01"
    raise RuntimeError(f"unsupported Skin25 frame: {target_name}")


def compose_frame(source: Image.Image, action: str) -> Image.Image:
    source = source.convert("RGBA")
    alpha_box = source.getchannel("A").getbbox()
    if alpha_box is None:
        raise RuntimeError("source frame is transparent: no visible pixels")
    target_height = TARGET_ATTACK_HEIGHT if action == "Attack" else TARGET_IDLE_HEIGHT
    scale = target_height / max(1, alpha_box[3] - alpha_box[1])
    width = max(1, round(source.width * scale))
    height = max(1, round(source.height * scale))
    resized = source.resize((width, height), Image.Resampling.LANCZOS)
    resized_alpha = resized.getchannel("A")
    resized_box = resized_alpha.getbbox()
    if resized_box is None:
        raise RuntimeError("resized frame is transparent: no visible pixels")
    canvas = Image.new("RGBA", TARGET_TEXTURE_CANVAS, (0, 0, 0, 0))
    x = (TARGET_TEXTURE_CANVAS[0] - (resized_box[2] - resized_box[0])) // 2 - resized_box[0]
    y = TARGET_BASELINE - resized_box[3]
    if x < -resized.width or x > TARGET_TEXTURE_CANVAS[0] or y < -resized.height or y > TARGET_TEXTURE_CANVAS[1]:
        raise RuntimeError(f"composed frame is outside canvas: source={source.size} box={resized_box}")
    canvas.alpha_composite(resized, (x, y))
    return canvas


def patch_texture_bundle(manifest_bundles: list[dict], preview_frames: list[tuple[str, Image.Image]]) -> Path:
    target_path = target_texture_path(manifest_bundles)
    environment, bundle, _ = bundle_parts(decrypt_bundle(target_path))
    source = source_images(manifest_bundles)
    target_names = []
    for obj in environment.objects:
        if obj.type.name != "Texture2D":
            continue
        data = obj.read()
        if not data.m_Name.startswith("Skin25_"):
            raise RuntimeError(f"unexpected target texture: {data.m_Name}")
        action = data.m_Name[len("Skin25_"):].rsplit("_", 1)[0]
        replacement = compose_frame(source[frame_source_name(data.m_Name)], action)
        data.image = replacement
        data.save()
        target_names.append(data.m_Name)
        if data.m_Name in {
            "Skin25_Idle_01", "Skin25_Idle_26", "Skin25_Idle_52",
            "Skin25_Run_01", "Skin25_Run_08", "Skin25_Attack_01",
            "Skin25_Attack_08", "Skin25_Hurt_01",
        }:
            preview_frames.append((data.m_Name, replacement.copy()))

    expected = {f"Skin25_Idle_{index:02d}" for index in range(1, 53)}
    expected |= {f"Skin25_Run_{index:02d}" for index in range(1, 9)}
    expected |= {f"Skin25_Attack_{index:02d}" for index in range(1, 9)}
    expected.add("Skin25_Hurt_01")
    if set(target_names) != expected:
        raise RuntimeError(f"Skin25 frame contract mismatch: {len(target_names)} textures")

    plain_dir = BUILD_DIR / "plain"
    plain_dir.mkdir(parents=True, exist_ok=True)
    output = plain_dir / TARGET_TEXTURE_NAME
    output.write_bytes(bundle.save(packer="lz4"))
    return output


def write_preview(frames: list[tuple[str, Image.Image]]) -> None:
    PREVIEW.parent.mkdir(parents=True, exist_ok=True)
    tile = (319, 250)
    label_height = 24
    columns = 3
    rows = (len(frames) + columns - 1) // columns
    sheet = Image.new("RGBA", (columns * tile[0], rows * (tile[1] + label_height)), (28, 29, 31, 255))
    draw = ImageDraw.Draw(sheet)
    for index, (name, image) in enumerate(sorted(frames)):
        x = (index % columns) * tile[0]
        y = (index // columns) * (tile[1] + label_height)
        sheet.alpha_composite(image.resize(tile, Image.Resampling.LANCZOS), (x, y + label_height))
        draw.text((x + 6, y + 5), name, fill=(255, 255, 255, 255))
    sheet.save(PREVIEW)


def patch_manifest(metadata: dict) -> bytes:
    header, assets, bundles = read_manifest(MANIFEST.read_bytes())
    if write_manifest(header, assets, bundles) != MANIFEST.read_bytes():
        raise RuntimeError("manifest round-trip changed unmodified data")
    target = bundles[TARGET_TEXTURE_ID]
    if target["name"] != TARGET_TEXTURE_NAME:
        raise RuntimeError("Skin25 texture bundle record moved")
    target = deepcopy(target)
    target["hash"] = metadata["hash"]
    target["crc"] = metadata["crc"]
    target["size"] = metadata["size"]
    bundles[TARGET_TEXTURE_ID] = target
    patched = write_manifest(header, assets, bundles)
    _, check_assets, check_bundles = read_manifest(patched)
    if len(check_assets) != len(assets) or len(check_bundles) != len(bundles):
        raise RuntimeError("patched manifest failed structural validation")
    return patched


def backup_once(path: Path) -> Path:
    backup = Path(str(path) + BACKUP_SUFFIX)
    if not backup.exists():
        shutil.copy2(path, backup)
        print("backup", backup)
    return backup


def atomic_write(path: Path, data: bytes) -> None:
    temporary = Path(str(path) + ".byakuya-skin25.tmp")
    temporary.write_bytes(data)
    os.replace(temporary, path)


def build() -> tuple[dict, bytes, list[Path]]:
    if BUILD_DIR.exists():
        shutil.rmtree(BUILD_DIR)
    header, assets, bundles = read_manifest(MANIFEST.read_bytes())
    if write_manifest(header, assets, bundles) != MANIFEST.read_bytes():
        raise RuntimeError("manifest round-trip changed unmodified data")
    preview_frames: list[tuple[str, Image.Image]] = []
    plain = patch_texture_bundle(bundles, preview_frames)
    write_preview(preview_frames)
    encrypted, digest, crc = encrypted_metadata(plain)
    encrypted_path = BUILD_DIR / "encrypted" / f"{digest}.bundle"
    encrypted_path.parent.mkdir(parents=True, exist_ok=True)
    encrypted_path.write_bytes(encrypted)
    metadata = {"path": encrypted_path, "hash": digest, "crc": crc, "size": len(encrypted)}
    patched_manifest = patch_manifest(metadata)
    print("built Byakuya Skin25 texture", encrypted_path.name, encrypted_path.stat().st_size)
    print("preview", PREVIEW)
    return metadata, patched_manifest, target_bundle_paths(bundles)


def verify_bundle(path: Path) -> None:
    environment = UnityPy.load(decrypt_bundle(path))
    textures = [obj.read() for obj in environment.objects if obj.type.name == "Texture2D"]
    sprites = [obj.read() for obj in environment.objects if obj.type.name == "Sprite"]
    if len(textures) != 69 or len(sprites) != 69:
        raise RuntimeError(f"Skin25 texture bundle object count mismatch: textures={len(textures)} sprites={len(sprites)}")
    for data in textures:
        if data.image.size != TARGET_TEXTURE_CANVAS or data.image.getchannel("A").getbbox() is None:
            raise RuntimeError(f"invalid Byakuya Skin25 frame: {data.m_Name}")
    print("verified Byakuya Skin25 texture bundle: 69 textures / 69 sprites")


def install(metadata: dict, patched_manifest: bytes, old_paths: list[Path]) -> None:
    backup_once(MANIFEST)
    backup_once(MANIFEST_HASH)
    for old_path in old_paths:
        backup_once(old_path)
    destination = AA1 / metadata["path"].name
    shutil.copy2(metadata["path"], destination)
    atomic_write(MANIFEST, patched_manifest)
    atomic_write(MANIFEST_HASH, hashlib.md5(patched_manifest).hexdigest().encode("ascii"))
    verify_bundle(destination)
    print("installed Byakuya over local Skin25:", destination.name)


def restore() -> None:
    manifest_backup = Path(str(MANIFEST) + BACKUP_SUFFIX)
    hash_backup = Path(str(MANIFEST_HASH) + BACKUP_SUFFIX)
    if not manifest_backup.is_file() or not hash_backup.is_file():
        raise RuntimeError("Byakuya Skin25 rollback backups are incomplete")
    old_manifest = manifest_backup.read_bytes()
    if hash_backup.read_text(encoding="ascii").strip() != hashlib.md5(old_manifest).hexdigest():
        raise RuntimeError("rollback manifest and hash sidecar do not match")
    atomic_write(MANIFEST, old_manifest)
    atomic_write(MANIFEST_HASH, hash_backup.read_bytes())
    _, _, bundles = read_manifest(old_manifest)
    for bundle_id in TARGET_BUNDLE_IDS:
        old_path = AA1 / f"{bundles[bundle_id]['hash']}.bundle"
        backup_path = Path(str(old_path) + BACKUP_SUFFIX)
        if not old_path.exists() and backup_path.exists():
            shutil.copy2(backup_path, old_path)
        if not old_path.exists():
            raise RuntimeError(f"original Skin25 bundle is missing: {old_path.name}")
    print("restored original Bumblebee Skin25 manifest and texture bundle")


def main() -> None:
    parser = argparse.ArgumentParser()
    action = parser.add_mutually_exclusive_group()
    action.add_argument("--install", action="store_true", help="install into client-test only")
    action.add_argument("--restore", action="store_true", help="restore the pre-Byakuya Skin25 files")
    args = parser.parse_args()
    if args.restore:
        restore()
        return
    metadata, patched_manifest, old_paths = build()
    verify_bundle(metadata["path"])
    if args.install:
        install(metadata, patched_manifest, old_paths)
    else:
        print("build verified; inspect the preview before --install")


if __name__ == "__main__":
    main()
