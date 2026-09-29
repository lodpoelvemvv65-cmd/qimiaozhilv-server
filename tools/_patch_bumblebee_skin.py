"""Build, verify, install, or roll back the independent Skin25 Bumblebee asset chain."""

from __future__ import annotations

import argparse
import hashlib
import os
import shutil
import struct
import sys
from copy import deepcopy
from pathlib import Path

import UnityPy
from Crypto.Cipher import AES
from Crypto.Util.Padding import unpad
from PIL import Image, ImageDraw
from PIL import ImageChops

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
REVIEW_DIR = ROOT / "assets" / "bumblebee" / "review" / "runtime-v9"
SOURCE_REVIEW_DIR = ROOT / "assets" / "bumblebee" / "review" / "runtime-v6"
RUN_EFFECT_DIR = ROOT / "assets" / "bumblebee" / "generated-v11"
IDLE_EFFECT_DIR = ROOT / "assets" / "bumblebee" / "generated-v13"
COMBAT_EFFECT_DIR = ROOT / "assets" / "bumblebee" / "generated-v13"
TAIL_RUN_FRAMES = {
    "Skin25_Run_01": RUN_EFFECT_DIR / "Skin25_Run_01_jet.png",
    "Skin25_Run_02": RUN_EFFECT_DIR / "Skin25_Run_02_jet.png",
}
ACTION_FRAMES = {
    **{
        f"Skin25_Idle_{index:02d}": IDLE_EFFECT_DIR / f"Skin25_Idle_{index:02d}.png"
        for index in range(1, 16)
    },
    **{
        f"Skin25_Attack_{index:02d}": COMBAT_EFFECT_DIR / f"Skin25_Attack_{index:02d}.png"
        for index in range(1, 9)
    },
}
BUILD_DIR = ROOT / "_work" / "bumblebee-v13-bundles"
BACKUP_SUFFIX = ".pre-bumblebee-v13.bak"
MODELVIEW = next((ROOT / "client-test").glob("*_Data/Managed/Unity.ModelView.dll"))
EXPECTED_MODELVIEW_SHA256 = "43e270ccfcabc4982f7f4bf309de358d62012bc7c12e68c17416596cc30f5d57"

# Skin30 has the required 52/8/8/1 clip and sprite contract. It remains untouched.
SOURCE_IDS = {"animator": 149, "prefab": 574, "texture": 822}
SOURCE_CABS = {
    "animator": "CAB-cc6cc007387f21456ae0c630c283dee6",
    "prefab": "CAB-ceb288f5a3249eee3ade9e0a87a7d7cd",
    "texture": "CAB-875666b0093febf765c75aba546ed0cb",
}
TARGET_CABS = {
    kind: "CAB-" + hashlib.md5(("bumblebee-v13-skin25-" + kind).encode()).hexdigest()
    for kind in SOURCE_CABS
}
TARGET_BUNDLE_NAMES = {
    "animator": "assets_download_animators_skin_skin25.bundle",
    "prefab": "assets_download_role_roleprefab_skin_skin25.bundle",
    "texture": "assets_download_texture_animationresource_role_skin_skin25.bundle",
}
EXPECTED_COUNTS = {"Idle": 52, "Run": 8, "Attack": 8, "Hurt": 1}
CLIENT_CANVAS_SIZE = (637, 500)
REVIEW_CANVAS_SIZE = (569, 370)
CLIENT_BASELINE_Y = 460  # Skin30 pivot is 40 px above the bottom of its 500 px canvas.
REVIEW_BASELINE_Y = 352
HUMANOID_SCALE = 242 / 307  # Skin28 Idle height / approved Bumblebee Idle height.
CAR_SCALES = {
    "Run_01": 304 / 485,  # Match Skin28 Run_01 width.
    "Run_02": 347 / 545,  # Match Skin28 Run_02 width.
}
CAR_SCALE = sum(CAR_SCALES.values()) / len(CAR_SCALES)
TRANSFORM_SCALES = tuple(
    HUMANOID_SCALE + (CAR_SCALE - HUMANOID_SCALE) * index / 3
    for index in range(4)
)
RUN_SEQUENCE = (
    "Transform_01",
    "Transform_02",
    "Transform_03",
    "Transform_04",
    "Run_01",
    "Run_02",
    "Run_01",
    "Run_02",
)


def rename_skin(value: str) -> str:
    return value.replace("Skin30", "Skin25").replace("skin30", "skin25")


def decrypt_bundle(path: Path) -> bytes:
    encrypted = path.read_bytes()
    if hashlib.md5(encrypted).hexdigest() != path.stem:
        raise RuntimeError(f"source bundle hash mismatch: {path.name}")
    return unpad(AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(encrypted), AES.block_size)


def bundle_parts(raw_or_path):
    if isinstance(raw_or_path, Path):
        raw_or_path = str(raw_or_path)
    environment = UnityPy.load(raw_or_path)
    bundle = next(iter(environment.files.values()))
    serialized = next(item for item in bundle.files.values() if hasattr(item, "externals"))
    return environment, bundle, serialized


def source_bundle(kind: str, bundles: list[dict]) -> bytes:
    record = bundles[SOURCE_IDS[kind]]
    expected_name = TARGET_BUNDLE_NAMES[kind].replace("skin25", "skin30")
    if record["name"] != expected_name:
        raise RuntimeError(f"unexpected Skin30 {kind} bundle at id {SOURCE_IDS[kind]}")
    return decrypt_bundle(AA1 / f"{record['hash']}.bundle")


def rename_serialized_file(bundle, serialized, target_cab: str) -> None:
    old_name = serialized.name
    rebuilt = {}
    for name, item in bundle.files.items():
        if name == old_name:
            rebuilt[target_cab] = item
        elif name == old_name + ".resS":
            # Replaced textures are embedded by UnityPy; the old stream is obsolete.
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


def review_frame_for(texture_name: str) -> Path:
    prefix = "Skin25_"
    if not texture_name.startswith(prefix):
        raise RuntimeError(f"unexpected Skin25 texture name: {texture_name}")
    action, index_text = texture_name[len(prefix):].rsplit("_", 1)
    index = int(index_text)
    if action == "Idle":
        source = f"Skin25_Idle_{(index - 1) % 15 + 1:02d}.png"
    elif action == "Run":
        source = f"Skin25_{RUN_SEQUENCE[index - 1]}.png"
    elif action == "Attack":
        source = f"Skin25_Attack_{index:02d}.png"
    elif action == "Hurt":
        source = "Skin25_Hurt_01.png"
    else:
        raise RuntimeError(f"unsupported animation action: {action}")
    frame_name = source.replace(".png", "")
    path = ACTION_FRAMES.get(frame_name, TAIL_RUN_FRAMES.get(frame_name, SOURCE_REVIEW_DIR / source))
    if not path.is_file():
        raise RuntimeError(f"missing approved Bumblebee frame: {path}")
    return path


def verify_action_sources() -> None:
    idle_frames = [
        Image.open(ACTION_FRAMES[f"Skin25_Idle_{index:02d}"]).convert("RGBA")
        for index in range(1, 16)
    ]
    try:
        if any(frame.size != REVIEW_CANVAS_SIZE for frame in idle_frames):
            raise RuntimeError("v13 Idle source frames do not use the approved review canvas")
        expected_pose_ids = (0, 1, 2, 3, 4, 3, 2, 1, 0, 5, 6, 7, 6, 5, 0)
        pose_bytes = []
        for index, frame in enumerate(idle_frames, 1):
            box = frame.getchannel("A").getbbox()
            if box is None:
                raise RuntimeError(f"v13 Idle source frame is empty: {index}")
            if box[3] > 355 or not 304 <= box[3] - box[1] <= 311:
                raise RuntimeError(f"v13 Idle frame {index} has an invalid baseline: {box}")
            right_arm = frame.crop((350, 45, 500, 210))
            bright_energy = sum(
                alpha > 32 and green > 130 and blue > 160 and blue > red * 1.2
                for red, green, blue, alpha in right_arm.get_flattened_data()
            )
            if bright_energy > 5:
                raise RuntimeError(f"v13 Idle frame {index} contains right-cannon energy")
            pose_bytes.append(frame.tobytes())
        unique_poses = list(dict.fromkeys(pose_bytes))
        if len(unique_poses) != 8:
            raise RuntimeError("v13 Idle lacks its eight independent poses")
        actual_pose_ids = tuple(unique_poses.index(frame) for frame in pose_bytes)
        if actual_pose_ids != expected_pose_ids:
            raise RuntimeError(f"v13 Idle sequence mismatch: {actual_pose_ids}")
    finally:
        for frame in idle_frames:
            frame.close()

    for index in range(1, 9):
        source = ACTION_FRAMES[f"Skin25_Attack_{index:02d}"]
        approved = COMBAT_EFFECT_DIR / f"Skin25_Attack_{index:02d}.png"
        if source != approved or source.read_bytes() != approved.read_bytes():
            raise RuntimeError(f"Attack {index} no longer matches the approved v13 source")


def client_frame(texture_name: str) -> Image.Image:
    with Image.open(review_frame_for(texture_name)) as source:
        image = source.convert("RGBA")
    if image.size != REVIEW_CANVAS_SIZE or image.getchannel("A").getbbox() is None:
        raise RuntimeError(f"invalid approved frame: {texture_name}")
    action, index_text = texture_name[len("Skin25_"):].rsplit("_", 1)
    index = int(index_text)
    scale = HUMANOID_SCALE
    if action == "Run":
        if index <= 4:
            scale = TRANSFORM_SCALES[index - 1]
        else:
            scale = CAR_SCALES[RUN_SEQUENCE[index - 1]]
    scaled_size = (
        round(REVIEW_CANVAS_SIZE[0] * scale),
        round(REVIEW_CANVAS_SIZE[1] * scale),
    )
    image = image.resize(scaled_size, Image.Resampling.LANCZOS)
    x = (CLIENT_CANVAS_SIZE[0] - scaled_size[0]) // 2
    y = round(CLIENT_BASELINE_Y - REVIEW_BASELINE_Y * scale)
    canvas = Image.new("RGBA", CLIENT_CANVAS_SIZE, (0, 0, 0, 0))
    canvas.alpha_composite(image, (x, y))
    box = canvas.getchannel("A").getbbox()
    if box is None or min(box[0], box[1], CLIENT_CANVAS_SIZE[0] - box[2], CLIENT_CANVAS_SIZE[1] - box[3]) < 4:
        raise RuntimeError(f"scaled client frame lacks a safe transparent gutter: {texture_name} {box}")
    return canvas


def patch_sprite_full_rect(sprite) -> None:
    """Replace the source actor's tight mesh with a full-rect sprite quad."""
    width = float(sprite.m_Rect.width)
    height = float(sprite.m_Rect.height)
    pixels_to_units = float(sprite.m_PixelsToUnits)
    pivot_x = float(sprite.m_Pivot.x)
    pivot_y = float(sprite.m_Pivot.y)
    left = -pivot_x * width / pixels_to_units
    right = (1.0 - pivot_x) * width / pixels_to_units
    bottom = -pivot_y * height / pixels_to_units
    top = (1.0 - pivot_y) * height / pixels_to_units

    # Unity 2020 stores position stream 0 first, followed by a zeroed UV stream;
    # the runtime derives texture coordinates using uvTransform.
    positions = (
        (left, top, 0.0),
        (right, bottom, 0.0),
        (right, top, 0.0),
        (left, bottom, 0.0),
    )
    vertex_data = b"".join(struct.pack("<3f", *position) for position in positions)
    vertex_data += b"".join(struct.pack("<2f", 0.0, 0.0) for _ in positions)

    render_data = sprite.m_RD
    render_data.settingsRaw &= ~(1 << 6)  # SpriteMeshType.FullRect
    render_data.textureRect.x = 0.0
    render_data.textureRect.y = 0.0
    render_data.textureRect.width = width
    render_data.textureRect.height = height
    render_data.textureRectOffset.x = 0.0
    render_data.textureRectOffset.y = 0.0
    render_data.uvTransform.x = pixels_to_units
    render_data.uvTransform.y = pivot_x * width
    render_data.uvTransform.z = pixels_to_units
    render_data.uvTransform.w = pivot_y * height
    render_data.m_IndexBuffer = list(struct.pack("<6H", 3, 0, 1, 2, 1, 0))

    submesh = render_data.m_SubMeshes[0]
    submesh.firstByte = 0
    submesh.indexCount = 6
    submesh.topology = 0
    submesh.baseVertex = 0
    submesh.firstVertex = 0
    submesh.vertexCount = 4
    submesh.localAABB.m_Center.x = (left + right) / 2
    submesh.localAABB.m_Center.y = (bottom + top) / 2
    submesh.localAABB.m_Center.z = 0.0
    submesh.localAABB.m_Extent.x = (right - left) / 2
    submesh.localAABB.m_Extent.y = (top - bottom) / 2
    submesh.localAABB.m_Extent.z = 0.0
    render_data.m_SubMeshes = [submesh]
    render_data.m_VertexData.m_VertexCount = 4
    render_data.m_VertexData.m_DataSize = vertex_data


def verify_sprite_full_rect(sprite) -> None:
    render_data = sprite.m_RD
    width = float(sprite.m_Rect.width)
    height = float(sprite.m_Rect.height)
    if render_data.settingsRaw & (1 << 6):
        raise RuntimeError(f"Sprite still uses a tight mesh: {sprite.m_Name}")
    if (
        render_data.textureRect.x != 0.0
        or render_data.textureRect.y != 0.0
        or render_data.textureRect.width != width
        or render_data.textureRect.height != height
        or render_data.textureRectOffset.x != 0.0
        or render_data.textureRectOffset.y != 0.0
    ):
        raise RuntimeError(f"Sprite texture rect is incomplete: {sprite.m_Name}")
    if render_data.m_VertexData.m_VertexCount != 4 or len(render_data.m_VertexData.m_DataSize) != 80:
        raise RuntimeError(f"Sprite does not contain one full quad: {sprite.m_Name}")
    if bytes(render_data.m_IndexBuffer) != struct.pack("<6H", 3, 0, 1, 2, 1, 0):
        raise RuntimeError(f"Sprite full-rect index buffer is invalid: {sprite.m_Name}")
    if len(render_data.m_SubMeshes) != 1:
        raise RuntimeError(f"Sprite contains unexpected submeshes: {sprite.m_Name}")
    submesh = render_data.m_SubMeshes[0]
    if submesh.indexCount != 6 or submesh.vertexCount != 4:
        raise RuntimeError(f"Sprite submesh is not a full quad: {sprite.m_Name}")

    floats = struct.unpack("<20f", render_data.m_VertexData.m_DataSize)
    positions = [floats[offset:offset + 3] for offset in range(0, 12, 3)]
    left = -float(sprite.m_Pivot.x) * width / float(sprite.m_PixelsToUnits)
    right = (1.0 - float(sprite.m_Pivot.x)) * width / float(sprite.m_PixelsToUnits)
    bottom = -float(sprite.m_Pivot.y) * height / float(sprite.m_PixelsToUnits)
    top = (1.0 - float(sprite.m_Pivot.y)) * height / float(sprite.m_PixelsToUnits)
    expected = ((left, top, 0.0), (right, bottom, 0.0), (right, top, 0.0), (left, bottom, 0.0))
    for actual, target in zip(positions, expected):
        if any(abs(value - wanted) > 1e-5 for value, wanted in zip(actual, target)):
            raise RuntimeError(f"Sprite quad does not cover its complete rect: {sprite.m_Name}")


def patch_bundle(kind: str, bundles: list[dict], plain_dir: Path) -> Path:
    environment, bundle, serialized = bundle_parts(source_bundle(kind, bundles))
    patch_external_cabs(serialized)
    texture_counts = {action: 0 for action in EXPECTED_COUNTS}
    for obj in environment.objects:
        data = obj.read()
        if obj.type.name == "AssetBundle":
            patch_asset_bundle_object(obj)
            continue
        if hasattr(data, "m_Name") and isinstance(data.m_Name, str):
            data.m_Name = rename_skin(data.m_Name)
        if obj.type.name == "Texture2D":
            action = data.m_Name[len("Skin25_"):].rsplit("_", 1)[0]
            if action not in texture_counts:
                raise RuntimeError(f"unexpected animation texture: {data.m_Name}")
            texture_counts[action] += 1
            data.image = client_frame(data.m_Name)
        if obj.type.name == "Sprite":
            patch_sprite_full_rect(data)
        if obj.type.name == "AnimationClip" and data.m_Name == "Skin25_Run":
            # Keep the one-shot transform, then clamp on the final vehicle frame.
            # The accompanying MonoAnimancer patch suppresses the automatic Idle
            # callback only for Skin25_Run; movement stop still invokes PlayIdle.
            data.m_MuscleClip.m_LoopTime = False
            data.m_WrapMode = 8  # UnityEngine.WrapMode.ClampForever
        data.save()

    if kind == "texture" and texture_counts != EXPECTED_COUNTS:
        raise RuntimeError(f"texture frame contract mismatch: {texture_counts}")
    rename_serialized_file(bundle, serialized, TARGET_CABS[kind])
    output = plain_dir / TARGET_BUNDLE_NAMES[kind]
    output.write_bytes(bundle.save(packer="lz4"))
    return output


def verify_plain_bundles(paths: dict[str, Path]) -> None:
    environment = UnityPy.load(*[str(path) for path in paths.values()])
    types: dict[str, int] = {}
    names = set()
    texture_counts = {action: 0 for action in EXPECTED_COUNTS}
    selected_bounds = {}
    idle_images = {}
    attack_images = {}
    prefab_ok = False
    loop_flags = {}
    wrap_modes = {}
    sprite_count = 0
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
            box = image.getchannel("A").getbbox()
            if image.size != CLIENT_CANVAS_SIZE or box is None:
                raise RuntimeError(f"invalid client texture: {name}")
            action = name[len("Skin25_"):].rsplit("_", 1)[0]
            texture_counts[action] += 1
            if name in {
                "Skin25_Idle_01",
                "Skin25_Run_01", "Skin25_Run_02", "Skin25_Run_03", "Skin25_Run_04",
                "Skin25_Run_05", "Skin25_Run_06",
            }:
                selected_bounds[name] = (box[2] - box[0], box[3] - box[1])
            if action == "Idle":
                idle_images[name] = image
                cannon = image.crop((380, 120, 520, 340))
                bright_energy = sum(
                    alpha > 32 and green > 130 and blue > 160 and blue > red * 1.2
                    for red, green, blue, alpha in cannon.get_flattened_data()
                )
                if bright_energy > 5:
                    raise RuntimeError(f"Idle still contains right-cannon energy: {name} {bright_energy}")
            elif action == "Attack":
                attack_images[name] = image
        elif obj.type.name == "Sprite":
            verify_sprite_full_rect(data)
            sprite_count += 1
        elif obj.type.name == "AnimationClip":
            loop_flags[name] = data.m_MuscleClip.m_LoopTime
            wrap_modes[name] = data.m_WrapMode
        elif obj.type.name == "GameObject" and name == "Skin25":
            prefab_ok = True

    expected_clips = {f"Skin25_{action}" for action in EXPECTED_COUNTS}
    if types.get("Texture2D") != 69 or types.get("Sprite") != 69 or sprite_count != 69:
        raise RuntimeError(f"expected 69 textures and sprites, found {types}")
    if texture_counts != EXPECTED_COUNTS or not expected_clips.issubset(names):
        raise RuntimeError("Skin25 frame or clip contract is incomplete")
    if loop_flags != {
        "Skin25_Hurt": False,
        "Skin25_Idle": True,
        "Skin25_Run": False,
        "Skin25_Attack": False,
    }:
        raise RuntimeError(f"unexpected animation loop flags: {loop_flags}")
    if wrap_modes.get("Skin25_Run") != 8:
        raise RuntimeError(f"Skin25_Run is not ClampForever: {wrap_modes}")
    if not prefab_ok:
        raise RuntimeError("Skin25 prefab root was not found")
    idle_height = selected_bounds["Skin25_Idle_01"][1]
    car_widths = (selected_bounds["Skin25_Run_05"][0], selected_bounds["Skin25_Run_06"][0])
    transform_heights = [selected_bounds[f"Skin25_Run_{index:02d}"][1] for index in range(1, 5)]
    # The exhaust intentionally extends the transparent bounds to the left;
    # retain the Skin28-sized car body while allowing the approved VFX gutter.
    if not 240 <= idle_height <= 248 or not 340 <= car_widths[0] <= 355 or not 345 <= car_widths[1] <= 365:
        raise RuntimeError(f"Bumblebee car plus exhaust exceeds the safe bounds: {selected_bounds}")
    if transform_heights != sorted(transform_heights, reverse=True):
        raise RuntimeError(f"transform size progression is not monotonic: {transform_heights}")
    if len({image.tobytes() for image in idle_images.values()}) < 3:
        raise RuntimeError("Idle no longer contains the approved v13 pose sequence")
    attack_widths = [image.getchannel("A").getbbox()[2] for image in attack_images.values()]
    if max(attack_widths) < 535:
        raise RuntimeError(f"Attack VFX is smaller than the approved v13 blast: {attack_widths}")
    folded_right = attack_images["Skin25_Attack_01"].getchannel("A").getbbox()[2]
    extending_right = attack_images["Skin25_Attack_02"].getchannel("A").getbbox()[2]
    if extending_right - folded_right < 5:
        raise RuntimeError("Attack cannon arm does not visibly extend before firing")

    _, _, animator = bundle_parts(paths["animator"])
    _, _, prefab = bundle_parts(paths["prefab"])
    if animator.externals[0].name != TARGET_CABS["texture"]:
        raise RuntimeError("animator does not reference the Bumblebee texture CAB")
    prefab_externals = {item.name for item in prefab.externals}
    if not {TARGET_CABS["animator"], TARGET_CABS["texture"]}.issubset(prefab_externals):
        raise RuntimeError("prefab Bumblebee dependencies are incomplete")


def save_client_contact(texture_path: Path) -> None:
    wanted = {
        "Skin25_Idle_01", "Skin25_Idle_08", "Skin25_Idle_11", "Skin25_Idle_52",
        "Skin25_Run_01", "Skin25_Run_02", "Skin25_Run_03", "Skin25_Run_04",
        "Skin25_Run_05", "Skin25_Run_06", "Skin25_Run_08",
        "Skin25_Attack_01", "Skin25_Attack_03", "Skin25_Attack_05", "Skin25_Attack_08",
        "Skin25_Hurt_01",
    }
    frames = []
    environment = UnityPy.load(str(texture_path))
    for obj in environment.objects:
        if obj.type.name != "Texture2D":
            continue
        data = obj.read()
        if data.m_Name in wanted:
            frames.append((data.m_Name, data.image.convert("RGBA")))
    if len(frames) != len(wanted):
        raise RuntimeError("client contact sheet is missing selected frames")
    frames.sort()
    tile_size = (398, 313)
    label_height = 22
    columns = 4
    rows = (len(frames) + columns - 1) // columns
    sheet = Image.new("RGBA", (columns * tile_size[0], rows * (tile_size[1] + label_height)), (27, 28, 30, 255))
    draw = ImageDraw.Draw(sheet)
    for position, (name, image) in enumerate(frames):
        x = position % columns * tile_size[0]
        y = position // columns * (tile_size[1] + label_height)
        sheet.alpha_composite(image.resize(tile_size, Image.Resampling.LANCZOS), (x, y + label_height))
        draw.text((x + 6, y + 4), name, fill="white")
    sheet.save(REVIEW_DIR / "bumblebee-client-contact.png")


def save_action_preview(texture_path: Path) -> None:
    environment = UnityPy.load(str(texture_path))
    textures = {
        data.m_Name: data.image.convert("RGBA")
        for obj in environment.objects
        if obj.type.name == "Texture2D"
        for data in [obj.read()]
    }
    names = (
        [f"Skin25_Idle_{index:02d}" for index in range(1, 16)]
        + [f"Skin25_Attack_{index:02d}" for index in range(1, 9)]
        + [f"Skin25_Run_{index:02d}" for index in range(1, 9)]
        + ["Skin25_Run_05", "Skin25_Run_06", "Skin25_Run_05", "Skin25_Run_06"]
        + [f"Skin25_Run_{index:02d}" for index in range(4, 0, -1)]
        + ["Skin25_Idle_01"]
    )
    missing = [name for name in names if name not in textures]
    if missing:
        raise RuntimeError(f"action preview is missing textures: {missing}")
    preview_frames = []
    for name in names:
        preview = Image.new("RGBA", CLIENT_CANVAS_SIZE, (24, 25, 27, 255))
        preview.alpha_composite(textures[name])
        label = name.replace("Skin25_", "")
        if name.startswith("Skin25_Run_") and name in {"Skin25_Run_01", "Skin25_Run_02", "Skin25_Run_03", "Skin25_Run_04"}:
            label += "  / reverse when stopping"
        ImageDraw.Draw(preview).text((12, 10), label, fill=(255, 255, 255, 255))
        preview_frames.append(preview.convert("P", palette=Image.Palette.ADAPTIVE))
    durations = [110] * 15 + [90] * 8 + [85] * 8 + [110] * 4 + [85] * 4 + [450]
    preview_frames[0].save(
        REVIEW_DIR / "bumblebee-action-preview.gif",
        save_all=True,
        append_images=preview_frames[1:],
        duration=durations,
        loop=0,
        disposal=2,
    )


def build_bundles() -> dict[str, dict]:
    verify_action_sources()
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
    REVIEW_DIR.mkdir(parents=True, exist_ok=True)
    paths = {kind: patch_bundle(kind, bundles, plain_dir) for kind in SOURCE_IDS}
    verify_plain_bundles(paths)
    save_client_contact(paths["texture"])
    save_action_preview(paths["texture"])
    metadata = {}
    for kind, path in paths.items():
        encrypted, digest, crc = encrypted_metadata(path)
        encrypted_path = encrypted_dir / f"{digest}.bundle"
        encrypted_path.write_bytes(encrypted)
        decrypted = unpad(AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(encrypted), AES.block_size)
        if decrypted[:7] != b"UnityFS":
            raise RuntimeError(f"encrypted {kind} bundle failed round-trip validation")
        metadata[kind] = {
            "path": encrypted_path,
            "hash": digest,
            "crc": crc,
            "size": len(encrypted),
        }
    return metadata


def build_modelview_patch() -> Path:
    output = BUILD_DIR / "Unity.ModelView.dll"
    digest = hashlib.sha256(MODELVIEW.read_bytes()).hexdigest()
    if digest != EXPECTED_MODELVIEW_SHA256:
        raise RuntimeError(f"current Unity.ModelView.dll is not the approved v13 build: {digest}")
    shutil.copy2(MODELVIEW, output)
    if not output.read_bytes().startswith(b"MZ"):
        raise RuntimeError("patched Unity.ModelView.dll is not a PE file")
    return output


def patch_manifest(metadata: dict[str, dict]) -> bytes:
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
    destination = Path(str(path) + BACKUP_SUFFIX)
    if not destination.exists():
        shutil.copy2(path, destination)
        print("backup", destination)
    return destination


def atomic_write(path: Path, data: bytes) -> None:
    temporary = Path(str(path) + ".bumblebee-v13.tmp")
    temporary.write_bytes(data)
    os.replace(temporary, path)


def install(metadata: dict[str, dict], patched_manifest: bytes, patched_modelview: Path) -> None:
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
    backup(MODELVIEW)
    shutil.copy2(patched_modelview, MODELVIEW)
    print("installed", MODELVIEW, MODELVIEW.stat().st_size)


def verify_installed(metadata: dict[str, dict], patched_modelview: Path) -> None:
    raw = MANIFEST.read_bytes()
    if MANIFEST_HASH.read_text(encoding="ascii").strip() != hashlib.md5(raw).hexdigest():
        raise RuntimeError("manifest hash sidecar does not match")
    _, assets, bundles = read_manifest(raw)
    if sum("Skin25" in asset["path"] for asset in assets) != 74:
        raise RuntimeError("installed manifest does not contain 74 Skin25 assets")
    bundle_records = {bundle["name"]: bundle for bundle in bundles if "skin25" in bundle["name"]}
    if set(bundle_records) != set(TARGET_BUNDLE_NAMES.values()):
        raise RuntimeError("installed manifest does not contain exactly three Skin25 bundles")
    for kind, item in metadata.items():
        record = bundle_records[TARGET_BUNDLE_NAMES[kind]]
        if any(record[field] != item[field] for field in ("hash", "crc", "size")):
            raise RuntimeError(f"installed {kind} manifest metadata mismatch")
        path = AA1 / f"{item['hash']}.bundle"
        encrypted = path.read_bytes()
        if hashlib.md5(encrypted).hexdigest() != item["hash"]:
            raise RuntimeError(f"installed bundle hash mismatch: {path.name}")
        if not UnityPy.load(decrypt_bundle(path)).objects:
            raise RuntimeError(f"installed bundle is empty: {path.name}")
    if MODELVIEW.read_bytes() != patched_modelview.read_bytes():
        raise RuntimeError("installed Unity.ModelView.dll differs from the verified patch")
    print("verified Bumblebee v13 Skin25: eight-pose idle / v13 attack / persistent vehicle run")


def restore() -> None:
    manifest_backup = Path(str(MANIFEST) + BACKUP_SUFFIX)
    hash_backup = Path(str(MANIFEST_HASH) + BACKUP_SUFFIX)
    modelview_backup = Path(str(MODELVIEW) + BACKUP_SUFFIX)
    if not manifest_backup.is_file() or not hash_backup.is_file() or not modelview_backup.is_file():
        raise RuntimeError("Bumblebee v13 rollback backup is incomplete")
    backup_raw = manifest_backup.read_bytes()
    if hash_backup.read_text(encoding="ascii").strip() != hashlib.md5(backup_raw).hexdigest():
        raise RuntimeError("rollback manifest and hash sidecar do not match")
    atomic_write(MANIFEST, backup_raw)
    atomic_write(MANIFEST_HASH, hash_backup.read_bytes())
    shutil.copy2(modelview_backup, MODELVIEW)
    print("restored pre-Bumblebee-v13 Skin25 manifest and MonoAnimancer; unreferenced bundles were retained")


def main() -> None:
    parser = argparse.ArgumentParser()
    action = parser.add_mutually_exclusive_group()
    action.add_argument("--install", action="store_true", help="install into client-test only")
    action.add_argument("--restore", action="store_true", help="restore the pre-Bumblebee-v13 client files")
    args = parser.parse_args()
    if args.restore:
        restore()
        return
    metadata = build_bundles()
    patched_modelview = build_modelview_patch()
    patched_manifest = patch_manifest(metadata)
    if args.install:
        install(metadata, patched_manifest, patched_modelview)
        verify_installed(metadata, patched_modelview)
    else:
        print("build verified; inspect runtime-v9/bumblebee-action-preview.gif before --install")


if __name__ == "__main__":
    main()
