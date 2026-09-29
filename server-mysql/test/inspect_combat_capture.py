#!/usr/bin/env python3
r"""Inspect combat frames in a Packet Monitor pcapng capture.

Usage: python .\test\inspect_combat_capture.py <capture.pcapng>
Requires dpkt. The game uses [u16 LE length][u16 LE opcode][protobuf body].
"""

from __future__ import annotations

import argparse
import collections
import ipaddress
import struct
from pathlib import Path

import dpkt


COMBAT_OPCODES = {20050, 20075, 20076, 20077, 20078, 20169}
OPCODE_NAMES = {
    20050: "M2C_MainStoryMonsterInfo",
    20075: "M2C_PlaySkill",
    20076: "M2C_MonsterPlaySkill",
    20077: "M2C_PlaySkillEffect",
    20078: "M2C_BattleSkillRet",
    20169: "M2C_SyncUnitAttribute",
}


def read_varint(data: bytes, offset: int) -> tuple[int, int]:
    value = 0
    for shift in range(0, 70, 7):
        if offset >= len(data):
            raise ValueError("truncated varint")
        byte = data[offset]
        offset += 1
        value |= (byte & 0x7F) << shift
        if byte < 0x80:
            return value, offset
    raise ValueError("oversized varint")


def decode_protobuf(data: bytes) -> dict[int, list[object]]:
    fields: dict[int, list[object]] = collections.defaultdict(list)
    offset = 0
    while offset < len(data):
        key, offset = read_varint(data, offset)
        number, wire_type = key >> 3, key & 7
        if number == 0:
            raise ValueError("invalid field zero")
        if wire_type == 0:
            value, offset = read_varint(data, offset)
        elif wire_type == 1:
            if offset + 8 > len(data):
                raise ValueError("truncated fixed64")
            value = struct.unpack_from("<Q", data, offset)[0]
            offset += 8
        elif wire_type == 2:
            size, offset = read_varint(data, offset)
            if offset + size > len(data):
                raise ValueError("truncated bytes")
            value = data[offset : offset + size]
            offset += size
        elif wire_type == 5:
            if offset + 4 > len(data):
                raise ValueError("truncated fixed32")
            value = data[offset : offset + 4]
            offset += 4
        else:
            raise ValueError(f"unsupported wire type {wire_type}")
        fields[number].append(value)
    return fields


def first(fields: dict[int, list[object]], number: int, default: object = 0) -> object:
    values = fields.get(number)
    return values[0] if values else default


def signed32(value: object) -> int:
    value = int(value) & 0xFFFFFFFF
    return value - 0x100000000 if value & 0x80000000 else value


def combat_fields(opcode: int, body: bytes) -> str:
    fields = decode_protobuf(body)
    actor = int(first(fields, 93))
    if opcode == 20050:
        return f"actor={actor} body={body.hex()}"
    if opcode == 20075:
        return (
            f"unit={first(fields, 1)} skill={first(fields, 2)} "
            f"cool={first(fields, 3)} target={first(fields, 4)} "
            f"mp={first(fields, 5)} actor={actor}"
        )
    if opcode == 20076:
        return (
            f"unit={first(fields, 1)} skill={first(fields, 2)} "
            f"target={first(fields, 3)} actor={actor}"
        )
    if opcode == 20077:
        return (
            f"unit={first(fields, 1)} target={first(fields, 4)} "
            f"time={first(fields, 6)} pos={first(fields, 8)} "
            f"effect={first(fields, 9)} targetType={first(fields, 10)} actor={actor}"
        )
    if opcode == 20078:
        return (
            f"unit={first(fields, 1)} hp={signed32(first(fields, 2))} "
            f"crit={bool(first(fields, 3))} actor={actor}"
        )
    value = first(fields, 3, b"\0\0\0\0")
    numeric_value = struct.unpack("<f", value)[0] if isinstance(value, bytes) else value
    return (
        f"unit={first(fields, 1)} numeric={first(fields, 2)} "
        f"value={numeric_value:g} actor={actor}"
    )


def packet_reader(path: Path):
    with path.open("rb") as stream:
        magic = stream.read(4)
        stream.seek(0)
        reader = dpkt.pcapng.Reader(stream) if magic == b"\x0a\x0d\x0d\x0a" else dpkt.pcap.Reader(stream)
        yield from reader


def tcp_segments(path: Path):
    seen: set[tuple[object, ...]] = set()
    for timestamp, packet in packet_reader(path):
        try:
            ethernet = dpkt.ethernet.Ethernet(packet)
            ip = ethernet.data
            tcp = ip.data
            if not isinstance(tcp, dpkt.tcp.TCP) or not tcp.data:
                continue
            source = (str(ipaddress.ip_address(ip.src)), tcp.sport)
            target = (str(ipaddress.ip_address(ip.dst)), tcp.dport)
        except (ValueError, AttributeError, dpkt.UnpackError):
            continue
        key = (source, target, tcp.seq, bytes(tcp.data))
        if key in seen:
            continue
        seen.add(key)
        yield timestamp, source, target, tcp.seq, bytes(tcp.data)


def reassembled_streams(path: Path):
    streams: dict[tuple[object, object], list[tuple[float, int, bytes]]] = collections.defaultdict(list)
    for timestamp, source, target, sequence, payload in tcp_segments(path):
        streams[(source, target)].append((timestamp, sequence, payload))
    for endpoints, segments in streams.items():
        segments.sort(key=lambda item: item[1])
        base_sequence = segments[0][1]
        data = bytearray()
        byte_times: list[float] = []
        for timestamp, sequence, payload in segments:
            offset = sequence - base_sequence
            if offset > len(data):
                break
            overlap = max(0, len(data) - offset)
            new_data = payload[overlap:]
            data.extend(new_data)
            byte_times.extend([timestamp] * len(new_data))
        yield endpoints, bytes(data), byte_times


def frames(data: bytes, byte_times: list[float]):
    offset = 0
    while offset + 4 <= len(data):
        length, opcode = struct.unpack_from("<HH", data, offset)
        if length < 2 or offset + 2 + length > len(data):
            return
        body_start = offset + 4
        body_end = offset + 2 + length
        yield byte_times[offset], opcode, data[body_start:body_end]
        offset = body_end


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("capture", type=Path)
    args = parser.parse_args()
    first_timestamp = None
    rows = []
    for (source, target), data, byte_times in reassembled_streams(args.capture):
        direction = f"{source[0]}:{source[1]} -> {target[0]}:{target[1]}"
        for timestamp, opcode, body in frames(data, byte_times):
            if opcode not in COMBAT_OPCODES:
                continue
            first_timestamp = timestamp if first_timestamp is None else min(first_timestamp, timestamp)
            rows.append((timestamp, direction, opcode, body))
    for timestamp, direction, opcode, body in sorted(rows):
        elapsed = timestamp - first_timestamp if first_timestamp is not None else 0
        try:
            details = combat_fields(opcode, body)
        except ValueError as error:
            details = f"decode-error={error} body={body.hex()}"
        print(f"{elapsed:8.3f}s {direction} {opcode} {OPCODE_NAMES[opcode]} {details}")


if __name__ == "__main__":
    main()
