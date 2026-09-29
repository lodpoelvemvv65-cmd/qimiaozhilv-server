#!/usr/bin/env python3
# -*- coding: utf-8 -*-
r"""解析一份抓包，回答「线上基础属性点是多少 / 主城挂机经验是多少」。

线上属性显示链路（文档/11-背包装备与线上数值.md，已用 2026-09-13 线上抓包复核）：
  - M2C_SyncUnitAttributeList(20170)：UnitId + 重复 AttributeMap{Key=NumericType,
    Value=float}，一次性同步整个面板（进场/换装/升级后）；
  - M2C_SyncUnitAttribute(20169)：单条 NumericType 变化（战斗中的 Energy 之类）。
面板数字直接取 NumericComponent，所以这两个包就是面板的唯一来源。
基础属性还分「值 / Base / Add」三份（如 MaxHp=1002、MaxHpBase=10021、MaxHpAdd=10022）。
经验是 NumericType=1027、等级是 1026；挂机开始/结束请求是 20087/20089，
20069 只是自动战斗开关。

本脚本输出：
  1. 按 opcode 统计：次数与到达间隔中位数（周期性推送一眼看出周期）；
  2. 属性推送时间线：每次 20170/20169 的 unit 与逐条 NumericType；
  3. 每个单位的最终属性快照，基础属性按「值/Base/Add」成对列出；
  4. 经验 1027 / 等级 1026 的每次变化与增量；
  5. 20069/20087-20090 出现的位置。

用法：在 server-mysql/ 下运行
  python .\test\inspect_idle_attribute_capture.py <capture.pcapng>
  python .\test\inspect_idle_attribute_capture.py <capture.pcapng> --unit 3973 --numeric 1027
  python .\test\inspect_idle_attribute_capture.py <capture.pcapng> --timeline --top 400

依赖 dpkt。.etl 需先用 Packet Monitor 转成 .pcapng。
"""
from __future__ import annotations

import argparse
import collections
import ipaddress
import struct
import sys
from pathlib import Path

import dpkt

sys.stdout.reconfigure(encoding='utf-8', errors='replace')

SYNC_ATTRIBUTE = 20169
SYNC_ATTRIBUTE_LIST = 20170
EXP_NUMERIC = 1027
LEVEL_NUMERIC = 1026
IDLE_OPCODES = {20069, 20087, 20088, 20089, 20090}

# 文档/11 的 NumericType 全表：面板上每个数字都是这里的一条属性推送。
NUMERIC_NAMES = {
    1001: 'Hp', 1002: 'MaxHp', 10021: 'MaxHpBase', 10022: 'MaxHpAdd',
    1003: 'Mp', 1004: 'MaxMp', 10041: 'MaxMpBase', 10042: 'MaxMpAdd',
    1005: 'Str', 10051: 'StrBase', 10052: 'StrAdd',
    1006: 'Quk', 10061: 'QukBase', 10062: 'QukAdd',
    1007: 'Spi', 10071: 'SpiBase', 10072: 'SpiAdd',
    1008: 'Wim', 10081: 'WimBase', 10082: 'WimAdd',
    1009: 'PhyAtk', 10091: 'PhyAtkBase', 10092: 'PhyAtkAdd',
    1010: 'SpiAtk', 10101: 'SpiAtkBase', 10102: 'SpiAtkAdd',
    1011: 'PhyDef', 10111: 'PhyDefBase', 10112: 'PhyDefAdd',
    1012: 'SpiDef', 10121: 'SpiDefBase', 10122: 'SpiDefAdd',
    1013: 'Pcrir', 1014: 'Mcrir', 1015: 'Pcri', 1016: 'Mcri', 1017: 'Dvo',
    1018: 'Rpcrir', 1019: 'Rpcri', 1020: 'Rmcrir', 1021: 'Rmcri',
    1022: 'Nphyi', 1023: 'Nmeni', 1024: 'YuanBao', 1025: 'Voucher',
    1026: 'Level', 1027: 'Exp', 1028: 'Coin', 1029: 'Transmigration',
    1030: 'Honor', 1031: 'Spd', 1032: 'Hit', 1033: 'Res', 1034: 'Phy',
    1035: 'Sta', 1036: 'SkinId', 1037: 'FamilyContribution', 1038: 'Title',
    1039: 'Energy', 1040: 'PvpCurrency', 1041: 'Gem', 1042: 'SuckR',
    1043: 'SuckV', 1044: 'HpRecover', 1045: 'PhyDA', 1046: 'MicDA',
    1047: 'PvpMoney',
}

# 基础属性面板按「值 / Base / Add」成对展示。
BASE_ATTRIBUTE_GROUPS = [
    (1002, 10021, 10022), (1004, 10041, 10042),
    (1005, 10051, 10052), (1006, 10061, 10062),
    (1007, 10071, 10072), (1008, 10081, 10082),
    (1009, 10091, 10092), (1010, 10101, 10102),
    (1011, 10111, 10112), (1012, 10121, 10122),
]


def numeric_name(numeric: int) -> str:
    return NUMERIC_NAMES.get(numeric, '?%d' % numeric)


def load_opcode_names() -> dict[int, str]:
    """从 protocol/opcodes.go 读名字，避免脚本里再维护一份清单。"""
    path = Path(__file__).resolve().parent.parent / 'protocol' / 'opcodes.go'
    names: dict[int, str] = {}
    try:
        for line in path.read_text(encoding='utf-8').splitlines():
            parts = line.split('=')
            if len(parts) != 2:
                continue
            name = parts[0].strip()
            value = parts[1].strip()
            if name.startswith('Op') and value.isdigit():
                names.setdefault(int(value), name)
    except OSError:
        pass
    return names


OPCODE_NAMES = load_opcode_names()


def opcode_name(opcode: int) -> str:
    return OPCODE_NAMES.get(opcode, 'opcode-%d' % opcode)


def read_varint(data: bytes, offset: int) -> tuple[int, int]:
    value = 0
    for shift in range(0, 70, 7):
        if offset >= len(data):
            raise ValueError('truncated varint')
        byte = data[offset]
        offset += 1
        value |= (byte & 0x7F) << shift
        if byte < 0x80:
            return value, offset
    raise ValueError('oversized varint')


def decode_protobuf(data: bytes) -> dict[int, list[object]]:
    """返回 tag -> [值...]；varint 给 int，fixed32 给 4 字节，bytes 给 bytes。"""
    fields: dict[int, list[object]] = collections.defaultdict(list)
    offset = 0
    while offset < len(data):
        key, offset = read_varint(data, offset)
        number, wire_type = key >> 3, key & 7
        if number == 0:
            raise ValueError('invalid field zero')
        if wire_type == 0:
            value, offset = read_varint(data, offset)
        elif wire_type == 1:
            if offset + 8 > len(data):
                raise ValueError('truncated fixed64')
            value = struct.unpack_from('<Q', data, offset)[0]
            offset += 8
        elif wire_type == 2:
            size, offset = read_varint(data, offset)
            if offset + size > len(data):
                raise ValueError('truncated bytes')
            value = data[offset:offset + size]
            offset += size
        elif wire_type == 5:
            if offset + 4 > len(data):
                raise ValueError('truncated fixed32')
            value = data[offset:offset + 4]
            offset += 4
        else:
            raise ValueError('unsupported wire type %d' % wire_type)
        fields[number].append(value)
    return fields


def first(fields: dict[int, list[object]], number: int, default: object = 0) -> object:
    values = fields.get(number)
    return values[0] if values else default


def float32(value: object) -> float:
    if isinstance(value, bytes) and len(value) == 4:
        return struct.unpack('<f', value)[0]
    if isinstance(value, bytes):
        return 0.0
    return float(value)


def attribute_entries(opcode: int, body: bytes) -> tuple[int, list[tuple[int, float]]]:
    """把 20170 的 AttributeMap 列表 / 20169 的单条解成 (unit, [(numeric, value)])。

    20170 的重复字段是 field 2，每条 AttributeMap{Key=1, Value=2(fixed32)}；
    20169 是 field1=UnitId、field2=NumericType、field3=Value(fixed32)。
    """
    fields = decode_protobuf(body)
    unit = int(first(fields, 1))
    if opcode == SYNC_ATTRIBUTE:
        return unit, [(int(first(fields, 2)), float32(first(fields, 3)))]
    entries: list[tuple[int, float]] = []
    for payload in fields.get(2, []):
        if not isinstance(payload, bytes):
            continue
        entry = decode_protobuf(payload)
        entries.append((int(first(entry, 1)), float32(first(entry, 2))))
    return unit, entries


def packet_reader(path: Path):
    with path.open('rb') as stream:
        magic = stream.read(4)
        stream.seek(0)
        reader = dpkt.pcapng.Reader(stream) if magic == b'\x0a\x0d\x0d\x0a' else dpkt.pcap.Reader(stream)
        yield from reader


def tcp_segments(path: Path):
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
        yield timestamp, source, target, tcp.seq, bytes(tcp.data)


def reassembled_streams(path: Path):
    streams: dict[tuple[object, object], list[tuple[float, int, bytes]]] = collections.defaultdict(list)
    seen: set[tuple[object, ...]] = set()
    for timestamp, source, target, sequence, payload in tcp_segments(path):
        key = (source, target, sequence, payload)
        if key in seen:
            continue
        seen.add(key)
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
        length, opcode = struct.unpack_from('<HH', data, offset)
        if length < 2 or offset + 2 + length > len(data):
            return
        yield byte_times[offset], opcode, data[offset + 4:offset + 2 + length]
        offset += 2 + length


def describe(opcode: int, body: bytes) -> str:
    try:
        fields = decode_protobuf(body)
    except ValueError:
        return 'len=%d body=%s' % (len(body), body[:48].hex())
    if opcode in (SYNC_ATTRIBUTE, SYNC_ATTRIBUTE_LIST):
        unit, entries = attribute_entries(opcode, body)
        return 'unit=%s %s' % (unit, ' '.join('%s=%g' % (numeric_name(k), v) for k, v in entries))
    if opcode in IDLE_OPCODES:
        return 'actor=%s body=%s' % (first(fields, 93), body.hex())
    return 'len=%d body=%s' % (len(body), body[:48].hex())


def median(values: list[float]) -> float:
    ordered = sorted(values)
    middle = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[middle]
    return (ordered[middle - 1] + ordered[middle]) / 2


def main() -> int:
    parser = argparse.ArgumentParser(description='解析抓包中的原版面板属性与挂机经验')
    parser.add_argument('capture', type=Path)
    parser.add_argument('--timeline', action='store_true', help='打印全部消息时间线')
    parser.add_argument('--unit', type=int, default=None, help='只看该单位（默认全部）')
    parser.add_argument('--numeric', type=int, default=None, help='属性时间线只看该 NumericType')
    parser.add_argument('--top', type=int, default=0, help='时间线最多打印多少条（0=不限）')
    parser.add_argument('--snapshot', type=int, default=None, help='只打印该单位的快照后退出')
    args = parser.parse_args()

    if not args.capture.exists():
        print('抓包不存在: %s' % args.capture)
        return 2
    if args.capture.suffix.lower() == '.etl':
        print('.etl 不能直接解析，请先转成 .pcapng（Packet Monitor 导出时选 pcapng）')
        return 2

    rows: list[tuple[float, str, int, bytes]] = []
    stream_count = 0
    for (source, target), data, byte_times in reassembled_streams(args.capture):
        if not data:
            continue
        stream_count += 1
        direction = '%s:%d->%s:%d' % (source[0], source[1], target[0], target[1])
        for timestamp, opcode, body in frames(data, byte_times):
            rows.append((timestamp, direction, opcode, body))
    if not rows:
        print('没有解析出游戏帧：确认抓包包含 7756/tcp 的游戏连接，且未在流中间截断。')
        return 1
    rows.sort(key=lambda row: row[0])
    base_time = rows[0][0]

    pushes: list[tuple[float, int, int, list[tuple[int, float]]]] = []
    for timestamp, _direction, opcode, body in rows:
        if opcode not in (SYNC_ATTRIBUTE, SYNC_ATTRIBUTE_LIST):
            continue
        try:
            unit, entries = attribute_entries(opcode, body)
        except ValueError:
            continue
        if args.unit is not None and unit != args.unit:
            continue
        pushes.append((timestamp - base_time, opcode, unit, entries))

    snapshot: dict[int, dict[int, float]] = collections.defaultdict(dict)
    for _elapsed, _opcode, unit, entries in pushes:
        for numeric, value in entries:
            snapshot[unit][numeric] = value

    if args.snapshot is not None:
        print_snapshot(args.snapshot, snapshot.get(args.snapshot, {}))
        return 0

    print('== 抓包概览 ==')
    print('文件      : %s' % args.capture.name)
    print('TCP 流    : %d' % stream_count)
    print('游戏帧    : %d' % len(rows))
    print('时长      : %.1fs' % (rows[-1][0] - base_time))
    print()

    print('== opcode 统计（次数 / 首次 / 末次 / 间隔中位数）==')
    grouped: dict[int, list[float]] = collections.defaultdict(list)
    for timestamp, _direction, opcode, _body in rows:
        grouped[opcode].append(timestamp)
    for opcode, times in sorted(grouped.items(), key=lambda item: (-len(item[1]), item[0])):
        gaps = [b - a for a, b in zip(times, times[1:])]
        gap_text = '间隔中位数=%.2fs 最小=%.2fs' % (median(gaps), min(gaps)) if gaps else '仅一次'
        print('%6d %-34s x%-5d 首次=%7.2fs 末次=%7.2fs %s' % (
            opcode, opcode_name(opcode), len(times), times[0] - base_time, times[-1] - base_time, gap_text))
    print()

    print('== 属性推送时间线（20170 列表 / 20169 单条，共 %d 次）==' % len(pushes))
    printed = 0
    for elapsed, opcode, unit, entries in pushes:
        if args.numeric is not None:
            entries = [(k, v) for k, v in entries if k == args.numeric]
            if not entries:
                continue
        if args.top and printed >= args.top:
            continue
        printed += 1
        detail = ' '.join('%d(%s)=%g' % (k, numeric_name(k), v) for k, v in entries)
        print('%8.2fs %d unit=%-10d n=%-3d %s' % (elapsed, opcode, unit, len(entries), detail))
    if not printed:
        print('  本次抓包里没有符合条件的属性推送')
    print()

    print('== 各 NumericType 首次/末次出现 ==')
    by_numeric: dict[int, list[float]] = collections.defaultdict(list)
    for elapsed, _opcode, _unit, entries in pushes:
        for numeric, _value in entries:
            by_numeric[numeric].append(elapsed)
    for numeric, times in sorted(by_numeric.items()):
        print('%5d %-16s 首次=%7.2fs 次数=%-4d 末次=%7.2fs' % (
            numeric, numeric_name(numeric), times[0], len(times), times[-1]))
    print()

    for unit, values in sorted(snapshot.items()):
        print_snapshot(unit, values)

    print('== 经验(1027) / 等级(1026) 变化 ==')
    previous: dict[tuple[int, int], float] = {}
    changes = 0
    for elapsed, _opcode, unit, entries in pushes:
        for numeric, value in entries:
            if numeric not in (EXP_NUMERIC, LEVEL_NUMERIC):
                continue
            key = (unit, numeric)
            before = previous.get(key)
            previous[key] = value
            changes += 1
            delta = '' if before is None else '  (delta=%+g)' % (value - before)
            print('%8.2fs unit=%-10d %-8s %g%s' % (elapsed, unit, numeric_name(numeric), value, delta))
    if not changes:
        print('  本次抓包里没有 1026/1027 推送：主城挂机经验需要一段「站主城、不进战斗」的抓包')
    print()

    idle_rows = [row for row in rows if row[2] in IDLE_OPCODES]
    print('== 挂机/自动战斗相关（20069/20087-20090）: %d 条 ==' % len(idle_rows))
    for timestamp, direction, opcode, body in idle_rows:
        print('%8.2fs %s %d %s %s' % (
            timestamp - base_time, direction, opcode, opcode_name(opcode), describe(opcode, body)))
    print()

    if args.timeline:
        print('== 全量时间线 ==')
        for index, (timestamp, direction, opcode, body) in enumerate(rows):
            if args.top and index >= args.top:
                print('  ...（共 %d 条，--top 限制）' % len(rows))
                break
            print('%8.2fs %s %6d %-34s %s' % (
                timestamp - base_time, direction, opcode, opcode_name(opcode), describe(opcode, body)))
    return 0


def print_snapshot(unit: int, values: dict[int, float]) -> None:
    print('== 单位 %d 的最终属性快照（%d 项）==' % (unit, len(values)))
    if not values:
        print('  没有该单位的属性推送')
        print()
        return
    for numeric in sorted(values):
        print('%5d %-16s %g' % (numeric, numeric_name(numeric), values[numeric]))
    print('-- 基础属性 值/Base/Add --')
    for value_id, base_id, add_id in BASE_ATTRIBUTE_GROUPS:
        if not any(key in values for key in (value_id, base_id, add_id)):
            continue
        print('%-8s 值=%-12g Base=%-12s Add=%s' % (
            numeric_name(value_id),
            values.get(value_id, 0),
            '%g' % values[base_id] if base_id in values else '(未推送)',
            '%g' % values[add_id] if add_id in values else '(未推送)'))
    print()


if __name__ == '__main__':
    raise SystemExit(main())
