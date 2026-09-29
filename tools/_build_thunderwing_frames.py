"""Cut the original thunderwing key poses into the Skin25 runtime frame set."""

from __future__ import annotations

import math
import shutil
import sys
from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, ImageFilter


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "assets" / "bumblebee" / "animation" / "thunderwing-keyposes-v5.png"
OUTPUT = ROOT / "_work" / "blue-mech-frames"
CANVAS = 330
DOWNSAMPLE = 4


def row_components(sheet: Image.Image, row: int) -> list[Image.Image]:
    row_image = sheet.crop((0, row * 720, sheet.width, (row + 1) * 720))
    small = row_image.getchannel("A").resize(
        (sheet.width // DOWNSAMPLE, 720 // DOWNSAMPLE), Image.Resampling.BILINEAR
    )
    width, height = small.size
    pixels = small.load()
    seen = bytearray(width * height)
    found = []
    for y in range(height):
        for x in range(width):
            offset = y * width + x
            if seen[offset] or pixels[x, y] < 96:
                continue
            seen[offset] = 1
            stack = [(x, y)]
            points = []
            while stack:
                point_x, point_y = stack.pop()
                points.append((point_x, point_y))
                for next_y in range(max(0, point_y - 1), min(height, point_y + 2)):
                    for next_x in range(max(0, point_x - 1), min(width, point_x + 2)):
                        next_offset = next_y * width + next_x
                        if not seen[next_offset] and pixels[next_x, next_y] >= 96:
                            seen[next_offset] = 1
                            stack.append((next_x, next_y))
            if len(points) > 1000:
                found.append(points)
    if len(found) != 4:
        raise RuntimeError(f"expected four connected characters in row {row + 1}, found {len(found)}")

    components = []
    for points in found:
        component_mask = Image.new("L", small.size, 0)
        component_pixels = component_mask.load()
        for x, y in points:
            component_pixels[x, y] = 255
        mask = component_mask.resize(row_image.size, Image.Resampling.NEAREST).filter(ImageFilter.MaxFilter(9))
        alpha = ImageChops.multiply(row_image.getchannel("A"), mask)
        box = alpha.getbbox()
        if box is None:
            raise RuntimeError("connected character became blank at full resolution")
        box = (
            max(0, box[0] - 6), max(0, box[1] - 6),
            min(row_image.width, box[2] + 6), min(row_image.height, box[3] + 6),
        )
        isolated = row_image.copy()
        isolated.putalpha(alpha)
        components.append(isolated.crop(box))
    components.sort(key=lambda image: image.getbbox()[0] if image.getbbox() else 0)
    # The crops lost their source X positions, so sort by the original component centers.
    components = [
        image for _, image in sorted(
            zip((sum(point[0] for point in points) / len(points) for points in found), components),
            key=lambda item: item[0],
        )
    ]
    return components


def render(image: Image.Image, scale: float) -> Image.Image:
    size = (max(1, round(image.width * scale)), max(1, round(image.height * scale)))
    rendered = image.resize(size, Image.Resampling.LANCZOS)
    result = Image.new("RGBA", (CANVAS, CANVAS), (0, 0, 0, 0))
    result.alpha_composite(rendered, ((CANVAS - rendered.width) // 2, 318 - rendered.height))
    return result


def shifted(image: Image.Image, x: int, y: int, angle: float = 0) -> Image.Image:
    actor = image.rotate(angle, Image.Resampling.BICUBIC, center=(CANVAS // 2, 260)) if angle else image
    result = Image.new("RGBA", (CANVAS, CANVAS), (0, 0, 0, 0))
    result.alpha_composite(actor, (x, y))
    return result


def add_slash(image: Image.Image, strength: float, phase: int) -> Image.Image:
    effect = Image.new("RGBA", image.size, (0, 0, 0, 0))
    draw = ImageDraw.Draw(effect)
    box = (58 + phase * 5, 42 + phase * 4, 306 - phase * 2, 308 - phase * 2)
    start = 286 + phase * 5
    end = 426 + phase * 6
    alpha = round(205 * strength)
    draw.arc(box, start=start, end=end, fill=(15, 198, 255, round(alpha * 0.50)), width=13)
    draw.arc(box, start=start + 2, end=end - 2, fill=(75, 231, 255, round(alpha * 0.78)), width=7)
    draw.arc(box, start=start + 4, end=end - 4, fill=(225, 255, 255, alpha), width=2)
    glow = effect.filter(ImageFilter.GaussianBlur(3.0))
    result = Image.alpha_composite(glow, effect)
    return Image.alpha_composite(result, image)


def contact_sheet(groups: dict[str, list[Image.Image]]) -> None:
    for action, frames in groups.items():
        columns = min(4, len(frames))
        rows = math.ceil(len(frames) / columns)
        sheet = Image.new("RGBA", (columns * CANVAS, rows * (CANVAS + 24)), (28, 28, 30, 255))
        draw = ImageDraw.Draw(sheet)
        for index, frame in enumerate(frames):
            x = index % columns * CANVAS
            y = index // columns * (CANVAS + 24)
            sheet.alpha_composite(frame, (x, y + 24))
            draw.text((x + 5, y + 5), f"Skin25_{action}_{index + 1:02d}", fill="white")
        sheet.save(OUTPUT / f"Skin25_{action}_contact.png")


def verify(groups: dict[str, list[Image.Image]]) -> None:
    expected = {"Idle": 12, "Run": 12, "Attack": 6, "Hurt": 1}
    if {action: len(frames) for action, frames in groups.items()} != expected:
        raise RuntimeError("generated frame counts do not match the Skin25 animation clips")
    for action, frames in groups.items():
        for index, frame in enumerate(frames, 1):
            alpha = frame.getchannel("A")
            box = alpha.getbbox()
            if box is None:
                raise RuntimeError(f"blank frame: {action} {index}")
            if box[0] == 0 or box[1] == 0 or box[2] == CANVAS or box[3] == CANVAS:
                raise RuntimeError(f"frame touches runtime canvas boundary: {action} {index} {box}")
    run_differences = [
        ImageChops.difference(groups["Run"][index], groups["Run"][index + 1]).getbbox()
        for index in range(3)
    ]
    if any(value is None for value in run_differences):
        raise RuntimeError("run cycle contains repeated static key poses")


def main() -> None:
    source = Image.open(SOURCE).convert("RGBA")
    if source.size != (2880, 2880) or source.getchannel("A").getextrema() != (0, 255):
        raise RuntimeError("key-pose sheet must be a 2880x2880 image with real transparency")

    rows = [row_components(source, row) for row in range(4)]
    selected = rows[0] + rows[1] + [rows[2][0], rows[2][2], rows[2][3], rows[3][1]]
    scale = min(306 / max(image.width for image in selected), 306 / max(image.height for image in selected))
    idle_keys = [render(image, scale) for image in rows[0]]
    run_keys = [render(image, scale) for image in rows[1]]
    attack_ready = render(rows[2][0], scale)
    attack_impact = render(rows[2][2], scale)
    attack_follow = render(rows[2][3], scale)
    hurt = render(rows[3][1], scale)
    groups = {
        "Idle": [idle_keys[index] for index in (0, 1, 2, 3, 2, 1, 0, 1, 2, 3, 2, 1)],
        "Run": [run_keys[index] for index in (0, 1, 2, 3, 0, 1, 2, 3, 0, 1, 2, 3)],
        "Attack": [
            attack_ready,
            shifted(attack_ready, -3, 1, -2.5),
            attack_impact,
            add_slash(attack_impact, 1.0, 0),
            add_slash(attack_follow, 0.58, 1),
            attack_ready,
        ],
        "Hurt": [hurt],
    }
    verify(groups)

    if OUTPUT.exists():
        shutil.rmtree(OUTPUT)
    OUTPUT.mkdir(parents=True)
    for action, frames in groups.items():
        for index, frame in enumerate(frames, 1):
            frame.save(OUTPUT / f"Skin25_{action}_{index:02d}.png")
    contact_sheet(groups)
    print("verified 31 independent Skin25 frames: 12 idle / 12 run / 6 attack / 1 hurt")


if __name__ == "__main__":
    main()
