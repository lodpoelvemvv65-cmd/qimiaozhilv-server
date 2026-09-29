"""Install Byakuya's timed spirit-sword idle and flash-step movement."""

from __future__ import annotations

import argparse
import hashlib
import math
import os
import shutil
import statistics
import struct
import subprocess
import sys
from copy import deepcopy
from pathlib import Path

import UnityPy
from Crypto.Cipher import AES
from Crypto.Util.Padding import unpad
from PIL import Image, ImageChops, ImageDraw, ImageFilter

from _patch_blue_mech_skin import (
    AES_IV,
    AES_KEY,
    encrypted_metadata,
    read_manifest,
    write_manifest,
)


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
CLIENT = ROOT / "client-test"
CLIENT_DATA = next(path for path in CLIENT.iterdir() if path.is_dir() and path.name.endswith("_Data"))
AA1 = CLIENT_DATA / "StreamingAssets" / "yoo" / "aa1"
MANIFEST = AA1 / "PackageManifest_aa1_2025-01-05-1027.bytes"
MANIFEST_HASH = MANIFEST.with_suffix(".hash")
TEXTURE_BUNDLE_NAME = "assets_download_texture_animationresource_role_skin_skin_byakuya.bundle"
ANIMATOR_BUNDLE_NAME = "assets_download_animators_skin_skin_byakuya.bundle"
IDLE_KEYPOSE_SHEET = (
    ROOT / "assets" / "byakuya" / "animation"
    / "byakuya-idle-spirit-sword-keyposes-chroma-v1.png"
)
BUILD_DIR = ROOT / "_work" / "byakuya-complete-animation"
BACKUP_SUFFIX = ".pre-byakuya-complete-animation.bak"
MODELVIEW = CLIENT_DATA / "Managed" / "Unity.ModelView.dll"
MODELVIEW_PATCHER_DIR = ROOT / "tools" / "client-byakuya-teleport-patcher"
MODELVIEW_PATCHER = (
    MODELVIEW_PATCHER_DIR / "bin" / "Release" / "net8.0"
    / "ClientByakuyaTeleportPatcher.exe"
)
TEXTURE_SIZE = (637, 500)
BASELINE = 462
IDLE_COUNT = 52
RUN_COUNT = 8
IDLE_PERIOD_SECONDS = 3.0
RUN_PERIOD_SECONDS = 1.0
IDLE_SEQUENCE = (
    [None] * 18
    + [0] * 2
    + [1] * 3
    + [2] * 3
    + [3] * 3
    + [5] * 3
    + [5] * 3
    + [5] * 4
    + [7] * 3
    + [0] * 2
    + [None] * 8
)
SPIRIT_PRESSURE_FRAMES = {
    32: 0.25,
    33: 0.48,
    34: 0.68,
    35: 0.82,
    36: 0.62,
    37: 0.40,
    38: 0.20,
}
EXPECTED_NAMES = {
    *(f"SkinByakuya_Idle_{index:02d}" for index in range(1, IDLE_COUNT + 1)),
    *(f"SkinByakuya_Run_{index:02d}" for index in range(1, RUN_COUNT + 1)),
    *(f"SkinByakuya_Attack_{index:02d}" for index in range(1, 9)),
    "SkinByakuya_Hurt_01",
}


def decrypt_bundle(path: Path) -> bytes:
    encrypted = path.read_bytes()
    if path.suffix == ".bundle" and hashlib.md5(encrypted).hexdigest() != path.stem:
        raise RuntimeError(f"bundle hash mismatch: {path.name}")
    plain = AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(encrypted)
    return unpad(plain, AES.block_size)


def bundle_parts(raw: bytes):
    environment = UnityPy.load(raw)
    bundle = next(iter(environment.files.values()))
    return environment, bundle


def manifest_record(raw: bytes, bundle_name: str) -> tuple[int, dict]:
    _, _, bundles = read_manifest(raw)
    matches = [(index, record) for index, record in enumerate(bundles) if record["name"] == bundle_name]
    if len(matches) != 1:
        raise RuntimeError(f"expected one {bundle_name} bundle, found {len(matches)}")
    return matches[0]


def source_manifest_raw() -> bytes:
    backup = Path(str(MANIFEST) + BACKUP_SUFFIX)
    return backup.read_bytes() if backup.is_file() else MANIFEST.read_bytes()


def source_bundle_path(bundle_name: str) -> Path:
    _, record = manifest_record(source_manifest_raw(), bundle_name)
    path = AA1 / f"{record['hash']}.bundle"
    if path.is_file():
        return path
    backup = Path(str(path) + BACKUP_SUFFIX)
    if backup.is_file():
        return backup
    raise FileNotFoundError(f"source SkinByakuya bundle is missing: {path.name}")


def alpha_crop(image: Image.Image) -> Image.Image:
    rgba = image.convert("RGBA")
    box = rgba.getchannel("A").getbbox()
    return rgba.crop(box) if box else rgba


def remove_green_background(image: Image.Image) -> Image.Image:
    rgba = image.convert("RGBA")
    pixels = rgba.load()
    for y in range(rgba.height):
        for x in range(rgba.width):
            red, green, blue, alpha = pixels[x, y]
            dominance = green - max(red, blue)
            if dominance >= 200:
                alpha = 0
            elif dominance > 0:
                alpha = min(alpha, 255 - dominance)
            if dominance > 0:
                green = min(green, max(red, blue))
            pixels[x, y] = red, green, blue, max(0, min(255, alpha))
    return alpha_crop(rgba)


def keep_largest_alpha_component(image: Image.Image) -> Image.Image:
    """Remove neighboring-pose fragments while retaining the connected actor and sword."""
    rgba = image.convert("RGBA")
    alpha = rgba.getchannel("A")
    width, height = rgba.size
    pixels = alpha.load()
    visited = bytearray(width * height)
    largest: list[tuple[int, int]] = []
    for start_y in range(height):
        for start_x in range(width):
            start = start_y * width + start_x
            if visited[start] or pixels[start_x, start_y] <= 16:
                continue
            visited[start] = 1
            pending = [(start_x, start_y)]
            component = []
            while pending:
                x, y = pending.pop()
                component.append((x, y))
                for neighbor_y in range(max(0, y - 1), min(height, y + 2)):
                    for neighbor_x in range(max(0, x - 1), min(width, x + 2)):
                        position = neighbor_y * width + neighbor_x
                        if visited[position] or pixels[neighbor_x, neighbor_y] <= 16:
                            continue
                        visited[position] = 1
                        pending.append((neighbor_x, neighbor_y))
            if len(component) > len(largest):
                largest = component
    if not largest:
        raise RuntimeError("generated Byakuya pose has no connected subject")
    component_mask = Image.new("L", rgba.size, 0)
    mask_pixels = component_mask.load()
    for x, y in largest:
        mask_pixels[x, y] = 255
    component_mask = component_mask.filter(ImageFilter.MaxFilter(5))
    rgba.putalpha(ImageChops.multiply(alpha, component_mask))
    return alpha_crop(rgba)


def clean_horizontal_sword_tip(image: Image.Image) -> Image.Image:
    """Replace the overlapped neighboring scarf at the blade tip with a plain sword."""
    rgba = image.convert("RGBA")
    if rgba.size != (348, 337):
        raise RuntimeError(f"unexpected horizontal Byakuya pose size: {rgba.size}")
    rgba.paste((0, 0, 0, 0), (268, 0, rgba.width, rgba.height))

    glow = Image.new("RGBA", rgba.size, (0, 0, 0, 0))
    glow_draw = ImageDraw.Draw(glow)
    glow_draw.line(((255, 95), (282, 91)), fill=(255, 105, 235, 145), width=15)
    glow = glow.filter(ImageFilter.GaussianBlur(6))
    rgba.alpha_composite(glow)

    blade = ImageDraw.Draw(rgba)
    blade.polygon(((254, 88), (286, 90), (255, 102)), fill=(255, 139, 238, 225))
    blade.line(((255, 94), (281, 91)), fill=(255, 235, 253, 245), width=3)
    return rgba


def split_chroma_sheet(path: Path) -> list[Image.Image]:
    if not path.is_file():
        raise FileNotFoundError(f"Byakuya animation sheet is missing: {path}")
    sheet = Image.open(path).convert("RGBA")
    if sheet.size != (1536, 1024):
        raise RuntimeError(f"unexpected Byakuya animation sheet size: {sheet.size}")
    cell_width, cell_height = sheet.width // 4, sheet.height // 2
    frames = []
    for row in range(2):
        for column in range(4):
            frame = sheet.crop((
                column * cell_width,
                row * cell_height,
                (column + 1) * cell_width,
                (row + 1) * cell_height,
            ))
            # The generated sheet has soft antialiasing at cell boundaries.
            # Clear a narrow gutter so a neighboring pose cannot leak in.
            frame.paste((0, 255, 0, 255), (0, 0, 18, frame.height))
            frame.paste((0, 255, 0, 255), (frame.width - 18, 0, frame.width, frame.height))
            frames.append(remove_green_background(frame))
    # Pose 6 crosses into its neighbors in the generated contact sheet. Its actor and
    # horizontal sword form the largest connected component; the loose white pieces do not.
    frames[5] = clean_horizontal_sword_tip(keep_largest_alpha_component(frames[5]))
    if len(frames) != 8 or any(frame.getchannel("A").getbbox() is None for frame in frames):
        raise RuntimeError("Byakuya animation sheet did not yield eight visible frames")
    return frames


def add_petal(layer: Image.Image, x: int, y: int, size: int, angle: float, alpha: int) -> None:
    petal = Image.new("RGBA", (size * 4, size * 4), (0, 0, 0, 0))
    draw = ImageDraw.Draw(petal)
    draw.ellipse((size, size // 2, size * 3, size * 7 // 2), fill=(255, 176, 241, alpha))
    petal = petal.rotate(angle, resample=Image.Resampling.BICUBIC, expand=False)
    layer.alpha_composite(petal, (x - petal.width // 2, y - petal.height // 2))


def tinted_copy(image: Image.Image, color: tuple[int, int, int], opacity: float) -> Image.Image:
    alpha = image.getchannel("A").point(lambda value: round(value * opacity))
    result = Image.new("RGBA", image.size, (*color, 0))
    result.putalpha(alpha)
    return result


def decorate_idle_pose(pose: Image.Image, index: int) -> Image.Image:
    pose = pose.convert("RGBA")
    phase = 2 * math.pi * index / IDLE_COUNT
    result = Image.new("RGBA", TEXTURE_SIZE, (0, 0, 0, 0))
    mask = pose.getchannel("A")
    blur_radius = 10 + round(3 * (1 + math.sin(phase)))
    blurred = mask.filter(ImageFilter.GaussianBlur(blur_radius))
    outer = ImageChops.subtract(blurred, mask)
    glow = Image.new("RGBA", TEXTURE_SIZE, (242, 92, 222, 0))
    glow.putalpha(outer.point(lambda value: round(value * (0.32 + 0.09 * math.sin(phase)))))
    result.alpha_composite(glow)

    petals = Image.new("RGBA", TEXTURE_SIZE, (0, 0, 0, 0))
    center_x, center_y = 286, 340
    for petal_index in range(10):
        angle = phase + petal_index * 2 * math.pi / 10
        radius_x = 112 + (petal_index % 3) * 19
        radius_y = 92 + (petal_index % 2) * 27
        x = center_x + round(math.cos(angle) * radius_x)
        y = center_y + round(math.sin(angle * 1.17) * radius_y)
        add_petal(petals, x, y, 3 + petal_index % 3,
                  math.degrees(angle) + petal_index * 13, 105 + petal_index * 9)
    result.alpha_composite(petals)
    result.alpha_composite(pose)
    if index in SPIRIT_PRESSURE_FRAMES:
        result.alpha_composite(spirit_pressure_layer(SPIRIT_PRESSURE_FRAMES[index]))
    return result


def spirit_pressure_layer(intensity: float) -> Image.Image:
    """A soft transparent pressure wake without a solid blade or crescent silhouette."""
    broad_mask = Image.new("L", TEXTURE_SIZE, 0)
    broad = ImageDraw.Draw(broad_mask)
    broad.line(
        ((338, 326), (405, 319), (478, 304), (558, 281), (620, 270)),
        fill=round(54 * intensity),
        width=66,
        joint="curve",
    )
    broad_mask = broad_mask.filter(ImageFilter.GaussianBlur(31))

    wisp_mask = Image.new("L", TEXTURE_SIZE, 0)
    wisps = ImageDraw.Draw(wisp_mask)
    wisps.line(
        ((340, 326), (418, 315), (500, 292), (590, 270)),
        fill=round(62 * intensity),
        width=17,
        joint="curve",
    )
    wisps.line(
        ((356, 343), (438, 337), (522, 319), (605, 292)),
        fill=round(34 * intensity),
        width=10,
        joint="curve",
    )
    wisp_mask = wisp_mask.filter(ImageFilter.GaussianBlur(15))

    alpha = ImageChops.lighter(broad_mask, wisp_mask)
    layer = Image.new("RGBA", TEXTURE_SIZE, (255, 151, 237, 0))
    layer.putalpha(alpha)
    return layer


def body_anchor(image: Image.Image, lower_height: int = 132) -> tuple[float, int]:
    """Find the lower-body center and foot baseline, ignoring pink VFX bounds."""
    image = image.convert("RGBA")
    candidates = []
    for y in range(image.height):
        for x in range(image.width):
            red, green, blue, alpha = image.getpixel((x, y))
            pink_effect = red > 150 and blue > 140 and green < min(red, blue) * 0.86
            if alpha > 128 and not pink_effect:
                candidates.append((x, y))
    if not candidates:
        raise RuntimeError("could not find Byakuya body pixels for anchoring")
    bottom = max(y for _, y in candidates)
    lower = [x for x, y in candidates if y >= bottom - lower_height]
    return float(statistics.median(lower)), bottom


def compose_keypose(frame: Image.Image, scale: float,
                    target_anchor: tuple[float, int]) -> Image.Image:
    frame = alpha_crop(frame)
    anchor_x, anchor_y = body_anchor(frame, round(132 / scale))
    resized = frame.resize(
        (max(1, round(frame.width * scale)), max(1, round(frame.height * scale))),
        Image.Resampling.LANCZOS,
    )
    box = resized.getchannel("A").getbbox()
    if box is None:
        raise RuntimeError("generated Byakuya key pose is transparent")
    x = round(target_anchor[0] - anchor_x * scale)
    y = round(target_anchor[1] - anchor_y * scale)
    result = Image.new("RGBA", TEXTURE_SIZE, (0, 0, 0, 0))
    result.alpha_composite(resized, (x, y))
    # Resampling can move the median lower-body center by one pixel. Correct the
    # rendered result so every action pose has the exact same registration point.
    rendered_anchor = body_anchor(result)
    x += round(target_anchor[0] - rendered_anchor[0])
    y += target_anchor[1] - rendered_anchor[1]
    result = Image.new("RGBA", TEXTURE_SIZE, (0, 0, 0, 0))
    result.alpha_composite(resized, (x, y))
    return result


def with_opacity(image: Image.Image, opacity: float) -> Image.Image:
    result = image.copy().convert("RGBA")
    result.putalpha(result.getchannel("A").point(lambda value: round(value * opacity)))
    return result


def aura_outline(image: Image.Image, strength: float, blur: int = 14) -> Image.Image:
    mask = image.getchannel("A")
    outer = ImageChops.subtract(mask.filter(ImageFilter.GaussianBlur(blur)), mask)
    result = Image.new("RGBA", image.size, (242, 92, 222, 0))
    result.putalpha(outer.point(lambda value: round(value * strength)))
    return result


def teleport_particles(index: int, intensity: float = 1.0) -> Image.Image:
    result = Image.new("RGBA", TEXTURE_SIZE, (0, 0, 0, 0))
    draw = ImageDraw.Draw(result)
    for line_index in range(9):
        x = 175 + (line_index * 31 + index * 17) % 170
        y = 230 + line_index * 23
        length = 42 + line_index * 10
        draw.line((x - length, y, x + length, y - 8),
                  fill=(255, 182, 244, round((40 + line_index * 9) * intensity)), width=2)
    for petal_index in range(14):
        x = 165 + (petal_index * 37 + index * 29) % 285
        y = 165 + (petal_index * 47 + index * 31) % 300
        add_petal(result, x, y, 3 + petal_index % 3,
                  index * 23 + petal_index * 37, round((90 + petal_index * 5) * intensity))
    return result


def teleport_frame(base: Image.Image, index: int) -> Image.Image:
    """Loop phase: vanish, flash once mid-path, then vanish before restarting."""
    result = Image.new("RGBA", TEXTURE_SIZE, (0, 0, 0, 0))
    particles = teleport_particles(index, (1.0, 1.0, 0.75, 1.0, 0.9, 0.55, 0.3, 0.12)[index])
    result.alpha_composite(particles.filter(ImageFilter.GaussianBlur(5)))
    result.alpha_composite(particles)
    if index == 0:
        result.alpha_composite(aura_outline(base, 0.72))
    opacity = (0.0, 0.0, 0.0, 1.0, 0.08, 0.0, 0.0, 0.0)[index]
    if opacity:
        result.alpha_composite(aura_outline(base, 0.8 if index in (0, 3) else 0.45))
        if index == 4:
            result.alpha_composite(tinted_copy(base, (225, 85, 215), 0.05), (-18, 0))
        result.alpha_composite(with_opacity(base, opacity))
    if result.getchannel("A").getbbox() is None:
        add_petal(result, 318, 330, 3, index * 35, 24)
    return result


def replacement_frames(original: dict[str, Image.Image]) -> dict[str, Image.Image]:
    idle_base = original["SkinByakuya_Idle_01"].convert("RGBA")
    keyposes = split_chroma_sheet(IDLE_KEYPOSE_SHEET)
    scale = 246 / keyposes[0].height
    target_anchor = body_anchor(idle_base)
    composed = [compose_keypose(frame, scale, target_anchor) for frame in keyposes]
    result = {}
    for index, keypose_index in enumerate(IDLE_SEQUENCE):
        pose = idle_base if keypose_index is None else composed[keypose_index]
        result[f"SkinByakuya_Idle_{index + 1:02d}"] = decorate_idle_pose(pose, index)
    for index in range(RUN_COUNT):
        result[f"SkinByakuya_Run_{index + 1:02d}"] = teleport_frame(idle_base, index)
    return result


def read_texture_images(raw: bytes) -> dict[str, Image.Image]:
    environment = UnityPy.load(raw)
    return {
        data.m_Name: data.image.convert("RGBA")
        for obj in environment.objects
        if obj.type.name == "Texture2D"
        for data in (obj.read(),)
    }


def preview_frame(image: Image.Image, action: str) -> Image.Image:
    crop = image.convert("RGBA").crop((88, 135, 549, 490))
    crop.thumbnail((400, 292), Image.Resampling.LANCZOS)
    canvas = Image.new("RGBA", (420, 340), (24, 24, 28, 255))
    canvas.alpha_composite(crop, ((420 - crop.width) // 2, 30 + (292 - crop.height) // 2))
    draw = ImageDraw.Draw(canvas)
    draw.rectangle((0, 0, 420, 28), fill=(12, 12, 14, 255))
    draw.rectangle((0, 320, 420, 340), fill=(12, 12, 14, 255))
    draw.text((8, 8), "SkinByakuya", fill="white")
    draw.text((8, 323), action, fill=(238, 214, 90, 255))
    return canvas.convert("RGB")


def save_gif(path: Path, frames: list[Image.Image], durations: list[int]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    frames[0].save(path, save_all=True, append_images=frames[1:], duration=durations,
                   loop=0, disposal=2, optimize=False)


def idle_gif_durations() -> list[int]:
    """GIF delays use 10 ms units; spread 40x60 + 12x50 to total exactly 3 s."""
    return [60 if (index * 40) // IDLE_COUNT != ((index - 1) * 40) // IDLE_COUNT else 50
            for index in range(IDLE_COUNT)]


def run_gif_durations() -> list[int]:
    """GIF delays use 10 ms units; alternate delays to total exactly one second."""
    return [120, 130] * 4


def write_previews(images: dict[str, Image.Image]) -> None:
    actions = (
        ("Idle", IDLE_COUNT, idle_gif_durations()),
        ("Run", RUN_COUNT, run_gif_durations()),
        ("Attack", 8, [100] * 8),
        ("Hurt", 1, [220]),
    )
    complete_frames, complete_durations = [], []
    for action, count, durations in actions:
        names = [f"SkinByakuya_{action}_{index:02d}" for index in range(1, count + 1)]
        frames = [preview_frame(images[name], action) for name in names]
        save_gif(BUILD_DIR / f"SkinByakuya-{action}.gif", frames, durations)
        complete_frames.extend(frames)
        complete_durations.extend(durations)
    save_gif(BUILD_DIR / "SkinByakuya-complete.gif", complete_frames, complete_durations)
    write_contact_sheet(images)
    write_sword_detail(images)
    write_flash_step_travel_preview(images)


def write_flash_step_travel_preview(images: dict[str, Image.Image]) -> None:
    cycle_durations = run_gif_durations()
    frames, durations = [], []
    frame_count = RUN_COUNT * 4
    for position in range(frame_count):
        source = images[f"SkinByakuya_Run_{position % RUN_COUNT + 1:02d}"]
        rendered = source.resize((382, 300), Image.Resampling.LANCZOS)
        canvas = Image.new("RGBA", (960, 360), (24, 24, 28, 255))
        center_x = 105 + round(750 * position / (frame_count - 1))
        canvas.alpha_composite(rendered, (center_x - 180, 34))
        draw = ImageDraw.Draw(canvas)
        draw.rectangle((0, 0, 960, 28), fill=(12, 12, 14, 255))
        draw.text((8, 8), "SkinByakuya", fill="white")
        frames.append(canvas.convert("RGB"))
        durations.append(cycle_durations[position % RUN_COUNT])
    save_gif(BUILD_DIR / "SkinByakuya-Run-long-distance.gif", frames, durations)


def write_sword_detail(images: dict[str, Image.Image]) -> None:
    indices = (30, 33, 36)
    panels = []
    for index in indices:
        frame = images[f"SkinByakuya_Idle_{index:02d}"].crop((180, 235, 637, 380))
        frame = frame.resize((914, 290), Image.Resampling.NEAREST)
        panel = Image.new("RGBA", (914, 320), (24, 24, 28, 255))
        panel.alpha_composite(frame, (0, 0))
        ImageDraw.Draw(panel).text((8, 298), f"Idle {index:02d}", fill="white")
        panels.append(panel)
    sheet = Image.new("RGBA", (914, 320 * len(panels)), (24, 24, 28, 255))
    for position, panel in enumerate(panels):
        sheet.alpha_composite(panel, (0, position * 320))
    sheet.convert("RGB").save(BUILD_DIR / "SkinByakuya-sword-detail.png")


def write_contact_sheet(images: dict[str, Image.Image]) -> None:
    idle_indices = (1, 19, 21, 24, 27, 30, 33, 36, 40, 43, 45, 52)
    entries = (
        [(f"Idle {index:02d}", images[f"SkinByakuya_Idle_{index:02d}"]) for index in idle_indices]
        + [(f"Flash {index:02d}", images[f"SkinByakuya_Run_{index:02d}"]) for index in range(1, 9)]
    )
    columns, tile_width, tile_height, label_height = 4, 280, 220, 22
    rows = math.ceil(len(entries) / columns)
    sheet = Image.new("RGBA", (columns * tile_width, rows * (tile_height + label_height)),
                      (24, 24, 28, 255))
    draw = ImageDraw.Draw(sheet)
    for position, (label, image) in enumerate(entries):
        x = position % columns * tile_width
        y = position // columns * (tile_height + label_height)
        rendered = image.crop((88, 105, 549, 490))
        rendered.thumbnail((tile_width - 8, tile_height - 8), Image.Resampling.LANCZOS)
        sheet.alpha_composite(rendered, (x + (tile_width - rendered.width) // 2,
                                         y + (tile_height - rendered.height) // 2))
        draw.text((x + 6, y + tile_height + 3), label, fill="white")
    sheet.convert("RGB").save(BUILD_DIR / "SkinByakuya-contact.png")


def verify_images(images: dict[str, Image.Image], original: dict[str, Image.Image]) -> None:
    if set(images) != EXPECTED_NAMES:
        raise RuntimeError(f"SkinByakuya texture contract mismatch: {len(images)} textures")
    if any(image.size != TEXTURE_SIZE for image in images.values()):
        raise RuntimeError("SkinByakuya contains a texture with the wrong dimensions")
    if any(image.getchannel("A").getbbox() is None for image in images.values()):
        raise RuntimeError("SkinByakuya contains an empty texture")

    unchanged = [name for name in EXPECTED_NAMES if "_Attack_" in name or "_Hurt_" in name]
    if any(images[name].tobytes() != original[name].tobytes() for name in unchanged):
        raise RuntimeError("Byakuya Attack/Hurt pixels changed unexpectedly")

    base = original["SkinByakuya_Idle_01"].convert("RGBA")
    opaque_positions = [
        (x, y) for y in range(base.height) for x in range(base.width)
        if base.getpixel((x, y))[3] == 255
    ]
    calm_frames = [index + 1 for index, keypose in enumerate(IDLE_SEQUENCE) if keypose is None]
    for index in calm_frames:
        image = images[f"SkinByakuya_Idle_{index:02d}"]
        if any(image.getpixel(position) != base.getpixel(position) for position in opaque_positions):
            raise RuntimeError(f"Byakuya calm idle body moved or changed in frame {index}")
    target_anchor = body_anchor(base)
    action_frames = [index + 1 for index, keypose in enumerate(IDLE_SEQUENCE) if keypose is not None]
    action_anchors = [body_anchor(images[f"SkinByakuya_Idle_{index:02d}"]) for index in action_frames]
    if any(x != target_anchor[0] or y != target_anchor[1] for x, y in action_anchors):
        raise RuntimeError(f"Byakuya spell poses are not registration-locked: {action_anchors}")
    sword_frame = images["SkinByakuya_Idle_30"]
    if any(
        sword_frame.getpixel((x, y))[3] > 150
        for y in range(275, 301)
        for x in range(450, sword_frame.width)
    ):
        raise RuntimeError("Byakuya spirit sword still has a bright trailing tip")
    body_matches = [
        sum(images[f"SkinByakuya_Run_{index:02d}"].getpixel(position) == base.getpixel(position)
            for position in opaque_positions)
        for index in range(1, RUN_COUNT + 1)
    ]
    if body_matches[3] < len(opaque_positions) * 0.9 or any(
        matches > len(opaque_positions) * 0.01
        for index, matches in enumerate(body_matches)
        if index != 3
    ):
        raise RuntimeError(f"Byakuya flash step does not show the full body exactly once: {body_matches}")
    if images["SkinByakuya_Run_08"].getchannel("A").getbbox() is None:
        raise RuntimeError("Byakuya flash step clamp frame is completely empty")
    print(f"verified fixed Byakuya calm body across {len(calm_frames)} idle frames")
    print(f"verified Byakuya spell pose anchor across {len(action_frames)} idle frames")
    print("verified Byakuya spirit sword has no trailing tip")
    print("verified Byakuya flash step shows the full body once per cycle")


def patch_plain_bundle(source_path: Path) -> tuple[Path, dict[str, Image.Image]]:
    source_raw = decrypt_bundle(source_path)
    original = read_texture_images(source_raw)
    if set(original) != EXPECTED_NAMES:
        raise RuntimeError(f"source SkinByakuya texture contract mismatch: {len(original)}")
    replacements = replacement_frames(original)
    environment, bundle = bundle_parts(source_raw)
    for obj in environment.objects:
        if obj.type.name != "Texture2D":
            continue
        data = obj.read()
        if data.m_Name in replacements:
            data.image = replacements[data.m_Name]
            data.save()

    BUILD_DIR.mkdir(parents=True, exist_ok=True)
    plain_path = BUILD_DIR / TEXTURE_BUNDLE_NAME
    plain_path.write_bytes(bundle.save(packer="lz4"))
    patched = read_texture_images(plain_path.read_bytes())
    verify_images(patched, original)
    write_previews(patched)
    return plain_path, original


def patch_animator_bundle(source_path: Path) -> Path:
    environment, bundle = bundle_parts(decrypt_bundle(source_path))
    found = set()
    for obj in environment.objects:
        if obj.type.name != "AnimationClip":
            continue
        data = obj.read()
        if data.m_Name == "SkinByakuya_Idle":
            retime_idle_clip(data)
            data.save()
            found.add("Idle")
        elif data.m_Name == "SkinByakuya_Run":
            retime_run_clip(data)
            data.m_MuscleClip.m_LoopTime = True
            data.m_WrapMode = 2  # UnityEngine.WrapMode.Loop
            data.save()
            found.add("Run")
    if found != {"Idle", "Run"}:
        raise RuntimeError(f"SkinByakuya animator patch was incomplete: {found}")
    path = BUILD_DIR / ANIMATOR_BUNDLE_NAME
    path.write_bytes(bundle.save(packer="lz4"))
    verify_animator_plain(path.read_bytes())
    return path


def retime_idle_clip(data) -> None:
    original_stop = float(data.m_MuscleClip.m_StopTime)
    if not 4.32 < original_stop < 4.34:
        raise RuntimeError(f"unexpected SkinByakuya_Idle duration: {original_stop}")
    stream = data.m_MuscleClip.m_Clip.data.m_StreamedClip
    values = list(stream.data)
    if len(values) != 366 or values[0] != 0xFF7FFFFF or values[-2:] != [0x7F800000, 0]:
        raise RuntimeError("unexpected SkinByakuya_Idle streamed curve layout")
    scale = IDLE_PERIOD_SECONDS / original_stop
    for position in range(7, len(values) - 2, 7):
        seconds = struct.unpack("<f", struct.pack("<I", values[position]))[0]
        values[position] = struct.unpack("<I", struct.pack("<f", seconds * scale))[0]
    stream.data = values
    sample_rate = IDLE_COUNT / IDLE_PERIOD_SECONDS
    data.m_SampleRate = sample_rate
    data.m_MuscleClip.m_Clip.data.m_DenseClip.m_SampleRate = sample_rate
    data.m_MuscleClip.m_StopTime = IDLE_PERIOD_SECONDS


def retime_run_clip(data) -> None:
    original_stop = float(data.m_MuscleClip.m_StopTime)
    if not 0.66 < original_stop < 0.67:
        raise RuntimeError(f"unexpected SkinByakuya_Run duration: {original_stop}")
    stream = data.m_MuscleClip.m_Clip.data.m_StreamedClip
    values = list(stream.data)
    if len(values) != 58 or values[0] != 0xFF7FFFFF or values[-2:] != [0x7F800000, 0]:
        raise RuntimeError("unexpected SkinByakuya_Run streamed curve layout")
    scale = RUN_PERIOD_SECONDS / original_stop
    for position in range(7, len(values) - 2, 7):
        seconds = struct.unpack("<f", struct.pack("<I", values[position]))[0]
        values[position] = struct.unpack("<I", struct.pack("<f", seconds * scale))[0]
    stream.data = values
    sample_rate = RUN_COUNT / RUN_PERIOD_SECONDS
    data.m_SampleRate = sample_rate
    data.m_MuscleClip.m_Clip.data.m_DenseClip.m_SampleRate = sample_rate
    data.m_MuscleClip.m_StopTime = RUN_PERIOD_SECONDS


def verify_animator_plain(raw: bytes) -> None:
    environment = UnityPy.load(raw)
    clips = {
        data.m_Name: (
            bool(data.m_MuscleClip.m_LoopTime),
            int(data.m_WrapMode),
            round(float(data.m_MuscleClip.m_StopTime), 4),
            round(float(data.m_SampleRate), 4),
        )
        for obj in environment.objects
        if obj.type.name == "AnimationClip"
        for data in (obj.read(),)
    }
    expected_names = {
        "SkinByakuya_Idle", "SkinByakuya_Run", "SkinByakuya_Attack", "SkinByakuya_Hurt"
    }
    if set(clips) != expected_names:
        raise RuntimeError(f"SkinByakuya clip contract mismatch: {clips}")
    idle = clips["SkinByakuya_Idle"]
    if idle[0] is not True or idle[2:] != (3.0, round(IDLE_COUNT / 3, 4)):
        raise RuntimeError(f"SkinByakuya_Idle is not an exact three-second loop: {idle}")
    run = clips["SkinByakuya_Run"]
    if run != (True, 2, RUN_PERIOD_SECONDS, RUN_COUNT / RUN_PERIOD_SECONDS):
        raise RuntimeError(f"SkinByakuya_Run is not looping flash-step: {run}")
    print("verified SkinByakuya_Idle: exactly 3.0 s / looping")
    print("verified SkinByakuya_Run: 1.0 s looping flash-step")


def patch_manifest(raw: bytes, metadata: dict[str, dict]) -> bytes:
    header, assets, bundles = read_manifest(raw)
    if write_manifest(header, assets, bundles) != raw:
        raise RuntimeError("aa1 manifest round-trip changed unmodified data")
    for bundle_name, item in metadata.items():
        index, record = manifest_record(raw, bundle_name)
        updated = deepcopy(record)
        updated["hash"] = item["hash"]
        updated["crc"] = item["crc"]
        updated["size"] = item["size"]
        bundles[index] = updated
    patched = write_manifest(header, assets, bundles)
    _, _, checked = read_manifest(patched)
    for bundle_name, item in metadata.items():
        _, checked_record = manifest_record(patched, bundle_name)
        if checked_record["hash"] != item["hash"]:
            raise RuntimeError(f"patched {bundle_name} manifest record failed validation")
    return patched


def encrypt_bundle(path: Path) -> dict:
    encrypted, digest, crc = encrypted_metadata(path)
    encrypted_path = BUILD_DIR / f"{digest}.bundle"
    encrypted_path.write_bytes(encrypted)
    return {"path": encrypted_path, "hash": digest, "crc": crc, "size": len(encrypted)}


def build_modelview_patch() -> Path:
    project = MODELVIEW_PATCHER_DIR / "ClientByakuyaTeleportPatcher.csproj"
    source = MODELVIEW_PATCHER_DIR / "Program.cs"
    if (not MODELVIEW_PATCHER.is_file()
            or MODELVIEW_PATCHER.stat().st_mtime < max(project.stat().st_mtime, source.stat().st_mtime)):
        subprocess.run(
            ["dotnet", "build", str(project), "-c", "Release", "--nologo"],
            check=True,
            cwd=ROOT,
        )
    output = BUILD_DIR / "Unity.ModelView.dll"
    subprocess.run([str(MODELVIEW_PATCHER), str(MODELVIEW), str(output)], check=True, cwd=ROOT)
    if not output.read_bytes().startswith(b"MZ"):
        raise RuntimeError("patched Unity.ModelView.dll is not a PE file")
    return output


def build() -> tuple[dict[str, dict], bytes, dict[str, Image.Image], Path]:
    if BUILD_DIR.exists():
        shutil.rmtree(BUILD_DIR)
    texture_plain, original = patch_plain_bundle(source_bundle_path(TEXTURE_BUNDLE_NAME))
    animator_plain = patch_animator_bundle(source_bundle_path(ANIMATOR_BUNDLE_NAME))
    metadata = {
        TEXTURE_BUNDLE_NAME: encrypt_bundle(texture_plain),
        ANIMATOR_BUNDLE_NAME: encrypt_bundle(animator_plain),
    }
    patched_manifest = patch_manifest(MANIFEST.read_bytes(), metadata)
    modelview = build_modelview_patch()
    verify_encrypted_texture(metadata[TEXTURE_BUNDLE_NAME]["path"], original)
    verify_encrypted_animator(metadata[ANIMATOR_BUNDLE_NAME]["path"])
    for item in metadata.values():
        print("built", item["path"].name, item["path"].stat().st_size)
    print("preview", BUILD_DIR / "SkinByakuya-complete.gif")
    return metadata, patched_manifest, original, modelview


def verify_encrypted_texture(path: Path, original: dict[str, Image.Image]) -> None:
    raw = decrypt_bundle(path)
    environment = UnityPy.load(raw)
    textures = [obj for obj in environment.objects if obj.type.name == "Texture2D"]
    sprites = [obj for obj in environment.objects if obj.type.name == "Sprite"]
    if len(textures) != 69 or len(sprites) != 69:
        raise RuntimeError(f"SkinByakuya object count mismatch: {len(textures)} textures / {len(sprites)} sprites")
    verify_images(read_texture_images(raw), original)
    print("verified SkinByakuya bundle: 69 textures / 69 sprites")


def verify_encrypted_animator(path: Path) -> None:
    verify_animator_plain(decrypt_bundle(path))


def backup_once(path: Path) -> Path:
    backup = Path(str(path) + BACKUP_SUFFIX)
    if not backup.exists():
        shutil.copy2(path, backup)
        print("backup", backup)
    return backup


def atomic_write(path: Path, data: bytes) -> None:
    temporary = Path(str(path) + ".byakuya-animation.tmp")
    temporary.write_bytes(data)
    os.replace(temporary, path)


def install(metadata: dict[str, dict], patched_manifest: bytes,
            original: dict[str, Image.Image], modelview: Path) -> None:
    backup_once(MANIFEST)
    backup_once(MANIFEST_HASH)
    for bundle_name, item in metadata.items():
        _, current_record = manifest_record(MANIFEST.read_bytes(), bundle_name)
        backup_once(AA1 / f"{current_record['hash']}.bundle")
        shutil.copy2(item["path"], AA1 / item["path"].name)
    atomic_write(MANIFEST, patched_manifest)
    atomic_write(MANIFEST_HASH, hashlib.md5(patched_manifest).hexdigest().encode("ascii"))
    backup_once(MODELVIEW)
    shutil.copy2(modelview, MODELVIEW)
    verify_installed(metadata, original, modelview)
    print("installed complete Byakuya animation in client-test")


def verify_installed(metadata: dict[str, dict], original: dict[str, Image.Image], modelview: Path) -> None:
    raw = MANIFEST.read_bytes()
    if MANIFEST_HASH.read_text(encoding="ascii").strip() != hashlib.md5(raw).hexdigest():
        raise RuntimeError("aa1 manifest hash sidecar does not match")
    for bundle_name, item in metadata.items():
        _, record = manifest_record(raw, bundle_name)
        if record["hash"] != item["hash"] or record["crc"] != item["crc"] or record["size"] != item["size"]:
            raise RuntimeError(f"installed {bundle_name} manifest metadata does not match")
        destination = AA1 / f"{record['hash']}.bundle"
        if hashlib.md5(destination.read_bytes()).hexdigest() != record["hash"]:
            raise RuntimeError(f"installed {bundle_name} hash does not match its filename")
    verify_encrypted_texture(AA1 / f"{metadata[TEXTURE_BUNDLE_NAME]['hash']}.bundle", original)
    verify_encrypted_animator(AA1 / f"{metadata[ANIMATOR_BUNDLE_NAME]['hash']}.bundle")
    if MODELVIEW.read_bytes() != modelview.read_bytes():
        raise RuntimeError("installed Unity.ModelView.dll differs from the verified patch")
    print("verified MonoAnimancer skips Run completion only for Skin25 and SkinByakuya")


def restore() -> None:
    manifest_backup = Path(str(MANIFEST) + BACKUP_SUFFIX)
    hash_backup = Path(str(MANIFEST_HASH) + BACKUP_SUFFIX)
    modelview_backup = Path(str(MODELVIEW) + BACKUP_SUFFIX)
    if not manifest_backup.is_file() or not hash_backup.is_file() or not modelview_backup.is_file():
        raise RuntimeError("Byakuya animation rollback backups are incomplete")
    raw = manifest_backup.read_bytes()
    if hash_backup.read_text(encoding="ascii").strip() != hashlib.md5(raw).hexdigest():
        raise RuntimeError("Byakuya rollback manifest and hash sidecar do not match")
    for bundle_name in (TEXTURE_BUNDLE_NAME, ANIMATOR_BUNDLE_NAME):
        _, record = manifest_record(raw, bundle_name)
        original_bundle = AA1 / f"{record['hash']}.bundle"
        bundle_backup = Path(str(original_bundle) + BACKUP_SUFFIX)
        if not original_bundle.exists() and bundle_backup.exists():
            shutil.copy2(bundle_backup, original_bundle)
        if not original_bundle.exists():
            raise RuntimeError(f"rollback SkinByakuya bundle is missing: {original_bundle.name}")
    atomic_write(MANIFEST, raw)
    atomic_write(MANIFEST_HASH, hash_backup.read_bytes())
    shutil.copy2(modelview_backup, MODELVIEW)
    print("restored pre-animation SkinByakuya bundle reference")


def main() -> None:
    parser = argparse.ArgumentParser()
    actions = parser.add_mutually_exclusive_group()
    actions.add_argument("--install", action="store_true", help="install into client-test only")
    actions.add_argument("--restore", action="store_true", help="restore the pre-animation client files")
    args = parser.parse_args()
    if args.restore:
        restore()
        return
    metadata, patched_manifest, original, modelview = build()
    if args.install:
        install(metadata, patched_manifest, original, modelview)
    else:
        print("build verified; inspect the GIF before --install")


if __name__ == "__main__":
    main()
