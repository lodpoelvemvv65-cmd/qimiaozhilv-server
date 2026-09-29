"""Shared paths for client patch inputs and rollback archives.

Runtime assets stay in the playable client. Patched DLL analysis copies and
rollback snapshots live under server-mysql/client-patches instead.
"""

from __future__ import annotations

from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
PATCH_ROOT = ROOT / "server-mysql" / "client-patches" / "client-test"
CURRENT = PATCH_ROOT / "current"
BACKUPS = PATCH_ROOT / "backups"
BUNDLE_NAME = "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"


def data_root(client: str = "client-test") -> Path:
    root = ROOT / client
    matches = sorted(root.glob("*_Data"))
    if len(matches) != 1:
        raise RuntimeError(f"expected exactly one *_Data directory under {root}")
    return matches[0]


def runtime_yoo(client: str = "client-test") -> Path:
    return data_root(client) / "StreamingAssets" / "yoo"


def runtime_bundle(client: str = "client-test") -> Path:
    return runtime_yoo(client) / "aa0" / BUNDLE_NAME


def runtime_decoded_bundle(client: str = "client-test") -> Path:
    return runtime_yoo(client) / "_dec" / "aa0" / BUNDLE_NAME


def current_dll(name: str) -> Path:
    return CURRENT / name


def patch_backup(path: Path, suffix: str) -> Path:
    """Map a former client-side sibling backup to the central archive."""
    parts = path.parts
    try:
        yoo_index = parts.index("yoo")
        relative = Path(*parts[yoo_index + 1 :])
    except ValueError:
        if path.parent == CURRENT and path.name == BUNDLE_NAME:
            relative = Path("aa0") / path.name
        elif path.parent == CURRENT:
            relative = Path("_extracted") / path.name
        else:
            relative = Path(path.name)
    return BACKUPS / Path(str(relative) + suffix)
