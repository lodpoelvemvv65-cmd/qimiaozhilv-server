"""Install two direct-ID character skins in the local client only.

The source bosses are 2D frame bundles. Player skins need the existing Skin25
Animator/Prefab contract (Idle, Run, Attack and Hurt), so this tool clones
that contract and replaces only the texture frames. It never changes Skin25
or the public client.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
import shutil
import struct
import zlib
from copy import deepcopy
from pathlib import Path

import UnityPy
from Crypto.Cipher import AES
from Crypto.Util.Padding import pad, unpad
from PIL import Image, ImageDraw

from _patch_blue_mech_skin import AES_IV, AES_KEY, encrypted_metadata, read_manifest, write_manifest


ROOT = Path(__file__).resolve().parent.parent
CLIENT = ROOT / "client-test"
CLIENT_DATA = next(path for path in CLIENT.iterdir() if path.is_dir() and path.name.endswith("_Data"))
YOO = CLIENT_DATA / "StreamingAssets" / "yoo"
AA0 = YOO / "aa0"
AA1 = YOO / "aa1"
AA1_MANIFEST = AA1 / "PackageManifest_aa1_2025-01-05-1027.bytes"
AA1_HASH = AA1_MANIFEST.with_suffix(".hash")
AA0_MANIFEST = AA0 / "PackageManifest_aa0_2025-01-05-1027.bytes"
AA0_HASH = AA0_MANIFEST.with_suffix(".hash")
CONFIG_BUNDLE = AA0 / "87c13c7e72546d4e4e0960392e451498.bundle"
BAG_BUNDLE = AA0 / "f786a0cc8dc91971338161393d2a12f8.bundle"
BAG_BUNDLE_NAME = "assets_download_fgui_bag.bundle"
BAG_ATLAS_NAME = "Bag_atlas0"
BAG_FUI_NAME = "Bag_fui"
BUILD_DIR = ROOT / "_work" / "custom-skins"
PREVIEW = BUILD_DIR / "custom-skins-preview.png"
BACKUP_SUFFIX = ".pre-custom-skins.bak"

SKIN25_ROOT = "Skin25"
TEXTURE_CANVAS = (637, 500)
BASELINE = 462
IDLE_HEIGHT = 246
ATTACK_HEIGHT = 260
TARGET_SPRITE_PIVOT = (TEXTURE_CANVAS[0] * 0.5, TEXTURE_CANVAS[1] * (1.0 - 0.08))

SKINS = {
    "byakuya": {
        "label": "Byakuya",
        "root": "SkinByakuya",
        "skin_id": 130001,
        "prefab_id": 13001,
        "equip_row": {
            "_id": 130001,
            "Name": "朽木白哉",
            "DetialType": 2,
            "Type": 2,
            "UseLevel": 1000,
            "SpecialKey": 9,
            "SpecialValue": 3000,
            "Hp": 10000,
            "Mp": 10000,
            "PhyAtk": 3000,
            "SpiAtk": 3000,
            "Pcri": 0.1,
            "Mcri": 0.1,
            "IconName": "130001",
            "Quality": 4,
            "Star": 6,
            "Description": "朽木白哉形象装备",
            "MaxHole": 4,
            "InlayPriceArr": [{"_t": "EquipBase+InlayPrice", "Price_Coin": 5000000, "Price_YuanBao": 0}],
            "CanInlayGemTypeArr": [1, 2, 3, 4, 5, 6, 7, 8],
        },
        "monster_texture_name": "assets_download_texture_animationresource_role_monster_death4.bundle",
        "monster_texture_id": 673,
        "bundle_names": {
            "animator": "assets_download_animators_skin_skin_byakuya.bundle",
            "prefab": "assets_download_role_roleprefab_skin_skin_byakuya.bundle",
            "texture": "assets_download_texture_animationresource_role_skin_skin_byakuya.bundle",
        },
    },
    "bankai_ichigo": {
        "label": "BankaiIchigo",
        "root": "SkinBankaiIchigo",
        "skin_id": 130002,
        "prefab_id": 13002,
        "equip_row": {
            "_id": 130002,
            "Name": "卍解一护",
            "DetialType": 2,
            "Type": 2,
            "UseLevel": 1000,
            "SpecialKey": 9,
            "SpecialValue": 10000,
            "Hp": 20000,
            "Mp": 20000,
            "PhyAtk": 10000,
            "SpiAtk": 10000,
            "Pcri": 0.1,
            "Mcri": 0.1,
            "IconName": "130002",
            "Quality": 4,
            "Star": 6,
            "Description": "卍解一护形象装备",
            "MaxHole": 4,
            "InlayPriceArr": [{"_t": "EquipBase+InlayPrice", "Price_Coin": 5000000, "Price_YuanBao": 0}],
            "CanInlayGemTypeArr": [1, 2, 3, 4, 5, 6, 7, 8],
        },
        "monster_texture_name": "assets_download_texture_animationresource_role_monster_death16.bundle",
        "monster_texture_id": 685,
        "bundle_names": {
            "animator": "assets_download_animators_skin_skin_bankai_ichigo.bundle",
            "prefab": "assets_download_role_roleprefab_skin_skin_bankai_ichigo.bundle",
            "texture": "assets_download_texture_animationresource_role_skin_skin_bankai_ichigo.bundle",
        },
    },
}


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


def rename_skin(value: str, target_root: str) -> str:
    return value.replace(SKIN25_ROOT, target_root).replace("skin25", target_root.lower())


def patch_external_cabs(serialized, source_cabs: dict[str, str], target_cabs: dict[str, str]) -> None:
    for external in serialized.externals:
        for kind, source_cab in source_cabs.items():
            if source_cab in external.path:
                external.path = external.path.replace(source_cab, target_cabs[kind])


def rename_serialized_file(bundle, serialized, target_cab: str, target_root: str) -> None:
    old_name = serialized.name
    rebuilt = {}
    for name, item in bundle.files.items():
        if name == old_name:
            rebuilt[target_cab] = item
        elif name == old_name + ".resS":
            continue
        else:
            rebuilt[rename_skin(name, target_root)] = item
    bundle.files = rebuilt
    serialized.name = target_cab
    bundle.cab_file = target_cab + ".resS"


def patch_asset_bundle_object(data, target_root: str) -> None:
    data.m_Name = rename_skin(data.m_Name, target_root)
    data.m_Container = [(rename_skin(path, target_root), info) for path, info in data.m_Container]


def source_images(manifest_bundles: list[dict], skin: dict) -> dict[str, Image.Image]:
    record = manifest_bundles[skin["monster_texture_id"]]
    if record["name"] != skin["monster_texture_name"]:
        raise RuntimeError(f"unexpected source texture bundle: {record['name']}")
    environment = UnityPy.load(decrypt_bundle(AA1 / f"{record['hash']}.bundle"))
    images = {}
    for obj in environment.objects:
        if obj.type.name != "Texture2D":
            continue
        data = obj.read()
        images[data.m_Name] = data.image.convert("RGBA")
    prefix = "Death4" if skin["monster_texture_id"] == 673 else "Death16"
    required = (
        [f"{prefix}_Idle_{index:02d}" for index in range(1, 11)]
        + [f"{prefix}_Attack_{index:02d}" for index in range(1, 25 if prefix == "Death4" else 18)]
        + [f"{prefix}_Hurt_01"]
    )
    missing = [name for name in required if name not in images]
    if missing:
        raise RuntimeError("source frames missing: " + ", ".join(missing))
    return images


def source_pivots(manifest_bundles: list[dict], skin: dict) -> dict[str, tuple[float, float]]:
    """Read the source Sprite registration points before extracting pixels."""
    record = manifest_bundles[skin["monster_texture_id"]]
    environment = UnityPy.load(decrypt_bundle(AA1 / f"{record['hash']}.bundle"))
    prefix = "Death4" if skin["monster_texture_id"] == 673 else "Death16"
    pivots = {}
    for obj in environment.objects:
        if obj.type.name != "Sprite":
            continue
        data = obj.read()
        if not data.m_Name.startswith(prefix + "_"):
            continue
        pivots[data.m_Name] = (float(data.m_Pivot.x), float(data.m_Pivot.y))
    if not pivots:
        raise RuntimeError(f"source Sprite pivots missing for {skin['label']}")
    return pivots


def reference_scale(images: dict[str, Image.Image], prefix: str, action: str, target_height: int) -> float:
    """Use one scale for a whole action so frame bounds cannot resize the actor."""
    heights = []
    for name, image in images.items():
        if not name.startswith(prefix + "_" + action + "_"):
            continue
        box = image.getchannel("A").getbbox()
        if box is not None:
            heights.append(box[3] - box[1])
    if not heights:
        raise RuntimeError(f"source {action} frames have no visible pixels")
    heights.sort()
    median = heights[len(heights) // 2]
    return target_height / max(1, median)


def idle_action_source(prefix: str, index: int, attack_count: int) -> str:
    """Hold still for most of the loop, then play one complete flourish."""
    if index < 35 or index > 46:
        return f"{prefix}_Idle_01"
    if prefix == "Death4":
        # Calm sleeve release and return, with the original petal/reiatsu aura.
        flourish = (24, 23, 22, 21, 20, 19, 20, 21, 22, 23, 24, 24)
    else:
        # Raise Tensa Zangetsu, shift into guard, then settle without striking.
        flourish = (1, 1, 2, 2, 3, 4, 3, 2, 2, 1, 1, 1)
    return f"{prefix}_Attack_{flourish[index - 35]:02d}"


def run_action_source(prefix: str, index: int, attack_count: int) -> str:
    """Use a coherent source sequence because these bosses ship no Run clip."""
    if prefix == "Death4":
        # Byakuya's movement reads as shunpo: upright, calm, sleeves and petals
        # moving around him instead of an exaggerated jogging cycle.
        sequence = (20, 21, 22, 23, 24, 23, 22, 21)
    else:
        # Ichigo advances with Tensa Zangetsu raised and the robe fully moving.
        sequence = (1, 2, 3, 4, 5, 4, 3, 2)
    return f"{prefix}_Attack_{sequence[(index - 1) % len(sequence)]:02d}"


def sampled_frame(prefix: str, action: str, index: int, images: dict[str, Image.Image]) -> Image.Image:
    if action == "Idle":
        count = 10
        source_index = (index - 1) % count + 1
    elif action == "Run":
        attack_count = len([name for name in images if name.startswith(prefix + "_Attack_")])
        return images[run_action_source(prefix, index, attack_count)]
    elif action == "Attack":
        source_count = len([name for name in images if name.startswith(prefix + "_Attack_")])
        target_count = 8
        source_index = int(((index - 1) * (source_count - 1) / (target_count - 1)) + 0.5) + 1
    elif action == "Hurt":
        source_index = 1
    else:
        raise RuntimeError(f"unsupported action: {action}")
    if action == "Hurt":
        return images[f"{prefix}_Hurt_01"]
    if action == "Run":
        return images[f"{prefix}_Idle_{source_index:02d}"]
    return images[f"{prefix}_{action}_{source_index:02d}"]


def compose_frame(
    source: Image.Image,
    action: str,
    source_pivot: tuple[float, float] | None = None,
    fixed_scale: float | None = None,
) -> Image.Image:
    source = source.convert("RGBA")
    alpha_box = source.getchannel("A").getbbox()
    if alpha_box is None:
        raise RuntimeError("source frame has no visible pixels")
    target_height = ATTACK_HEIGHT if action == "Attack" else IDLE_HEIGHT
    scale = fixed_scale if fixed_scale is not None else target_height / max(1, alpha_box[3] - alpha_box[1])
    resized = source.resize(
        (max(1, round(source.width * scale)), max(1, round(source.height * scale))),
        Image.Resampling.LANCZOS,
    )
    if source_pivot is not None:
        pivot = (source_pivot[0] * resized.width, (1.0 - source_pivot[1]) * resized.height)
    else:
        resized_box = resized.getchannel("A").getbbox()
        if resized_box is None:
            raise RuntimeError("resized frame has no visible pixels")
        pivot = ((resized_box[0] + resized_box[2]) * 0.5, resized_box[3])
    canvas = Image.new("RGBA", TEXTURE_CANVAS, (0, 0, 0, 0))
    x = round(TARGET_SPRITE_PIVOT[0] - pivot[0])
    y = round(TARGET_SPRITE_PIVOT[1] - pivot[1])
    if x < -resized.width or x > TEXTURE_CANVAS[0] or y < -resized.height or y > TEXTURE_CANVAS[1]:
        raise RuntimeError(f"composed frame is outside canvas: source={source.size} pivot={pivot}")
    canvas.alpha_composite(resized, (x, y))
    return canvas


def target_frame_images(
    skin: dict,
    images: dict[str, Image.Image],
    pivots: dict[str, tuple[float, float]],
) -> dict[str, Image.Image]:
    prefix = "Death4" if skin["monster_texture_id"] == 673 else "Death16"
    idle_scale = reference_scale(images, prefix, "Idle", IDLE_HEIGHT)
    result = {}
    for action, count in (("Idle", 52), ("Run", 8), ("Attack", 8), ("Hurt", 1)):
        for index in range(1, count + 1):
            target_name = f"{skin['root']}_{action}_{index:02d}"
            attack_count = len([name for name in images if name.startswith(prefix + "_Attack_")])
            if action == "Idle":
                source_name = idle_action_source(prefix, index, attack_count)
            elif action == "Run":
                source_name = run_action_source(prefix, index, attack_count)
            elif action == "Hurt":
                source_name = f"{prefix}_Hurt_01"
            else:
                source_name = f"{prefix}_Attack_{int(((index - 1) * (attack_count - 1) / 7) + 0.5) + 1:02d}"
            result[target_name] = compose_frame(
                images[source_name],
                action,
                pivots[source_name],
                fixed_scale=idle_scale,
            )
    return result


def patch_bundle(kind: str, skin: dict, source_plain: dict[str, bytes], frame_images: dict[str, Image.Image], source_cabs: dict[str, str], target_cabs: dict[str, str], output_dir: Path) -> Path:
    environment, bundle, serialized = bundle_parts(source_plain[kind])
    patch_external_cabs(serialized, source_cabs, target_cabs)
    target_root = skin["root"]
    for obj in environment.objects:
        data = obj.read()
        if obj.type.name == "AssetBundle":
            patch_asset_bundle_object(data, target_root)
        elif hasattr(data, "m_Name") and isinstance(data.m_Name, str):
            data.m_Name = rename_skin(data.m_Name, target_root)
        if obj.type.name == "Texture2D":
            if data.m_Name not in frame_images:
                raise RuntimeError(f"missing replacement frame: {data.m_Name}")
            data.image = frame_images[data.m_Name]
        data.save()
    rename_serialized_file(bundle, serialized, target_cabs[kind], target_root)
    output = output_dir / skin["bundle_names"][kind]
    output.write_bytes(bundle.save(packer="lz4"))
    return output


def source_plain_bundles(manifest_bundles: list[dict]) -> tuple[dict[str, bytes], dict[str, str], dict[str, int]]:
    ids = {}
    for kind, name in (
        ("animator", "assets_download_animators_skin_skin25.bundle"),
        ("prefab", "assets_download_role_roleprefab_skin_skin25.bundle"),
        ("texture", "assets_download_texture_animationresource_role_skin_skin25.bundle"),
    ):
        matches = [i for i, record in enumerate(manifest_bundles) if record["name"] == name]
        if len(matches) != 1:
            raise RuntimeError(f"expected one Skin25 {kind} bundle, found {len(matches)}")
        ids[kind] = matches[0]
    plain = {kind: decrypt_bundle(AA1 / f"{manifest_bundles[index]['hash']}.bundle") for kind, index in ids.items()}
    cabs = {}
    for kind, raw in plain.items():
        _, bundle, serialized = bundle_parts(raw)
        cabs[kind] = serialized.name
    return plain, cabs, ids


def target_cabs(slug: str) -> dict[str, str]:
    return {
        kind: "CAB-" + hashlib.md5(("mhq-custom-skin-" + slug + "-" + kind).encode("ascii")).hexdigest()
        for kind in ("animator", "prefab", "texture")
    }


def build_bundles(manifest_bundles: list[dict], source_plain: dict[str, bytes], source_cab_map: dict[str, str]) -> dict[str, dict]:
    if BUILD_DIR.exists():
        shutil.rmtree(BUILD_DIR)
    encrypted_dir = BUILD_DIR / "encrypted"
    encrypted_dir.mkdir(parents=True)
    metadata = {}
    preview_frames = []
    for slug, skin in SKINS.items():
        plain_dir = BUILD_DIR / slug / "plain"
        plain_dir.mkdir(parents=True)
        images = source_images(manifest_bundles, skin)
        frames = target_frame_images(skin, images, source_pivots(manifest_bundles, skin))
        cabs = target_cabs(slug)
        for name in (
            f"{skin['root']}_Idle_01",
            f"{skin['root']}_Idle_34",
            f"{skin['root']}_Idle_35",
            f"{skin['root']}_Idle_38",
            f"{skin['root']}_Idle_42",
            f"{skin['root']}_Idle_46",
            f"{skin['root']}_Idle_47",
            f"{skin['root']}_Run_01",
            f"{skin['root']}_Run_03",
            f"{skin['root']}_Run_05",
            f"{skin['root']}_Run_07",
            f"{skin['root']}_Attack_01",
        ):
            preview_frames.append((name, frames[name]))
        paths = {
            kind: patch_bundle(kind, skin, source_plain, frames, source_cab_map, cabs, plain_dir)
            for kind in ("animator", "prefab", "texture")
        }
        verify_plain_skin(paths, skin, cabs)
        for kind, path in paths.items():
            encrypted, digest, crc = encrypted_metadata(path)
            encrypted_path = encrypted_dir / f"{digest}.bundle"
            encrypted_path.write_bytes(encrypted)
            metadata[f"{slug}:{kind}"] = {
                "slug": slug,
                "kind": kind,
                "path": encrypted_path,
                "hash": digest,
                "crc": crc,
                "size": len(encrypted),
            }
    write_preview(preview_frames)
    return metadata


def verify_plain_skin(paths: dict[str, Path], skin: dict, cabs: dict[str, str]) -> None:
    texture_env = UnityPy.load(str(paths["texture"]))
    textures = [obj.read() for obj in texture_env.objects if obj.type.name == "Texture2D"]
    sprites = [obj.read() for obj in texture_env.objects if obj.type.name == "Sprite"]
    if len(textures) != 69 or len(sprites) != 69:
        raise RuntimeError(f"{skin['label']} texture object count mismatch: {len(textures)}/{len(sprites)}")
    expected = {f"{skin['root']}_{action}_{index:02d}" for action, count in (("Idle", 52), ("Run", 8), ("Attack", 8)) for index in range(1, count + 1)}
    expected.add(f"{skin['root']}_Hurt_01")
    if {data.m_Name for data in textures} != expected:
        raise RuntimeError(f"{skin['label']} texture names do not match the player contract")
    if any(data.image.getchannel("A").getbbox() is None for data in textures):
        raise RuntimeError(f"{skin['label']} contains an empty texture")
    prefab_env = UnityPy.load(str(paths["prefab"]))
    if not any(obj.type.name == "GameObject" and obj.read().m_Name == skin["root"] for obj in prefab_env.objects):
        raise RuntimeError(f"{skin['label']} prefab root is missing")
    _, _, animator_serialized = bundle_parts(paths["animator"].read_bytes())
    if not any(cabs["texture"] in external.path for external in animator_serialized.externals):
        raise RuntimeError(f"{skin['label']} animator bundle has incomplete CAB references")
    _, _, prefab_serialized = bundle_parts(paths["prefab"].read_bytes())
    prefab_paths = [external.path for external in prefab_serialized.externals]
    if not all(any(cabs[kind] in path for path in prefab_paths) for kind in ("animator", "texture")):
        raise RuntimeError(f"{skin['label']} prefab bundle has incomplete CAB references")


def write_preview(frames: list[tuple[str, Image.Image]]) -> None:
    PREVIEW.parent.mkdir(parents=True, exist_ok=True)
    tile = (319, 250)
    label_height = 24
    columns = 3
    rows = (len(frames) + columns - 1) // columns
    sheet = Image.new("RGBA", (columns * tile[0], rows * (tile[1] + label_height)), (28, 29, 31, 255))
    draw = ImageDraw.Draw(sheet)
    for index, (name, image) in enumerate(frames):
        x = index % columns * tile[0]
        y = index // columns * (tile[1] + label_height)
        sheet.alpha_composite(image.resize(tile, Image.Resampling.LANCZOS), (x, y + label_height))
        draw.text((x + 6, y + 5), name, fill="white")
    sheet.save(PREVIEW)


def patch_manifest(manifest_raw: bytes, metadata: dict[str, dict]) -> bytes:
    header, assets, bundles = read_manifest(manifest_raw)
    if write_manifest(header, assets, bundles) != manifest_raw:
        raise RuntimeError("aa1 manifest round-trip changed unmodified data")
    source_ids = {}
    for kind, name in (
        ("animator", "assets_download_animators_skin_skin25.bundle"),
        ("prefab", "assets_download_role_roleprefab_skin_skin25.bundle"),
        ("texture", "assets_download_texture_animationresource_role_skin_skin25.bundle"),
    ):
        matches = [i for i, record in enumerate(bundles) if record["name"] == name]
        if len(matches) != 1:
            raise RuntimeError(f"manifest Skin25 {kind} record missing")
        source_ids[kind] = matches[0]
    for slug, skin in SKINS.items():
        target_ids = {}
        for kind, name in skin["bundle_names"].items():
            matches = [i for i, record in enumerate(bundles) if record["name"] == name]
            if len(matches) > 1:
                raise RuntimeError(f"duplicate custom bundle record: {name}")
            target_ids[kind] = matches[0] if matches else len(bundles)
            if not matches:
                bundles.append(deepcopy(bundles[source_ids[kind]]))
        source_assets = [asset for asset in assets if asset["bundle"] in source_ids.values() and SKIN25_ROOT in asset["path"]]
        assets = [asset for asset in assets if skin["root"] not in asset["path"]]
        id_mapping = {source_ids[kind]: target_ids[kind] for kind in source_ids}
        for source in source_assets:
            cloned = deepcopy(source)
            cloned["address"] = rename_skin(cloned["address"], skin["root"])
            cloned["path"] = rename_skin(cloned["path"], skin["root"])
            cloned["bundle"] = id_mapping[cloned["bundle"]]
            cloned["dependencies"] = [id_mapping.get(item, item) for item in cloned["dependencies"]]
            assets.append(cloned)
        if sum(skin["root"] in asset["path"] for asset in assets) != 74:
            raise RuntimeError(f"manifest did not contain all 74 {skin['label']} assets")
        for kind, target_id in target_ids.items():
            record = deepcopy(bundles[source_ids[kind]])
            record["name"] = skin["bundle_names"][kind]
            item = metadata[f"{slug}:{kind}"]
            record["hash"] = item["hash"]
            record["crc"] = item["crc"]
            record["size"] = item["size"]
            record["references"] = [id_mapping.get(value, value) for value in record["references"]]
            bundles[target_id] = record
        if target_ids["prefab"] not in bundles[952]["references"]:
            bundles[952]["references"].append(target_ids["prefab"])
    paths = [asset["path"] for asset in assets]
    if len(paths) != len(set(paths)):
        raise RuntimeError("aa1 manifest contains duplicate asset paths")
    patched = write_manifest(header, assets, bundles)
    read_manifest(patched)
    return patched


def encode_rows(rows) -> str:
    text = "[\r\n" + ",\r\n".join(json.dumps(row, ensure_ascii=False, separators=(",", ":")) for row in rows) + "\r\n]"
    return base64.b64encode(zlib.compress(text.encode("utf-8"))).decode("ascii")


def decode_rows(data):
    raw = data.m_Script.encode("utf-8") if isinstance(data.m_Script, str) else bytes(data.m_Script)
    text = zlib.decompress(base64.b64decode(raw.strip())).decode("utf-8")
    return json.loads(re.sub(r",\s*([}\]])", r"\1", text))


def patch_config_bundle() -> Path:
    environment = UnityPy.load(str(CONFIG_BUNDLE))
    wanted = {"EquipBase": [], "SkinBase": [], "Sys_Prefab": []}
    for obj in environment.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name in wanted:
            wanted[data.m_Name] = (obj, data)
    if any(not value for value in wanted.values()):
        raise RuntimeError("config bundle is missing EquipBase, SkinBase or Sys_Prefab")
    equip_obj, equip_data = wanted["EquipBase"]
    equip_rows = {int(row[0]): row for row in decode_rows(equip_data)}
    for skin in SKINS.values():
        equip_rows[skin["skin_id"]] = [skin["skin_id"], skin["equip_row"]]
    equip_data.m_Script = encode_rows([equip_rows[key] for key in sorted(equip_rows)])
    equip_data.save()
    skin_obj, skin_data = wanted["SkinBase"]
    skin_rows = {int(row[0]): row for row in decode_rows(skin_data)}
    for skin in SKINS.values():
        skin_rows[skin["skin_id"]] = [skin["skin_id"], {"_id": skin["skin_id"], "PrfabId": skin["prefab_id"]}]
    skin_data.m_Script = encode_rows([skin_rows[key] for key in sorted(skin_rows)])
    skin_data.save()
    prefab_obj, prefab_data = wanted["Sys_Prefab"]
    prefab_rows = {int(row[0]): row for row in decode_rows(prefab_data)}
    for skin in SKINS.values():
        prefab_rows[skin["prefab_id"]] = [skin["prefab_id"], {
            "_id": skin["prefab_id"],
            "Name": skin["root"],
            "Desc": "custom direct-id skin",
            "AssetPath": f"Assets/Download/Role/RolePrefab/Skin/{skin['root']}.prefab",
            "PoolId": 1,
            "CullDespawned": True,
            "CullAbove": 0,
            "CullDelay": 30,
            "CullMaxPerPass": 1,
        }]
    prefab_data.m_Script = encode_rows([prefab_rows[key] for key in sorted(prefab_rows)])
    prefab_data.save()
    for file_item in environment.files.values():
        file_item.mark_changed()
    output_dir = BUILD_DIR / "config"
    output_dir.mkdir(parents=True, exist_ok=True)
    environment.save(pack="lz4", out_path=str(output_dir))
    output = output_dir / CONFIG_BUNDLE.name
    if not output.is_file():
        raise RuntimeError("UnityPy did not create the patched config bundle")
    return output


def backup(path: Path) -> None:
    target = Path(str(path) + BACKUP_SUFFIX)
    if not target.exists():
        shutil.copy2(path, target)


def atomic_write(path: Path, data: bytes) -> None:
    temporary = Path(str(path) + ".custom-skins.tmp")
    temporary.write_bytes(data)
    os.replace(temporary, path)


def install(metadata: dict[str, dict], patched_manifest: bytes, config_path: Path) -> None:
    backup(AA1_MANIFEST)
    backup(AA1_HASH)
    backup(CONFIG_BUNDLE)
    for item in metadata.values():
        shutil.copy2(item["path"], AA1 / item["path"].name)
    atomic_write(AA1_MANIFEST, patched_manifest)
    atomic_write(AA1_HASH, hashlib.md5(patched_manifest).hexdigest().encode("ascii"))
    shutil.copy2(config_path, CONFIG_BUNDLE)
    print("installed custom skins in client-test", ", ".join(f"{skin['skin_id']}={skin['root']}" for skin in SKINS.values()))


def verify_installed(metadata: dict[str, dict]) -> None:
    raw = AA1_MANIFEST.read_bytes()
    if AA1_HASH.read_text(encoding="ascii").strip() != hashlib.md5(raw).hexdigest():
        raise RuntimeError("aa1 manifest hash sidecar does not match")
    _, assets, bundles = read_manifest(raw)
    for slug, skin in SKINS.items():
        if sum(skin["root"] in asset["path"] for asset in assets) != 74:
            raise RuntimeError(f"installed manifest is missing {skin['label']} assets")
        names = set(skin["bundle_names"].values())
        records = {bundle["name"]: bundle for bundle in bundles if bundle["name"] in names}
        if names != set(records):
            raise RuntimeError(f"installed manifest is missing {skin['label']} bundle records")
        for kind in ("animator", "prefab", "texture"):
            item = metadata[f"{slug}:{kind}"]
            path = AA1 / f"{item['hash']}.bundle"
            if hashlib.md5(path.read_bytes()).hexdigest() != item["hash"]:
                raise RuntimeError(f"installed {skin['label']} {kind} bundle hash mismatch")
    env = UnityPy.load(str(CONFIG_BUNDLE))
    rows = {}
    for obj in env.objects:
        if obj.type.name == "TextAsset" and obj.read().m_Name in ("EquipBase", "SkinBase", "Sys_Prefab"):
            rows[obj.read().m_Name] = {int(row[0]): row for row in decode_rows(obj.read())}
    for skin in SKINS.values():
        equip_row = rows.get("EquipBase", {}).get(skin["skin_id"])
        if equip_row is None or equip_row[1].get("Type") != 2:
            raise RuntimeError(f"installed config is missing EquipBase row for {skin['label']}")
        if equip_row[1].get("IconName") != str(skin["skin_id"]):
            raise RuntimeError(f"installed config has the wrong Bag icon for {skin['label']}")
        if skin["skin_id"] not in rows["SkinBase"] or skin["prefab_id"] not in rows["Sys_Prefab"]:
            raise RuntimeError(f"config bundle is missing {skin['label']} rows")
    print("verified custom skin manifest, bundles and client datatable")


def restore() -> None:
    for path in (AA1_MANIFEST, AA1_HASH, CONFIG_BUNDLE):
        backup_path = Path(str(path) + BACKUP_SUFFIX)
        if not backup_path.is_file():
            raise RuntimeError(f"custom skin backup is missing: {backup_path}")
        shutil.copy2(backup_path, path)
    print("restored pre-custom-skins client and datatable files; generated bundles were retained as unreferenced files")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--install", action="store_true", help="install into client-test and update local table sources")
    parser.add_argument("--restore", action="store_true", help="restore the first pre-custom-skins backup")
    args = parser.parse_args()
    if args.restore:
        restore()
        from _patch_custom_skin_bag import restore as restore_bag
        restore_bag()
        return
    _, assets, bundles = read_manifest(AA1_MANIFEST.read_bytes())
    source_plain, source_cabs, _ = source_plain_bundles(bundles)
    metadata = build_bundles(bundles, source_plain, source_cabs)
    patched_manifest = patch_manifest(AA1_MANIFEST.read_bytes(), metadata)
    config_path = patch_config_bundle()
    if args.install:
        install(metadata, patched_manifest, config_path)
        verify_installed(metadata)
        from _patch_custom_skin_bag import install as install_bag
        install_bag()
    else:
        print("build verified; inspect", PREVIEW, "before --install")


if __name__ == "__main__":
    main()
