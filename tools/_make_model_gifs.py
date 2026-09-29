"""Build GIF previews for client monsters, bosses, NPCs, and pets."""

from __future__ import annotations

import re
import sys
from dataclasses import dataclass
from pathlib import Path

from PIL import Image, ImageDraw

from _inspect_skin_frames import AA1, MANIFEST, extract_frames
from _patch_blue_mech_skin import read_manifest


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
OUTPUT = ROOT / "_work" / "model-gif-preview"
ACTION_ORDER = ("Idle", "Run", "Attack", "Hurt", "Other")
ACTION_DELAY = {"Idle": 110, "Run": 90, "Attack": 100, "Hurt": 130, "Other": 500}
TEXTURE_PREFIX = "assets_download_texture_animationresource_role_"


@dataclass(frozen=True)
class Model:
    category: str
    name: str
    bundle_hash: str


def classify(bundle_name: str, bundle_hash: str) -> Model | None:
    if not bundle_name.startswith(TEXTURE_PREFIX) or not bundle_name.endswith(".bundle"):
        return None
    suffix = bundle_name[len(TEXTURE_PREFIX):-len(".bundle")]
    patterns = (
        (r"monster_monster(\d+)", "monster", "Monster{:02d}"),
        (r"monster_death(\d+)", "death", "Death{:02d}"),
        (r"boss_boss(\d+)", "boss", "Boss{}"),
        (r"monster_worldboss", "boss", "WorldBoss"),
        (r"familyboss_familyboss(\d+)", "familyboss", "FamilyBoss{}"),
        (r"npc_(.+)", "npc", "{}"),
        (r"pet_pet(\d+)", "pet", "Pet{:02d}"),
        (r"monster_coinmonster", "other-monster", "CoinMonster"),
    )
    for pattern, category, template in patterns:
        match = re.fullmatch(pattern, suffix)
        if not match:
            continue
        value = match.group(1) if match.groups() else None
        if value is None:
            name = template
        elif value.isdigit():
            name = template.format(int(value))
        else:
            name = template.format(value)
        return Model(category, name, bundle_hash)
    return None


def natural_key(value: str):
    return tuple(int(part) if part.isdigit() else part.lower() for part in re.split(r"(\d+)", value))


def fit_frame(image: Image.Image, size: int) -> Image.Image:
    image = image.convert("RGBA")
    bbox = image.getchannel("A").getbbox()
    if bbox:
        image = image.crop(bbox)
    scale = min(size / max(1, image.width), size / max(1, image.height), 1.0)
    if scale != 1.0:
        image = image.resize(
            (max(1, round(image.width * scale)), max(1, round(image.height * scale))),
            Image.Resampling.LANCZOS,
        )
    canvas = Image.new("RGBA", (size, size), (27, 27, 30, 255))
    canvas.alpha_composite(image, ((size - image.width) // 2, (size - image.height) // 2))
    return canvas


def flatten(groups, size: int):
    frames: list[Image.Image] = []
    durations: list[int] = []
    counts: dict[str, int] = {}
    for action in ACTION_ORDER:
        action_frames = groups.get(action, [])
        counts[action] = len(action_frames)
        for _, image in action_frames:
            frames.append(fit_frame(image, size))
            durations.append(ACTION_DELAY[action])
    return frames, durations, counts


def save_gif(path: Path, frames: list[Image.Image], durations: list[int]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    frames[0].save(
        path,
        save_all=True,
        append_images=frames[1:],
        duration=durations,
        loop=0,
        disposal=2,
        optimize=False,
    )


def save_gallery(path: Path, models, columns: int = 5, cycles: int = 48) -> None:
    tile_size, label_height, gap = 180, 22, 8
    rows = (len(models) + columns - 1) // columns
    width = columns * (tile_size + gap) + gap
    height = rows * (tile_size + label_height + gap) + gap
    sheets = []
    for frame_index in range(cycles):
        sheet = Image.new("RGB", (width, height), (18, 18, 21))
        draw = ImageDraw.Draw(sheet)
        for index, (model, frames) in enumerate(models):
            col, row = index % columns, index // columns
            x = gap + col * (tile_size + gap)
            y = gap + row * (tile_size + label_height + gap)
            frame = frames[frame_index % len(frames)].resize((tile_size, tile_size), Image.Resampling.LANCZOS)
            sheet.paste(frame.convert("RGB"), (x, y))
            draw.text((x + 4, y + tile_size + 3), model.name, fill="white")
        sheets.append(sheet)
    save_gif(path, sheets, [110] * len(sheets))
    sheets[0].save(path.with_name(path.stem + "-first-frame.png"))


def chunked(items, size: int):
    for start in range(0, len(items), size):
        yield items[start:start + size]


def main() -> None:
    requested = set(sys.argv[1:])
    _, _, bundles = read_manifest(MANIFEST.read_bytes())
    models = [model for bundle in bundles if (model := classify(bundle["name"], bundle["hash"]))]
    models.sort(key=lambda item: (item.category, natural_key(item.name)))
    if requested:
        models = [model for model in models if model.category in requested]

    rendered: dict[str, list[tuple[Model, list[Image.Image]]]] = {}
    for model in models:
        groups = extract_frames(AA1 / f"{model.bundle_hash}.bundle")
        frames, durations, counts = flatten(groups, 260)
        if not frames:
            raise RuntimeError(f"{model.name} has no renderable frames")
        save_gif(OUTPUT / model.category / f"{model.name}.gif", frames, durations)
        rendered.setdefault(model.category, []).append((model, frames))
        summary = " ".join(f"{key}={value}" for key, value in counts.items() if value)
        print(f"{model.category}/{model.name}: {summary}")

    for category, entries in rendered.items():
        group_size = 21 if category == "monster" else 20
        for group in chunked(entries, group_size):
            first, last = group[0][0].name, group[-1][0].name
            save_gallery(OUTPUT / category / f"{first}-{last}-gallery.gif", group)
    print(f"wrote {len(models)} model GIFs to {OUTPUT}")


if __name__ == "__main__":
    main()
