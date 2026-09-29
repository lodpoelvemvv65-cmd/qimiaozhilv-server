"""Build a combined Bankai Ichigo idle, sword-flight, attack and hurt preview."""

from __future__ import annotations

import math
import shutil
import sys
from collections import deque
from pathlib import Path

import UnityPy
from PIL import Image, ImageChops, ImageDraw

import _make_bankai_ichigo_effect_gifs as effects


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
SKIN_BUNDLE = (
    ROOT / "_work" / "custom-skins" / "bankai_ichigo" / "plain"
    / "assets_download_texture_animationresource_role_skin_skin_bankai_ichigo.bundle"
)
FLIGHT_SOURCE = (
    ROOT / "assets" / "ichigo" / "animation"
    / "bankai-ichigo-sword-flight-pose-chroma-v2.png"
)
MANJI_SOURCE = (
    ROOT / "assets" / "ichigo" / "animation"
    / "bankai-ichigo-crossed-sword-manji-v3.png"
)
ATTACK_VFX_SOURCE = (
    ROOT / "assets" / "ichigo" / "animation"
    / "bankai-ichigo-reiatsu-forward-slash-v6b.png"
)
FLIGHT_WAKE_SOURCE = (
    ROOT / "assets" / "ichigo" / "animation"
    / "bankai-ichigo-reiatsu-flight-wake-v4.png"
)
AMBIENT_VFX_SOURCE = (
    ROOT / "assets" / "ichigo" / "animation"
    / "bankai-ichigo-reiatsu-hurt-impact-v4.png"
)
SWORD_SOURCE = (
    ROOT / "assets" / "ichigo" / "animation"
    / "bankai-ichigo-flight-sword-isolated-v5.png"
)
OUTPUT_DIR = ROOT / "_work" / "ichigo-complete-animation" / "v6"
FRAME_DIR = OUTPUT_DIR / "frames"
CANVAS_SIZE = (637, 500)
BACKGROUND = (24, 24, 28, 255)


def ease(value: float) -> float:
    value = max(0.0, min(1.0, value))
    return value * value * (3.0 - 2.0 * value)


def with_opacity(image: Image.Image, opacity: float) -> Image.Image:
    rgba = image.convert("RGBA")
    rgba.putalpha(rgba.getchannel("A").point(
        lambda alpha: round(alpha * max(0.0, min(1.0, opacity)))
    ))
    return rgba


def load_skin_images() -> dict[str, Image.Image]:
    if not SKIN_BUNDLE.is_file():
        raise FileNotFoundError(SKIN_BUNDLE)
    environment = UnityPy.load(SKIN_BUNDLE.read_bytes())
    images = {
        data.m_Name: data.image.convert("RGBA")
        for obj in environment.objects
        if obj.type.name == "Texture2D"
        for data in (obj.read(),)
    }
    expected = {
        *(f"SkinBankaiIchigo_Idle_{index:02d}" for index in range(1, 53)),
        *(f"SkinBankaiIchigo_Run_{index:02d}" for index in range(1, 9)),
        *(f"SkinBankaiIchigo_Attack_{index:02d}" for index in range(1, 9)),
        "SkinBankaiIchigo_Hurt_01",
    }
    if set(images) != expected or any(image.size != CANVAS_SIZE for image in images.values()):
        raise RuntimeError(f"unexpected Bankai Ichigo skin contract: {len(images)} textures")
    return images


def load_flight_sprite() -> Image.Image:
    if not FLIGHT_SOURCE.is_file():
        raise FileNotFoundError(FLIGHT_SOURCE)
    keyed = effects.remove_green_background(Image.open(FLIGHT_SOURCE))
    return effects.place_sprite(keyed, (520, 290), 438)


def load_manji_vfx() -> Image.Image:
    if not MANJI_SOURCE.is_file():
        raise FileNotFoundError(MANJI_SOURCE)
    source = Image.open(MANJI_SOURCE).convert("RGBA")
    return effects.alpha_crop(remove_small_components(source))


def load_vfx(path: Path) -> Image.Image:
    if not path.is_file():
        raise FileNotFoundError(path)
    return effects.alpha_crop(Image.open(path).convert("RGBA"))


def load_sword_sprite() -> Image.Image:
    if not SWORD_SOURCE.is_file():
        raise FileNotFoundError(SWORD_SOURCE)
    source = remove_small_components(Image.open(SWORD_SOURCE).convert("RGBA"), 180)
    alpha = source.getchannel("A").point(lambda value: 0 if value < 28 else value)
    source.putalpha(alpha)
    return effects.alpha_crop(source)


def remove_small_components(image: Image.Image, minimum_area: int = 110) -> Image.Image:
    rgba = image.convert("RGBA")
    alpha = rgba.getchannel("A")
    scale = 4
    reduced = alpha.resize(
        (max(1, alpha.width // scale), max(1, alpha.height // scale)),
        Image.Resampling.BILINEAR,
    ).point(lambda value: 255 if value >= 36 else 0)
    width, height = reduced.size
    pixels = reduced.load()
    visited = bytearray(width * height)
    keep = Image.new("L", reduced.size, 0)
    keep_pixels = keep.load()
    for y in range(height):
        for x in range(width):
            offset = y * width + x
            if visited[offset] or not pixels[x, y]:
                continue
            visited[offset] = 1
            queue = deque(((x, y),))
            component: list[tuple[int, int]] = []
            while queue:
                current_x, current_y = queue.popleft()
                component.append((current_x, current_y))
                for next_x, next_y in (
                    (current_x - 1, current_y), (current_x + 1, current_y),
                    (current_x, current_y - 1), (current_x, current_y + 1),
                ):
                    if not (0 <= next_x < width and 0 <= next_y < height):
                        continue
                    next_offset = next_y * width + next_x
                    if visited[next_offset] or not pixels[next_x, next_y]:
                        continue
                    visited[next_offset] = 1
                    queue.append((next_x, next_y))
            if len(component) >= minimum_area:
                for component_x, component_y in component:
                    keep_pixels[component_x, component_y] = 255
    keep = keep.resize(alpha.size, Image.Resampling.BILINEAR)
    rgba.putalpha(ImageChops.multiply(alpha, keep))
    return rgba


def composite_center(canvas: Image.Image, image: Image.Image,
                     center: tuple[int, int], offset: tuple[int, int] = (0, 0)) -> None:
    x = center[0] - image.width // 2 + offset[0]
    y = center[1] - image.height // 2 + offset[1]
    canvas.alpha_composite(image, (x, y))


def transform_sprite(image: Image.Image, angle: float = 0.0,
                     offset: tuple[int, int] = (0, 0), scale: float = 1.0) -> Image.Image:
    source = image.convert("RGBA")
    if scale != 1.0:
        resized = source.resize(
            (max(1, round(source.width * scale)), max(1, round(source.height * scale))),
            Image.Resampling.LANCZOS,
        )
        source = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
        composite_center(source, resized, (CANVAS_SIZE[0] // 2, CANVAS_SIZE[1] // 2))
    if angle:
        source = source.rotate(angle, Image.Resampling.BICUBIC, center=(318, 350))
    if offset != (0, 0):
        moved = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
        moved.alpha_composite(source, offset)
        source = moved
    return source


def remove_held_sword(sprite: Image.Image) -> Image.Image:
    result = sprite.copy().convert("RGBA")
    alpha = result.getchannel("A")
    draw = ImageDraw.Draw(alpha)
    draw.line((366, 339, 447, 446), fill=0, width=11)
    result.putalpha(alpha)
    return result


def remove_flight_sword(sprite: Image.Image) -> Image.Image:
    result = sprite.copy().convert("RGBA")
    original_alpha = result.getchannel("A")
    alpha = original_alpha.copy()
    draw = ImageDraw.Draw(alpha)
    draw.rectangle((0, 402, CANVAS_SIZE[0], CANVAS_SIZE[1]), fill=0)
    for box in ((218, 382, 267, 427), (334, 382, 392, 427)):
        alpha.paste(original_alpha.crop(box), box)
    result.putalpha(alpha)
    return result


def place_vfx(source: Image.Image, size: tuple[int, int], center: tuple[int, int],
              angle: float = 0.0, opacity: float = 1.0,
              mirror: bool = False) -> Image.Image:
    effect = source.convert("RGBA")
    if mirror:
        effect = effect.transpose(Image.Transpose.FLIP_LEFT_RIGHT)
    effect = effect.resize(size, Image.Resampling.LANCZOS)
    if angle:
        effect = effect.rotate(angle, Image.Resampling.BICUBIC, expand=True)
    effect = with_opacity(effect, opacity)
    layer = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    composite_center(layer, effect, center)
    return layer


def weapon_motion(sword: Image.Image, progress: float,
                  opacity: float = 1.0) -> Image.Image:
    progress = ease(progress)
    width = round(138 + (500 - 138) * progress)
    height = round(27 + (52 - 27) * progress)
    center = (
        round(400 + (318 - 400) * progress),
        round(382 + (416 - 382) * progress),
    )
    return place_vfx(
        sword,
        (width, height),
        center,
        angle=-52 * (1.0 - progress),
        opacity=opacity,
    )


def crossed_weapon_vfx(source: Image.Image, progress: float, frame_index: int) -> Image.Image:
    progress = ease(progress)
    layer = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    if progress <= 0.0:
        return layer
    size = round(180 + progress * 270)
    emblem = source.resize((size, size), Image.Resampling.LANCZOS)
    emblem = emblem.rotate((1.0 - progress) * -38 + math.sin(frame_index * 0.4) * 2,
                            Image.Resampling.BICUBIC, expand=True)
    emblem = with_opacity(emblem, min(1.0, progress * 1.35))
    composite_center(layer, emblem, (318, 270))
    return layer


def ambient_reiatsu(source: Image.Image, frame_index: int, strength: float) -> Image.Image:
    phase = frame_index * 0.31
    scale = 1.0 + math.sin(phase) * 0.035
    opacity = strength * (0.16 + 0.035 * math.sin(phase + 0.8))
    return place_vfx(
        source,
        (round(390 * scale), round(325 * scale)),
        (318, 326 + round(math.sin(phase * 0.7) * 3)),
        angle=-42 + math.sin(phase * 0.5) * 2.5,
        opacity=opacity,
    )


def flight_reiatsu(source: Image.Image, frame_index: int, strength: float) -> Image.Image:
    phase = frame_index * 0.39
    width = round(480 + 18 * math.sin(phase))
    height = round(218 + 10 * math.sin(phase + 1.2))
    return place_vfx(
        source,
        (width, height),
        (105 + round(math.sin(phase * 0.6) * 5), 365 + round(math.sin(phase) * 5)),
        opacity=0.64 * strength,
    )


def release_frame(sprite: Image.Image, emblem: Image.Image, ambient: Image.Image,
                  progress: float, frame_index: int) -> Image.Image:
    strength = 0.55 + 0.75 * ease(progress)
    frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    frame.alpha_composite(ambient_reiatsu(ambient, frame_index, strength))
    frame.alpha_composite(crossed_weapon_vfx(emblem, progress, frame_index))
    frame.alpha_composite(sprite)
    return frame


def transform_to_flight(standing: Image.Image, flight: Image.Image,
                        sword: Image.Image, emblem: Image.Image, wake: Image.Image,
                        progress: float, frame_index: int) -> Image.Image:
    progress = ease(progress)
    frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    pulse = 0.7 + 0.3 * math.sin(progress * math.pi)
    frame.alpha_composite(crossed_weapon_vfx(emblem, 0.88 * (1.0 - progress), frame_index))
    frame.alpha_composite(flight_reiatsu(wake, frame_index, pulse * progress))
    switch_point = 0.8
    frame.alpha_composite(weapon_motion(sword, progress))
    if progress < switch_point:
        readiness = progress / switch_point
        body = transform_sprite(
            standing,
            angle=-1.8 * math.sin(readiness * math.pi),
            offset=(round(readiness * 3), round(math.sin(readiness * math.pi) * 5)),
        )
        frame.alpha_composite(body)
    else:
        mounting = (progress - switch_point) / (1.0 - switch_point)
        body = transform_sprite(
            flight,
            angle=-1.6 * (1.0 - mounting),
            offset=(0, round((1.0 - mounting) * 11)),
        )
        frame.alpha_composite(body)
    return frame


def flight_frame(sprite: Image.Image, sword: Image.Image, wake: Image.Image,
                 index: int, total: int) -> Image.Image:
    phase = 2 * math.pi * index / total
    acceleration = min(1.0, (index + 1) / 5)
    bob = round(math.sin(phase) * 7 * acceleration)
    drift = round(math.sin(phase * 2 + 0.6) * 3 * acceleration)
    tilt = -2.1 * acceleration + math.sin(phase) * 1.7
    scale = 1.0 + 0.011 * math.sin(phase * 2) * acceleration
    moving = transform_sprite(sprite, angle=tilt, offset=(drift, bob), scale=scale)
    strength = acceleration * (0.78 + 0.22 * (0.5 + 0.5 * math.sin(phase + 0.7)))
    frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    frame.alpha_composite(flight_reiatsu(wake, index, strength))
    moving_sword = transform_sprite(
        weapon_motion(sword, 1.0),
        angle=tilt * 0.24,
        offset=(drift, bob),
        scale=scale,
    )
    frame.alpha_composite(moving_sword)
    for offset, opacity in ((-64, 0.055), (-34, 0.09)):
        frame.alpha_composite(effects.tinted_copy(moving, (145, 0, 18), opacity), (offset, 0))
    frame.alpha_composite(moving)
    return frame


def transform_to_attack(flight: Image.Image, attack: Image.Image,
                        sword: Image.Image, wake: Image.Image,
                        progress: float, frame_index: int) -> Image.Image:
    progress = ease(progress)
    frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    frame.alpha_composite(flight_reiatsu(wake, frame_index, 1.0 - progress))
    switch_point = 0.78
    weapon_progress = 1.0 - progress
    weapon_opacity = 1.0 if progress < switch_point else 0.0
    frame.alpha_composite(weapon_motion(sword, weapon_progress, max(0.0, weapon_opacity)))
    if progress < switch_point:
        body = transform_sprite(flight, angle=-2.1 * (1.0 - progress / switch_point))
        frame.alpha_composite(body)
    else:
        frame.alpha_composite(attack)
    return frame


def attack_frame(sprite: Image.Image, slash: Image.Image, index: int) -> Image.Image:
    profiles = (
        ((230, 150), (350, 315), -42, 0.12, (0, 0)),
        ((280, 180), (355, 302), -38, 0.20, (0, 0)),
        ((340, 220), (365, 280), -32, 0.32, (0, -2)),
        ((430, 250), (385, 250), -22, 0.50, (3, -2)),
        ((520, 285), (425, 285), -10, 0.72, (8, 0)),
        ((650, 310), (470, 310), 0, 0.96, (14, 0)),
        ((700, 330), (500, 300), 4, 0.86, (18, -2)),
        ((720, 315), (535, 300), 7, 0.68, (20, 0)),
        ((660, 280), (550, 295), 8, 0.48, (11, 0)),
        ((560, 240), (560, 290), 10, 0.30, (5, 0)),
        ((430, 190), (570, 290), 12, 0.16, (0, 0)),
        ((360, 160), (590, 290), 14, 0.0, (0, 0)),
    )
    size, center, angle, opacity, offset = profiles[index]
    moved = transform_sprite(sprite, offset=offset)
    frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    if opacity > 0.0:
        frame.alpha_composite(place_vfx(
            slash, size, center, angle=angle, opacity=opacity
        ))
    if 3 <= index <= 9:
        frame.alpha_composite(
            effects.tinted_copy(moved, (112, 0, 12), 0.05 + (9 - index) * 0.008),
            (-18, 0),
        )
    frame.alpha_composite(moved)
    return frame


def hurt_frame(sprite: Image.Image, index: int) -> Image.Image:
    offsets = ((-18, 0), (-13, 2), (-8, 1), (-3, 0), (0, 0))
    angles = (-5.0, -3.5, -2.0, -0.8, 0.0)
    flash_opacity = (0.48, 0.25, 0.10, 0.0, 0.0)
    moved = transform_sprite(sprite, angle=angles[index], offset=offsets[index])
    frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    if flash_opacity[index] > 0.0:
        frame.alpha_composite(
            effects.tinted_copy(moved, (255, 238, 230), flash_opacity[index])
        )
    frame.alpha_composite(moved)
    return frame


def calm_frame(sprite: Image.Image, ambient: Image.Image,
               frame_index: int, strength: float = 0.55) -> Image.Image:
    frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    frame.alpha_composite(ambient_reiatsu(ambient, frame_index, strength))
    frame.alpha_composite(sprite)
    return frame


def build_timeline(images: dict[str, Image.Image], flight: Image.Image,
                   emblem: Image.Image, slash: Image.Image,
                   wake: Image.Image, ambient: Image.Image,
                   sword: Image.Image
                   ) -> tuple[list[Image.Image], list[int], list[str]]:
    frames: list[Image.Image] = []
    durations: list[int] = []
    stages: list[str] = []

    def append(frame: Image.Image, duration: int, stage: str) -> None:
        frames.append(frame)
        durations.append(duration)
        stages.append(stage)

    idle_base = images["SkinBankaiIchigo_Idle_01"]
    for index in range(8):
        append(calm_frame(idle_base, ambient, index), 110, "idle")

    release_names = tuple(range(33, 49))
    for index, source_index in enumerate(release_names):
        if index <= 10:
            progress = index / 10
        else:
            progress = 1.0 - 0.35 * (index - 10) / (len(release_names) - 11)
        source = images[f"SkinBankaiIchigo_Idle_{source_index:02d}"]
        append(release_frame(source, emblem, ambient, progress, index + 8), 80, "release")

    standing = remove_held_sword(images["SkinBankaiIchigo_Idle_48"])
    flight_character = remove_flight_sword(flight)
    for index in range(14):
        append(transform_to_flight(standing, flight_character, sword, emblem, wake,
                                   index / 13, index + 24),
               70, "transform-to-flight")

    for index in range(20):
        append(flight_frame(flight_character, sword, wake, index, 20), 60, "run")

    attack_first = images["SkinBankaiIchigo_Attack_01"]
    for index in range(12):
        append(transform_to_attack(flight_character, attack_first, sword, wake,
                                   index / 11, index + 58),
               70, "transform-to-attack")

    attack_sources = (1, 1, 2, 3, 4, 5, 5, 5, 6, 7, 8, 8)
    attack_durations = (80, 70, 70, 70, 70, 80, 80, 70, 80, 90, 100, 100)
    for index, (source_index, duration) in enumerate(
        zip(attack_sources, attack_durations)
    ):
        sprite = images[f"SkinBankaiIchigo_Attack_{source_index:02d}"]
        append(attack_frame(sprite, slash, index), duration, "attack")

    hurt = images["SkinBankaiIchigo_Hurt_01"]
    for index in range(5):
        append(hurt_frame(hurt, index), 110, "hurt")

    for index in range(6):
        progress = ease(index / 5)
        frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
        frame.alpha_composite(with_opacity(hurt, 1.0 - progress))
        frame.alpha_composite(with_opacity(
            calm_frame(idle_base, ambient, index + 80), progress
        ))
        append(frame, 100, "recover")

    return frames, durations, stages


def render_preview(frame: Image.Image) -> Image.Image:
    preview = Image.new("RGBA", CANVAS_SIZE, BACKGROUND)
    preview.alpha_composite(frame)
    return preview.convert("RGB")


def save_gif(path: Path, frames: list[Image.Image], durations: list[int]) -> None:
    previews = [render_preview(frame) for frame in frames]
    previews[0].save(
        path,
        save_all=True,
        append_images=previews[1:],
        duration=durations,
        loop=0,
        disposal=2,
        optimize=False,
    )


def save_contact(path: Path, frames: list[Image.Image]) -> None:
    sample_count = 12
    indices = [round(index * (len(frames) - 1) / (sample_count - 1))
               for index in range(sample_count)]
    columns, rows = 4, 3
    tile_size = (318, 250)
    sheet = Image.new("RGB", (columns * tile_size[0], rows * tile_size[1]), BACKGROUND[:3])
    for position, index in enumerate(indices):
        tile = render_preview(frames[index]).resize(tile_size, Image.Resampling.LANCZOS)
        sheet.paste(tile, ((position % columns) * tile_size[0],
                           (position // columns) * tile_size[1]))
    sheet.save(path)


def save_weapon_contact(path: Path, frames: list[Image.Image], stages: list[str]) -> None:
    indices = list(range(20, 39)) + list(range(56, 71))
    columns = 6
    tile_size = (212, 167)
    rows = math.ceil(len(indices) / columns)
    sheet = Image.new(
        "RGB",
        (columns * tile_size[0], rows * tile_size[1]),
        BACKGROUND[:3],
    )
    draw = ImageDraw.Draw(sheet)
    for position, index in enumerate(indices):
        tile = render_preview(frames[index]).resize(tile_size, Image.Resampling.LANCZOS)
        x = (position % columns) * tile_size[0]
        y = (position // columns) * tile_size[1]
        sheet.paste(tile, (x, y))
        draw.text((x + 5, y + 5), f"{index + 1:03d}-{stages[index]}", fill=(255, 255, 255))
    sheet.save(path)


def save_combat_contact(path: Path, frames: list[Image.Image], stages: list[str]) -> None:
    indices = [
        index for index, stage in enumerate(stages)
        if stage in {"attack", "hurt"}
    ]
    columns = 6
    tile_size = (212, 167)
    rows = math.ceil(len(indices) / columns)
    sheet = Image.new(
        "RGB",
        (columns * tile_size[0], rows * tile_size[1]),
        BACKGROUND[:3],
    )
    draw = ImageDraw.Draw(sheet)
    for position, index in enumerate(indices):
        tile = render_preview(frames[index]).resize(tile_size, Image.Resampling.LANCZOS)
        x = (position % columns) * tile_size[0]
        y = (position // columns) * tile_size[1]
        sheet.paste(tile, (x, y))
        draw.text((x + 5, y + 5), f"{index + 1:03d}-{stages[index]}", fill=(255, 255, 255))
    sheet.save(path)


def verify(frames: list[Image.Image], durations: list[int], stages: list[str]) -> None:
    if len(frames) != 93 or len(durations) != len(frames) or len(stages) != len(frames):
        raise RuntimeError(
            f"unexpected timeline contract: {len(frames)} frames / "
            f"{len(durations)} durations / {len(stages)} stages"
        )
    if any(frame.size != CANVAS_SIZE or frame.getchannel("A").getbbox() is None
           for frame in frames):
        raise RuntimeError("combined timeline contains an invalid frame")
    expected_stages = {
        "idle", "release", "transform-to-flight", "run",
        "transform-to-attack", "attack", "hurt", "recover",
    }
    if set(stages) != expected_stages:
        raise RuntimeError(f"combined timeline is missing a stage: {set(stages)}")
    print("verified combined timeline:", len(frames), "frames /", sum(durations), "ms")
    print("verified no lightning or procedural particle effect is used")


def main() -> None:
    if OUTPUT_DIR.exists():
        shutil.rmtree(OUTPUT_DIR)
    FRAME_DIR.mkdir(parents=True)
    images = load_skin_images()
    flight = load_flight_sprite()
    emblem = load_manji_vfx()
    slash = load_vfx(ATTACK_VFX_SOURCE)
    wake = load_vfx(FLIGHT_WAKE_SOURCE)
    ambient = load_vfx(AMBIENT_VFX_SOURCE)
    sword = load_sword_sprite()
    frames, durations, stages = build_timeline(
        images, flight, emblem, slash, wake, ambient, sword
    )
    verify(frames, durations, stages)
    for index, (frame, stage) in enumerate(zip(frames, stages), 1):
        frame.save(FRAME_DIR / f"{index:03d}-{stage}.png")
    gif = OUTPUT_DIR / "SkinBankaiIchigo-Idle-Run-Attack-Hurt-v6.gif"
    contact = OUTPUT_DIR / "SkinBankaiIchigo-Idle-Run-Attack-Hurt-v6-contact.png"
    weapon_contact = OUTPUT_DIR / "SkinBankaiIchigo-Weapon-Transitions-v6-contact.png"
    combat_contact = OUTPUT_DIR / "SkinBankaiIchigo-Attack-Hurt-v6-contact.png"
    combat_gif = OUTPUT_DIR / "SkinBankaiIchigo-Attack-Hurt-v6.gif"
    save_gif(gif, frames, durations)
    save_contact(contact, frames)
    save_weapon_contact(weapon_contact, frames, stages)
    save_combat_contact(combat_contact, frames, stages)
    transition_indices = [
        index for index, stage in enumerate(stages)
        if stage == "transform-to-attack"
    ][-2:]
    combat_indices = transition_indices + [
        index for index, stage in enumerate(stages)
        if stage in {"attack", "hurt", "recover"}
    ]
    save_gif(
        combat_gif,
        [frames[index] for index in combat_indices],
        [durations[index] for index in combat_indices],
    )
    print("gif", gif)
    print("contact", contact)
    print("weapon contact", weapon_contact)
    print("combat contact", combat_contact)
    print("combat gif", combat_gif)


if __name__ == "__main__":
    main()
