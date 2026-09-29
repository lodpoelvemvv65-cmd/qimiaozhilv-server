#!/usr/bin/env python3
"""Patch the Unity GlobalProto endpoint without resizing resources.assets."""

from __future__ import annotations

import argparse
import ipaddress
import json
import re
import shutil
import struct
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_ASSET = ROOT / "client-test" / "梦幻奇遇记_Data" / "resources.assets"
MARKERS = (b'{ "_t" : "GlobalProto"', b'{"_t":"GlobalProto"')
BACKUP_SUFFIX = ".server-address.bak"
DOMAIN_RE = re.compile(
    r"(?=^.{1,253}\.?$)(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*"
    r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.?$"
)


def endpoint(value: str, default_port: int) -> str:
    value = value.strip()
    if not value or "://" in value or any(char in value for char in "/\\?#@"):
        raise ValueError("server must be an IP/domain with an optional numeric port")

    host = value
    port = default_port
    if value.startswith("["):
        match = re.fullmatch(r"\[([^]]+)](?::([0-9]+))?", value)
        if not match:
            raise ValueError("invalid bracketed IPv6 endpoint")
        host = match.group(1)
        if match.group(2):
            port = int(match.group(2))
    elif value.count(":") == 1:
        possible_host, possible_port = value.rsplit(":", 1)
        if not possible_port.isdigit():
            raise ValueError("port must be numeric")
        host, port = possible_host, int(possible_port)
    elif value.count(":") > 1:
        host = value

    if not 1 <= port <= 65535:
        raise ValueError("port must be between 1 and 65535")
    try:
        parsed_ip = ipaddress.ip_address(host)
    except ValueError:
        if not DOMAIN_RE.fullmatch(host):
            raise ValueError(f"invalid server host: {host!r}") from None
        return f"{host.rstrip('.')}:{port}"
    if parsed_ip.version == 6:
        return f"[{parsed_ip.compressed}]:{port}"
    return f"{parsed_ip.compressed}:{port}"


def locate_global_proto(raw: bytes) -> tuple[int, int, dict[str, object]]:
    starts: set[int] = set()
    for marker in MARKERS:
        cursor = 0
        while True:
            found = raw.find(marker, cursor)
            if found < 0:
                break
            starts.add(found)
            cursor = found + len(marker)
    if len(starts) != 1:
        raise ValueError(f"expected one GlobalProto object, found {len(starts)}")

    start = next(iter(starts))
    if start < 4:
        raise ValueError("GlobalProto length prefix is missing")
    length = struct.unpack_from("<I", raw, start - 4)[0]
    if length < min(map(len, MARKERS)) or start + length > len(raw):
        raise ValueError(f"invalid GlobalProto length {length}")
    payload = raw[start : start + length]
    try:
        config = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValueError(f"GlobalProto JSON is invalid: {exc}") from exc
    if config.get("_t") != "GlobalProto":
        raise ValueError("located object is not GlobalProto")
    return start, length, config


def show(asset: Path) -> None:
    _, _, config = locate_global_proto(asset.read_bytes())
    print(f"file: {asset}")
    print(f"Address: {config.get('Address')}")
    print(f"LocalAddress: {config.get('LocalAddress')}")
    print(f"isLocal: {config.get('isLocal')}")


def patch(asset: Path, server: str, create_backup: bool = True) -> None:
    raw = asset.read_bytes()
    start, allocated, config = locate_global_proto(raw)
    config["Address"] = server
    config["LocalAddress"] = server
    # Local mode keeps YooAsset on the bundled resources instead of the old CDN.
    config["isLocal"] = True

    encoded = json.dumps(config, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    if len(encoded) > allocated:
        raise ValueError(
            f"patched GlobalProto needs {len(encoded)} bytes but only {allocated} are allocated"
        )
    encoded += b" " * (allocated - len(encoded))

    backup = asset.with_name(asset.name + BACKUP_SUFFIX)
    if create_backup and not backup.exists():
        shutil.copy2(asset, backup)
    updated = bytearray(raw)
    updated[start : start + allocated] = encoded
    asset.write_bytes(updated)

    verified_raw = asset.read_bytes()
    _, verified_allocated, verified = locate_global_proto(verified_raw)
    if len(verified_raw) != len(raw) or verified_allocated != allocated:
        raise RuntimeError("resources.assets size changed unexpectedly")
    if verified.get("Address") != server or verified.get("LocalAddress") != server:
        raise RuntimeError("patched server address did not verify")
    if verified.get("isLocal") is not True:
        raise RuntimeError("isLocal verification failed")
    print(f"patched: {asset}")
    print(f"server: {server}")
    if create_backup:
        print(f"backup: {backup}")


def restore(asset: Path) -> None:
    backup = asset.with_name(asset.name + BACKUP_SUFFIX)
    if not backup.exists():
        raise FileNotFoundError(f"backup does not exist: {backup}")
    shutil.copy2(backup, asset)
    print(f"restored: {asset}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("server", nargs="?", help="public IP/domain, optionally with :port")
    parser.add_argument("--port", type=int, default=7756, help="port used when server omits it")
    parser.add_argument("--asset", type=Path, default=DEFAULT_ASSET, help="resources.assets path")
    parser.add_argument("--output", type=Path, help="write a separately patched asset and keep --asset unchanged")
    parser.add_argument("--show", action="store_true", help="show current values without changing the file")
    parser.add_argument("--restore", action="store_true", help="restore the first pre-patch backup")
    args = parser.parse_args()

    asset = args.asset.resolve()
    if not asset.is_file():
        parser.error(f"asset not found: {asset}")
    if args.show:
        try:
            show(asset)
        except (OSError, ValueError) as exc:
            parser.error(str(exc))
        return
    if args.restore:
        try:
            restore(asset)
        except OSError as exc:
            parser.error(str(exc))
        return
    if not args.server:
        parser.error("server is required unless --show or --restore is used")
    try:
        server = endpoint(args.server, args.port)
        target = asset
        create_backup = True
        if args.output:
            target = args.output.resolve()
            if target == asset:
                raise ValueError("--output must differ from --asset")
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(asset, target)
            create_backup = False
        patch(target, server, create_backup=create_backup)
    except (OSError, ValueError, RuntimeError) as exc:
        parser.error(str(exc))


if __name__ == "__main__":
    main()
