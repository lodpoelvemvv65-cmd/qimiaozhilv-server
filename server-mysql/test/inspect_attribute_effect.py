#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""从一份战斗抓包里判断「某个属性到底有没有生效」。

背景：属性本身不在协议里，客户端只看得到**效果**。所以判据只能是
「客户端可见的输出」——这份脚本把抓包里所有能被属性影响的输出统计出来，
再逐条给出「这个属性能不能靠抓包验证」的结论。

用法（抓包支持 pcapng 与「已解码帧的 jsonl」两种）：
    python .\\test\\inspect_attribute_effect.py <capture.pcapng|frames.jsonl>
    python .\\test\\inspect_attribute_effect.py <capture> --control bufficon_vertigo --skill 500041

输出的六个小节：
    1. 会话与时长
    2. 施法（20075）—— 按 unit/skill 计数
    3. 状态落地（20080）—— 按 icon/unit 计数 + 相邻间隔（判断是否在持续内重施）
    4. 血量变化（20078）—— 正数=治疗 / 负数=伤害，并统计暴击次数
    5. 面板数值（20169/20170）—— 含 1033 抵抗等；这是**读出属性值**的唯一途径
    6. 结论 —— 控制落地率 = 状态帧数 / 该技能施法次数

注意（决定了哪些属性**验不了**）：
    * 怪物→玩家的**直伤必中**，本来就不参与命中/抗性判定 ⇒ 直伤里验不出「命中」。
    * **被护盾完全吸收的一击不会发任何帧**（`battle_skill_events.go` 的
      `case CombatEventDamage: if event.Amount > 0`），所以护盾挡了多少**看不到**。
    * 自动战斗里被控制拦下的施法**不发包**（`castSkillLockedWithLogAtHeld` 用的是
      丢弃的 resp），所以「眩晕导致不能动」只能靠 20080 的图标判断，看不到拒绝。
    * 暴击可见：20078 带 `IsCrit` 字段。
"""

from __future__ import annotations

import argparse
import collections
import io
import json
import struct
import sys
from pathlib import Path

sys.stdout.reconfigure(encoding="utf-8")

sys.path.insert(0, str(Path(__file__).resolve().parent))
from inspect_combat_capture import (  # noqa: E402
    decode_protobuf, first, frames, reassembled_streams, signed32,
)

OPCODES = {20075, 20076, 20077, 20078, 20080, 20169, 20170, 20083}
NAMES = {
    20075: "PlaySkill", 20076: "MonsterPlaySkill", 20077: "PlaySkillEffect",
    20078: "BattleSkillRet", 20080: "BattleChangeState",
    20169: "SyncUnitAttribute", 20170: "SyncUnitAttributeList", 20083: "UnitDead",
}


def read_frames(path: Path):
    """yield (t, opcode, fields: dict[int, list])。支持 pcapng 与 jsonl。"""
    if path.suffix.lower() == ".jsonl":
        for line in io.open(path, encoding="utf-8"):
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            if row.get("op") not in OPCODES:
                continue
            # jsonl 的字段键是字符串，统一成 int，跟 pcapng 路径一致
            raw = row.get("fields") or {}
            fields = {int(k): (v if isinstance(v, list) else [v])
                      for k, v in raw.items() if str(k).lstrip("-").isdigit()}
            yield float(row.get("t") or 0), int(row["op"]), fields
        return
    for _endpoints, data, byte_times in reassembled_streams(path):
        for timestamp, opcode, body in frames(data, byte_times):
            if opcode not in OPCODES:
                continue
            try:
                yield timestamp, opcode, decode_protobuf(body)
            except ValueError:
                continue


def as_int(value, default=0):
    return int(value) if isinstance(value, int) else default


def as_float(value, default=0.0):
    if isinstance(value, bytes) and len(value) == 4:
        return struct.unpack("<f", value)[0]
    if isinstance(value, (int, float)):
        return float(value)
    return default


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("capture", type=Path)
    parser.add_argument("--control", default="bufficon_vertigo",
                        help="要评估的控制 buff 图标名（默认眩晕）")
    parser.add_argument("--skill", type=int, default=0,
                        help="控制技能的技能 ID（如 500041）；给了才算落地率")
    args = parser.parse_args()

    rows = list(read_frames(args.capture))
    if not rows:
        print("没读到任何战斗帧。")
        return
    rows.sort(key=lambda r: r[0])
    t0, t1 = rows[0][0], rows[-1][0]

    casts = collections.Counter()          # (unit, skill)
    states = collections.defaultdict(list)  # (icon, unit) -> [t]
    heal_hits = crit_hits = dmg_hits = 0
    heal_total = dmg_total = 0
    numeric = {}                            # (unit, numeric) -> value
    dead = collections.Counter()

    for t, op, f in rows:
        if op == 20075:
            casts[(as_int(first(f, 1)), as_int(first(f, 2)))] += 1
        elif op == 20080:
            icon = first(f, 3, "")
            if isinstance(icon, bytes):
                icon = icon.decode("utf-8", "replace")
            if icon:
                states[(icon, as_int(first(f, 2)))].append(t)
        elif op == 20078:
            delta = signed32(first(f, 2))
            if delta > 0:
                heal_hits += 1
                heal_total += delta
            elif delta < 0:
                dmg_hits += 1
                dmg_total += -delta
                if bool(first(f, 3)):
                    crit_hits += 1
        elif op == 20169:
            numeric[(as_int(first(f, 1)), as_int(first(f, 2)))] = as_float(first(f, 3))
        elif op == 20170:
            for entry in (f.get(2) or []):
                pass  # 20170 是打包列表，字段结构见协议；这里只登记条数
        elif op == 20083:
            dead[as_int(first(f, 1))] += 1

    print("=" * 78)
    print("抓包：%s" % args.capture.name)
    print("帧数 %d，时长 %.1fs，覆盖 %d 个会话/单位"
          % (len(rows), t1 - t0, len({u for u, _ in casts})))
    print("=" * 78)

    print("\n[2] 施法（20075）—— unit=施法者 skill=技能")
    for (unit, skill), n in casts.most_common(20):
        print("    unit=%-12s skill=%-8s x%d" % (unit, skill, n))

    print("\n[3] 状态落地（20080）—— icon + 单位 + 次数（括号内是相邻间隔）")
    for (icon, unit), ts in sorted(states.items(), key=lambda kv: -len(kv[1])):
        ts.sort()
        gaps = ["%.1f" % (b - a) for a, b in zip(ts, ts[1:])]
        print("    %-24s unit=%-12s x%-3d 间隔=[%s]"
              % (icon, unit, len(ts), ",".join(gaps[:10])))

    print("\n[4] 血量变化（20078）")
    print("    治疗 %d 次，合计 %d；伤害 %d 次，合计 %d；其中暴击 %d 次（暴击率 %.1f%%）"
          % (heal_hits, heal_total, dmg_hits, dmg_total, crit_hits,
             (100.0 * crit_hits / dmg_hits) if dmg_hits else 0.0))
    print("    ⚠ 被护盾**完全吸收**的一击不发任何帧 ⇒ 这里的「伤害次数」偏少是正常的")

    print("\n[5] 面板数值（20169）—— 这是从抓包**读出属性值**的唯一途径")
    if not numeric:
        print("    （没有 20169；20170 是打包下发，需要另解）")
    else:
        keys = sorted({k for _, k in numeric})
        print("    出现过的 NumericType：%s" % keys)
        for key in (1002, 1009, 1010, 1011, 1012, 1031, 1032, 1033):
            vals = sorted({(u, v) for (u, k), v in numeric.items() if k == key})
            if vals:
                print("    %-6d %s" % (key, ", ".join("unit%s=%g" % (u, v) for u, v in vals[:6])))
        if 1033 not in keys:
            print("    ⚠ **没有 1033（抵抗）** ⇒ 这份抓包读不出抗性，")
            print("      只能改用「同一角色改装备前后对照」的实验设计（见 文档/39）")

    print("\n[6] 结论")
    if not args.skill:
        print("    （用 --skill <技能ID> 指定控制技能，才能算落地率）")
        return
    caster_counts = collections.Counter()
    for (unit, skill), n in casts.items():
        if skill == args.skill:
            caster_counts[unit] += n
    total = sum(caster_counts.values())
    if not total:
        print("    这份抓包里没有 skill=%d 的施法，无法计算落地率。" % args.skill)
        return
    print("    技能 %d 施法 %d 次，施法者：%s"
          % (args.skill, total, ", ".join("unit%s x%d" % (u, n) for u, n in caster_counts.most_common())))
    hits = {u: len(ts) for (icon, u), ts in states.items() if icon == args.control}
    if not hits:
        print("    %s 一次都没落地 ⇒ 落地率 0%%" % args.control)
    else:
        for u, n in sorted(hits.items(), key=lambda kv: -kv[1]):
            print("    %s 在 unit%s 落地 %d 次 ⇒ 该单位落地率 %.1f%%（单目标技能可直接这么算）"
                  % (args.control, u, n, 100.0 * n / total))
    print("    ⚠ AoE（敌方全体）技能会对**每个被覆盖的单位**各滚一次，")
    print("      此时「某单位落地次数 / 施法次数」才是正确口径，不要把各单位的次数相加。")
    res = sorted({v for (u, k), v in numeric.items() if k == 1033})
    if res:
        for r in res:
            print("    抓到抗性 %g ⇒ 理论落地率 = 配置 × 100/(100+%g)" % (r, r))
    else:
        print("    ⚠ 抓包里没有 1033（抵抗）⇒ 只能做「同角色改装备前后对照」的实验（见 文档/39）")


if __name__ == "__main__":
    main()
