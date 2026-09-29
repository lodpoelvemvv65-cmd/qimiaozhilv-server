#!/usr/bin/env python3
# -*- coding: utf-8 -*-
r"""解析一份线上抓包，把「角色面板 + 星魂 + 装备穿戴」拆开对账。

用途：线上角色面板比我们公式多出一截属性时，用抓包定位这截是谁给的。抓包必须包含
以下操作，脚本才能拿到对应数据：

  * 登录进图（进场自动推一次 20170 全量面板，1 秒后客户端自动发 20396 拉星魂背包）
  * 打开人物信息 → 属性页
  * 打开背包 → 星魂界面（触发 20396 → 20400 星魂全量）
  * 打开装备/穿戴栏（触发 20258 → 20259 背包+穿戴快照）
  * 可选：脱一件装备再穿上（20273/20274，确认 20170 增量对应关系）

脚本输出：
  1. 概览 + opcode 统计；
  2. 每个单位的面板属性（20170/20169），基础属性按 值/Base/Add 成对列出
     （线上实测只推「值」，Base/Add 一般不推，这里会标「未推送」）；
  3. 角色信息 20003 UnitCharacter（等级/转生/宠物/潜能点），含嵌在 20252 里的；
  4. 星魂 20400/20401/20402/20403：逐件 typeId/level/quality/main/vice/viceAdd/
     posType/isUsed + usedIdMap + suitKVs；
  5. 背包/穿戴快照（20259/20260/20270/20274 等）：槽位索引 + 物品 Id + 数量 + 描述；
  6. --bonus：把抓到的星魂代入服务端同一套 starSoulBonus 公式算出加成，和面板对账
     （离线参考，读 datatable_json/StarSoulAttributeConfig.json，与服务端运行时无关）；
  7. --op N：对任意 opcode 做泛型 protobuf 递归 dump（字段/类型/值/嵌套），
     用于脚本还没专门解码的消息。

用法：在 server-mysql/ 下运行
  python .\test\inspect_character_build_capture.py <capture.pcapng>
  python .\test\inspect_character_build_capture.py <capture.pcapng> --bonus
  python .\test\inspect_character_build_capture.py <capture.pcapng> --op 20252
  python .\test\inspect_character_build_capture.py <capture.pcapng> --json out.json

依赖 dpkt。.etl 需先用 Packet Monitor 转成 .pcapng；抓包统一放 参考数据/抓包归档/。
"""
from __future__ import annotations

import argparse
import collections
import json
import math
import struct
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import inspect_idle_attribute_capture as base  # noqa: E402

SYNC_ATTRIBUTE = 20169
SYNC_ATTRIBUTE_LIST = 20170
UNIT_CHARACTER = 20003
GET_CHARACTER = 20252
STAR_SOUL_BAG = (20400, 20401, 20402, 20403)
BAG_SNAPSHOT = (20259, 20260, 20270, 20274, 20275)
# BagMap 列表字段号（服务端 pb_extra.encodeBagMapList 的调用点）：1 背包、2 穿戴、
# 3 他人穿戴（20426/20427 GetOtherUserBag）。
BAG_FIELD_NAMES = {1: '背包', 2: '穿戴', 3: '他人穿戴'}

# 六维/战斗 NumericType → 星魂 AttributeType Key（服务端 gemKeyToNumeric 的逆映射）。
NUMERIC_TO_STARSOUL_KEY = {
    1005: 3, 1006: 4, 1007: 5, 1008: 6,
    1009: 7, 1010: 8, 1011: 9, 1012: 10,
    1013: 11, 1014: 12, 1015: 13, 1016: 14,
    1018: 15, 1020: 16, 1019: 17, 1021: 18,
    1017: 19, 1034: 20, 1035: 21,
    1001: 1, 1002: 1, 1003: 2, 1004: 2,
}
STARSOUL_KEY_TO_NUMERIC = {
    1: 1002, 2: 1004, 3: 1005, 4: 1006, 5: 1007, 6: 1008,
    7: 1009, 8: 1010, 9: 1011, 10: 1012, 11: 1013, 12: 1014,
    13: 1015, 14: 1016, 15: 1018, 16: 1020, 17: 1019, 18: 1021,
    19: 1017, 20: 1034, 21: 1035,
}
STARSOUL_KEY_NAMES = {
    1: '生命', 2: '精力', 3: '力量', 4: '敏捷', 5: '精神', 6: '智慧',
    7: '物攻', 8: '精攻', 9: '物防', 10: '精防', 11: '物暴系数', 12: '精暴系数',
    13: '物暴伤害', 14: '精暴伤害', 15: '抗物暴', 16: '抗精暴', 17: '物暴率',
    18: '精暴率', 19: 'Dvo', 20: '体质', 21: '耐力',
    22: '?22', 23: '?23', 25: '?25', 26: '?26', 27: '?27', 28: '?28', 29: '?29',
    30: '?30', 31: '?31',
}


def star_soul_key_label(key: int) -> str:
    return '%s(Key%d)' % (STARSOUL_KEY_NAMES.get(key, '?'), key)


# ===================== 解包工具 =====================

def packed_varints(payload: bytes) -> list[int]:
    out: list[int] = []
    offset = 0
    while offset < len(payload):
        try:
            value, offset = base.read_varint(payload, offset)
        except ValueError:
            break
        out.append(value)
    return out


def packed_floats(payload: bytes) -> list[float]:
    return [struct.unpack_from('<f', payload, i)[0] for i in range(0, len(payload) - 3, 4)]


def nested(payload: object):
    """尝试把 bytes 当成嵌套消息解出来；失败返回 None。"""
    if not isinstance(payload, bytes) or not payload:
        return None
    try:
        return base.decode_protobuf(payload)
    except ValueError:
        return None


def looks_like_text(payload: bytes) -> bool:
    if not payload:
        return False
    printable = sum(1 for b in payload if 32 <= b < 127 or b in (9, 10, 13))
    return printable / len(payload) > 0.9


def render_leaf(payload: bytes, limit: int = 48) -> str:
    if looks_like_text(payload):
        text = payload.decode('utf-8', 'replace')
        return '"%s"' % (text if len(text) <= limit else text[:limit] + '…')
    hexed = payload[:limit].hex()
    return '0x%s%s' % (hexed, '…' if len(payload) > limit else '')


def dump_tree(body: bytes, indent: str = '  ', depth: int = 0, max_depth: int = 6,
              leaf_limit: int = 24) -> None:
    """泛型 protobuf 递归 dump：字段号、wire type、值；bytes 优先按嵌套消息解释。"""
    try:
        fields = base.decode_protobuf(body)
    except ValueError as error:
        print('%s<解码失败: %s> %s' % (indent, error, body[:32].hex()))
        return
    for number in sorted(fields):
        values = fields[number]
        shown = values[:leaf_limit]
        for value in shown:
            if isinstance(value, int):
                print('%s[%d] varint = %d' % (indent, number, value))
                continue
            if len(value) == 4:
                print('%s[%d] fixed32 = %g (0x%s)' % (
                    indent, number, struct.unpack('<f', value)[0], value.hex()))
                continue
            sub = nested(value) if depth < max_depth else None
            if sub:
                print('%s[%d] message:' % (indent, number))
                dump_tree(value, indent + '  ', depth + 1, max_depth, leaf_limit)
            else:
                print('%s[%d] bytes(%d) = %s' % (indent, number, len(value), render_leaf(value)))
        if len(values) > len(shown):
            print('%s[%d] … 共 %d 项' % (indent, number, len(values)))


# ===================== 分帧与提取 =====================

def collect_frames(path: Path):
    frames: list[tuple[float, int, bytes]] = []
    streams = 0
    for _endpoints, data, times in base.reassembled_streams(path):
        got = list(base.frames(data, times))
        if got:
            streams += 1
            frames.extend(got)
    frames.sort(key=lambda item: item[0])
    return frames, streams


def decode_unit_character(body: bytes) -> dict[str, object]:
    fields = base.decode_protobuf(body)
    return {
        'Id': int(base.first(fields, 1)),
        'NickName': (base.first(fields, 2, b'') or b'').decode('utf-8', 'replace')
        if isinstance(base.first(fields, 2, b''), bytes) else '',
        'JobId': int(base.first(fields, 4)),
        'Family': (base.first(fields, 7, b'') or b'').decode('utf-8', 'replace')
        if isinstance(base.first(fields, 7, b''), bytes) else '',
        'SkinId': int(base.first(fields, 8)),
        'CharacterPoint': int(base.first(fields, 9)),
        'SkillPoint': int(base.first(fields, 10)),
        'Level': int(base.first(fields, 11)),
        'Trans': int(base.first(fields, 12)),
        'MaxHp': int(base.first(fields, 14)),
        'MaxMp': int(base.first(fields, 16)),
        'PetId': int(base.first(fields, 17)),
        'isShowPet': bool(base.first(fields, 19)),
        'PetLevel': int(base.first(fields, 20)),
    }


def decode_star_soul_item(body: bytes) -> dict[str, object]:
    fields = base.decode_protobuf(body)
    vice: list[int] = []
    for value in fields.get(9, []):
        if isinstance(value, int):
            vice.append(value)
        else:
            vice.extend(packed_varints(value))
    vice_add: list[float] = []
    for value in fields.get(10, []):
        if isinstance(value, bytes):
            if len(value) == 4:
                vice_add.append(struct.unpack('<f', value)[0])
            else:
                vice_add.extend(packed_floats(value))
        else:
            vice_add.append(float(value))
    return {
        'Id': int(base.first(fields, 1)),
        'typeId': int(base.first(fields, 2)),
        'level': int(base.first(fields, 3)),
        'exp': int(base.first(fields, 4)),
        'posType': int(base.first(fields, 5)),
        'quality': int(base.first(fields, 6)),
        'isUsed': bool(base.first(fields, 7)),
        'main': int(base.first(fields, 8)),
        'vice': vice,
        'viceAdd': vice_add,
        'isLocked': bool(base.first(fields, 11)),
    }


def decode_star_soul_bag(body: bytes) -> dict[str, object]:
    fields = base.decode_protobuf(body)
    items: list[dict[str, object]] = []
    for payload in fields.get(1, []):
        if isinstance(payload, bytes):
            items.append(decode_star_soul_item(payload))
    used: list[int] = []
    for value in fields.get(2, []):
        if isinstance(value, int):
            used.append(value)
        else:
            used.extend(packed_varints(value))
    suits: list[tuple[int, int]] = []
    for payload in fields.get(5, []):
        entry = nested(payload)
        if entry:
            suits.append((int(base.first(entry, 1)), int(base.first(entry, 2))))
    return {'items': items, 'usedIdMap': used, 'suitKVs': suits, 'unitId': int(base.first(fields, 3))}


def merge_star_soul_bag(state: dict[str, object], opcode: int, body: bytes) -> bool:
    """把 20400/20401 全量、20402 单件、20403 穿戴位增量合并进同一份快照。

    20400/20401 的 usedIdMap 是「槽位 → 星魂 Id」定长列表（0 表示空槽），20403 只推
    变化的那一格（key=槽位, value=Id，value=0 表示脱下）。
    """
    fields = base.decode_protobuf(body)
    if opcode in (20400, 20401):
        bag = decode_star_soul_bag(body)
        for item in bag['items']:
            state['items'][item['Id']] = item
        if bag['usedIdMap']:
            state['usedIdMap'] = bag['usedIdMap']
        if bag['suitKVs']:
            state['suitKVs'] = bag['suitKVs']
        state['unitId'] = bag['unitId'] or state['unitId']
    elif opcode == 20402:
        payload = base.first(fields, 2)
        if isinstance(payload, bytes):
            item = decode_star_soul_item(payload)
            state['items'][item['Id']] = item
    elif opcode == 20403:
        slot = int(base.first(fields, 3))
        soul_id = int(base.first(fields, 4))
        used = state['usedSlots']
        if soul_id:
            used[slot] = soul_id
        else:
            used.pop(slot, None)
        suits = []
        for payload in fields.get(5, []):
            entry = nested(payload)
            if entry:
                suits.append((int(base.first(entry, 1)), int(base.first(entry, 2))))
        if suits:
            state['suitKVs'] = suits
    else:
        return False
    state['opcodes'].append(opcode)
    return True


def decode_bag_entry(body: bytes) -> dict[str, object] | None:
    """BagMap{1:Index, 2:NetItem{1:ItemId,2:ItemType,4:Count,...}}；形状不符返回 None。"""
    fields = base.decode_protobuf(body)
    net = base.first(fields, 2)
    if not isinstance(net, bytes):
        return None
    item = nested(net)
    if not item:
        return None
    desc = base.first(item, 7, b'')
    return {
        'Index': int(base.first(fields, 1)),
        'ItemId': int(base.first(item, 1)),
        'ItemType': int(base.first(item, 2)),
        'Count': int(base.first(item, 4)),
        'Desc': desc.decode('utf-8', 'replace') if isinstance(desc, bytes) else '',
    }


def decode_bag_lists(body: bytes) -> list[tuple[int, list[dict[str, object]]]]:
    """返回 [(字段号, 条目列表)]，只保留能解成 BagMap 列表的字段。"""
    fields = base.decode_protobuf(body)
    out: list[tuple[int, list[dict[str, object]]]] = []
    for number in sorted(fields):
        entries: list[dict[str, object]] = []
        for payload in fields[number]:
            if not isinstance(payload, bytes):
                entries = []
                break
            entry = decode_bag_entry(payload)
            if entry is None:
                entries = []
                break
            entries.append(entry)
        if entries:
            out.append((number, entries))
    return out


# ===================== 星魂加成（服务端同一套公式，离线参考） =====================

def load_star_soul_attributes(datatable_dir: Path) -> dict[int, dict[str, object]]:
    path = datatable_dir / 'StarSoulAttributeConfig.json'
    text = path.read_text(encoding='utf-8')
    import re
    text = re.sub(r',\s*([\]}])', r'\1', text)
    rows = json.loads(text)
    return {int(key): row for key, row in rows}


def star_soul_quality_coefficient(quality: int) -> float:
    if quality <= 2:
        return 0.48
    if quality <= 4:
        return 0.8
    return 1.0


def star_soul_bonus(items: list[dict[str, object]], used_ids: set[int],
                    attributes: dict[int, dict[str, object]]) -> dict[int, float]:
    """复刻服务端 starSoulBonus：主属性 ×1.1^等级，副属性 ×(1+档位)×品质系数。"""
    out: dict[int, float] = collections.defaultdict(float)
    for item in items:
        if item['Id'] not in used_ids:
            continue
        add_star_soul_attribute(out, attributes, int(item['main']),
                                math.pow(1.1, float(item['level'])))
        for index, attribute_id in enumerate(item['vice']):
            vice_add = item['viceAdd'][index] if index < len(item['viceAdd']) else 0.0
            add_star_soul_attribute(out, attributes, int(attribute_id),
                                    (1 + vice_add) * star_soul_quality_coefficient(int(item['quality'])))
    return out


def add_star_soul_attribute(out: dict[int, float], attributes: dict[int, dict[str, object]],
                            attribute_id: int, scale: float) -> None:
    row = attributes.get(attribute_id)
    if not row:
        return
    numeric = STARSOUL_KEY_TO_NUMERIC.get(int(row.get('Key', 0)))
    if numeric is None:
        return
    out[numeric] += float(row.get('Value', 0)) * scale


# ===================== 主流程 =====================

def main() -> int:
    parser = argparse.ArgumentParser(description='线上抓包：面板 / 星魂 / 穿戴 拆解对账')
    parser.add_argument('capture', help='.pcapng 抓包文件')
    parser.add_argument('--unit', type=int, help='只打印该单位的属性')
    parser.add_argument('--op', type=int, action='append', default=[],
                        help='对指定 opcode 做泛型 dump（可重复）')
    parser.add_argument('--hex', action='store_true', help='泛型 dump 时同时打印原始字节')
    parser.add_argument('--bonus', action='store_true', help='把星魂代入服务端公式算加成')
    parser.add_argument('--json', help='把解出的面板/星魂/穿戴写成 JSON')
    parser.add_argument('--top', type=int, default=40, help='opcode 统计条数')
    args = parser.parse_args()

    path = Path(args.capture)
    if not path.exists():
        print('找不到抓包：%s' % path)
        return 1

    frames, streams = collect_frames(path)
    if not frames:
        print('没有解出游戏帧：确认抓的是【原版客户端 ↔ 线上服务器】的 TCP，不是本地服')
        return 1

    duration = frames[-1][0] - frames[0][0]
    # 帧时间戳来自 pcapng 绝对时间；统一平移到「抓包起点 = 0s」，便于对着录像看时间点。
    t0 = frames[0][0]
    frames = [(stamp - t0, opcode, body) for stamp, opcode, body in frames]
    print('== 抓包概览 ==')
    print('文件      : %s' % path.name)
    print('TCP 流    : %d' % streams)
    print('游戏帧    : %d' % len(frames))
    print('时长      : %.1fs' % duration)
    print()

    counts = collections.Counter(opcode for _t, opcode, _b in frames)
    print('== opcode 统计 ==')
    for opcode, count in counts.most_common(args.top):
        stamp = min(t for t, op, _b in frames if op == opcode)
        print(' %-6d %-42s x%-5d 首次=%6.2fs' % (opcode, base.opcode_name(opcode), count, stamp))
    print()

    # ---- 面板属性 ----
    panels: dict[int, dict[int, float]] = collections.defaultdict(dict)
    panel_timeline: list[str] = []
    for stamp, opcode, body in frames:
        if opcode not in (SYNC_ATTRIBUTE, SYNC_ATTRIBUTE_LIST):
            continue
        try:
            unit, entries = base.attribute_entries(opcode, body)
        except ValueError:
            continue
        panels[unit].update(entries)
        panel_timeline.append('%6.2fs %d unit=%s %s' % (
            stamp, opcode, unit,
            ' '.join('%s=%g' % (base.numeric_name(k), v) for k, v in entries)))

    print('== 属性推送（%d 次）==' % len(panel_timeline))
    for line in panel_timeline[:60]:
        print('  ' + line)
    if len(panel_timeline) > 60:
        print('  … 共 %d 次，只列前 60 次' % len(panel_timeline))
    print()

    for unit in sorted(panels):
        if args.unit and unit != args.unit:
            continue
        values = panels[unit]
        print('== 单位 %s 的最终面板（%d 项）==' % (unit, len(values)))
        for numeric in sorted(values):
            print('  %5d %-14s %g' % (numeric, base.numeric_name(numeric), values[numeric]))
        print('  -- 基础属性 值/Base/Add --')
        for value_id, base_id, add_id in base.BASE_ATTRIBUTE_GROUPS:
            if value_id not in values and base_id not in values and add_id not in values:
                continue
            print('  %-8s 值=%-10g Base=%-10s Add=%s' % (
                base.numeric_name(value_id), values.get(value_id, 0),
                '%g' % values[base_id] if base_id in values else '(未推送)',
                '%g' % values[add_id] if add_id in values else '(未推送)'))
        print()

    # ---- 角色信息 ----
    characters: dict[int, dict[str, object]] = {}
    for _stamp, opcode, body in frames:
        if opcode not in (UNIT_CHARACTER, GET_CHARACTER):
            continue
        payload = body
        if opcode == GET_CHARACTER:
            fields = base.decode_protobuf(body)
            payload = base.first(fields, 1)
            if not isinstance(payload, bytes):
                continue
        try:
            character = decode_unit_character(payload)
        except ValueError:
            continue
        if character['Id']:
            characters[character['Id']] = character

    if characters:
        print('== 角色信息 ==')
        keys = ['Id', 'NickName', 'JobId', 'Level', 'Trans', 'CharacterPoint', 'SkillPoint',
                'Family', 'SkinId', 'MaxHp', 'MaxMp', 'PetId', 'PetLevel', 'isShowPet']
        for unit in sorted(characters):
            print('  ' + '  '.join('%s=%s' % (k, characters[unit][k]) for k in keys))
        print()

    # ---- 星魂 ----
    soul_state: dict[str, object] = {
        'items': {}, 'usedIdMap': [], 'usedSlots': {}, 'suitKVs': [],
        'unitId': 0, 'opcodes': [],
    }
    for _stamp, opcode, body in frames:
        if opcode not in STAR_SOUL_BAG:
            continue
        try:
            merge_star_soul_bag(soul_state, opcode, body)
        except ValueError:
            continue

    soul_items = list(soul_state['items'].values())
    used_slots: dict[int, int] = soul_state['usedSlots']
    used_from_map = {value for value in soul_state['usedIdMap'] if value}
    used_ids = set(used_from_map) | set(used_slots.values())

    if soul_state['opcodes']:
        print('== 星魂（合并 opcode %s，共 %d 件，穿戴 %d 件）==' % (
            ' '.join(str(op) for op in sorted(set(soul_state['opcodes']))),
            len(soul_items), len(used_ids)))
        for item in sorted(soul_items, key=lambda entry: (entry['posType'], entry['Id'])):
            vice = ' '.join(star_soul_key_label(v) for v in item['vice'])
            wear = '已穿戴' if item['isUsed'] or item['Id'] in used_ids else '背包'
            print('  槽%-2d 类型%-5d 等级%-3d 品质%-2d %-4s 主=%-22s 副=%s' % (
                item['posType'], item['typeId'], item['level'], item['quality'], wear,
                star_soul_key_label(int(item['main'])), vice or '(无)'))
        if soul_state['suitKVs']:
            print('  套装 KVs: %s' % ' '.join('类型%d→Suit%d' % kv for kv in soul_state['suitKVs']))
        if soul_state['usedIdMap']:
            print('  usedIdMap: %s' % soul_state['usedIdMap'])
        if used_slots:
            print('  穿戴槽位(20403): %s' % ' '.join(
                '槽%d→%d' % kv for kv in sorted(used_slots.items())))
        if not soul_items:
            slots = len(soul_state['usedIdMap'])
            print('  星魂背包为空%s' % (
                '（usedIdMap 的 %d 个槽位全部为 0，即没有任何星魂穿戴中）' % slots
                if slots else '（抓包里只有空包响应，没有 20402 单件同步）'))
        print()

    # ---- 背包 / 穿戴 ----
    bag_snapshot: list[tuple[int, list[dict[str, object]]]] = []
    for _stamp, opcode, body in frames:
        if opcode not in BAG_SNAPSHOT:
            continue
        try:
            lists = decode_bag_lists(body)
        except ValueError:
            continue
        size = sum(len(entries) for _field, entries in lists)
        if size and size >= sum(len(entries) for _f, entries in bag_snapshot):
            bag_snapshot = lists
    if bag_snapshot:
        print('== 背包/穿戴快照 ==')
        for field, entries in bag_snapshot:
            print('  字段 %d %s（%d 项）:' % (
                field, BAG_FIELD_NAMES.get(field, '?'), len(entries)))
            for entry in entries:
                print('    槽%-3d 物品%-8d 类型%-3d 数量%-4d %s' % (
                    entry['Index'], entry['ItemId'], entry['ItemType'],
                    entry['Count'], entry['Desc']))
        print()

    # ---- 星魂加成对账 ----
    if args.bonus:
        datatable_dir = Path(__file__).resolve().parent.parent.parent / 'datatable_json'
        if not soul_items:
            print('== 星魂加成 ==\n  抓包里没有星魂数据：请在抓包时打开【背包 → 星魂界面】')
        elif not datatable_dir.exists():
            print('== 星魂加成 ==\n  找不到 %s（离线参考表）' % datatable_dir)
        else:
            attributes = load_star_soul_attributes(datatable_dir)
            if not used_ids:
                used_ids = {item['Id'] for item in soul_items if item['isUsed']}
            bonus = star_soul_bonus(soul_items, used_ids, attributes)
            print('== 星魂加成（服务端 starSoulBonus 复刻，只看已穿戴 %d 件）==' % len(used_ids))
            for numeric in sorted(bonus):
                print('  %5d %-14s +%g' % (numeric, base.numeric_name(numeric), bonus[numeric]))
            print()
            first_unit = next(iter(panels), None)
            if first_unit is not None:
                values = panels[first_unit]
                print('== 面板 vs 星魂（%d 件）==' % len(used_ids))
                for numeric in sorted(set(list(bonus) + [k for k in values if k in
                                                         NUMERIC_TO_STARSOUL_KEY])):
                    if numeric not in values and numeric not in bonus:
                        continue
                    print('  %-14s 面板=%-14g 星魂=%-14g 其余=%-14g' % (
                        base.numeric_name(numeric), values.get(numeric, 0),
                        bonus.get(numeric, 0), values.get(numeric, 0) - bonus.get(numeric, 0)))
                print()

    # ---- 泛型 dump ----
    for opcode in args.op:
        matched = [(stamp, body) for stamp, op, body in frames if op == opcode]
        print('== opcode %d %s：%d 帧 ==' % (opcode, base.opcode_name(opcode), len(matched)))
        for index, (stamp, body) in enumerate(matched[:5]):
            print('  -- #%d @%.2fs len=%d --' % (index, stamp, len(body)))
            dump_tree(body, indent='    ')
            if args.hex:
                print('     raw: %s' % body.hex())
        print()

    if args.json:
        payload = {
            'panels': {str(unit): {str(k): v for k, v in values.items()}
                       for unit, values in panels.items()},
            'characters': {str(unit): character for unit, character in characters.items()},
            'starSoul': {
                'items': soul_items,
                'usedIdMap': soul_state['usedIdMap'],
                'usedSlots': soul_state['usedSlots'],
                'suitKVs': soul_state['suitKVs'],
                'unitId': soul_state['unitId'],
            },
            'bagSnapshots': [[field, entries] for field, entries in bag_snapshot],
        }
        Path(args.json).write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding='utf-8')
        print('已写出 %s' % args.json)

    return 0


if __name__ == '__main__':
    raise SystemExit(main())
