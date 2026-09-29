"""Build review-only Skin25 frames and GIF without touching either client."""

from __future__ import annotations

import shutil
import sys
from pathlib import Path

from PIL import Image, ImageDraw, ImageFilter


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
HUMANOID_SHEET = ROOT / "assets" / "bumblebee" / "review" / "bumblebee-faithful-keyposes-v2.png"
VEHICLE_SOURCE = ROOT / "assets" / "bumblebee" / "review" / "bumblebee-vehicle-v1.png"
TRANSFORM_SOURCE = ROOT / "assets" / "bumblebee" / "review" / "bumblebee-transform-keyposes-v1.png"
CANNON_VFX_SOURCE = ROOT / "assets" / "bumblebee" / "review" / "bumblebee-heavy-cannon-vfx-v1.png"
OUTPUT = ROOT / "assets" / "bumblebee" / "review" / "runtime-v6"
FRAME_SIZE = (569, 370)  # Matches the online Skin28/Optimus texture canvas.
CELL = 512
TRANSFORM_CELL = 1024
CANNON_VFX_CELL = 512
CANNON_MUZZLE = (410, 94)


def clean_generated_edges(image: Image.Image) -> Image.Image:
    """Remove the saturated red matte pixels produced around generated silhouettes."""
    cleaned = []
    pixels = image.convert("RGBA").get_flattened_data()
    for red, green, blue, alpha in pixels:
        if alpha < 8 or (red > 185 and green < 72 and blue < 72 and red > green * 2.5):
            cleaned.append((0, 0, 0, 0))
        else:
            cleaned.append((red, green, blue, alpha))
    result = Image.new("RGBA", image.size)
    result.putdata(cleaned)
    return result


def alpha_crop(image: Image.Image, label: str) -> Image.Image:
    box = image.getchannel("A").getbbox()
    if box is None:
        raise RuntimeError(f"blank source pose: {label}")
    return image.crop(box)


def humanoid_poses() -> list[list[Image.Image]]:
    sheet = Image.open(HUMANOID_SHEET).convert("RGBA")
    if sheet.size != (CELL * 4, CELL * 4):
        raise RuntimeError(f"expected a 2048x2048 key-pose sheet, got {sheet.size}")

    rows = []
    for row in range(4):
        poses = []
        for column in range(4):
            cell = sheet.crop(
                (column * CELL, row * CELL, (column + 1) * CELL, (row + 1) * CELL)
            )
            cleaned = clean_generated_edges(cell)
            source_box = cleaned.getchannel("A").getbbox()
            if source_box is None:
                raise RuntimeError(f"blank key pose at row {row + 1}, column {column + 1}")
            if min(source_box[0], source_box[1], CELL - source_box[2], CELL - source_box[3]) < 4:
                raise RuntimeError(f"key pose lacks a safe transparent gutter: {source_box}")
            poses.append(cleaned.crop(source_box))
        rows.append(poses)
    return rows


def render_humanoids(rows: list[list[Image.Image]]) -> list[list[Image.Image]]:
    # Run uses the vehicle form, so only idle, attack and reaction poses determine humanoid scale.
    actors = rows[0] + rows[2] + rows[3]
    scale = min(455 / max(actor.width for actor in actors), 332 / max(actor.height for actor in actors))
    rendered_rows = []
    for row_index, row in enumerate(rows):
        rendered = []
        for actor in row:
            size = (max(1, round(actor.width * scale)), max(1, round(actor.height * scale)))
            sprite = clean_generated_edges(actor.resize(size, Image.Resampling.LANCZOS))
            frame = Image.new("RGBA", FRAME_SIZE, (0, 0, 0, 0))
            x = (FRAME_SIZE[0] - sprite.width) // 2
            y = 352 - sprite.height
            frame.alpha_composite(sprite, (x, y))
            rendered.append(frame)
        rendered_rows.append(rendered)
    return rendered_rows


def render_vehicle() -> tuple[Image.Image, tuple[int, int, int, int]]:
    source = clean_generated_edges(Image.open(VEHICLE_SOURCE).convert("RGBA"))
    actor = alpha_crop(source, "vehicle")
    scale = min(485 / actor.width, 218 / actor.height)
    size = (max(1, round(actor.width * scale)), max(1, round(actor.height * scale)))
    car = clean_generated_edges(actor.resize(size, Image.Resampling.LANCZOS))
    frame = Image.new("RGBA", FRAME_SIZE, (0, 0, 0, 0))
    x = max(68, (FRAME_SIZE[0] - car.width) // 2 + 18)
    y = 335 - car.height
    frame.alpha_composite(car, (x, y))
    return frame, (x, y, x + car.width, y + car.height)


def render_transformations() -> list[Image.Image]:
    sheet = Image.open(TRANSFORM_SOURCE).convert("RGBA")
    if sheet.size != (TRANSFORM_CELL * 2, TRANSFORM_CELL * 2):
        raise RuntimeError(f"expected a 2048x2048 transform sheet, got {sheet.size}")

    target_boxes = ((370, 315), (430, 280), (470, 255), (482, 235))
    baselines = (350, 346, 340, 337)
    center_offsets = (0, 7, 15, 22)
    frames = []
    for index in range(4):
        column = index % 2
        row = index // 2
        cell = sheet.crop(
            (column * TRANSFORM_CELL, row * TRANSFORM_CELL,
             (column + 1) * TRANSFORM_CELL, (row + 1) * TRANSFORM_CELL)
        )
        actor = alpha_crop(clean_generated_edges(cell), f"transform {index + 1}")
        target_width, target_height = target_boxes[index]
        scale = min(target_width / actor.width, target_height / actor.height)
        size = (max(1, round(actor.width * scale)), max(1, round(actor.height * scale)))
        sprite = clean_generated_edges(actor.resize(size, Image.Resampling.LANCZOS))
        frame = Image.new("RGBA", FRAME_SIZE, (0, 0, 0, 0))
        x = (FRAME_SIZE[0] - sprite.width) // 2 + center_offsets[index]
        y = baselines[index] - sprite.height
        frame.alpha_composite(sprite, (x, y))
        if index == 0:
            # The generator left one detached grey crescent outside the actor.
            frame.paste((0, 0, 0, 0), (430, 0, FRAME_SIZE[0], FRAME_SIZE[1]))
        frames.append(frame)
    return frames


def add_exhaust(vehicle: Image.Image, car_box: tuple[int, int, int, int]) -> Image.Image:
    x0, y0, _, y1 = car_box
    height = y1 - y0
    center_y = y0 + round(height * 0.36)
    effect = Image.new("RGBA", FRAME_SIZE, (0, 0, 0, 0))
    draw = ImageDraw.Draw(effect)
    # Layered, stepped polygons retain the source game's pixel-sprite character.
    draw.polygon(
        [(x0 + 20, center_y - 27), (x0 - 52, center_y - 38), (x0 - 18, center_y - 10),
         (x0 - 60, center_y), (x0 - 18, center_y + 12), (x0 - 48, center_y + 35),
         (x0 + 20, center_y + 24)],
        fill=(0, 132, 210, 105),
    )
    draw.polygon(
        [(x0 + 19, center_y - 17), (x0 - 37, center_y - 24), (x0 - 10, center_y - 6),
         (x0 - 51, center_y), (x0 - 10, center_y + 7), (x0 - 34, center_y + 22),
         (x0 + 19, center_y + 15)],
        fill=(0, 213, 246, 185),
    )
    draw.polygon(
        [(x0 + 21, center_y - 8), (x0 - 24, center_y - 11), (x0 - 8, center_y),
         (x0 - 25, center_y + 10), (x0 + 21, center_y + 8)],
        fill=(210, 255, 255, 235),
    )
    result = Image.alpha_composite(effect, vehicle)
    return result


def shift_frame(frame: Image.Image, y_offset: int) -> Image.Image:
    if abs(y_offset) > 1:
        raise RuntimeError("idle displacement must remain within one pixel")
    shifted = Image.new("RGBA", FRAME_SIZE, (0, 0, 0, 0))
    shifted.alpha_composite(frame, (0, y_offset))
    return shifted


def generated_cannon_effects() -> list[Image.Image]:
    """Extract only the authored plasma fire from the generated 4x2 VFX sheet."""
    sheet = Image.open(CANNON_VFX_SOURCE).convert("RGBA")
    expected_size = (CANNON_VFX_CELL * 4, CANNON_VFX_CELL * 2)
    if sheet.size != expected_size:
        raise RuntimeError(f"expected a {expected_size} cannon VFX sheet, got {sheet.size}")

    source_crop = (320, 0, CANNON_VFX_CELL, 280)
    source_muzzle = (360, 110)
    scale = 0.80
    effects = []
    for index in range(8):
        column = index % 4
        row = index // 4
        cell = clean_generated_edges(
            sheet.crop(
                (column * CANNON_VFX_CELL, row * CANNON_VFX_CELL,
                 (column + 1) * CANNON_VFX_CELL, (row + 1) * CANNON_VFX_CELL)
            )
        )
        pixels = list(cell.get_flattened_data())
        isolated = []
        for offset, (red, green, blue, alpha) in enumerate(pixels):
            x = offset % CANNON_VFX_CELL
            if alpha == 0 or x < source_crop[0]:
                isolated.append((0, 0, 0, 0))
                continue
            cyan = blue > 120 and green > 105 and blue > red * 0.78
            white_hot = min(red, green, blue) > 155 and max(red, green, blue) - min(red, green, blue) < 90
            molten_gold = red > 205 and green > 135 and blue < 175
            # Near the muzzle, retain only luminous plasma colors so none of the
            # generator's redrawn actor/cannon can survive behind the fixed actor.
            if x < 390 and not (cyan or white_hot or molten_gold):
                isolated.append((0, 0, 0, 0))
            else:
                isolated.append((red, green, blue, alpha))
        isolated_cell = Image.new("RGBA", cell.size, (0, 0, 0, 0))
        isolated_cell.putdata(isolated)
        source = isolated_cell.crop(source_crop)
        # Several generated flight frames reach the right cell edge. Fade the
        # authored plasma itself instead of leaving a rectangular atlas cut.
        fade_width = 52
        source_pixels = list(source.get_flattened_data())
        faded = []
        for offset, (red, green, blue, alpha) in enumerate(source_pixels):
            x = offset % source.width
            if x >= source.width - fade_width:
                factor = max(0.0, (source.width - 1 - x) / fade_width)
                alpha = round(alpha * factor * factor)
            faded.append((red, green, blue, alpha))
        source.putdata(faded)
        size = (round(source.width * scale), round(source.height * scale))
        source = clean_generated_edges(source.resize(size, Image.Resampling.LANCZOS))
        layer = Image.new("RGBA", FRAME_SIZE, (0, 0, 0, 0))
        x = round(CANNON_MUZZLE[0] - (source_muzzle[0] - source_crop[0]) * scale)
        y = round(CANNON_MUZZLE[1] - (source_muzzle[1] - source_crop[1]) * scale)
        layer.alpha_composite(source, (x, y))
        effects.append(layer)
    return effects


def scaled_cannon_effect(effect: Image.Image, scale: float) -> Image.Image:
    box = effect.getchannel("A").getbbox()
    if box is None:
        return effect.copy()
    actor = effect.crop(box)
    size = (max(1, round(actor.width * scale)), max(1, round(actor.height * scale)))
    actor = clean_generated_edges(actor.resize(size, Image.Resampling.LANCZOS))
    x = round(CANNON_MUZZLE[0] + (box[0] - CANNON_MUZZLE[0]) * scale)
    y = round(CANNON_MUZZLE[1] + (box[1] - CANNON_MUZZLE[1]) * scale)
    layer = Image.new("RGBA", FRAME_SIZE, (0, 0, 0, 0))
    layer.alpha_composite(actor, (x, y))
    return layer


def cannon_effect(phase: int, *, heavy: bool) -> Image.Image:
    """Render either the small idle pulse or the larger combat cannon shot."""
    effect = Image.new("RGBA", FRAME_SIZE, (0, 0, 0, 0))
    glow = Image.new("RGBA", FRAME_SIZE, (0, 0, 0, 0))
    glow_draw = ImageDraw.Draw(glow)
    draw = ImageDraw.Draw(effect)
    muzzle_x, muzzle_y = 410, 94

    if not heavy:
        if phase == 5:
            glow_draw.ellipse((muzzle_x - 5, muzzle_y - 5, muzzle_x + 5, muzzle_y + 5), fill=(35, 222, 255, 105))
            draw.ellipse((muzzle_x - 2, muzzle_y - 2, muzzle_x + 2, muzzle_y + 2), fill=(220, 255, 255, 220))
        elif phase == 6:
            glow_draw.ellipse((muzzle_x - 8, muzzle_y - 8, muzzle_x + 12, muzzle_y + 8), fill=(10, 196, 255, 135))
            draw.polygon(
                [(muzzle_x - 2, muzzle_y - 5), (muzzle_x + 17, muzzle_y), (muzzle_x - 2, muzzle_y + 5)],
                fill=(155, 248, 255, 235),
            )
        elif 7 <= phase <= 10:
            centers = (447, 476, 505, 534)
            center_x = centers[phase - 7]
            radius = 5 if phase < 10 else 3
            glow_draw.ellipse(
                (center_x - radius * 2, muzzle_y - radius * 2,
                 center_x + radius * 2, muzzle_y + radius * 2),
                fill=(0, 181, 255, 105 if phase < 10 else 65),
            )
            draw.line((center_x - 13, muzzle_y, center_x - radius, muzzle_y), fill=(20, 190, 240, 120), width=2)
            draw.ellipse(
                (center_x - radius, muzzle_y - radius, center_x + radius, muzzle_y + radius),
                fill=(170, 250, 255, 235 if phase < 10 else 150),
            )
    else:
        if phase in (0, 1):
            radius = 7 + phase * 5
            glow_draw.ellipse(
                (muzzle_x - radius, muzzle_y - radius, muzzle_x + radius, muzzle_y + radius),
                fill=(0, 190, 255, 115 + phase * 35),
            )
            draw.ellipse(
                (muzzle_x - radius // 2, muzzle_y - radius // 2,
                 muzzle_x + radius // 2, muzzle_y + radius // 2),
                fill=(255, 224, 95, 195 + phase * 25),
            )
        elif phase == 2:
            glow_draw.ellipse((muzzle_x - 18, muzzle_y - 17, muzzle_x + 28, muzzle_y + 17), fill=(0, 188, 255, 175))
            draw.polygon(
                [(muzzle_x - 3, muzzle_y - 12), (muzzle_x + 35, muzzle_y), (muzzle_x - 3, muzzle_y + 12)],
                fill=(255, 222, 80, 240),
            )
            draw.line((muzzle_x + 2, muzzle_y, muzzle_x + 33, muzzle_y), fill=(235, 255, 255, 255), width=4)
        elif 3 <= phase <= 6:
            centers = (458, 489, 520, 535)
            center_x = centers[phase - 3]
            radius = (12, 15, 17, 9)[phase - 3]
            alpha = (235, 245, 235, 135)[phase - 3]
            tail_start = max(muzzle_x + 5, center_x - 45)
            draw.polygon(
                [(tail_start, muzzle_y - 6), (center_x - radius + 2, muzzle_y - 9),
                 (center_x - radius + 2, muzzle_y + 9), (tail_start, muzzle_y + 6)],
                fill=(0, 181, 242, min(alpha, 150)),
            )
            glow_draw.ellipse(
                (center_x - radius * 2, muzzle_y - radius * 2,
                 center_x + radius * 2, muzzle_y + radius * 2),
                fill=(0, 171, 255, min(alpha, 150)),
            )
            draw.ellipse(
                (center_x - radius, muzzle_y - radius, center_x + radius, muzzle_y + radius),
                fill=(28, 214, 255, alpha),
                outline=(210, 255, 255, alpha),
                width=2,
            )
            core = max(3, radius // 2)
            draw.ellipse(
                (center_x - core, muzzle_y - core, center_x + core, muzzle_y + core),
                fill=(255, 220, 72, alpha),
            )
        elif phase == 7:
            for x_offset, y_offset in ((8, -8), (15, 3), (23, -2)):
                draw.rectangle(
                    (muzzle_x + x_offset, muzzle_y + y_offset,
                     muzzle_x + x_offset + 2, muzzle_y + y_offset + 2),
                    fill=(90, 225, 255, 110),
                )

    effect = Image.alpha_composite(glow.filter(ImageFilter.GaussianBlur(4.0 if heavy else 2.5)), effect)
    return effect


def add_cannon_effect(frame: Image.Image, phase: int, *, heavy: bool) -> Image.Image:
    # Compositing the fixed actor last guarantees that cannon effects never move or redraw it.
    return Image.alpha_composite(cannon_effect(phase, heavy=heavy), frame)


def build_frames(humanoid: list[list[Image.Image]]) -> dict[str, list[Image.Image]]:
    # Idle and combat use the exact same actor bitmap; only their cannon VFX differ.
    base_actor = humanoid[0][0]
    vehicle, car_box = render_vehicle()
    heavy_effects = generated_cannon_effects()
    idle_frames = []
    for phase in range(15):
        idle_base = base_actor.copy()
        effect_index = phase - 5
        if 0 <= effect_index < len(heavy_effects):
            idle_effect = scaled_cannon_effect(heavy_effects[effect_index], 0.48)
            idle_base = Image.alpha_composite(idle_effect, idle_base)
        idle_frames.append(idle_base)
    return {
        "Idle": idle_frames,
        "Transform": render_transformations(),
        "Run": [vehicle, add_exhaust(vehicle, car_box)],
        "Attack": [Image.alpha_composite(effect, base_actor) for effect in heavy_effects],
        "Hurt": [humanoid[3][0].copy()],
    }


def verify_fixed_actor(frames: list[Image.Image], base_actor: Image.Image, action: str) -> None:
    base_pixels = list(base_actor.get_flattened_data())
    opaque_offsets = [offset for offset, pixel in enumerate(base_pixels) if pixel[3] == 255]
    if not opaque_offsets:
        raise RuntimeError("fixed actor has no opaque verification pixels")
    for index, frame in enumerate(frames, 1):
        frame_pixels = list(frame.get_flattened_data())
        if any(frame_pixels[offset] != base_pixels[offset] for offset in opaque_offsets):
            raise RuntimeError(f"{action}_{index:02d} moved or redrew the actor")


def save_frames(groups: dict[str, list[Image.Image]], base_actor: Image.Image) -> None:
    expected = {"Idle": 15, "Transform": 4, "Run": 2, "Attack": 8, "Hurt": 1}
    actual = {action: len(frames) for action, frames in groups.items()}
    if actual != expected:
        raise RuntimeError(f"frame contract mismatch: {actual}")
    verify_fixed_actor(groups["Idle"], base_actor, "Idle")
    verify_fixed_actor(groups["Attack"], base_actor, "Attack")
    for action, frames in groups.items():
        for index, frame in enumerate(frames, 1):
            box = frame.getchannel("A").getbbox()
            if frame.size != FRAME_SIZE or box is None:
                raise RuntimeError(f"invalid frame: {action}_{index:02d}")
            gutter = min(box[0], box[1], FRAME_SIZE[0] - box[2], FRAME_SIZE[1] - box[3])
            if gutter < 4:
                raise RuntimeError(f"frame touches the runtime boundary: {action}_{index:02d} {box}")
            red_matte = sum(
                1
                for red, green, blue, alpha in frame.get_flattened_data()
                if alpha > 0 and red > 185 and green < 72 and blue < 72 and red > green * 2.5
            )
            if red_matte:
                raise RuntimeError(f"frame retains {red_matte} red matte pixels: {action}_{index:02d}")
            frame.save(OUTPUT / f"Skin25_{action}_{index:02d}.png")


def save_contact_sheet(groups: dict[str, list[Image.Image]]) -> None:
    selected = (
        [("Idle", index) for index in (0, 5, 6, 8, 10)]
        + [("Transform", index) for index in range(4)]
        + [("Run", index) for index in range(2)]
        + [("Attack", index) for index in range(8)]
        + [("Hurt", 0)]
    )
    thumb_size = (360, 234)
    columns = 4
    rows = (len(selected) + columns - 1) // columns
    sheet = Image.new("RGBA", (columns * thumb_size[0], rows * (thumb_size[1] + 26)), (27, 28, 30, 255))
    draw = ImageDraw.Draw(sheet)
    for position, (action, index) in enumerate(selected):
        x = position % columns * thumb_size[0]
        y = position // columns * (thumb_size[1] + 26)
        preview = groups[action][index].resize(thumb_size, Image.Resampling.LANCZOS)
        sheet.alpha_composite(preview, (x, y + 26))
        draw.text((x + 6, y + 6), f"Skin25_{action}_{index + 1:02d}", fill="white")
    sheet.save(OUTPUT / "bumblebee-contact.png")


def gif_palette(frames: list[Image.Image]) -> list[Image.Image]:
    width, height = FRAME_SIZE
    palette_source = Image.new("RGB", (width, height * len(frames)), (0, 0, 0))
    for index, frame in enumerate(frames):
        rgb = Image.new("RGB", frame.size, (0, 0, 0))
        rgb.paste(frame.convert("RGB"), mask=frame.getchannel("A"))
        palette_source.paste(rgb, (0, index * height))
    palette = palette_source.quantize(colors=255, method=Image.Quantize.MEDIANCUT)

    indexed_frames = []
    for frame in frames:
        rgb = Image.new("RGB", frame.size, (0, 0, 0))
        rgb.paste(frame.convert("RGB"), mask=frame.getchannel("A"))
        indexed = rgb.quantize(palette=palette)
        alpha = frame.getchannel("A")
        indexed_data = bytearray(indexed.tobytes())
        for offset, value in enumerate(alpha.tobytes()):
            if value < 16:
                indexed_data[offset] = 255
        indexed.frombytes(bytes(indexed_data))
        indexed.putpalette(palette.getpalette() + [0, 0, 0])
        indexed_frames.append(indexed)
    return indexed_frames


def save_gif(groups: dict[str, list[Image.Image]]) -> None:
    frames = (
        groups["Idle"]
        + groups["Transform"]
        + groups["Run"] * 5
        + list(reversed(groups["Transform"]))
        + groups["Idle"][:1]
        + groups["Attack"]
        + groups["Idle"][:1]
    )
    durations = (
        [125] * 14 + [360]
        + [95] * 4
        + [90] * 10
        + [95] * 4
        + [420]
        + [170, 170, 100, 105, 105, 105, 150, 340]
        + [650]
    )
    if len(frames) != len(durations):
        raise RuntimeError("GIF frame and duration counts differ")
    # Pillow collapses consecutive identical GIF frames. Coalesce them first so
    # the accumulated display time is deterministic and can still be verified.
    gif_frames = []
    gif_durations = []
    for frame, duration in zip(frames, durations):
        if gif_frames and frame.tobytes() == gif_frames[-1].tobytes():
            gif_durations[-1] += duration
        else:
            gif_frames.append(frame)
            gif_durations.append(duration)
    indexed = gif_palette(gif_frames)
    output = OUTPUT / "bumblebee-action-preview.gif"
    indexed[0].save(
        output,
        save_all=True,
        append_images=indexed[1:],
        duration=gif_durations,
        loop=0,
        disposal=2,
        transparency=255,
        optimize=False,
    )
    with Image.open(output) as result:
        has_transparency = "transparency" in result.info
        if result.n_frames != len(gif_frames):
            raise RuntimeError(f"GIF has {result.n_frames} frames, expected {len(gif_frames)}")
        actual_duration = 0
        for index in range(result.n_frames):
            result.seek(index)
            actual_duration += result.info.get("duration", 0)
        expected_duration = sum(duration // 10 * 10 for duration in gif_durations)
        if actual_duration != expected_duration:
            raise RuntimeError("GIF timing changed while identical frames were coalesced")
        if result.size != FRAME_SIZE or result.info.get("loop") != 0:
            raise RuntimeError("GIF size or loop metadata is invalid")
        if not has_transparency:
            raise RuntimeError("GIF does not expose a transparent palette index")


def main() -> None:
    if OUTPUT.exists():
        shutil.rmtree(OUTPUT)
    OUTPUT.mkdir(parents=True)
    humanoid = render_humanoids(humanoid_poses())
    groups = build_frames(humanoid)
    save_frames(groups, humanoid[0][0])
    save_contact_sheet(groups)
    save_gif(groups)
    print("verified review-only Skin25 v6: no ground ring / authored plasma VFX / fixed actor")
    print("client directories were not modified")


if __name__ == "__main__":
    main()
