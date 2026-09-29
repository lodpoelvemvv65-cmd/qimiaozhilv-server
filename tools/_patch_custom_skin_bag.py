"""Add direct-ID character icons to the local Bag FairyGUI package.

The existing bag atlas is an encrypted YooAsset bundle. This patch appends two
Image package items and Sprite records, then updates only the aa0 manifest
record. Existing item ids and atlas pixels are left untouched.
"""

from __future__ import annotations

import hashlib
import argparse
import os
import shutil
import struct
from pathlib import Path

import UnityPy
from Crypto.Cipher import AES
from Crypto.Util.Padding import pad, unpad
from PIL import Image, ImageDraw, ImageFilter

from _extract_character_background_refs import parse_package
from _patch_blue_mech_skin import AES_IV, AES_KEY, encrypted_metadata, read_manifest, write_manifest


ROOT = Path(__file__).resolve().parent.parent
CLIENT = ROOT / "client-test"
CLIENT_DATA = next(path for path in CLIENT.iterdir() if path.is_dir() and path.name.endswith("_Data"))
YOO = CLIENT_DATA / "StreamingAssets" / "yoo"
AA0 = YOO / "aa0"
BUILD_DIR = ROOT / "_work" / "custom-skins" / "bag"
BAG_BUNDLE = AA0 / "f786a0cc8dc91971338161393d2a12f8.bundle"
BAG_MANIFEST = AA0 / "PackageManifest_aa0_2025-01-05-1027.bytes"
BAG_HASH = BAG_MANIFEST.with_suffix(".hash")
BACKUP_SUFFIX = ".pre-custom-skins.bak"


def decrypt(path: Path) -> bytes:
    raw = path.read_bytes()
    if hashlib.md5(raw).hexdigest() != path.stem:
        raise RuntimeError(f"bundle hash mismatch: {path.name}")
    return unpad(AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(raw), AES.block_size)


def atomic_write(path: Path, data: bytes) -> None:
    temporary = Path(str(path) + ".custom-skins.tmp")
    temporary.write_bytes(data)
    os.replace(temporary, path)


def backup(path: Path) -> None:
    target = Path(str(path) + BACKUP_SUFFIX)
    if not target.exists():
        shutil.copy2(path, target)


def index_offsets(raw: bytes) -> tuple[int, list[int]]:
    # FGUI header ends at index_pos; the six relative block offsets follow it.
    index_pos = 44
    count = raw[index_pos]
    if count != 6 or raw[index_pos + 1] != 0:
        raise RuntimeError("unexpected Bag FGUI index table")
    offsets = [struct.unpack_from(">i", raw, index_pos + 2 + i * 4)[0] for i in range(count)]
    return index_pos, offsets


def item_records(raw: bytes, start: int) -> list[bytes]:
    pos = start
    count = struct.unpack_from(">h", raw, pos)[0]
    pos += 2
    records = []
    for _ in range(count):
        record_start = pos
        relative = struct.unpack_from(">i", raw, pos)[0]
        pos += 4
        end = pos + relative
        records.append(raw[record_start:end])
        pos = end
    return records


def sprite_records(raw: bytes, start: int) -> list[bytes]:
    pos = start
    count = struct.unpack_from(">h", raw, pos)[0]
    pos += 2
    records = []
    for _ in range(count):
        record_start = pos
        relative = struct.unpack_from(">H", raw, pos)[0]
        pos += 2
        end = pos + relative
        records.append(raw[record_start:end])
        pos = end
    return records


def make_item(template: bytes, item_id_ref: int, name_ref: int) -> bytes:
    data = bytearray(template)
    # Record: relative length, Image type, item id, name, source path/file...
    struct.pack_into(">H", data, 5, item_id_ref)
    struct.pack_into(">H", data, 7, name_ref)
    return bytes(data)


def make_sprite(template: bytes, item_id_ref: int, rect: tuple[int, int, int, int]) -> bytes:
    data = bytearray(template)
    # Record: relative length, item id, atlas id, x/y/w/h, rotated, optional mesh.
    struct.pack_into(">H", data, 2, item_id_ref)
    struct.pack_into(">iiii", data, 6, *rect)
    return bytes(data)


def fit_icon(frame: Image.Image, online_template: Image.Image) -> Image.Image:
    frame = frame.convert("RGBA")
    box = frame.getchannel("A").getbbox()
    if box is None:
        raise RuntimeError("generated skin frame is empty")
    frame = frame.crop(box)
    width, height = frame.size
    # Online character-equipment icons are portraits, not full-body shots.
    frame = frame.crop((round(width * 0.20), 0, round(width * 0.92), round(height * 0.80)))
    frame.thumbnail((28, 30), Image.Resampling.LANCZOS)
    icon = online_template.convert("RGBA").filter(ImageFilter.GaussianBlur(4))
    overlay = Image.new("RGBA", (32, 32), (25, 22, 32, 155))
    icon = Image.alpha_composite(icon, overlay)
    draw = ImageDraw.Draw(icon)
    draw.rectangle((0, 0, 31, 31), outline=(132, 128, 137, 255))
    draw.rectangle((1, 1, 30, 30), outline=(54, 51, 59, 255))
    icon.alpha_composite(frame, (31 - frame.width, 31 - frame.height))
    # This yellow smile is baked into every online character-equipment icon.
    icon.alpha_composite(online_template.crop((0, 0, 13, 15)), (0, 0))
    return icon


def generated_icon(root: str, online_template: Image.Image) -> Image.Image:
    path = ROOT / "_work" / "custom-skins" / ("byakuya" if root == "SkinByakuya" else "bankai_ichigo") / "plain" / (
        "assets_download_texture_animationresource_role_skin_skin_byakuya.bundle"
        if root == "SkinByakuya" else
        "assets_download_texture_animationresource_role_skin_skin_bankai_ichigo.bundle"
    )
    env = UnityPy.load(str(path))
    for obj in env.objects:
        if obj.type.name == "Texture2D" and obj.read().m_Name == f"{root}_Idle_01":
            return fit_icon(obj.read().image, online_template)
    raise RuntimeError(f"generated frame missing for {root}")


def patch_plain() -> tuple[bytes, dict]:
    plain = decrypt(BAG_BUNDLE)
    env = UnityPy.load(plain)
    fui_obj = atlas_obj = None
    fui_data = None
    fui = None
    atlas_data = None
    atlas = None
    for obj in env.objects:
        data = obj.read()
        if obj.type.name == "TextAsset" and data.m_Name == "Bag_fui":
            fui_obj, fui_data = obj, data
            fui = data.m_Script.encode("utf-8", "surrogateescape") if isinstance(data.m_Script, str) else bytes(data.m_Script)
        elif obj.type.name == "Texture2D" and data.m_Name == "Bag_atlas0":
            atlas_obj, atlas_data = obj, data
            atlas = data.image.convert("RGBA")
    if fui_obj is None or atlas_obj is None or fui_data is None or atlas_data is None or fui is None or atlas is None:
        raise RuntimeError("Bag_fui or Bag_atlas0 is missing")

    package_id, package_name, strings, items, sprites = parse_package(fui)
    by_name = {item.name: item for item in items}
    if "130001" in by_name or "130002" in by_name:
        raise RuntimeError("custom Bag icons already exist; refusing to duplicate them")
    template_item = by_name["120618"]
    online_item = by_name["120594"]
    online_sprite = sprites[online_item.item_id]
    ox, oy, ow, oh = online_sprite.rect
    online_template = atlas.crop((ox, oy, ox + ow, oy + oh))
    template_item_index = items.index(template_item)
    index_pos, offsets = index_offsets(fui)
    item_start = index_pos + offsets[1]
    sprite_start = index_pos + offsets[2]
    string_start = index_pos + offsets[4]
    item_block = item_records(fui, item_start)
    sprite_block = sprite_records(fui, sprite_start)
    if len(item_block) != len(items) or len(sprite_block) != len(sprites):
        raise RuntimeError("Bag FGUI table count mismatch")

    # Internal ids are package-local strings, while the visible names are the
    # direct item ids used by EquipBase.IconName.
    new_strings = list(strings)
    refs = {}
    for visible in ("130001", "130002"):
        internal = f"custom_{visible}"
        refs[visible] = (len(new_strings), len(new_strings) + 1)
        new_strings.extend((internal, visible))
    item_additions = []
    sprite_additions = []
    atlas = atlas.copy()
    for visible, root, rect in (
        ("130001", "SkinByakuya", (1400, 632, 32, 32)),
        ("130002", "SkinBankaiIchigo", (1432, 632, 32, 32)),
    ):
        x, y, w, h = rect
        if atlas.getchannel("A").crop((x, y, x + w, y + h)).getbbox() is not None:
            raise RuntimeError(f"Bag atlas slot is occupied: {rect}")
        atlas.alpha_composite(generated_icon(root, online_template), (x, y))
        item_additions.append(make_item(item_block[template_item_index], *refs[visible]))
        template_sprite_id = strings.index(template_item.item_id)
        sprite_template = next(record for record in sprite_block if struct.unpack_from(">H", record, 2)[0] == template_sprite_id)
        sprite_additions.append(make_sprite(sprite_template, refs[visible][0], rect))

    new_item_block = struct.pack(">H", len(item_block) + 2) + b"".join(item_block + item_additions)
    new_sprite_block = struct.pack(">H", len(sprite_block) + 2) + b"".join(sprite_block + sprite_additions)
    string_block = struct.pack(">i", len(new_strings)) + b"".join(struct.pack(">H", len(value.encode())) + value.encode() for value in new_strings)

    block0 = fui[index_pos + offsets[0]:item_start]
    prefix = bytearray(fui[:index_pos])
    new_item_start = index_pos + 2 + 4 * 6 + len(block0)
    new_sprite_start = new_item_start + len(new_item_block)
    new_string_start = new_sprite_start + len(new_sprite_block)
    new_offsets = [new_item_start - index_pos - len(block0), new_item_start - index_pos, new_sprite_start - index_pos, offsets[3], new_string_start - index_pos, offsets[5]]
    index = bytearray([6, 0]) + b"".join(struct.pack(">i", value) for value in new_offsets)
    new_fui = bytes(prefix) + bytes(index) + block0 + new_item_block + new_sprite_block + string_block
    if len(new_fui) <= len(fui):
        raise RuntimeError("Bag FGUI package did not grow")

    fui_data.m_Script = new_fui.decode("utf-8", "surrogateescape")
    fui_data.save()
    atlas_data.image = atlas
    atlas_data.save()
    for file_item in env.files.values():
        file_item.mark_changed()
    bundle = next(iter(env.files.values()))
    output_plain = BUILD_DIR / "plain.bundle"
    output_plain.parent.mkdir(parents=True, exist_ok=True)
    output_plain.write_bytes(bundle.save(packer="lz4"))
    encrypted, digest, crc = encrypted_metadata(output_plain)
    output = BUILD_DIR / f"{digest}.bundle"
    output.write_bytes(encrypted)
    return encrypted, {"hash": digest, "crc": crc, "size": len(encrypted), "plain": output_plain, "path": output, "new_fui": new_fui}


def install() -> None:
    _, metadata = patch_plain()
    backup(BAG_BUNDLE)
    backup(BAG_MANIFEST)
    backup(BAG_HASH)
    destination = AA0 / f"{metadata['hash']}.bundle"
    shutil.copy2(metadata["path"], destination)
    header, assets, bundles = read_manifest(BAG_MANIFEST.read_bytes())
    matches = [record for record in bundles if record["name"] == "assets_download_fgui_bag.bundle"]
    if len(matches) != 1:
        raise RuntimeError("Bag manifest record is missing")
    matches[0].update(hash=metadata["hash"], crc=metadata["crc"], size=metadata["size"])
    patched = write_manifest(header, assets, bundles)
    atomic_write(BAG_MANIFEST, patched)
    atomic_write(BAG_HASH, hashlib.md5(patched).hexdigest().encode("ascii"))
    # Keep the old hash-named bundle available for rollback, but make sure the
    # new encrypted file is internally addressed by its own MD5.
    if hashlib.md5(destination.read_bytes()).hexdigest() != metadata["hash"]:
        raise RuntimeError("installed Bag bundle hash mismatch")
    verify()
    print(f"installed custom Bag icons: 130001, 130002 -> {metadata['hash']}.bundle")


def verify() -> None:
    _, _, bundles = read_manifest(BAG_MANIFEST.read_bytes())
    record = next(item for item in bundles if item["name"] == "assets_download_fgui_bag.bundle")
    path = AA0 / f"{record['hash']}.bundle"
    env = UnityPy.load(decrypt(path))
    for obj in env.objects:
        data = obj.read()
        if obj.type.name == "TextAsset" and data.m_Name == "Bag_fui":
            raw = data.m_Script.encode("utf-8", "surrogateescape") if isinstance(data.m_Script, str) else bytes(data.m_Script)
            _, _, _, items, sprites = parse_package(raw)
            names = {item.name for item in items}
            if not {"120618", "120642", "130001", "130002"}.issubset(names):
                raise RuntimeError("Bag icon item verification failed")
            print(f"verified Bag FGUI: {len(items)} items / {len(sprites)} sprites")
            return
    raise RuntimeError("installed Bag_fui is missing")


def restore() -> None:
    for path in (BAG_MANIFEST, BAG_HASH):
        source = Path(str(path) + BACKUP_SUFFIX)
        if not source.is_file():
            raise RuntimeError(f"Bag rollback backup is missing: {source}")
        shutil.copy2(source, path)
    print("restored pre-custom-skins Bag manifest; generated bundle files were retained")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--restore", action="store_true")
    args = parser.parse_args()
    restore() if args.restore else install()
