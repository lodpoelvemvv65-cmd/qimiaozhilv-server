"""Build a transparent review GIF from the golden mech key-pose sheet."""

from __future__ import annotations

from pathlib import Path

from PIL import Image


ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "assets" / "bumblebee" / "animation" / "gold-mech-transform-keyposes-preview-v1.png"
OUTPUT = ROOT / "assets" / "bumblebee" / "animation" / "gold-mech-transform-preview-v1.gif"
CELL = 256
CANVAS = 360


def extract(sheet: Image.Image, row: int, column: int) -> Image.Image:
    cell = sheet.crop((column * CELL, row * CELL, (column + 1) * CELL, (row + 1) * CELL))
    alpha = cell.getchannel("A")
    box = alpha.getbbox()
    if box is None:
        raise RuntimeError(f"blank key pose at row {row + 1}, column {column + 1}")
    actor = cell.crop(box)
    scale = min(330 / actor.width, 330 / actor.height)
    actor = actor.resize(
        (max(1, round(actor.width * scale)), max(1, round(actor.height * scale))),
        Image.Resampling.LANCZOS,
    )
    frame = Image.new("RGBA", (CANVAS, CANVAS), (0, 0, 0, 0))
    frame.alpha_composite(actor, ((CANVAS - actor.width) // 2, CANVAS - actor.height - 12))
    return frame


def main() -> None:
    sheet = Image.open(SOURCE).convert("RGBA")
    if sheet.width != sheet.height or sheet.width < 512:
        raise RuntimeError(f"expected a square key-pose sheet, got {sheet.size}")
    if sheet.size != (1024, 1024):
        sheet = sheet.resize((1024, 1024), Image.Resampling.LANCZOS)
    if sheet.getchannel("A").getextrema() != (0, 255):
        raise RuntimeError("key-pose sheet does not have a real transparent background")

    rows = [[extract(sheet, row, column) for column in range(4)] for row in range(4)]
    frames = (
        rows[0] * 2
        + rows[1]
        + rows[2] * 2
        + list(reversed(rows[1]))
        + rows[0][:1]
        + rows[3]
        + rows[0][:1]
    )
    durations = (
        [180] * 8
        + [140] * 4
        + [120] * 8
        + [140] * 4
        + [220]
        + [160, 160, 120, 220]
        + [500]
    )
    if len(frames) != len(durations):
        raise RuntimeError("preview frame and duration counts differ")

    # GIF supports one transparent palette index. Quantize every frame against
    # a shared palette so the transparent backdrop remains stable in playback.
    matte = Image.new("RGBA", (CANVAS, CANVAS), (0, 0, 0, 0))
    palette_source = Image.new("RGB", (CANVAS, CANVAS * len(frames)), (0, 0, 0))
    for index, frame in enumerate(frames):
        composite = Image.alpha_composite(matte, frame).convert("RGB")
        palette_source.paste(composite, (0, index * CANVAS))
    palette = palette_source.quantize(colors=255, method=Image.Quantize.MEDIANCUT)
    gif_frames = []
    for frame in frames:
        rgb = Image.new("RGB", frame.size, (0, 0, 0))
        rgb.paste(frame.convert("RGB"), mask=frame.getchannel("A"))
        indexed = rgb.quantize(palette=palette)
        alpha = frame.getchannel("A")
        indexed_pixels = indexed.load()
        alpha_pixels = alpha.load()
        for y in range(CANVAS):
            for x in range(CANVAS):
                indexed_pixels[x, y] = 255 if alpha_pixels[x, y] < 16 else indexed_pixels[x, y]
        indexed.putpalette(palette.getpalette() + [0, 0, 0])
        gif_frames.append(indexed)

    gif_frames[0].save(
        OUTPUT,
        save_all=True,
        append_images=gif_frames[1:],
        duration=durations,
        loop=0,
        disposal=2,
        transparency=255,
        optimize=False,
    )
    with Image.open(OUTPUT) as result:
        if result.n_frames != len(frames):
            raise RuntimeError(f"GIF has {result.n_frames} frames, expected {len(frames)}")
    print(f"wrote transparent {len(frames)}-frame preview: {OUTPUT}")


if __name__ == "__main__":
    main()
