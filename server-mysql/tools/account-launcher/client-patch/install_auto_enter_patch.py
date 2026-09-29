# -*- coding: utf-8 -*-
"""Install or restore automatic activation of the role-selection start button."""

import argparse
import hashlib
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

from install_client_patch import (
    REPOSITORY_ROOT,
    atomic_write,
    client_paths,
    ensure_plain_bundle,
    extract_hotfix_view,
    replace_hotfix_view,
    verify_consistent,
)
from client_patch_paths import patch_backup

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

BACKUP_SUFFIX = ".pre-launcher-auto-enter.bak"
PATCH_PROJECT = Path(__file__).with_name("auto-enter") / "ClientLauncherAutoEnterPatcher.csproj"


def backup_once(path):
    backup = patch_backup(Path(path), BACKUP_SUFFIX)
    backup.parent.mkdir(parents=True, exist_ok=True)
    if not backup.exists():
        shutil.copy2(path, backup)
        print("backup", backup)


def install():
    extracted, bundle, decoded = client_paths()
    for path in (extracted, bundle):
        if not path.is_file():
            raise FileNotFoundError(path)
    ensure_plain_bundle(bundle)
    source_dll = extract_hotfix_view(bundle)
    if not source_dll.startswith(b"MZ"):
        raise RuntimeError("current bundle HotfixView.dll is not a PE file")
    if extracted.read_bytes() != source_dll:
        raise RuntimeError("current bundle and extracted HotfixView.dll differ before patching")
    if decoded.is_file() and extract_hotfix_view(decoded) != source_dll:
        raise RuntimeError("current bundle and decoded bundle differ before patching")

    with tempfile.TemporaryDirectory(prefix="launcher-auto-enter-") as temp_name:
        temp_dir = Path(temp_name)
        input_dll = temp_dir / "HotfixView.input.dll"
        patched_path = temp_dir / "HotfixView.dll"
        input_dll.write_bytes(source_dll)
        subprocess.run(
            [
                "dotnet",
                "run",
                "--project",
                str(PATCH_PROJECT),
                "--configuration",
                "Release",
                "--",
                str(input_dll),
                str(patched_path),
            ],
            cwd=REPOSITORY_ROOT,
            check=True,
        )
        patched_dll = patched_path.read_bytes()
        if not patched_dll.startswith(b"MZ"):
            raise RuntimeError("patched HotfixView.dll is not a PE file")
        if patched_dll == source_dll:
            verify_consistent(extracted, bundle, decoded)
            print("launcher auto-enter patch is already installed")
            return

        patched_bundle_path = replace_hotfix_view(bundle, patched_dll, temp_dir)
        ensure_plain_bundle(patched_bundle_path)
        if extract_hotfix_view(patched_bundle_path) != patched_dll:
            raise RuntimeError("generated bundle does not contain the patched HotfixView.dll")
        patched_bundle = patched_bundle_path.read_bytes()

    backup_once(extracted)
    backup_once(bundle)
    if decoded.is_file():
        backup_once(decoded)
    atomic_write(extracted, patched_dll)
    atomic_write(bundle, patched_bundle)
    if decoded.is_file():
        atomic_write(decoded, patched_bundle)

    verify_consistent(extracted, bundle, decoded)
    print("installed client-test launcher auto-enter patch")
    print("HotfixView.dll sha256", hashlib.sha256(patched_dll).hexdigest().upper())
    print("config bundle sha256", hashlib.sha256(patched_bundle).hexdigest().upper())


def restore():
    extracted, bundle, decoded = client_paths()
    required = (extracted, bundle)
    missing = [patch_backup(path, BACKUP_SUFFIX) for path in required
               if not patch_backup(path, BACKUP_SUFFIX).is_file()]
    if missing:
        raise FileNotFoundError("missing launcher auto-enter backup: " + ", ".join(map(str, missing)))

    targets = [extracted, bundle]
    if patch_backup(decoded, BACKUP_SUFFIX).is_file():
        targets.append(decoded)
    for path in targets:
        backup = patch_backup(path, BACKUP_SUFFIX)
        atomic_write(path, backup.read_bytes())
        print("restored", path)
    verify_consistent(extracted, bundle, decoded if decoded in targets else None)
    print("restored client-test state from launcher auto-enter backups")


def main():
    parser = argparse.ArgumentParser()
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--install", action="store_true")
    action.add_argument("--restore", action="store_true")
    args = parser.parse_args()
    if args.install:
        install()
    else:
        restore()


if __name__ == "__main__":
    main()
