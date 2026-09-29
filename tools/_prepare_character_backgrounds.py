# -*- coding: utf-8 -*-
"""Prepare the generated character backgrounds for the FairyGUI preview slot."""

from pathlib import Path

from PIL import Image


ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "assets" / "character-backgrounds" / "generated"
OUTPUT = ROOT / "assets" / "character-backgrounds" / "prepared"
TARGET_SIZE = (840, 1040)


def prepare(source: Path) -> Image.Image:
    image = Image.open(source).convert("RGBA")
    target_ratio = TARGET_SIZE[0] / TARGET_SIZE[1]
    width, height = image.size
    source_ratio = width / height
    if source_ratio > target_ratio:
        crop_width = round(height * target_ratio)
        left = (width - crop_width) // 2
        image = image.crop((left, 0, left + crop_width, height))
    else:
        crop_height = round(width / target_ratio)
        top = (height - crop_height) // 2
        image = image.crop((0, top, width, top + crop_height))
    return image.resize(TARGET_SIZE, Image.Resampling.LANCZOS)


def main() -> None:
    OUTPUT.mkdir(parents=True, exist_ok=True)
    for source in sorted(SOURCE.glob("*.png")):
        item_id = source.name.split("-", 1)[0]
        output = OUTPUT / f"{item_id}.png"
        prepare(source).save(output, format="PNG", optimize=True)
        print(f"{output.name}: {Image.open(source).size} -> {TARGET_SIZE}")


if __name__ == "__main__":
    main()
