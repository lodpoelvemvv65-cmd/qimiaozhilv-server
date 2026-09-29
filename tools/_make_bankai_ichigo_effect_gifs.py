"""Build fixed-character Bankai Ichigo idle and sword-flight effect GIFs."""

from __future__ import annotations

import math
import shutil
import sys
from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, ImageFilter


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
IDLE_SOURCE = ROOT / "_work" / "death16-probe" / "Death16_Idle_01.png"
FLIGHT_SOURCE = (
    ROOT / "assets" / "ichigo" / "animation"
    / "bankai-ichigo-sword-flight-pose-chroma-v2.png"
)
OUTPUT_DIR = ROOT / "_work" / "ichigo-complete-animation" / "v2"
FRAME_DIR = OUTPUT_DIR / "frames"
CANVAS_SIZE = (637, 500)
BASELINE = 462
IDLE_FRAME_COUNT = 24
FLIGHT_FRAME_COUNT = 16


def alpha_crop(image: Image.Image) -> Image.Image:
    rgba = image.convert("RGBA")
    box = rgba.getchannel("A").getbbox()
    if box is None:
        raise RuntimeError("image has no visible pixels")
    return rgba.crop(box)


def remove_green_background(image: Image.Image) -> Image.Image:
    rgba = image.convert("RGBA")
    pixels = rgba.load()
    for y in range(rgba.height):
        for x in range(rgba.width):
            red, green, blue, alpha = pixels[x, y]
            dominance = green - max(red, blue)
            if green > 110 and dominance >= 72:
                alpha = 0
            elif green > 100 and dominance > 8:
                alpha = round(alpha * (72 - dominance) / 64)
            if dominance > 0:
                green = min(green, max(red, blue))
            pixels[x, y] = red, green, blue, max(0, min(255, alpha))
    return rgba


def place_sprite(source: Image.Image, max_size: tuple[int, int], bottom: int) -> Image.Image:
    sprite = alpha_crop(source)
    scale = min(max_size[0] / sprite.width, max_size[1] / sprite.height)
    resized = sprite.resize(
        (max(1, round(sprite.width * scale)), max(1, round(sprite.height * scale))),
        Image.Resampling.LANCZOS,
    )
    canvas = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    canvas.alpha_composite(resized, ((CANVAS_SIZE[0] - resized.width) // 2, bottom - resized.height))
    return canvas


def idle_sprite() -> Image.Image:
    if not IDLE_SOURCE.is_file():
        raise FileNotFoundError(IDLE_SOURCE)
    return place_sprite(Image.open(IDLE_SOURCE).convert("RGBA"), (390, 246), BASELINE)


def flight_sprite() -> Image.Image:
    if not FLIGHT_SOURCE.is_file():
        raise FileNotFoundError(FLIGHT_SOURCE)
    keyed = remove_green_background(Image.open(FLIGHT_SOURCE))
    return place_sprite(keyed, (560, 330), 438)


def alpha_outline(sprite: Image.Image, color: tuple[int, int, int],
                  blur: int, opacity: float) -> Image.Image:
    mask = sprite.getchannel("A")
    outer = ImageChops.subtract(mask.filter(ImageFilter.GaussianBlur(blur)), mask)
    layer = Image.new("RGBA", CANVAS_SIZE, (*color, 0))
    layer.putalpha(outer.point(lambda value: round(value * opacity)))
    return layer


def tinted_copy(sprite: Image.Image, color: tuple[int, int, int], opacity: float) -> Image.Image:
    layer = Image.new("RGBA", CANVAS_SIZE, (*color, 0))
    layer.putalpha(sprite.getchannel("A").point(lambda value: round(value * opacity)))
    return layer


def energy_particles(frame_index: int, count: int, strength: float,
                     flight: bool = False) -> Image.Image:
    layer = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    draw = ImageDraw.Draw(layer)
    for particle in range(count):
        cycle = (particle * 41 + frame_index * (19 if flight else 13)) % 360
        y = 445 - cycle
        spread = 285 if flight else 210
        x = 318 + ((particle * 83 + frame_index * 29) % (spread * 2)) - spread
        if flight:
            x -= (frame_index * 31 + particle * 17) % 125
        size = 1 + particle % 4
        alpha = round((75 + particle % 5 * 25) * strength)
        color = (255, 58 + particle % 3 * 34, 28, alpha)
        draw.polygon(
            ((x, y - size * 2), (x + size, y), (x, y + size * 2), (x - size, y)),
            fill=color,
        )
    return layer


def ground_seal(frame_index: int, strength: float) -> Image.Image:
    layer = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    glow = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    draw = ImageDraw.Draw(layer)
    glow_draw = ImageDraw.Draw(glow)
    rotation = frame_index * 17
    ellipses = ((112, 414, 525, 482), (145, 425, 492, 477), (196, 438, 441, 472))
    for ring, box in enumerate(ellipses):
        start = (rotation * (1 if ring % 2 == 0 else -1) + ring * 53) % 360
        alpha = round((120 - ring * 24) * strength)
        color = (255, 36 + ring * 22, 20, alpha)
        draw.arc(box, start=start, end=start + 235, fill=color, width=3 - ring // 2)
        glow_draw.arc(box, start=start, end=start + 235, fill=(255, 24, 12, alpha), width=10)
    glow = glow.filter(ImageFilter.GaussianBlur(9))
    glow.alpha_composite(layer)
    return glow


def aura_flames(frame_index: int, strength: float) -> Image.Image:
    mask = Image.new("L", CANVAS_SIZE, 0)
    draw = ImageDraw.Draw(mask)
    for flame in range(11):
        center_x = 164 + flame * 31
        base_y = 453 - (flame % 3) * 5
        height = 70 + ((flame * 37 + frame_index * 23) % 155)
        sway = round(math.sin(frame_index * 0.52 + flame * 1.31) * 20)
        width = 14 + flame % 4 * 5
        draw.polygon(
            ((center_x - width, base_y), (center_x + width, base_y),
             (center_x + sway, base_y - height)),
            fill=round((55 + flame % 4 * 13) * strength),
        )
    mask = mask.filter(ImageFilter.GaussianBlur(14))
    layer = Image.new("RGBA", CANVAS_SIZE, (176, 0, 18, 0))
    layer.putalpha(mask)
    return layer


def lightning(frame_index: int, strength: float) -> Image.Image:
    if frame_index not in {7, 8, 9, 10, 11, 12}:
        return Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    sharp = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    draw = ImageDraw.Draw(sharp)
    for side in (-1, 1):
        points = []
        for step in range(7):
            y = 420 - step * 47
            x = 318 + side * (88 + step * 19) + ((frame_index + step * 3) % 5 - 2) * 9
            points.append((x, y))
        alpha = round(235 * strength)
        draw.line(points, fill=(255, 228, 205, alpha), width=3, joint="curve")
    glow = sharp.filter(ImageFilter.GaussianBlur(10))
    red = Image.new("RGBA", CANVAS_SIZE, (255, 18, 8, 0))
    red.putalpha(glow.getchannel("A").point(lambda value: round(value * 0.85)))
    red.alpha_composite(sharp)
    return red


def idle_frame(sprite: Image.Image, index: int) -> Image.Image:
    phase = 2 * math.pi * index / IDLE_FRAME_COUNT
    surge = math.sin(math.pi * max(0.0, min(1.0, (index - 5) / 10))) ** 2
    strength = 0.62 + 0.25 * (0.5 + 0.5 * math.sin(phase)) + 0.55 * surge
    frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    frame.alpha_composite(aura_flames(index, strength))
    frame.alpha_composite(alpha_outline(sprite, (104, 0, 12), 28, 0.70 * strength))
    frame.alpha_composite(alpha_outline(sprite, (255, 38, 18), 12, 0.72 * strength))
    frame.alpha_composite(ground_seal(index, min(1.0, strength)))
    frame.alpha_composite(lightning(index, min(1.0, surge + 0.25)))
    frame.alpha_composite(energy_particles(index, 34, min(1.0, strength)))
    frame.alpha_composite(sprite)
    return frame


def speed_lines(frame_index: int, strength: float) -> Image.Image:
    layer = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    draw = ImageDraw.Draw(layer)
    for line_index in range(17):
        y = 105 + (line_index * 29 + frame_index * 11) % 325
        x = 14 + (line_index * 71 - frame_index * 47) % 480
        length = 48 + line_index % 6 * 27
        alpha = round((52 + line_index % 5 * 18) * strength)
        draw.line((x, y, min(625, x + length), y - 4), fill=(255, 72, 38, alpha), width=2)
    blur = layer.filter(ImageFilter.GaussianBlur(5))
    blur.alpha_composite(layer)
    return blur


def flight_wake(frame_index: int, strength: float) -> Image.Image:
    layer = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    draw = ImageDraw.Draw(layer)
    phase = 2 * math.pi * frame_index / FLIGHT_FRAME_COUNT
    for wake_index in range(7):
        y = 326 + wake_index * 14 + round(math.sin(phase + wake_index) * 9)
        start_x = 265 - wake_index * 9
        end_x = 18 + wake_index * 8
        control = 155 + round(math.sin(phase * 1.7 + wake_index) * 38)
        points = []
        for step in range(13):
            t = step / 12
            x = round(start_x * (1 - t) + end_x * t)
            curve = math.sin(t * math.pi) * (control - 130)
            points.append((x, round(y - curve)))
        alpha = round((70 + wake_index * 15) * strength)
        draw.line(points, fill=(125 + wake_index * 16, 4, 18, alpha), width=4 + wake_index % 3)
    glow = layer.filter(ImageFilter.GaussianBlur(13))
    glow.alpha_composite(layer)
    return glow


def flight_frame(sprite: Image.Image, index: int) -> Image.Image:
    phase = 2 * math.pi * index / FLIGHT_FRAME_COUNT
    strength = 0.78 + 0.22 * (0.5 + 0.5 * math.sin(phase))
    frame = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    frame.alpha_composite(speed_lines(index, strength))
    frame.alpha_composite(flight_wake(index, strength))
    for offset, opacity in ((-84, 0.08), (-55, 0.12), (-30, 0.16)):
        ghost = tinted_copy(sprite, (155, 0, 20), opacity * strength)
        frame.alpha_composite(ghost, (offset, 0))
    frame.alpha_composite(alpha_outline(sprite, (115, 0, 18), 26, 0.72 * strength))
    frame.alpha_composite(alpha_outline(sprite, (255, 44, 22), 10, 0.82 * strength))
    sword_glow = Image.new("RGBA", CANVAS_SIZE, (0, 0, 0, 0))
    sword_draw = ImageDraw.Draw(sword_glow)
    sword_draw.line((78, 412, 584, 412), fill=(255, 28, 14, round(150 * strength)), width=14)
    frame.alpha_composite(sword_glow.filter(ImageFilter.GaussianBlur(14)))
    frame.alpha_composite(energy_particles(index, 42, strength, flight=True))
    frame.alpha_composite(sprite)
    return frame


def render_preview(frame: Image.Image) -> Image.Image:
    canvas = Image.new("RGBA", CANVAS_SIZE, (24, 24, 28, 255))
    canvas.alpha_composite(frame)
    return canvas.convert("RGB")


def save_gif(path: Path, frames: list[Image.Image], duration: int) -> None:
    previews = [render_preview(frame) for frame in frames]
    previews[0].save(
        path,
        save_all=True,
        append_images=previews[1:],
        duration=[duration] * len(previews),
        loop=0,
        disposal=2,
        optimize=False,
    )


def save_contact(path: Path, frames: list[Image.Image]) -> None:
    indices = [round(index * (len(frames) - 1) / 7) for index in range(8)]
    sheet = Image.new("RGB", (1274, 500), (24, 24, 28))
    for position, index in enumerate(indices):
        tile = render_preview(frames[index]).resize((318, 250), Image.Resampling.LANCZOS)
        sheet.paste(tile, ((position % 4) * 318, (position // 4) * 250))
    sheet.save(path)


def verify_fixed_sprite(frames: list[Image.Image], sprite: Image.Image, label: str) -> None:
    opaque = [
        (x, y) for y in range(sprite.height) for x in range(sprite.width)
        if sprite.getpixel((x, y))[3] == 255
    ]
    if not opaque:
        raise RuntimeError(f"{label} sprite has no opaque pixels")
    for frame_index, frame in enumerate(frames, 1):
        if frame.size != CANVAS_SIZE:
            raise RuntimeError(f"{label} frame {frame_index} has the wrong size")
        if any(frame.getpixel(position) != sprite.getpixel(position) for position in opaque):
            raise RuntimeError(f"{label} body changed in frame {frame_index}")
    print(f"verified {label}: fixed body across {len(frames)} frames")


def write_frames(action: str, frames: list[Image.Image]) -> None:
    directory = FRAME_DIR / action.lower()
    directory.mkdir(parents=True, exist_ok=True)
    for index, frame in enumerate(frames, 1):
        frame.save(directory / f"SkinBankaiIchigo_{action}_{index:02d}.png")


def main() -> None:
    if OUTPUT_DIR.exists():
        shutil.rmtree(OUTPUT_DIR)
    OUTPUT_DIR.mkdir(parents=True)
    idle_base = idle_sprite()
    flight_base = flight_sprite()
    idle = [idle_frame(idle_base, index) for index in range(IDLE_FRAME_COUNT)]
    flight = [flight_frame(flight_base, index) for index in range(FLIGHT_FRAME_COUNT)]
    verify_fixed_sprite(idle, idle_base, "idle")
    verify_fixed_sprite(flight, flight_base, "sword flight")
    write_frames("Idle", idle)
    write_frames("Run", flight)
    save_gif(OUTPUT_DIR / "SkinBankaiIchigo-Idle-v2.gif", idle, 125)
    save_gif(OUTPUT_DIR / "SkinBankaiIchigo-Run-SwordFlight-v2.gif", flight, 60)
    save_contact(OUTPUT_DIR / "SkinBankaiIchigo-Idle-v2-contact.png", idle)
    save_contact(OUTPUT_DIR / "SkinBankaiIchigo-Run-SwordFlight-v2-contact.png", flight)
    print("idle", OUTPUT_DIR / "SkinBankaiIchigo-Idle-v2.gif")
    print("run", OUTPUT_DIR / "SkinBankaiIchigo-Run-SwordFlight-v2.gif")


if __name__ == "__main__":
    main()
