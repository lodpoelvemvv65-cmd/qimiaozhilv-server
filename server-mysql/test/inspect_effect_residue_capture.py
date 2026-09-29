#!/usr/bin/env python3
r"""Inspect battle-effect residue frames in a Packet Monitor pcapng capture.

Answers: after a battle settles, does the online server clean up skill
effects/buff icons (20080 Reduce with Time=0), or does the client own the
teardown via the Time carried in the Add/20077 packet?

Usage: python .\test\inspect_effect_residue_capture.py <capture.pcapng> [--unit N]
Requires dpkt. The game uses [u16 LE length][u16 LE opcode][protobuf body].
"""

from __future__ import annotations

import argparse
import collections
import struct
from pathlib import Path

import dpkt


OPCODES = {
    20054: "M2C_BattleVictory",
    20055: "M2C_BattleDefeat",
    20075: "M2C_PlaySkill",
    20076: "M2C_MonsterPlaySkill",
    20077: "M2C_PlaySkillEffect",
    20080: "M2C_BattleChangeState",
    20081: "M2C_BattleTouchState",
    20082: "M2C_MainstoryMonsterDead",
    20088: "M2C_UnitDead",
}

EU = "<HH"


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
            value = struct.unpack_from("<Q", data, offset)[0]
            offset += 8
        elif wire_type == 2:
            size, offset = read_varint(data, offset)
            value = data[offset:offset + size]
            offset += size
        elif wire_type == 5:
            value = struct.unpack_from("<I", data, offset)[0]
            offset += 4
        else:
            raise ValueError(f"unsupported wire type {wire_type}")
        fields[number].append(value)
    return fields


def field(fields: dict[int, list[object]], number: int, default: object = 0) -> object:
    values = fields.get(number)
    return values[0] if values else default


def describe(opcode: int, fields: dict[int, list[object]]) -> str:
    if opcode == 20077:
        return (
            f"unit={field(fields, 1)} target={field(fields, 4)} "
            f"time={field(fields, 6)} pos={field(fields, 8)} "
            f"effect={field(fields, 9)} targetType={field(fields, 10)}"
        )
    if opcode == 20080:
        icon = field(fields, 3, b"")
        icon = icon.decode("utf-8", "replace") if isinstance(icon, bytes) else icon
        kind = {1: "Add", 2: "Reduce"}.get(field(fields, 6), field(fields, 6))
        desc = field(fields, 4, b"")
        desc = desc.decode("utf-8", "replace") if isinstance(desc, bytes) else desc
        return (
            f"id={field(fields, 1)} target={field(fields, 2)} icon={icon} "
            f"type={kind} time={field(fields, 7)} isBuff={bool(field(fields, 8))} desc={desc}"
        )
    if opcode in (20082, 20088):
        return f"unit={field(fields, 1)}"
    if opcode in (20054, 20055):
        return f"battleType={field(fields, 1)}"
    if opcode == 20075:
        return f"unit={field(fields, 1)} skill={field(fields, 2)} cd={field(fields, 3)} target={field(fields, 4)}"
    if opcode == 20076:
        return f"unit={field(fields, 1)} skill={field(fields, 2)} target={field(fields, 3)}"
    return ""


def packet_reader(path: Path):
    with path.open("rb") as handle:
        try:
            yield from dpkt.pcapng.Reader(handle)
        except ValueError:
            handle.seek(0)
            yield from dpkt.pcap.Reader(handle)


def reassembled_streams(path: Path):
    flows: dict[tuple, list[tuple[float, bytes]]] = collections.defaultdict(list)
    for timestamp, raw in packet_reader(path):
        if len(raw) < 14:
            continue
        if raw[12:14] != b"\x08\x00":
            continue
        ip = raw[14:]
        header_len = (ip[0] & 0x0F) * 4
        if ip[9] != 6:
            continue
        tcp = ip[header_len:]
        source = (ip[12:16], struct.unpack_from(">H", tcp, 0)[0])
        target = (ip[16:20], struct.unpack_from(">H", tcp, 2)[0])
        payload_offset = ((tcp[12] >> 4) & 0x0F) * 4
        payload = tcp[payload_offset:]
        if not payload:
            continue
        flows[(source, target)].append((timestamp, payload))
    for key, chunks in flows.items():
        chunks.sort(key=lambda chunk: chunk[0])
        data = bytearray()
        times: list[float] = []
        for timestamp, payload in chunks:
            data.extend(payload)
            times.extend([timestamp] * len(payload))
        yield key, bytes(data), times


def frames(data: bytes, byte_times: list[float]):
    offset = 0
    while offset + 4 <= len(data):
        length, opcode = struct.unpack_from(EU, data, offset)
        if length < 2 or offset + 2 + length > len(data):
            return
        yield byte_times[offset], opcode, data[offset + 4:offset + 2 + length]
        offset += 2 + length


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("capture", type=Path)
    parser.add_argument("--unit", type=int, default=0, help="only show frames touching this unit id")
    parser.add_argument("--ops", default="", help="comma separated extra opcodes to include")
    args = parser.parse_args()

    wanted = set(OPCODES)
    if args.ops:
        for token in args.ops.split(","):
            token = token.strip()
            if token:
                wanted.add(int(token))

    rows = []
    for (source, target), data, byte_times in reassembled_streams(args.capture):
        direction = f"{source[0].hex()}:{source[1]}->{target[0].hex()}:{target[1]}"
        for timestamp, opcode, body in frames(data, byte_times):
            if opcode not in wanted:
                continue
            try:
                fields = decode_protobuf(body)
            except ValueError as error:
                rows.append((timestamp, direction, opcode, f"decode-error={error} body={body.hex()}"))
                continue
            if args.unit:
                touched = {int(field(fields, n)) for n in (1, 2, 4)}
                if args.unit not in touched:
                    continue
            rows.append((timestamp, direction, opcode, describe(opcode, fields)))

    if not rows:
        print("no matching frames")
        return
    rows.sort()
    start = rows[0][0]
    for timestamp, direction, opcode, details in rows:
        name = OPCODES.get(opcode, f"op{opcode}")
        marker = ""
        if opcode in (20054, 20055):
            marker = "  <<< battle settled"
        elif opcode == 20080 and "type=Reduce" in details:
            marker = "  <<< cleanup frame"
        print(f"{timestamp - start:8.3f}s {direction} {opcode} {name} {details}{marker}")

    victories = [timestamp for timestamp, _, opcode, _ in rows if opcode == 20054]
    print("\n--- summary ---")
    print(f"20054 battle-victory frames: {len(victories)}")
    effects = collections.OrderedDict()
    icons_add = collections.OrderedDict()
    icons_reduce = collections.OrderedDict()
    for _, _, opcode, details in rows:
        if opcode == 20077:
            effects[details.split("effect=")[1]] = effects.get(details.split("effect=")[1], 0) + 1
        elif opcode == 20080:
            key = details
            if "type=Add" in details:
                icons_add[key] = icons_add.get(key, 0) + 1
            elif "type=Reduce" in details:
                icons_reduce[key] = icons_reduce.get(key, 0) + 1
    print(f"20077 distinct (unit/target/time/pos/effect/type) variants: {len(effects)}")
    for key, count in effects.items():
        print(f"   x{count} {key}")
    print(f"20080 Add frames: {sum(icons_add.values())}")
    for key, count in icons_add.items():
        print(f"   x{count} {key}")
    print(f"20080 Reduce frames: {sum(icons_reduce.values())}")
    for key, count in icons_reduce.items():
        print(f"   x{count} {key}")
    if victories:
        last = victories[-1]
        after = [row for row in rows if row[0] > last]
        print(f"frames after the last victory: {len(after)}")
        for timestamp, direction, opcode, details in after:
            name = OPCODES.get(opcode, f"op{opcode}")
            print(f"   +{timestamp - last:6.3f}s {opcode} {name} {details}")
        tail = [row for row in after if row[2] == 20080]
        print(f"20080 frames after victory: {len(tail)} (0 means the online server sent no cleanup)")
    else:
        print("no 20054 in this capture — the battle never settled on the wire")


if __name__ == "__main__":
    main()
