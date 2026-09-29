#!/usr/bin/env python3
r"""Decode online 20252 WornBagMapList captures and total worn attributes.

Run from server-mysql:
  python .\test\analyze_online_worn_attributes.py \
    ..\_work\pcap_frames\online-control-resist-20260918.jsonl

This is an offline capture/reference-data check. Production code does not use
the JSON files read here.
"""

from __future__ import annotations

import json
import re
import struct
import sys
from collections import defaultdict
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
TABLE_DIR = ROOT / "datatable_json"

ATTRIBUTE_NAMES = {
    1: "MaxHP",
    2: "MaxMP",
    3: "Str",
    4: "Quk",
    5: "Spi",
    6: "Wim",
    7: "PhyAtk",
    8: "SpiAtk",
    9: "PhyDef",
    10: "SpiDef",
    11: "PhyCritRate",
    12: "SpiCritRate",
    13: "PhyCritValue",
    14: "SpiCritValue",
    15: "AntiPhyCritRate",
    16: "AntiSpiCritRate",
    17: "AntiPhyCritValue",
    18: "AntiSpiCritValue",
    19: "Auxiliary",
    20: "Phy",
    21: "Sta",
    22: "PhyReduction",
    23: "SpiReduction",
    24: "Speed",
    25: "Hit",
    26: "Resistance",
    27: "LifestealRate",
    28: "LifestealValue",
    29: "HPRecovery",
    30: "PhyDamageAmp",
    31: "SpiDamageAmp",
}

EQUIP_FIELDS = {
    1: "Hp",
    2: "Mp",
    3: "Str",
    4: "Quk",
    5: "Spi",
    6: "Wim",
    7: "PhyAtk",
    8: "SpiAtk",
    9: "PhyDef",
    10: "SpiDef",
    11: "Pcrir",
    12: "Mcrir",
    13: "Pcri",
    14: "Mcri",
    15: "Rpcrir",
    16: "Rmcrir",
    17: "Rpcri",
    18: "Rmcri",
    19: "Dvo",
    20: "Phy",
    21: "Sta",
    22: "Nphyi",
    23: "Nmeni",
    24: "Spd",
    25: "Hit",
    26: "Res",
    27: "SuckR",
    28: "SuckV",
    29: "HpRecover",
    30: "PhyDA",
    31: "MicDA",
}

HEX_VALUE = re.compile(r"^<[0-9]+B\s+([0-9a-fA-F]+)>$")


def read_varint(data: bytes, offset: int) -> tuple[int, int]:
    value = 0
    shift = 0
    while offset < len(data):
        byte = data[offset]
        offset += 1
        value |= (byte & 0x7F) << shift
        if byte < 0x80:
            return value, offset
        shift += 7
        if shift >= 70:
            raise ValueError("varint is too long")
    raise ValueError("truncated varint")


def decode_fields(data: bytes) -> list[tuple[int, int, object]]:
    fields: list[tuple[int, int, object]] = []
    offset = 0
    while offset < len(data):
        key, offset = read_varint(data, offset)
        number, wire = key >> 3, key & 7
        if wire == 0:
            value, offset = read_varint(data, offset)
        elif wire == 1:
            if offset + 8 > len(data):
                raise ValueError("truncated fixed64")
            value = data[offset : offset + 8]
            offset += 8
        elif wire == 2:
            size, offset = read_varint(data, offset)
            if offset + size > len(data):
                raise ValueError("truncated bytes")
            value = data[offset : offset + size]
            offset += size
        elif wire == 5:
            if offset + 4 > len(data):
                raise ValueError("truncated fixed32")
            value = data[offset : offset + 4]
            offset += 4
        else:
            raise ValueError(f"unsupported wire type {wire}")
        fields.append((number, wire, value))
    return fields


def values(fields: list[tuple[int, int, object]], number: int) -> list[object]:
    return [value for field, _wire, value in fields if field == number]


def first_int(fields: list[tuple[int, int, object]], number: int) -> int:
    found = values(fields, number)
    return int(found[0]) if found else 0


def first_text(fields: list[tuple[int, int, object]], number: int) -> str:
    found = values(fields, number)
    if not found:
        return ""
    return bytes(found[0]).decode("utf-8", errors="replace")


def nested_hex(value: str) -> bytes:
    match = HEX_VALUE.match(value)
    if not match:
        raise ValueError(f"unexpected capture bytes value: {value!r}")
    return bytes.fromhex(match.group(1))


def load_table(name: str) -> dict[int, dict]:
    raw = (TABLE_DIR / name).read_text(encoding="utf-8")
    raw = re.sub(r",\s*([}\]])", r"\1", raw)
    return {int(key): row for key, row in json.loads(raw)}


def job_family(job_id: int) -> int:
    if job_id <= 0:
        return 1
    if job_id <= 8:
        return (job_id + 1) // 2
    if job_id < 10000:
        return job_id // 1000
    if job_id < 100000:
        return job_id // 10000
    return job_id // 100000


def packed_varints(raw_values: list[object]) -> list[int]:
    result: list[int] = []
    for value in raw_values:
        if isinstance(value, int):
            result.append(value)
            continue
        data = bytes(value)
        offset = 0
        while offset < len(data):
            decoded, offset = read_varint(data, offset)
            result.append(decoded)
    return result


def attribute_map(data: bytes) -> tuple[int, float]:
    fields = decode_fields(data)
    key = first_int(fields, 1)
    raw = values(fields, 2)
    value = struct.unpack("<f", bytes(raw[0]))[0] if raw else 0.0
    return key, value


def add_attribute(total: dict[int, float], key: int, value: object) -> None:
    if key in ATTRIBUTE_NAMES and isinstance(value, (int, float)):
        total[key] += float(value)


def equipment_attributes(
    equipment: list[bytes],
    equip_base: dict[int, dict],
    manual: dict[int, dict],
    affixes: dict[int, dict],
    materials: dict[int, dict],
    strengthen: dict[int, dict],
) -> tuple[dict[int, float], list[int]]:
    total: dict[int, float] = defaultdict(float)
    item_ids: list[int] = []
    for encoded_bag in equipment:
        bag = decode_fields(encoded_bag)
        equip_values = values(bag, 3)
        if not equip_values:
            continue
        equip = decode_fields(bytes(equip_values[0]))
        item_id = first_int(equip, 1)
        item_ids.append(item_id)
        template = equip_base.get(item_id, {})

        for key, field in EQUIP_FIELDS.items():
            add_attribute(total, key, template.get(field, 0))

        special_key = int(template.get("SpecialKey") or 0)
        special_value = float(template.get("SpecialValue") or 0)
        if special_key and special_value:
            factor = 1.0
            level = first_int(equip, 9)
            if level in strengthen:
                factor += float(strengthen[level].get("AttribteAdd") or 0)
            add_attribute(total, special_key, special_value * factor)

        for encoded in values(equip, 5):
            key, delta = attribute_map(bytes(encoded))
            field = EQUIP_FIELDS.get(key)
            if field:
                add_attribute(total, key, float(template.get(field) or 0) * delta)

        for attr_id in packed_varints(values(equip, 6)):
            row = manual.get(attr_id, {})
            add_attribute(total, int(row.get("Key") or 0), row.get("Value", 0))

        for attr_id in packed_varints(values(equip, 11)):
            row = affixes.get(attr_id, {})
            for entry in row.get("AffixArr") or []:
                add_attribute(total, int(entry.get("Key") or 0), entry.get("Value", 0))

        for gem_id in packed_varints(values(equip, 12)):
            row = materials.get(gem_id, {})
            add_attribute(total, int(row.get("GemKey") or 0), row.get("GemValue", 0))
    return dict(total), item_ids


def main() -> int:
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")
    if len(sys.argv) != 2:
        print(f"usage: {Path(sys.argv[0]).name} <capture.jsonl>", file=sys.stderr)
        return 2

    capture = Path(sys.argv[1]).resolve()
    equip_base = load_table("EquipBase.json")
    manual = load_table("ManulEquipAttribute.json")
    affixes = load_table("EquipAffixConfig.json")
    materials = load_table("MaterialBase.json")
    strengthen = load_table("Strengthentable.json")
    character_growth = load_table("CharacterGrowth.json")

    latest: dict[int, tuple[dict, list[bytes]]] = {}
    with capture.open(encoding="utf-8") as handle:
        for line in handle:
            row = json.loads(line)
            if row.get("op") != 20252:
                continue
            fields = row.get("fields") or {}
            character_raw = fields.get("1")
            equipment_raw = fields.get("3") or []
            if not character_raw:
                continue
            character = decode_fields(nested_hex(character_raw))
            player_id = first_int(character, 1)
            latest[player_id] = (
                {
                    "id": player_id,
                    "name": first_text(character, 2),
                    "job": first_int(character, 4),
                    "level": first_int(character, 11),
                    "trans": first_int(character, 12),
                },
                [nested_hex(value) for value in equipment_raw],
            )

    print(f"capture={capture}")
    print(f"characters={len(latest)}")
    print(
        "id\tname\tjob\tlevel\ttrans\titems\tworn_res\tworn_hit\tworn_speed\t"
        "worn_spi\tworn_sta\tderived_res_from_worn\tminimum_res"
    )
    for player_id in sorted(latest):
        character, equipment = latest[player_id]
        total, item_ids = equipment_attributes(
            equipment, equip_base, manual, affixes, materials, strengthen
        )
        resistance_row = character_growth.get(260 + job_family(character["job"]), {})
        derived_resistance = (
            total.get(5, 0) * float(resistance_row.get("Spi") or 0)
            + total.get(21, 0) * float(resistance_row.get("Sta") or 0)
        )
        minimum_resistance = total.get(26, 0) + derived_resistance
        print(
            "{id}\t{name}\t{job}\t{level}\t{trans}\t{items}\t{res:.6g}\t{hit:.6g}\t"
            "{speed:.6g}\t{spi:.6g}\t{sta:.6g}\t{derived:.6g}\t{minimum:.6g}".format(
                **character,
                items=len(item_ids),
                res=total.get(26, 0),
                hit=total.get(25, 0),
                speed=total.get(24, 0),
                spi=total.get(5, 0),
                sta=total.get(21, 0),
                derived=derived_resistance,
                minimum=minimum_resistance,
            )
        )
        print("  item_ids=" + ",".join(str(item_id) for item_id in item_ids))
        populated = [
            f"{key}:{ATTRIBUTE_NAMES[key]}={total[key]:.6g}"
            for key in sorted(total)
            if abs(total[key]) > 1e-12
        ]
        print("  worn_attributes=" + " ".join(populated))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
