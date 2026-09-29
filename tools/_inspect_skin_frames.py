"""Extract read-only previews of the original client skin animation frames."""

from __future__ import annotations

import argparse
import csv
import re
import sys
from collections import defaultdict
from pathlib import Path

import UnityPy
from Crypto.Cipher import AES
from Crypto.Util.Padding import unpad
from PIL import Image, ImageDraw

from _patch_blue_mech_skin import AES_IV, AES_KEY, read_manifest


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
AA1 = ROOT / "client-test" / "梦幻奇遇记_Data" / "StreamingAssets" / "yoo" / "aa1"
MANIFEST = AA1 / "PackageManifest_aa1_2025-01-05-1027.bytes"
OUTPUT = ROOT / "_work" / "skin-frame-audit"
SKIN_BUNDLE = re.compile(
    r"assets_download_texture_animationresource_role_skin_skin(?P<skin>\d+)\.bundle$"
)
ACTION = re.compile(r"_(Idle|Run|Attack|Hurt)_", re.IGNORECASE)


def decrypt_bundle(path: Path) -> bytes:
    encrypted = path.read_bytes()
    plain = AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(encrypted)
    return unpad(plain, AES.block_size)


def alpha_crop(image: Image.Image) -> Image.Image:
    rgba = image.convert("RGBA")
    box = rgba.getchannel("A").getbbox()
    return rgba.crop(box) if box else rgba


def fit(image: Image.Image, width: int, height: int) -> Image.Image:
    image = alpha_crop(image)
    scale = min(width / max(1, image.width), height / max(1, image.height), 1.0)
    size = (max(1, round(image.width * scale)), max(1, round(image.height * scale)))
    return image.resize(size, Image.Resampling.LANCZOS)


def extract_frames(bundle_path: Path) -> dict[str, list[tuple[str, Image.Image]]]:
    groups: dict[str, list[tuple[str, Image.Image]]] = defaultdict(list)
    environment = UnityPy.load(decrypt_bundle(bundle_path))
    sprites = [obj for obj in environment.objects if obj.type.name == "Sprite"]
    frame_objects = sprites or [obj for obj in environment.objects if obj.type.name == "Texture2D"]
    for obj in frame_objects:
        data = obj.read()
        match = ACTION.search(data.m_Name)
        action = match.group(1).title() if match else "Other"
        groups[action].append((data.m_Name, data.image.convert("RGBA")))
    for frames in groups.values():
        frames.sort(key=lambda item: item[0])
    return groups


def pick(frames: list[tuple[str, Image.Image]], count: int) -> list[tuple[str, Image.Image]]:
    if len(frames) <= count:
        return frames
    if count == 1:
        return [frames[0]]
    return [frames[round(index * (len(frames) - 1) / (count - 1))] for index in range(count)]


def make_preview(skin: int, groups: dict[str, list[tuple[str, Image.Image]]]) -> Image.Image:
    chosen = (
        pick(groups.get("Idle", []), 2)
        + pick(groups.get("Run", []), 4)
        + pick(groups.get("Attack", []), 6)
        + pick(groups.get("Hurt", []), 1)
    )
    tile_w, tile_h, label_h = 220, 220, 24
    columns = 4
    rows = max(1, (len(chosen) + columns - 1) // columns)
    sheet = Image.new("RGBA", (columns * tile_w, rows * (tile_h + label_h) + 32), (28, 28, 30, 255))
    draw = ImageDraw.Draw(sheet)
    draw.text((8, 8), f"Skin{skin}  Idle {len(groups.get('Idle', []))} / Run {len(groups.get('Run', []))} / "
              f"Attack {len(groups.get('Attack', []))} / Hurt {len(groups.get('Hurt', []))}", fill="white")
    for index, (name, image) in enumerate(chosen):
        x = index % columns * tile_w
        y = index // columns * (tile_h + label_h) + 32
        rendered = fit(image, tile_w - 8, tile_h - 8)
        px = x + (tile_w - rendered.width) // 2
        py = y + (tile_h - rendered.height) // 2
        sheet.alpha_composite(rendered, (px, py))
        draw.text((x + 5, y + tile_h + 4), name, fill=(220, 220, 220, 255))
    return sheet


def make_gallery(previews: list[tuple[int, Image.Image]]) -> Image.Image:
    thumb_w, thumb_h, label_h = 220, 220, 28
    columns = 6
    rows = max(1, (len(previews) + columns - 1) // columns)
    gallery = Image.new("RGBA", (columns * thumb_w, rows * (thumb_h + label_h)), (25, 25, 27, 255))
    draw = ImageDraw.Draw(gallery)
    for index, (skin, preview) in enumerate(previews):
        x = index % columns * thumb_w
        y = index // columns * (thumb_h + label_h)
        # The first frame occupies the top-left preview tile.
        frame = preview.crop((0, 32, 220, 252))
        gallery.alpha_composite(frame, (x, y))
        draw.text((x + 8, y + thumb_h + 6), f"Skin{skin}", fill="white")
    return gallery


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("skins", nargs="*", type=int, help="skin numbers; default is every original skin")
    args = parser.parse_args()

    _, _, bundles = read_manifest(MANIFEST.read_bytes())
    requested = set(args.skins)
    records = []
    previews = []
    OUTPUT.mkdir(parents=True, exist_ok=True)
    for bundle in bundles:
        match = SKIN_BUNDLE.fullmatch(bundle["name"])
        if not match:
            continue
        skin = int(match.group("skin"))
        if skin == 25 or (requested and skin not in requested):
            continue
        bundle_path = AA1 / f"{bundle['hash']}.bundle"
        groups = extract_frames(bundle_path)
        preview = make_preview(skin, groups)
        preview.save(OUTPUT / f"Skin{skin:02d}.png")
        previews.append((skin, preview))
        records.append({
            "skin": skin,
            "idle": len(groups.get("Idle", [])),
            "run": len(groups.get("Run", [])),
            "attack": len(groups.get("Attack", [])),
            "hurt": len(groups.get("Hurt", [])),
            "other": len(groups.get("Other", [])),
        })
        print(records[-1])

    with (OUTPUT / "report.csv").open("w", newline="", encoding="utf-8-sig") as handle:
        writer = csv.DictWriter(handle, fieldnames=("skin", "idle", "run", "attack", "hurt", "other"))
        writer.writeheader()
        writer.writerows(records)
    make_gallery(previews).save(OUTPUT / "gallery.png")
    print(f"wrote {len(records)} skin previews to {OUTPUT}")


if __name__ == "__main__":
    main()
