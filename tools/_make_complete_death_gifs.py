"""Prepare and render complete Death01-Death16 animation GIF previews."""

from __future__ import annotations

import argparse
import json
import math
import re
import sys
from dataclasses import dataclass
from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, ImageFilter, ImageFont

from _inspect_skin_frames import AA1, MANIFEST, extract_frames
from _patch_blue_mech_skin import read_manifest


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
WORK = ROOT / "_work" / "death-complete-gifs"
OUTPUT = ROOT / "_work" / "model-gif-preview" / "death" / "complete"
PREFIX = "assets_download_texture_animationresource_role_monster_death"
ACTION_ORDER = ("Idle", "Run", "Attack", "Hurt")
ACTION_DELAY = {"Idle": 110, "Run": 85, "Attack": 95, "Hurt": 140}
CHARACTERS = {
    1: "Ichigo Kurosaki",
    2: "Rukia Kuchiki",
    3: "Renji Abarai",
    4: "Byakuya Kuchiki",
    5: "Jushiro Ukitake",
    6: "Ikkaku Madarame",
    7: "Kisuke Urahara",
    8: "Ichigo Kurosaki (Shikai)",
    9: "Kaname Tosen",
    10: "Gin Ichimaru",
    11: "Marechiyo Omaeda",
    12: "Hiyori Sarugaki",
    13: "Baraggan Louisenbairn",
    14: "Coyote Starrk",
    15: "Ulquiorra Cifer",
    16: "Ichigo Kurosaki (Bankai)",
}


@dataclass(frozen=True)
class DeathModel:
    number: int
    bundle_hash: str

    @property
    def name(self) -> str:
        return f"Death{self.number:02d}"


def models(requested: set[int] | None = None) -> list[DeathModel]:
    _, _, bundles = read_manifest(MANIFEST.read_bytes())
    found = []
    pattern = re.compile(rf"{PREFIX}(\d+)\.bundle$")
    for bundle in bundles:
        match = pattern.fullmatch(bundle["name"])
        if match:
            number = int(match.group(1))
            if not requested or number in requested:
                found.append(DeathModel(number, bundle["hash"]))
    return sorted(found, key=lambda item: item.number)


def alpha_crop(image: Image.Image) -> Image.Image:
    rgba = image.convert("RGBA")
    box = rgba.getchannel("A").getbbox()
    return rgba.crop(box) if box else rgba


def fit(image: Image.Image, size: tuple[int, int], allow_upscale: bool = False,
        preserve_bounds: bool = False) -> Image.Image:
    image = image.convert("RGBA") if preserve_bounds else alpha_crop(image)
    width, height = size
    scale = min(width / max(1, image.width), height / max(1, image.height))
    if not allow_upscale:
        scale = min(scale, 1.0)
    target = (max(1, round(image.width * scale)), max(1, round(image.height * scale)))
    return image.resize(target, Image.Resampling.LANCZOS)


def choose(frames: list[tuple[str, Image.Image]], count: int) -> list[tuple[str, Image.Image]]:
    if len(frames) <= count:
        return frames
    if count == 1:
        return [frames[0]]
    return [frames[round(index * (len(frames) - 1) / (count - 1))] for index in range(count)]


def checkerboard(size: tuple[int, int], block: int = 24) -> Image.Image:
    image = Image.new("RGBA", size, (238, 238, 238, 255))
    draw = ImageDraw.Draw(image)
    for y in range(0, size[1], block):
        for x in range(0, size[0], block):
            if (x // block + y // block) % 2:
                draw.rectangle((x, y, x + block - 1, y + block - 1), fill=(210, 210, 210, 255))
    return image


def prepare_reference(model: DeathModel, groups: dict[str, list[tuple[str, Image.Image]]]) -> None:
    original = WORK / "original" / model.name
    for action, entries in groups.items():
        action_dir = original / action
        action_dir.mkdir(parents=True, exist_ok=True)
        for index, (_, frame) in enumerate(entries, 1):
            alpha_crop(frame).save(action_dir / f"{action}-{index:02d}.png")

    reference = checkerboard((1024, 1024))
    draw = ImageDraw.Draw(reference)
    draw.rectangle((0, 0, 1024, 72), fill=(25, 25, 29, 255))
    title = f"{model.name} - {CHARACTERS.get(model.number, 'Bleach character')}"
    draw.text((24, 20), title, fill="white", font=ImageFont.load_default(size=24))
    samples = (
        ("Idle", choose(groups.get("Idle", []), 2)),
        ("Attack", choose(groups.get("Attack", []), 3)),
        ("Hurt", choose(groups.get("Hurt", []), 1)),
    )
    cells = [(x, y) for y in (92, 544) for x in (20, 352, 684)]
    index = 0
    for action, entries in samples:
        for name, frame in entries:
            x, y = cells[index]
            rendered = fit(frame, (312, 390), allow_upscale=True)
            reference.alpha_composite(rendered, (x + (312 - rendered.width) // 2, y + (390 - rendered.height) // 2))
            draw.rectangle((x, y + 390, x + 312, y + 428), fill=(25, 25, 29, 255))
            draw.text((x + 8, y + 402), f"{action}: {name}", fill="white")
            index += 1
    reference_dir = WORK / "reference"
    reference_dir.mkdir(parents=True, exist_ok=True)
    reference.save(reference_dir / f"{model.name}-reference.png")


def prepare() -> None:
    records = []
    for model in models():
        groups = extract_frames(AA1 / f"{model.bundle_hash}.bundle")
        prepare_reference(model, groups)
        counts = {action: len(groups.get(action, [])) for action in ACTION_ORDER}
        records.append({"number": model.number, "name": model.name,
                        "character": CHARACTERS.get(model.number), "counts": counts})
        print(model.name, counts)
    WORK.mkdir(parents=True, exist_ok=True)
    (WORK / "audit.json").write_text(json.dumps(records, indent=2), encoding="utf-8")
    print(f"prepared {len(records)} references under {WORK}")


def remove_light_background(image: Image.Image) -> Image.Image:
    rgba = image.convert("RGBA")
    pixels = rgba.load()
    for y in range(rgba.height):
        for x in range(rgba.width):
            red, green, blue, alpha = pixels[x, y]
            low, high = min(red, green, blue), max(red, green, blue)
            if low > 245 and high - low < 8:
                alpha = 0
            elif low > 220 and high - low < 12:
                alpha = round(alpha * (245 - low) / 25)
            pixels[x, y] = red, green, blue, alpha
    return rgba


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
    return rgba


def split_generated(path: Path, rows: int, columns: int, chroma: bool = False) -> list[Image.Image]:
    sheet = path.open("rb")
    with sheet:
        image = Image.open(sheet).convert("RGBA")
    cell_width, cell_height = image.width // columns, image.height // rows
    frames = []
    for row in range(rows):
        for column in range(columns):
            box = (column * cell_width, row * cell_height,
                   (column + 1) * cell_width, (row + 1) * cell_height)
            frame = image.crop(box)
            if chroma:
                frame = remove_green_background(frame)
            elif not frame.getchannel("A").getextrema()[0] < 255:
                frame = remove_light_background(frame)
            frames.append(alpha_crop(frame))
    return frames


def generated_groups(model: DeathModel) -> dict[str, list[tuple[str, Image.Image]]]:
    path = WORK / "generated" / f"{model.name}-actions.png"
    chroma_path = WORK / "generated" / f"{model.name}-actions-chroma.png"
    chroma = chroma_path.exists()
    if chroma:
        path = chroma_path
    if not path.exists():
        raise FileNotFoundError(f"missing generated action sheet: {path}")
    if model.number == 12:
        frames = split_generated(path, 4, 4, chroma=chroma)
        return {
            "Run": [(f"generated-run-{index + 1}", frame) for index, frame in enumerate(frames[:8])],
            "Idle": [(f"generated-idle-{index + 1}", frame) for index, frame in enumerate(frames[8:12])],
            "Hurt": [(f"generated-hurt-{index + 1}", frame) for index, frame in enumerate(frames[12:16])],
        }
    frames = split_generated(path, 2, 4, chroma=chroma)
    processed = WORK / "processed" / model.name
    processed.mkdir(parents=True, exist_ok=True)
    for index, frame in enumerate(frames, 1):
        frame.save(processed / f"Run-{index:02d}.png")
    return {"Run": [(f"generated-run-{index + 1}", frame) for index, frame in enumerate(frames)]}


def tint_alpha(image: Image.Image, color: tuple[int, int, int], opacity: float) -> Image.Image:
    alpha = image.getchannel("A").point(lambda value: round(value * opacity))
    tinted = Image.new("RGBA", image.size, (*color, 0))
    tinted.putalpha(alpha)
    return tinted


def add_petal(layer: Image.Image, x: int, y: int, size: int, angle: float, alpha: int) -> None:
    petal = Image.new("RGBA", (size * 3, size * 3), (0, 0, 0, 0))
    draw = ImageDraw.Draw(petal)
    draw.ellipse((size, size // 2, size * 2, size * 5 // 2), fill=(255, 176, 241, alpha))
    petal = petal.rotate(angle, resample=Image.Resampling.BICUBIC, expand=False)
    layer.alpha_composite(petal, (x - petal.width // 2, y - petal.height // 2))


def add_byakuya_effects(image: Image.Image, action: str, index: int, total: int) -> Image.Image:
    sprite = alpha_crop(image)
    pad_x, pad_y = (105, 65) if action == "Run" else (70, 55)
    canvas = Image.new("RGBA", (sprite.width + pad_x * 2, sprite.height + pad_y * 2), (0, 0, 0, 0))
    x, y = pad_x, pad_y

    if action == "Idle":
        phase = 2 * math.pi * index / max(1, total)
        mask = sprite.getchannel("A")
        blurred = mask.filter(ImageFilter.GaussianBlur(11 + round(3 * (1 + math.sin(phase)))))
        outer = ImageChops.subtract(blurred, mask)
        glow = Image.new("RGBA", sprite.size, (242, 92, 222, 0))
        glow.putalpha(outer.point(lambda value: round(value * (0.38 + 0.12 * math.sin(phase)))))
        canvas.alpha_composite(glow, (x, y))

        petals = Image.new("RGBA", canvas.size, (0, 0, 0, 0))
        center_x, center_y = x + sprite.width // 2, y + sprite.height // 2
        for petal_index in range(8):
            angle = phase + petal_index * math.pi / 4
            px = center_x + round(math.cos(angle) * (sprite.width * 0.56))
            py = center_y + round(math.sin(angle * 1.25) * (sprite.height * 0.46))
            add_petal(petals, px, py, 4 + petal_index % 3, math.degrees(angle), 120 + petal_index * 10)
        canvas.alpha_composite(petals)

    if action == "Run":
        ghost = tint_alpha(sprite, (225, 85, 215), 0.13)
        canvas.alpha_composite(ghost, (x - 34, y + 3))
        trails = Image.new("RGBA", canvas.size, (0, 0, 0, 0))
        draw = ImageDraw.Draw(trails)
        for trail_index in range(6):
            start_x = 8 + (trail_index * 17 + index * 11) % 45
            trail_y = pad_y + round(sprite.height * (0.24 + trail_index * 0.10))
            length = 48 + trail_index * 13
            draw.line((start_x, trail_y, start_x + length, trail_y - 4),
                      fill=(255, 196, 244, 70 + trail_index * 12), width=2)
        glow_trails = trails.filter(ImageFilter.GaussianBlur(4))
        canvas.alpha_composite(glow_trails)
        canvas.alpha_composite(trails)
        for petal_index in range(10):
            px = 18 + (petal_index * 29 + index * 19) % max(40, pad_x + sprite.width // 2)
            py = 24 + (petal_index * 47 + index * 31) % max(50, canvas.height - 48)
            add_petal(canvas, px, py, 3 + petal_index % 3, index * 21 + petal_index * 37, 125)

    canvas.alpha_composite(sprite, (x, y))
    return canvas


def render_frame(image: Image.Image, title: str, action: str, size: int = 360,
                 preserve_bounds: bool = False) -> Image.Image:
    canvas = Image.new("RGBA", (size, size + 52), (24, 24, 28, 255))
    rendered = fit(image, (size - 18, size - 18), allow_upscale=True,
                   preserve_bounds=preserve_bounds)
    canvas.alpha_composite(rendered, ((size - rendered.width) // 2, (size - rendered.height) // 2 + 28))
    draw = ImageDraw.Draw(canvas)
    draw.rectangle((0, 0, size, 30), fill=(12, 12, 14, 255))
    draw.rectangle((0, size + 30, size, size + 52), fill=(12, 12, 14, 255))
    draw.text((8, 8), title, fill="white")
    draw.text((8, size + 35), action, fill=(238, 214, 90, 255))
    return canvas.convert("RGB")


def save_gif(path: Path, frames: list[Image.Image], durations: list[int]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    frames[0].save(path, save_all=True, append_images=frames[1:], duration=durations,
                   loop=0, disposal=2, optimize=False)


def render_model(model: DeathModel) -> list[Image.Image]:
    groups = extract_frames(AA1 / f"{model.bundle_hash}.bundle")
    generated = generated_groups(model)
    groups["Run"] = generated["Run"]
    if model.number == 4 and groups.get("Idle"):
        idle_source = groups["Idle"][0][1]
        groups["Idle"] = [(name, idle_source.copy()) for name, _ in groups["Idle"]]
    if model.number == 12:
        groups["Idle"] = generated["Idle"]
        groups["Hurt"] = generated["Hurt"]

    frames, durations = [], []
    for action in ACTION_ORDER:
        entries = groups.get(action, [])
        if not entries:
            raise RuntimeError(f"{model.name} still has no {action} frames")
        action_frames, action_durations = [], []
        for index, (_, image) in enumerate(entries):
            decorated = model.number == 4 and action in {"Idle", "Run"}
            if decorated:
                image = add_byakuya_effects(image, action, index, len(entries))
            action_frames.append(render_frame(
                image, f"{model.name}  {CHARACTERS[model.number]}", action,
                preserve_bounds=decorated,
            ))
            action_durations.append(ACTION_DELAY[action])
        action_durations[-1] += 280
        save_gif(OUTPUT / f"{model.name}-{action}.gif", action_frames, action_durations)
        frames.extend(action_frames)
        durations.extend(action_durations)
    save_gif(OUTPUT / f"{model.name}-complete.gif", frames, durations)
    return frames


def gallery(entries: list[tuple[DeathModel, list[Image.Image]]]) -> None:
    columns, tile_width, tile_height = 4, 270, 309
    rows = math.ceil(len(entries) / columns)
    cycles = max(len(frames) for _, frames in entries)
    sheets = []
    for index in range(cycles):
        sheet = Image.new("RGB", (columns * tile_width, rows * tile_height), (18, 18, 21))
        for model_index, (_, frames) in enumerate(entries):
            frame = frames[index % len(frames)].resize((tile_width, tile_height), Image.Resampling.LANCZOS)
            sheet.paste(frame, ((model_index % columns) * tile_width, (model_index // columns) * tile_height))
        sheets.append(sheet)
    save_gif(OUTPUT / "Death01-Death16-complete-gallery.gif", sheets, [100] * len(sheets))
    sheets[0].save(OUTPUT / "Death01-Death16-complete-gallery-first-frame.png")


def render(requested: set[int] | None = None) -> None:
    rendered = [(model, render_model(model)) for model in models(requested)]
    if len(rendered) == 16:
        gallery(rendered)
    print(f"rendered {len(rendered)} complete GIFs under {OUTPUT}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("prepare", "render"))
    parser.add_argument("--death", type=int, action="append", help="render only this Death number")
    args = parser.parse_args()
    prepare() if args.mode == "prepare" else render(set(args.death or []))


if __name__ == "__main__":
    main()
