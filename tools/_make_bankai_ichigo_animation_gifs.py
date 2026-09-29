"""Build review GIFs from the generated Bankai Ichigo key-pose sheets."""

from __future__ import annotations

import shutil
import sys
from pathlib import Path

from PIL import Image, ImageDraw


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
ASSET_DIR = ROOT / "assets" / "ichigo" / "animation"
OUTPUT_DIR = ROOT / "_work" / "ichigo-complete-animation"
FRAME_DIR = OUTPUT_DIR / "frames"
IDLE_SHEET = ASSET_DIR / "bankai-ichigo-idle-keyposes-chroma-v1.png"
RUN_SHEET = ASSET_DIR / "bankai-ichigo-run-8frame-chroma-v1.png"
SHEET_SIZE = (1536, 1024)
GRID = (4, 2)
PREVIEW_SIZE = (520, 540)
IDLE_DURATIONS = [600, 300, 300, 250, 250, 400, 300, 600]
RUN_DURATIONS = [90] * 8


def remove_green_background(image: Image.Image) -> Image.Image:
    """Key a generated chroma background while suppressing green edge spill."""
    rgba = image.convert("RGBA")
    pixels = rgba.load()
    for y in range(rgba.height):
        for x in range(rgba.width):
            red, green, blue, alpha = pixels[x, y]
            dominance = green - max(red, blue)
            if green > 110 and dominance >= 80:
                alpha = 0
            elif green > 100 and dominance > 10:
                alpha = round(alpha * (80 - dominance) / 70)
            if dominance > 0:
                green = min(green, max(red, blue))
            pixels[x, y] = red, green, blue, max(0, min(255, alpha))
    return rgba


def split_sheet(path: Path) -> list[Image.Image]:
    if not path.is_file():
        raise FileNotFoundError(path)
    sheet = Image.open(path).convert("RGBA")
    if sheet.size != SHEET_SIZE:
        raise RuntimeError(f"unexpected sheet size for {path.name}: {sheet.size}")
    columns, rows = GRID
    cell_width, cell_height = sheet.width // columns, sheet.height // rows
    frames = []
    for row in range(rows):
        for column in range(columns):
            frame = sheet.crop((
                column * cell_width,
                row * cell_height,
                (column + 1) * cell_width,
                (row + 1) * cell_height,
            ))
            # Generated antialiasing can touch a cell boundary even when poses do not.
            frame.paste((0, 255, 0, 255), (0, 0, 3, frame.height))
            frame.paste((0, 255, 0, 255), (frame.width - 3, 0, frame.width, frame.height))
            frame = remove_green_background(frame)
            if frame.getchannel("A").getbbox() is None:
                raise RuntimeError(f"transparent frame in {path.name}: {len(frames) + 1}")
            frames.append(frame)
    if len(frames) != 8:
        raise RuntimeError(f"expected eight frames in {path.name}")
    return frames


def render_frame(frame: Image.Image) -> Image.Image:
    """Keep source-cell coordinates stable so the GIF exposes registration drift."""
    rendered = frame.resize((360, 480), Image.Resampling.LANCZOS)
    canvas = Image.new("RGBA", PREVIEW_SIZE, (24, 24, 28, 255))
    canvas.alpha_composite(rendered, ((PREVIEW_SIZE[0] - rendered.width) // 2, 24))
    draw = ImageDraw.Draw(canvas)
    draw.line((40, 505, PREVIEW_SIZE[0] - 40, 505), fill=(56, 56, 64, 255), width=1)
    return canvas.convert("RGB")


def save_gif(path: Path, frames: list[Image.Image], durations: list[int]) -> None:
    if len(frames) != len(durations):
        raise RuntimeError(f"frame/duration mismatch for {path.name}")
    frames[0].save(
        path,
        save_all=True,
        append_images=frames[1:],
        duration=durations,
        loop=0,
        disposal=2,
        optimize=False,
    )


def save_contact_sheet(path: Path, frames: list[Image.Image]) -> None:
    columns, rows = GRID
    tiles = [render_frame(frame).resize((260, 270), Image.Resampling.LANCZOS) for frame in frames]
    sheet = Image.new("RGB", (columns * 260, rows * 270), (24, 24, 28))
    for index, tile in enumerate(tiles):
        sheet.paste(tile, ((index % columns) * 260, (index // columns) * 270))
    sheet.save(path)


def write_action(action: str, frames: list[Image.Image], durations: list[int]) -> Path:
    action_dir = FRAME_DIR / action.lower()
    action_dir.mkdir(parents=True, exist_ok=True)
    for index, frame in enumerate(frames, 1):
        frame.save(action_dir / f"SkinBankaiIchigo_{action}_{index:02d}.png")
    rendered = [render_frame(frame) for frame in frames]
    gif_path = OUTPUT_DIR / f"SkinBankaiIchigo-{action}.gif"
    save_gif(gif_path, rendered, durations)
    save_contact_sheet(OUTPUT_DIR / f"SkinBankaiIchigo-{action}-contact.png", frames)
    return gif_path


def main() -> None:
    if FRAME_DIR.exists():
        shutil.rmtree(FRAME_DIR)
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    idle = split_sheet(IDLE_SHEET)
    run = split_sheet(RUN_SHEET)
    idle_gif = write_action("Idle", idle, IDLE_DURATIONS)
    run_gif = write_action("Run", run, RUN_DURATIONS)
    print("idle", idle_gif)
    print("run", run_gif)
    print("verified 8 idle poses / 3.0 s loop")
    print("verified 8 run frames / 0.72 s loop")


if __name__ == "__main__":
    main()
