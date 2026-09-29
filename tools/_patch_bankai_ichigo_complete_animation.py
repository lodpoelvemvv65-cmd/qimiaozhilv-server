"""Install the complete V6 Bankai Ichigo animation in client-test only."""

from __future__ import annotations

import argparse
import hashlib
import math
import os
import shutil
import struct
import subprocess
import sys
from copy import deepcopy
from pathlib import Path

import UnityPy
from Crypto.Cipher import AES
from Crypto.Util.Padding import unpad
from PIL import Image, ImageChops, ImageDraw

import _make_bankai_ichigo_combined_gif as v6
from _patch_blue_mech_skin import (
    AES_IV,
    AES_KEY,
    encrypted_metadata,
    read_manifest,
    write_manifest,
)


sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = Path(__file__).resolve().parent.parent
CLIENT = ROOT / "client-test"
PUBLIC_CLIENT = ROOT / "client-127.0.0.1"
CLIENT_DATA = next(path for path in CLIENT.iterdir() if path.is_dir() and path.name.endswith("_Data"))
AA1 = CLIENT_DATA / "StreamingAssets" / "yoo" / "aa1"
MANIFEST = AA1 / "PackageManifest_aa1_2025-01-05-1027.bytes"
MANIFEST_HASH = MANIFEST.with_suffix(".hash")
TEXTURE_BUNDLE_NAME = (
    "assets_download_texture_animationresource_role_skin_skin_bankai_ichigo.bundle"
)
ANIMATOR_BUNDLE_NAME = "assets_download_animators_skin_skin_bankai_ichigo.bundle"
MODELVIEW = CLIENT_DATA / "Managed" / "Unity.ModelView.dll"
PATCHER_DIR = ROOT / "tools" / "client-bankai-ichigo-animation-patcher"
PATCHER_PROJECT = PATCHER_DIR / "ClientBankaiIchigoAnimationPatcher.csproj"
PATCHER_SOURCE = PATCHER_DIR / "Program.cs"
PATCHER = (
    PATCHER_DIR / "bin" / "Release" / "net8.0"
    / "ClientBankaiIchigoAnimationPatcher.exe"
)
BUILD_DIR = ROOT / "_work" / "bankai-ichigo-complete-animation"
BACKUP_SUFFIX = ".pre-bankai-ichigo-complete-animation.bak"
TEXTURE_SIZE = (637, 500)
IDLE_COUNT = 52
RUN_COUNT = 8
ATTACK_COUNT = 8
RUN_PERIOD_SECONDS = 1.0
ATTACK_PERIOD_SECONDS = 0.8
EXPECTED_NAMES = {
    *(f"SkinBankaiIchigo_Idle_{index:02d}" for index in range(1, IDLE_COUNT + 1)),
    *(f"SkinBankaiIchigo_Run_{index:02d}" for index in range(1, RUN_COUNT + 1)),
    *(f"SkinBankaiIchigo_Attack_{index:02d}" for index in range(1, ATTACK_COUNT + 1)),
    "SkinBankaiIchigo_Hurt_01",
}


def decrypt_bundle(path: Path) -> bytes:
    encrypted = path.read_bytes()
    if path.suffix == ".bundle" and hashlib.md5(encrypted).hexdigest() != path.stem:
        raise RuntimeError(f"bundle hash mismatch: {path.name}")
    return unpad(AES.new(AES_KEY, AES.MODE_CBC, AES_IV).decrypt(encrypted), AES.block_size)


def bundle_parts(raw: bytes):
    environment = UnityPy.load(raw)
    bundle = next(iter(environment.files.values()))
    return environment, bundle


def manifest_record(raw: bytes, bundle_name: str) -> tuple[int, dict]:
    _, _, bundles = read_manifest(raw)
    matches = [
        (index, record)
        for index, record in enumerate(bundles)
        if record["name"] == bundle_name
    ]
    if len(matches) != 1:
        raise RuntimeError(f"expected one {bundle_name} bundle, found {len(matches)}")
    return matches[0]


def source_manifest_raw() -> bytes:
    backup = Path(str(MANIFEST) + BACKUP_SUFFIX)
    return backup.read_bytes() if backup.is_file() else MANIFEST.read_bytes()


def source_bundle_path(bundle_name: str) -> Path:
    _, record = manifest_record(source_manifest_raw(), bundle_name)
    path = AA1 / f"{record['hash']}.bundle"
    if path.is_file():
        return path
    backup = Path(str(path) + BACKUP_SUFFIX)
    if backup.is_file():
        return backup
    raise FileNotFoundError(f"source Bankai Ichigo bundle is missing: {path.name}")


def image_contract(raw: bytes) -> tuple[dict[str, Image.Image], dict[int, bytes]]:
    environment = UnityPy.load(raw)
    images = {}
    sprites = {}
    sprite_names = set()
    for obj in environment.objects:
        if obj.type.name == "Texture2D":
            data = obj.read()
            images[data.m_Name] = data.image.convert("RGBA")
        elif obj.type.name == "Sprite":
            data = obj.read()
            sprite_names.add(data.m_Name)
            sprites[obj.path_id] = obj.get_raw_data()
    if set(images) != EXPECTED_NAMES or sprite_names != EXPECTED_NAMES:
        raise RuntimeError(
            f"Bankai Ichigo contract mismatch: {len(images)} textures / {len(sprites)} sprites"
        )
    if len(sprites) != 69 or any(image.size != TEXTURE_SIZE for image in images.values()):
        raise RuntimeError("Bankai Ichigo does not use 69 full-size 637x500 frames")
    return images, sprites


def build_v6_timeline(images: dict[str, Image.Image]) -> list[Image.Image]:
    frames, durations, stages = v6.build_timeline(
        images,
        v6.load_flight_sprite(),
        v6.load_manji_vfx(),
        v6.load_vfx(v6.ATTACK_VFX_SOURCE),
        v6.load_vfx(v6.FLIGHT_WAKE_SOURCE),
        v6.load_vfx(v6.AMBIENT_VFX_SOURCE),
        v6.load_sword_sprite(),
    )
    v6.verify(frames, durations, stages)
    return frames


def replacement_frames(original: dict[str, Image.Image]) -> dict[str, Image.Image]:
    timeline = build_v6_timeline(original)

    calm_before = [timeline[index % 8] for index in range(15)]
    draw_and_release = list(timeline[8:19])
    sheath_and_recover = list(timeline[17:7:-1])
    calm_after = [timeline[index % 8] for index in range(16)]
    idle = calm_before + draw_and_release + sheath_and_recover + calm_after

    flight_character = v6.remove_flight_sword(v6.load_flight_sprite())
    sword = v6.load_sword_sprite()
    wake = v6.load_vfx(v6.FLIGHT_WAKE_SOURCE)
    run = [
        v6.flight_frame(flight_character, sword, wake, index + 4, RUN_COUNT)
        for index in range(RUN_COUNT)
    ]

    attack = [timeline[index] for index in (70, 72, 73, 74, 75, 77, 79, 81)]
    if len(idle) != IDLE_COUNT or len(run) != RUN_COUNT or len(attack) != ATTACK_COUNT:
        raise RuntimeError("V6-to-client animation mapping has the wrong frame count")

    replacements = {
        **{
            f"SkinBankaiIchigo_Idle_{index:02d}": frame.copy()
            for index, frame in enumerate(idle, 1)
        },
        **{
            f"SkinBankaiIchigo_Run_{index:02d}": frame.copy()
            for index, frame in enumerate(run, 1)
        },
        **{
            f"SkinBankaiIchigo_Attack_{index:02d}": frame.copy()
            for index, frame in enumerate(attack, 1)
        },
        "SkinBankaiIchigo_Hurt_01": original["SkinBankaiIchigo_Hurt_01"].copy(),
    }
    if set(replacements) != EXPECTED_NAMES:
        raise RuntimeError("mapped Bankai Ichigo frames do not satisfy the 69-frame contract")
    return replacements


def images_equal(left: Image.Image, right: Image.Image) -> bool:
    return left.mode == right.mode and left.size == right.size and ImageChops.difference(left, right).getbbox() is None


def verify_images(
    actual: dict[str, Image.Image],
    expected: dict[str, Image.Image],
    original: dict[str, Image.Image],
) -> None:
    if set(actual) != EXPECTED_NAMES:
        raise RuntimeError(f"patched Bankai Ichigo image count mismatch: {len(actual)}")
    for name, image in actual.items():
        if image.size != TEXTURE_SIZE or image.getchannel("A").getbbox() is None:
            raise RuntimeError(f"invalid Bankai Ichigo frame: {name}")
        if not images_equal(image, expected[name]):
            raise RuntimeError(f"Bankai Ichigo frame changed during bundle serialization: {name}")
    hurt_name = "SkinBankaiIchigo_Hurt_01"
    if not images_equal(actual[hurt_name], original[hurt_name]):
        raise RuntimeError("ordinary Bankai Ichigo Hurt frame was unexpectedly changed")
    run_band_counts = []
    for index in range(1, RUN_COUNT + 1):
        run_frame = actual[f"SkinBankaiIchigo_Run_{index:02d}"]
        bounds = run_frame.getchannel("A").getbbox()
        band_count = sum(
            run_frame.getpixel((x, y))[3] > 80
            for y in range(400, 431)
            for x in range(100, 570)
        )
        run_band_counts.append(band_count)
        if bounds is None or bounds[2] - bounds[0] < 560 or band_count < 8000:
            raise RuntimeError(
                f"Bankai Ichigo Run_{index:02d} is not continuously mounted on the sword"
            )
    for action, count in (("Idle", IDLE_COUNT), ("Run", RUN_COUNT), ("Attack", ATTACK_COUNT)):
        changed = sum(
            not images_equal(
                actual[f"SkinBankaiIchigo_{action}_{index:02d}"],
                original[f"SkinBankaiIchigo_{action}_{index:02d}"],
            )
            for index in range(1, count + 1)
        )
        required = count - 1 if action == "Attack" else count
        if changed < required:
            raise RuntimeError(f"only {changed}/{count} Bankai Ichigo {action} frames changed")
    print("verified Bankai Ichigo: 52 Idle / 8 Run / 8 Attack / 1 unchanged Hurt")
    print("verified all eight Bankai Ichigo Run frames remain mounted on the sword")


def gif_durations(total_ms: int, count: int) -> list[int]:
    base = total_ms // count // 10 * 10
    extra_steps = max(0, (total_ms - base * count) // 10)
    return [
        base + (10 if (index + 1) * extra_steps // count != index * extra_steps // count else 0)
        for index in range(count)
    ]


def write_previews(images: dict[str, Image.Image]) -> None:
    idle = [images[f"SkinBankaiIchigo_Idle_{index:02d}"] for index in range(1, 53)]
    run = [images[f"SkinBankaiIchigo_Run_{index:02d}"] for index in range(1, 9)]
    attack = [images[f"SkinBankaiIchigo_Attack_{index:02d}"] for index in range(1, 9)]
    hurt = images["SkinBankaiIchigo_Hurt_01"]
    sequence = idle + run + run[4:] * 2 + attack + [hurt, hurt]
    durations = (
        gif_durations(4330, len(idle))
        + [125] * len(run)
        + [125] * 8
        + [100] * len(attack)
        + [180, 260]
    )
    preview = [v6.render_preview(frame) for frame in sequence]
    gif = BUILD_DIR / "SkinBankaiIchigo-client-complete.gif"
    preview[0].save(
        gif,
        save_all=True,
        append_images=preview[1:],
        duration=durations,
        loop=0,
        disposal=2,
        optimize=False,
    )

    all_frames = [
        *(name for name in sorted(EXPECTED_NAMES) if "_Idle_" in name),
        *(name for name in sorted(EXPECTED_NAMES) if "_Run_" in name),
        *(name for name in sorted(EXPECTED_NAMES) if "_Attack_" in name),
        "SkinBankaiIchigo_Hurt_01",
    ]
    columns, tile_width, tile_height, label_height = 6, 212, 167, 19
    rows = math.ceil(len(all_frames) / columns)
    sheet = Image.new("RGB", (columns * tile_width, rows * (tile_height + label_height)), (24, 24, 28))
    draw = ImageDraw.Draw(sheet)
    for position, name in enumerate(all_frames):
        x = position % columns * tile_width
        y = position // columns * (tile_height + label_height)
        tile = v6.render_preview(images[name]).resize((tile_width, tile_height), Image.Resampling.LANCZOS)
        sheet.paste(tile, (x, y + label_height))
        draw.text((x + 4, y + 3), name.replace("SkinBankaiIchigo_", ""), fill="white")
    sheet.save(BUILD_DIR / "SkinBankaiIchigo-client-contact.png")


def patch_texture_bundle(source_path: Path) -> tuple[Path, dict[str, Image.Image], dict[int, bytes], dict[str, Image.Image]]:
    source_raw = decrypt_bundle(source_path)
    original, source_sprites = image_contract(source_raw)
    replacements = replacement_frames(original)
    environment, bundle = bundle_parts(source_raw)
    for obj in environment.objects:
        if obj.type.name != "Texture2D":
            continue
        data = obj.read()
        data.image = replacements[data.m_Name]
        data.save()

    path = BUILD_DIR / TEXTURE_BUNDLE_NAME
    path.write_bytes(bundle.save(packer="lz4"))
    patched, patched_sprites = image_contract(path.read_bytes())
    if patched_sprites != source_sprites:
        raise RuntimeError("Bankai Ichigo Sprite PathIDs or Full Rect meshes changed")
    verify_images(patched, replacements, original)
    write_previews(patched)
    return path, original, source_sprites, replacements


def retime_clip(data, expected_stop: float, target_stop: float, frame_count: int) -> None:
    original_stop = float(data.m_MuscleClip.m_StopTime)
    if abs(original_stop - expected_stop) > 0.001:
        raise RuntimeError(f"unexpected {data.m_Name} duration: {original_stop}")
    stream = data.m_MuscleClip.m_Clip.data.m_StreamedClip
    values = list(stream.data)
    if (
        len(values) != frame_count * 7 + 2
        or values[0] != 0xFF7FFFFF
        or values[-2:] != [0x7F800000, 0]
    ):
        raise RuntimeError(f"unexpected {data.m_Name} streamed curve layout")
    scale = target_stop / original_stop
    for position in range(7, len(values) - 2, 7):
        seconds = struct.unpack("<f", struct.pack("<I", values[position]))[0]
        values[position] = struct.unpack("<I", struct.pack("<f", seconds * scale))[0]
    stream.data = values
    sample_rate = frame_count / target_stop
    data.m_SampleRate = sample_rate
    data.m_MuscleClip.m_Clip.data.m_DenseClip.m_SampleRate = sample_rate
    data.m_MuscleClip.m_StopTime = target_stop


def patch_animator_bundle(source_path: Path) -> Path:
    environment, bundle = bundle_parts(decrypt_bundle(source_path))
    found = set()
    for obj in environment.objects:
        if obj.type.name != "AnimationClip":
            continue
        data = obj.read()
        if data.m_Name == "SkinBankaiIchigo_Run":
            retime_clip(data, 2 / 3, RUN_PERIOD_SECONDS, RUN_COUNT)
            data.m_MuscleClip.m_LoopTime = True
            data.m_WrapMode = 2  # UnityEngine.WrapMode.Loop
            data.save()
            found.add("Run")
        elif data.m_Name == "SkinBankaiIchigo_Attack":
            retime_clip(data, 2 / 3, ATTACK_PERIOD_SECONDS, ATTACK_COUNT)
            data.m_MuscleClip.m_LoopTime = False
            data.m_WrapMode = 0
            data.save()
            found.add("Attack")
    if found != {"Run", "Attack"}:
        raise RuntimeError(f"Bankai Ichigo Animator patch was incomplete: {found}")
    path = BUILD_DIR / ANIMATOR_BUNDLE_NAME
    path.write_bytes(bundle.save(packer="lz4"))
    verify_animator(path.read_bytes())
    return path


def animator_contract(raw: bytes) -> dict[str, tuple[bool, int, float, float]]:
    environment = UnityPy.load(raw)
    return {
        data.m_Name: (
            bool(data.m_MuscleClip.m_LoopTime),
            int(data.m_WrapMode),
            round(float(data.m_MuscleClip.m_StopTime), 4),
            round(float(data.m_SampleRate), 4),
        )
        for obj in environment.objects
        if obj.type.name == "AnimationClip"
        for data in (obj.read(),)
    }


def verify_animator(raw: bytes) -> None:
    clips = animator_contract(raw)
    expected_names = {
        "SkinBankaiIchigo_Idle",
        "SkinBankaiIchigo_Run",
        "SkinBankaiIchigo_Attack",
        "SkinBankaiIchigo_Hurt",
    }
    if set(clips) != expected_names:
        raise RuntimeError(f"Bankai Ichigo clip contract mismatch: {clips}")
    idle = clips["SkinBankaiIchigo_Idle"]
    if idle[0] is not True or idle[2:] != (4.3333, 12.0):
        raise RuntimeError(f"Bankai Ichigo Idle changed unexpectedly: {idle}")
    if clips["SkinBankaiIchigo_Run"] != (True, 2, 1.0, 8.0):
        raise RuntimeError(f"Bankai Ichigo Run timing is wrong: {clips['SkinBankaiIchigo_Run']}")
    if clips["SkinBankaiIchigo_Attack"] != (False, 0, 0.8, 10.0):
        raise RuntimeError(f"Bankai Ichigo Attack timing is wrong: {clips['SkinBankaiIchigo_Attack']}")
    hurt = clips["SkinBankaiIchigo_Hurt"]
    if hurt != (False, 0, 0.0833, 12.0):
        raise RuntimeError(f"Bankai Ichigo Hurt changed unexpectedly: {hurt}")
    print("verified Animator: Idle loop / Run 1.0 s continuous sword flight / Attack 0.8 s / original Hurt")


def patch_manifest(raw: bytes, metadata: dict[str, dict]) -> bytes:
    header, assets, bundles = read_manifest(raw)
    if write_manifest(header, assets, bundles) != raw:
        raise RuntimeError("aa1 manifest round-trip changed unmodified data")
    for bundle_name, item in metadata.items():
        index, record = manifest_record(raw, bundle_name)
        updated = deepcopy(record)
        updated["hash"] = item["hash"]
        updated["crc"] = item["crc"]
        updated["size"] = item["size"]
        bundles[index] = updated
    patched = write_manifest(header, assets, bundles)
    for bundle_name, item in metadata.items():
        _, checked = manifest_record(patched, bundle_name)
        if checked["hash"] != item["hash"] or checked["crc"] != item["crc"]:
            raise RuntimeError(f"patched {bundle_name} manifest record failed validation")
    return patched


def encrypt_bundle(path: Path) -> dict:
    encrypted, digest, crc = encrypted_metadata(path)
    encrypted_path = BUILD_DIR / f"{digest}.bundle"
    encrypted_path.write_bytes(encrypted)
    return {
        "path": encrypted_path,
        "hash": digest,
        "crc": crc,
        "size": len(encrypted),
    }


def build_modelview_patch() -> Path:
    if (
        not PATCHER.is_file()
        or PATCHER.stat().st_mtime < max(PATCHER_PROJECT.stat().st_mtime, PATCHER_SOURCE.stat().st_mtime)
    ):
        subprocess.run(
            ["dotnet", "build", str(PATCHER_PROJECT), "-c", "Release", "--nologo"],
            check=True,
            cwd=ROOT,
        )
    output = BUILD_DIR / "Unity.ModelView.dll"
    baseline = Path(str(MODELVIEW) + BACKUP_SUFFIX)
    source = baseline if baseline.is_file() else MODELVIEW
    subprocess.run(
        [str(PATCHER), str(source), str(output)],
        check=True,
        cwd=ROOT,
    )
    if not output.read_bytes().startswith(b"MZ"):
        raise RuntimeError("patched Unity.ModelView.dll is not a PE file")
    return output


def build() -> tuple[dict[str, dict], bytes, dict[str, Image.Image], dict[int, bytes], dict[str, Image.Image], Path]:
    if BUILD_DIR.exists():
        shutil.rmtree(BUILD_DIR)
    BUILD_DIR.mkdir(parents=True)
    texture_plain, original, sprites, replacements = patch_texture_bundle(
        source_bundle_path(TEXTURE_BUNDLE_NAME)
    )
    animator_plain = patch_animator_bundle(source_bundle_path(ANIMATOR_BUNDLE_NAME))
    metadata = {
        TEXTURE_BUNDLE_NAME: encrypt_bundle(texture_plain),
        ANIMATOR_BUNDLE_NAME: encrypt_bundle(animator_plain),
    }
    patched_manifest = patch_manifest(MANIFEST.read_bytes(), metadata)
    modelview = build_modelview_patch()
    verify_encrypted_texture(metadata[TEXTURE_BUNDLE_NAME]["path"], original, sprites, replacements)
    verify_animator(decrypt_bundle(metadata[ANIMATOR_BUNDLE_NAME]["path"]))
    for item in metadata.values():
        print("built", item["path"].name, item["path"].stat().st_size)
    print("preview", BUILD_DIR / "SkinBankaiIchigo-client-complete.gif")
    return metadata, patched_manifest, original, sprites, replacements, modelview


def verify_encrypted_texture(
    path: Path,
    original: dict[str, Image.Image],
    source_sprites: dict[int, bytes],
    replacements: dict[str, Image.Image],
) -> None:
    images, sprites = image_contract(decrypt_bundle(path))
    if sprites != source_sprites:
        raise RuntimeError("encrypted Bankai Ichigo bundle changed Sprite PathIDs or meshes")
    verify_images(images, replacements, original)
    print("verified encrypted Bankai Ichigo bundle: 69 Texture2D / 69 unchanged Full Rect Sprite")


def ensure_client_stopped() -> None:
    if os.name != "nt":
        return
    client_root = str(CLIENT.resolve()).replace("'", "''")
    script = (
        f"$root=[IO.Path]::GetFullPath('{client_root}').TrimEnd('\\')+'\\';"
        "$running=Get-Process -ErrorAction SilentlyContinue | Where-Object {"
        "$_.Path -and [IO.Path]::GetFullPath($_.Path).StartsWith($root,[StringComparison]::OrdinalIgnoreCase)};"
        "if($running){$running|Select-Object Id,ProcessName,Path|Format-Table -AutoSize;exit 23}"
    )
    result = subprocess.run(
        ["powershell", "-NoProfile", "-Command", script],
        text=True,
        encoding="utf-8",
        errors="replace",
        capture_output=True,
    )
    if result.returncode == 23:
        raise RuntimeError(
            "client-test is still running; close the game and UnityCrashHandler64 before install/restore\n"
            + result.stdout.strip()
        )
    if result.returncode != 0:
        raise RuntimeError("could not verify client process state: " + result.stderr.strip())


def tree_fingerprint(root: Path) -> str:
    digest = hashlib.sha256()
    for path in sorted((item for item in root.rglob("*") if item.is_file()), key=lambda item: item.as_posix().lower()):
        relative = path.relative_to(root).as_posix().encode("utf-8")
        digest.update(struct.pack("<I", len(relative)))
        digest.update(relative)
        digest.update(struct.pack("<Q", path.stat().st_size))
        file_digest = hashlib.sha256()
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                file_digest.update(chunk)
        digest.update(file_digest.digest())
    return digest.hexdigest().upper()


def backup_once(path: Path) -> Path:
    backup = Path(str(path) + BACKUP_SUFFIX)
    if not backup.exists():
        shutil.copy2(path, backup)
        print("backup", backup)
    return backup


def atomic_write(path: Path, data: bytes) -> None:
    temporary = Path(str(path) + ".bankai-ichigo-animation.tmp")
    temporary.write_bytes(data)
    os.replace(temporary, path)


def install(
    metadata: dict[str, dict],
    patched_manifest: bytes,
    original: dict[str, Image.Image],
    sprites: dict[int, bytes],
    replacements: dict[str, Image.Image],
    modelview: Path,
) -> None:
    ensure_client_stopped()
    public_before = tree_fingerprint(PUBLIC_CLIENT)
    backup_once(MANIFEST)
    backup_once(MANIFEST_HASH)
    for bundle_name, item in metadata.items():
        _, current_record = manifest_record(MANIFEST.read_bytes(), bundle_name)
        backup_once(AA1 / f"{current_record['hash']}.bundle")
        shutil.copy2(item["path"], AA1 / item["path"].name)
    atomic_write(MANIFEST, patched_manifest)
    atomic_write(MANIFEST_HASH, hashlib.md5(patched_manifest).hexdigest().encode("ascii"))
    backup_once(MODELVIEW)
    shutil.copy2(modelview, MODELVIEW)
    verify_installed(metadata, original, sprites, replacements, modelview)
    public_after = tree_fingerprint(PUBLIC_CLIENT)
    if public_after != public_before:
        raise RuntimeError("public client changed during local Bankai Ichigo installation")
    print("verified public client unchanged", public_after)
    print("installed complete Bankai Ichigo animation in client-test only")


def verify_installed(
    metadata: dict[str, dict],
    original: dict[str, Image.Image],
    sprites: dict[int, bytes],
    replacements: dict[str, Image.Image],
    modelview: Path,
) -> None:
    raw = MANIFEST.read_bytes()
    if MANIFEST_HASH.read_text(encoding="ascii").strip() != hashlib.md5(raw).hexdigest():
        raise RuntimeError("aa1 manifest hash sidecar does not match")
    for bundle_name, item in metadata.items():
        _, record = manifest_record(raw, bundle_name)
        if (
            record["hash"] != item["hash"]
            or record["crc"] != item["crc"]
            or record["size"] != item["size"]
        ):
            raise RuntimeError(f"installed {bundle_name} manifest metadata does not match")
        destination = AA1 / f"{record['hash']}.bundle"
        if hashlib.md5(destination.read_bytes()).hexdigest() != record["hash"]:
            raise RuntimeError(f"installed {bundle_name} hash does not match its filename")
    verify_encrypted_texture(
        AA1 / f"{metadata[TEXTURE_BUNDLE_NAME]['hash']}.bundle",
        original,
        sprites,
        replacements,
    )
    verify_animator(decrypt_bundle(AA1 / f"{metadata[ANIMATOR_BUNDLE_NAME]['hash']}.bundle"))
    if MODELVIEW.read_bytes() != modelview.read_bytes():
        raise RuntimeError("installed Unity.ModelView.dll differs from the verified patch")
    print("verified MonoAnimancer keeps Bankai Run active like Byakuya without a time-jump callback")


def restore() -> None:
    ensure_client_stopped()
    public_before = tree_fingerprint(PUBLIC_CLIENT)
    manifest_backup = Path(str(MANIFEST) + BACKUP_SUFFIX)
    hash_backup = Path(str(MANIFEST_HASH) + BACKUP_SUFFIX)
    modelview_backup = Path(str(MODELVIEW) + BACKUP_SUFFIX)
    if not manifest_backup.is_file() or not hash_backup.is_file() or not modelview_backup.is_file():
        raise RuntimeError("Bankai Ichigo animation rollback backups are incomplete")
    raw = manifest_backup.read_bytes()
    if hash_backup.read_text(encoding="ascii").strip() != hashlib.md5(raw).hexdigest():
        raise RuntimeError("Bankai Ichigo rollback manifest and hash sidecar do not match")
    for bundle_name in (TEXTURE_BUNDLE_NAME, ANIMATOR_BUNDLE_NAME):
        _, record = manifest_record(raw, bundle_name)
        original_bundle = AA1 / f"{record['hash']}.bundle"
        bundle_backup = Path(str(original_bundle) + BACKUP_SUFFIX)
        if not original_bundle.exists() and bundle_backup.exists():
            shutil.copy2(bundle_backup, original_bundle)
        if not original_bundle.exists():
            raise RuntimeError(f"rollback Bankai Ichigo bundle is missing: {original_bundle.name}")
    atomic_write(MANIFEST, raw)
    atomic_write(MANIFEST_HASH, hash_backup.read_bytes())
    shutil.copy2(modelview_backup, MODELVIEW)
    public_after = tree_fingerprint(PUBLIC_CLIENT)
    if public_after != public_before:
        raise RuntimeError("public client changed during local Bankai Ichigo rollback")
    print("restored pre-animation Bankai Ichigo bundle references and Unity.ModelView.dll")
    print("verified public client unchanged", public_after)


def main() -> None:
    parser = argparse.ArgumentParser()
    actions = parser.add_mutually_exclusive_group()
    actions.add_argument("--install", action="store_true", help="install into client-test only")
    actions.add_argument("--restore", action="store_true", help="restore the pre-animation client files")
    args = parser.parse_args()
    if args.restore:
        restore()
        return
    metadata, patched_manifest, original, sprites, replacements, modelview = build()
    if args.install:
        install(metadata, patched_manifest, original, sprites, replacements, modelview)
    else:
        print("build verified; inspect the GIF before --install")


if __name__ == "__main__":
    main()
