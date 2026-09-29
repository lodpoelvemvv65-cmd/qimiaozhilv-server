"""Extract the online character-background thumbnails from the Bag FGUI atlas."""

from __future__ import annotations

import struct
from dataclasses import dataclass
from pathlib import Path

import UnityPy
from PIL import Image, ImageDraw, ImageFont


ROOT = Path(__file__).resolve().parent.parent
DECODED = ROOT / "client-test" / "梦幻奇遇记_Data" / "StreamingAssets" / "yoo" / "_dec" / "aa0"
BAG_BUNDLE = DECODED / "f786a0cc8dc91971338161393d2a12f8.bundle"
CHARACTER_BUNDLE = DECODED / "2373febfb6a1aa7dca6d8c6666302a93.bundle"
OUTPUT = ROOT / "assets" / "character-backgrounds" / "reference"
ICON_NAMES = tuple(str(value) for value in range(120657, 120677))
EQUIP_NAMES = (
    "小星星", "满天繁星", "热舞气球", "星光闪闪", "霓虹灯",
    "星夜传说", "蒲公英", "四色光", "午后时光", "北极光",
    "滴血玫瑰", "光斓", "蝴蝶花", "枫叶飘舞", "河桥",
    "节奏跳动", "橘色旋律", "雨夜", "星系", "樱",
)


@dataclass(frozen=True)
class PackageItem:
    item_type: int
    item_id: str | None
    name: str | None
    width: int
    height: int
    object_type: int | None
    raw_data: bytes | None


@dataclass(frozen=True)
class AtlasSprite:
    item_id: str | None
    atlas_id: str | None
    rect: tuple[int, int, int, int]
    rotated: bool


class Buffer:
    def __init__(self, data: bytes, offset: int = 0, length: int | None = None):
        self.data = data
        self.offset = offset
        self.pos = 0
        self.length = len(data) - offset if length is None else length
        self.strings: list[str] = []

    def byte(self) -> int:
        value = self.data[self.offset + self.pos]
        self.pos += 1
        return value

    def boolean(self) -> bool:
        return self.byte() == 1

    def ushort(self) -> int:
        value = struct.unpack_from(">H", self.data, self.offset + self.pos)[0]
        self.pos += 2
        return value

    def short(self) -> int:
        value = struct.unpack_from(">h", self.data, self.offset + self.pos)[0]
        self.pos += 2
        return value

    def uint(self) -> int:
        value = struct.unpack_from(">I", self.data, self.offset + self.pos)[0]
        self.pos += 4
        return value

    def integer(self) -> int:
        value = struct.unpack_from(">i", self.data, self.offset + self.pos)[0]
        self.pos += 4
        return value

    def float(self) -> float:
        value = struct.unpack_from(">f", self.data, self.offset + self.pos)[0]
        self.pos += 4
        return value

    def read_buffer(self) -> bytes:
        length = self.integer()
        start = self.offset + self.pos
        value = self.data[start:start + length]
        self.pos += length
        return value

    def string(self, length: int | None = None) -> str:
        if length is None:
            length = self.ushort()
        value = self.data[self.offset + self.pos:self.offset + self.pos + length].decode("utf-8")
        self.pos += length
        return value

    def string_ref(self) -> str | None:
        index = self.ushort()
        if index == 65534:
            return None
        if index == 65533:
            return ""
        return self.strings[index]

    def seek(self, index_pos: int, block: int) -> bool:
        old = self.pos
        self.pos = index_pos
        count = self.byte()
        if block >= count:
            self.pos = old
            return False
        use_short = self.byte() == 1
        self.pos += (2 if use_short else 4) * block
        relative = self.short() if use_short else self.integer()
        if relative <= 0:
            self.pos = old
            return False
        self.pos = index_pos + relative
        return True


def parse_package(raw: bytes) -> tuple[str, str, list[str], list[PackageItem], dict[str, AtlasSprite]]:
    buffer = Buffer(raw)
    if buffer.uint() != 0x46475549:
        raise RuntimeError("not a FairyGUI package")
    version = buffer.integer()
    if buffer.boolean():
        raise RuntimeError("compressed FairyGUI packages are not supported")
    package_id = buffer.string()
    package_name = buffer.string()
    buffer.pos += 20
    index_pos = buffer.pos

    if not buffer.seek(index_pos, 4):
        raise RuntimeError("FairyGUI string table is missing")
    buffer.strings = [buffer.string() for _ in range(buffer.integer())]
    if buffer.seek(index_pos, 5):
        for _ in range(buffer.integer()):
            index = buffer.ushort()
            buffer.strings[index] = buffer.string(buffer.integer())

    if not buffer.seek(index_pos, 1):
        raise RuntimeError("FairyGUI item table is missing")
    items: list[PackageItem] = []
    for _ in range(buffer.short()):
        next_pos = buffer.integer() + buffer.pos
        item_type = buffer.byte()
        item_id = buffer.string_ref()
        name = buffer.string_ref()
        buffer.string_ref()  # source path
        buffer.string_ref()  # source file
        buffer.boolean()
        width = buffer.integer()
        height = buffer.integer()
        object_type = None
        raw_data = None
        if item_type == 0:  # Image
            object_type = 0
        elif item_type == 1:  # MovieClip
            buffer.boolean()
            object_type = 1
            raw_data = buffer.read_buffer()
        elif item_type == 3:  # Component
            extension = buffer.byte()
            object_type = extension if extension > 0 else 9
            raw_data = buffer.read_buffer()
        items.append(PackageItem(item_type, item_id, name, width, height, object_type, raw_data))
        buffer.pos = next_pos

    item_ids = {item.item_id for item in items}
    if not buffer.seek(index_pos, 2):
        raise RuntimeError("FairyGUI sprite table is missing")
    sprites: dict[str, AtlasSprite] = {}
    for _ in range(buffer.short()):
        next_pos = buffer.ushort() + buffer.pos
        item_id = buffer.string_ref()
        atlas_id = buffer.string_ref()
        if atlas_id not in item_ids:
            raise RuntimeError(f"sprite references unknown atlas {atlas_id}")
        rect = tuple(buffer.integer() for _ in range(4))
        rotated = buffer.boolean()
        if version >= 2 and buffer.boolean():
            for _ in range(4):
                buffer.integer()
        if item_id is not None:
            sprites[item_id] = AtlasSprite(item_id, atlas_id, rect, rotated)
        buffer.pos = next_pos
    return package_id, package_name, buffer.strings, items, sprites


OBJECT_TYPES = (
    "Image", "MovieClip", "Swf", "Graph", "Loader", "Group", "Text",
    "RichText", "InputText", "Component", "List", "Label", "Button",
    "ComboBox", "ProgressBar", "Slider", "ScrollBar", "Tree", "Loader3D",
)


def parse_component_children(item: PackageItem, strings: list[str], items: list[PackageItem]) -> list[dict]:
    if item.raw_data is None:
        raise RuntimeError(f"{item.name} has no component raw data")
    buffer = Buffer(item.raw_data)
    buffer.strings = strings
    item_by_id = {candidate.item_id: candidate for candidate in items}
    if not buffer.seek(0, 2):
        raise RuntimeError(f"{item.name} has no child block")

    children = []
    for index in range(buffer.short()):
        data_len = buffer.ushort()
        child_pos = buffer.pos
        if not buffer.seek(child_pos, 0):
            raise RuntimeError(f"{item.name} child {index} has no base block")
        declared_type = buffer.byte()
        source_id = buffer.string_ref()
        package_id = buffer.string_ref()
        source_item = item_by_id.get(source_id) if package_id is None else None

        # GObject.Setup_BeforeAdd, block 0.
        child_id = buffer.string_ref()
        child_name = buffer.string_ref()
        x = buffer.integer()
        y = buffer.integer()
        has_size = buffer.boolean()
        width = buffer.integer() if has_size else (source_item.width if source_item else None)
        height = buffer.integer() if has_size else (source_item.height if source_item else None)
        if buffer.boolean():
            for _ in range(4):
                buffer.integer()
        if buffer.boolean():
            buffer.float()
            buffer.float()
        if buffer.boolean():
            buffer.float()
            buffer.float()
        if buffer.boolean():
            buffer.float()
            buffer.float()
            buffer.boolean()
        alpha = buffer.float()
        rotation = buffer.float()
        visible = buffer.boolean()
        touchable = buffer.boolean()
        grayed = buffer.boolean()
        blend_mode = buffer.byte()
        filter_type = buffer.byte()
        if filter_type == 1:
            for _ in range(4):
                buffer.float()
        custom_data = buffer.string_ref()

        group_id = -1
        if buffer.seek(child_pos, 1):
            buffer.string_ref()  # tooltips
            group_id = buffer.short()

        loader_url = None
        if declared_type == 4 and buffer.seek(child_pos, 5):
            loader_url = buffer.string_ref()

        resolved_type = source_item.object_type if source_item and source_item.object_type is not None else declared_type
        children.append({
            "index": index,
            "declared_type": OBJECT_TYPES[declared_type],
            "resolved_type": OBJECT_TYPES[resolved_type],
            "source_id": source_id,
            "source_name": source_item.name if source_item else None,
            "package_id": package_id,
            "id": child_id,
            "name": child_name,
            "x": x,
            "y": y,
            "width": width,
            "height": height,
            "alpha": alpha,
            "rotation": rotation,
            "visible": visible,
            "touchable": touchable,
            "grayed": grayed,
            "blend_mode": blend_mode,
            "custom_data": custom_data,
            "group_id": group_id,
            "loader_url": loader_url,
        })
        buffer.pos = child_pos + data_len
    return children


def load_bundle(path: Path, text_name: str, texture_name: str | None = None):
    environment = UnityPy.load(str(path))
    raw = None
    texture = None
    for obj in environment.objects:
        data = obj.read()
        if obj.type.name == "TextAsset" and data.m_Name == text_name:
            script = data.m_Script
            raw = script.encode("utf-8", "surrogateescape") if isinstance(script, str) else bytes(script)
        elif texture_name and obj.type.name == "Texture2D" and data.m_Name == texture_name:
            texture = data.image.convert("RGBA")
    if raw is None:
        raise RuntimeError(f"{text_name} not found in {path.name}")
    if texture_name and texture is None:
        raise RuntimeError(f"{texture_name} not found in {path.name}")
    return raw, texture


def font(size: int):
    candidates = (
        Path("C:/Windows/Fonts/msyh.ttc"),
        Path("C:/Windows/Fonts/simhei.ttf"),
        Path("C:/Windows/Fonts/arial.ttf"),
    )
    for path in candidates:
        if path.is_file():
            return ImageFont.truetype(str(path), size)
    return ImageFont.load_default()


def main() -> None:
    OUTPUT.mkdir(parents=True, exist_ok=True)
    bag_raw, atlas = load_bundle(BAG_BUNDLE, "Bag_fui", "Bag_atlas0")
    _, _, _, bag_items, sprites = parse_package(bag_raw)
    item_by_name = {item.name: item for item in bag_items}
    missing = [name for name in ICON_NAMES if name not in item_by_name]
    if missing:
        raise RuntimeError(f"missing background icons: {missing}")

    title_font = font(18)
    label_font = font(15)
    sheet = Image.new("RGB", (5 * 180, 4 * 190 + 42), (22, 24, 30))
    draw = ImageDraw.Draw(sheet)
    draw.text((14, 9), "人物背景线上缩略图 120657-120676", fill="white", font=title_font)
    for index, (icon_name, equip_name) in enumerate(zip(ICON_NAMES, EQUIP_NAMES)):
        item = item_by_name[icon_name]
        sprite = sprites.get(item.item_id or "")
        if sprite is None:
            raise RuntimeError(f"{icon_name} has no atlas sprite")
        x, y, width, height = sprite.rect
        icon = atlas.crop((x, y, x + width, y + height))
        if sprite.rotated:
            icon = icon.transpose(Image.Transpose.ROTATE_90)
        output = OUTPUT / f"{int(icon_name) + 233}_{icon_name}_{equip_name}.png"
        icon.save(output)

        col, row = index % 5, index // 5
        left, top = col * 180, row * 190 + 42
        preview = icon.resize((144, 144), Image.Resampling.NEAREST)
        sheet.paste(preview.convert("RGB"), (left + 18, top), preview.getchannel("A"))
        label = f"{int(icon_name) + 233}  {equip_name}"
        bbox = draw.textbbox((0, 0), label, font=label_font)
        draw.text((left + (180 - bbox[2]) / 2, top + 151), label, fill=(236, 238, 244), font=label_font)
    sheet.save(OUTPUT / "contact-sheet.png")

    character_raw, character_atlas = load_bundle(CHARACTER_BUNDLE, "Character_fui", "Character_atlas0")
    package_id, package_name, character_strings, character_items, character_sprites = parse_package(character_raw)
    dimensions = {
        item.name: (item.width, item.height, item.item_type)
        for item in character_items if item.name in {"CharacterUI", "MovieBGTest"}
    }
    print(f"Bag atlas: {atlas.width}x{atlas.height}")
    print(f"Character package: {package_name} ({package_id})")
    print(f"Character dimensions: {dimensions}")
    character_ui = next(item for item in character_items if item.name == "CharacterUI")
    role_frame = next(item for item in character_items if item.name == "Character_rolege")
    role_frame_sprite = character_sprites[role_frame.item_id or ""]
    x, y, width, height = role_frame_sprite.rect
    role_frame_image = character_atlas.crop((x, y, x + width, y + height))
    if role_frame_sprite.rotated:
        role_frame_image = role_frame_image.transpose(Image.Transpose.ROTATE_90)
    role_frame_image.save(OUTPUT / "Character_rolege.png")
    print("CharacterUI children (back to front):")
    for child in parse_component_children(character_ui, character_strings, character_items):
        source = child["source_name"] or child["source_id"] or "-"
        print(
            f"  [{child['index']:02}] {child['resolved_type']:<10} "
            f"name={child['name']!r:<24} source={source!r:<20} "
            f"xy=({child['x']},{child['y']}) size=({child['width']},{child['height']}) "
            f"group={child['group_id']} visible={child['visible']} "
            f"alpha={child['alpha']:.2f} url={child['loader_url']!r}"
        )
    print(f"Extracted {len(ICON_NAMES)} references to {OUTPUT}")


if __name__ == "__main__":
    main()
