"""Build animated previews for original client skin action frames."""

from __future__ import annotations

import re
import sys
from pathlib import Path

from PIL import Image, ImageDraw

from _inspect_skin_frames import AA1, MANIFEST, extract_frames
from _patch_blue_mech_skin import read_manifest


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

OUTPUT = Path(__file__).resolve().parent.parent / "_work" / "skin-gif-preview"
SKIN_BUNDLE = re.compile(
    r"assets_download_texture_animationresource_role_skin_skin(?P<skin>\d+)\.bundle$"
)
ORDER = ("Idle", "Run", "Attack", "Hurt")
ACTION_DELAY = {"Idle": 120, "Run": 100, "Attack": 110, "Hurt": 180}


def fit_frame(image: Image.Image, size: int = 260) -> Image.Image:
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
    canvas = Image.new("RGBA", (size, size), (28, 28, 31, 255))
    canvas.alpha_composite(image, ((size - image.width) // 2, (size - image.height) // 2))
    return canvas


def action_frames(groups: dict[str, list[tuple[str, Image.Image]]]):
    frames: list[Image.Image] = []
    durations: list[int] = []
    for action in ORDER:
        for _, image in groups.get(action, []):
            frames.append(fit_frame(image))
            durations.append(ACTION_DELAY[action])
    return frames, durations


def make_contact(previews: list[tuple[int, list[Image.Image]]]) -> None:
    tile_w, tile_h = 280, 300
    columns = 3
    rows = (len(previews) + columns - 1) // columns
    sheets = []
    for frame_index in range(50):
        sheet = Image.new("RGB", (columns * tile_w, rows * tile_h), (20, 20, 23))
        draw = ImageDraw.Draw(sheet)
        for index, (skin, frames) in enumerate(previews):
            x = index % columns * tile_w
            y = index // columns * tile_h
            frame = frames[frame_index % len(frames)]
            sheet.paste(frame.convert("RGB"), (x + 10, y + 10))
            draw.text((x + 10, y + 275), f"Skin{skin}", fill="white")
        sheets.append(sheet)
    first_skin = min(skin for skin, _ in previews)
    last_skin = max(skin for skin, _ in previews)
    gallery_name = f"Skin{first_skin}-{last_skin}-gallery"
    sheets[0].save(OUTPUT / f"{gallery_name}-first-frame.png")
    sheets[0].save(
        OUTPUT / f"{gallery_name}.gif",
        save_all=True,
        append_images=sheets[1:],
        duration=110,
        loop=0,
        disposal=2,
        optimize=False,
    )


def main() -> None:
    requested = {int(value) for value in sys.argv[1:]} if len(sys.argv) > 1 else set(range(37, 46))
    _, _, bundles = read_manifest(MANIFEST.read_bytes())
    OUTPUT.mkdir(parents=True, exist_ok=True)
    previews: list[tuple[int, list[Image.Image]]] = []
    for bundle in bundles:
        match = SKIN_BUNDLE.fullmatch(bundle["name"])
        if not match:
            continue
        skin = int(match.group("skin"))
        if skin not in requested:
            continue
        path = AA1 / f"{bundle['hash']}.bundle"
        groups = extract_frames(path)
        frames, durations = action_frames(groups)
        if not frames:
            raise RuntimeError(f"Skin{skin} has no action frames")
        output = OUTPUT / f"Skin{skin:02d}.gif"
        frames[0].save(
            output,
            save_all=True,
            append_images=frames[1:],
            duration=durations,
            loop=0,
            disposal=2,
            optimize=False,
        )
        previews.append((skin, frames))
        print(
            f"Skin{skin}: {len(frames)} frames, "
            f"Idle={len(groups.get('Idle', []))} Run={len(groups.get('Run', []))} "
            f"Attack={len(groups.get('Attack', []))} Hurt={len(groups.get('Hurt', []))} -> {output}"
        )
    make_contact(sorted(previews))
    print(f"wrote {len(previews)} GIFs and one animated gallery to {OUTPUT}")


if __name__ == "__main__":
    main()
