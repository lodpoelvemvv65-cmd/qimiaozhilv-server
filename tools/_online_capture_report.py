# -*- coding: utf-8 -*-
"""线上抓包总报告：一次性回答本轮复核里所有「有疑问」的点。

实测结论见 文档/40-线上抓包实测结论-技能-盾-治疗-抗性-2026-09-18.md。

用法：
    python tools/_online_capture_report.py <capture.pcapng|frames.jsonl>

输出 9 节（对应操作清单里的 A~I）：
  A 抗性 vs 怪物控制     控制落地率 vs 配置几率   ← 核心
  B 面板 NumericType     线上推了哪些属性（推不推 1033）
  C DoT 节拍             同值伤害帧的间隔
  D 暴击                  暴击率 + 暴击/非暴击伤害对比
  E 伤害分布              正负血量变化的量级分布
  F 护盾帧字段            20080 护盾帧带不带数值
  G 同 icon 的 Id 集合    重施同一 buff 时 Id 变不变
  H 控制图标清单          各控制技能命中时用哪个图标
  I 控制重施间隔          同一单位控制帧的间隔 vs 持续
"""
from __future__ import annotations

import collections
import io
import json
import statistics
import struct
import sys
from pathlib import Path

sys.stdout.reconfigure(encoding="utf-8")
ROOT = Path(__file__).resolve().parent.parent

CONTROL = {
    500006: (35, "bufficon_vertigo", "眩晕"),
    500020: (35, "bufficon_vertigo", "眩晕"),
    500039: (65, "bufficon_vertigo", "眩晕"),
    500040: (85, "bufficon_vertigo", "眩晕"),
    500041: (95, "bufficon_vertigo", "眩晕"),
    500011: (35, "bufficon_silence", "沉默"),
    500025: (35, "bufficon_silence", "沉默"),
    500013: (35, "bufficon_silence", "石化(配置用沉默图标)"),
    500027: (35, "bufficon_silence", "石化(配置用沉默图标)"),
}
CONTROL_ICONS = {v[1] for v in CONTROL.values()}
NUMERIC = {
    1001: "当前HP", 1002: "最大HP", 1003: "当前MP", 1004: "最大MP",
    1005: "力量", 1006: "敏捷", 1007: "精神", 1008: "智慧",
    1009: "物攻", 1010: "魔攻", 1011: "物防", 1012: "精神防御",
    1013: "物暴率", 1014: "魔暴率", 1015: "物暴值", 1016: "魔暴值",
    1017: "辅助值", 1018: "抗物暴率", 1019: "抗物暴值", 1020: "抗魔暴率", 1021: "抗魔暴值",
    1022: "物免伤", 1023: "魔免伤", 1026: "等级", 1027: "经验", 1029: "转生",
    1031: "速度", 1032: "命中", 1033: "抵抗", 1034: "体质", 1035: "耐力",
    1036: "皮肤", 1042: "吸血率", 1043: "吸血量", 1044: "生命回复",
    1045: "物理增伤", 1046: "精神增伤",
}


def read_frames(path: Path):
    if path.suffix.lower() == ".jsonl":
        for line in io.open(path, encoding="utf-8"):
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            raw = row.get("fields") or {}
            fields = {int(k): (v if isinstance(v, list) else [v])
                      for k, v in raw.items() if str(k).lstrip("-").isdigit()}
            yield float(row.get("t") or 0), int(row["op"]), fields
        return
    sys.path.insert(0, str(ROOT / "server-mysql" / "test"))
    from inspect_combat_capture import decode_protobuf, frames, reassembled_streams
    for _ep, data, times in reassembled_streams(path):
        for ts, op, body in frames(data, times):
            try:
                yield ts, op, decode_protobuf(body)
            except ValueError:
                continue


def i32(v):
    v = int(v) & 0xFFFFFFFF
    return v - 0x100000000 if v & 0x80000000 else v


def f32(v):
    if isinstance(v, bytes) and len(v) == 4:
        return struct.unpack("<f", v)[0]
    return float(v) if isinstance(v, (int, float)) else 0.0


def txt(v):
    return v.decode("utf-8", "replace") if isinstance(v, bytes) else (v or "")


def head(title):
    print("\n" + "=" * 78)
    print(title)
    print("=" * 78)


def main() -> None:
    path = Path(sys.argv[1])
    casts = collections.Counter()          # (unit, skill)
    states = collections.defaultdict(list)  # (icon, unit) -> [(t, id, time_ms, ctype)]
    hps = []                                # (t, unit, delta, crit)
    numeric = collections.defaultdict(dict)  # unit -> {numeric: value}
    dead = collections.Counter()

    for t, op, f in read_frames(path):
        if op == 20075:
            casts[(int((f.get(1) or [0])[0]), int((f.get(2) or [0])[0]))] += 1
        elif op == 20080:
            icon = txt((f.get(3) or [""])[0])
            if not icon:
                continue
            states[(icon, int((f.get(2) or [0])[0]))].append(
                (t, int((f.get(1) or [0])[0]), int((f.get(7) or [0])[0]), int((f.get(6) or [0])[0])))
        elif op == 20078:
            hps.append((t, int((f.get(1) or [0])[0]), i32((f.get(2) or [0])[0]), bool((f.get(3) or [0])[0])))
        elif op == 20169:
            numeric[int((f.get(1) or [0])[0])][int((f.get(2) or [0])[0])] = f32((f.get(3) or [0])[0])
        elif op == 20083:
            dead[int((f.get(1) or [0])[0])] += 1

    print("=" * 78)
    print("线上抓包总报告：%s" % path.name)
    print("=" * 78)

    # ---------- A ----------
    head("A 抗性 vs 怪物控制（核心）")
    per_skill = collections.Counter()
    for (unit, skill), n in casts.items():
        if skill in CONTROL:
            per_skill[skill] += n
    if not per_skill:
        print("  **没有任何怪物控制技能施法** ⇒ 这次实验的 A 项无效（需要重打，见操作清单步骤 3）")
    else:
        windows = collections.Counter()
        frames_cnt = collections.Counter()
        for (icon, _u), items in states.items():
            items.sort()
            frames_cnt[icon] += len(items)
            n, prev = 0, None
            for t, *_ in items:
                if prev is None or t - prev > 1.5:
                    n += 1
                prev = t
            windows[icon] += n
        print("  控制图标：帧数 / 独立段数（独立段数≈真实落地次数，Refresh 会多发帧）")
        for icon, n in frames_cnt.most_common():
            if icon in CONTROL_ICONS:
                print("    %-18s 帧数 %-4d 独立段 %d" % (icon, n, windows.get(icon, 0)))
        print()
        for skill, n in per_skill.most_common():
            chance, icon, kind = CONTROL[skill]
            landed = windows.get(icon, 0)
            rate = 100.0 * landed / n if n else 0.0
            if n < 20:
                verdict = "样本不足（<20 次施法）"
            elif rate > chance * 0.8:
                verdict = "≈ 配置 ⇒ **原版不拿抗性削控制**"
            elif rate < chance * 0.6:
                verdict = "明显低于配置 ⇒ **原版确实削**"
            else:
                verdict = "略低（样本偏少）"
            print("    skill %-7d %-16s 配置 %3d%%  施法 %-4d 落地 %-4d 实测 %5.1f%%   %s"
                  % (skill, kind, chance, n, landed, rate, verdict))

    # ---------- B ----------
    head("B 原版面板推了哪些 NumericType")
    if not numeric:
        print("  没有 20169（可能是 20170 打包下发，需要另解）")
    for unit, fields in sorted(numeric.items()):
        keys = sorted(fields)
        print("  unit=%-16s 共 %d 个键" % (unit, len(keys)))
        print("    %s" % ", ".join("%d=%s(%g)" % (k, NUMERIC.get(k, "?"), fields[k]) for k in keys))
    allkeys = {k for fields in numeric.values() for k in fields}
    print("\n  **1033（抵抗）%s**" % ("有 ✓" if 1033 in allkeys else "没有 ✗ ⇒ 线上抓包读不出抗性"))
    print("  **1032（命中）%s / 1031（速度）%s**"
          % ("有" if 1032 in allkeys else "没有", "有" if 1031 in allkeys else "没有"))

    # ---------- C ----------
    head("C DoT 节拍：同一个伤害值重复出现的间隔")
    neg = collections.defaultdict(list)
    for t, unit, delta, _c in hps:
        if delta < 0:
            neg[(unit, delta)].append(t)
    rows = []
    for (unit, delta), ts in neg.items():
        if len(ts) < 3:
            continue
        ts.sort()
        gaps = [b - a for a, b in zip(ts, ts[1:])]
        rows.append((len(ts), unit, -delta, statistics.median(gaps), min(gaps), max(gaps)))
    rows.sort(reverse=True)
    if not rows:
        print("  没有「同一个伤害值重复 ≥3 次」的样本 —— 这次没吃到 DoT")
    for n, unit, dmg, med, lo, hi in rows[:12]:
        flag = "  ← 像 DoT（节拍稳定）" if hi - lo < 0.6 else ""
        print("  unit=%-14s 伤害=%-9d 出现 %-3d 次  间隔 中位 %.2fs [%.2f, %.2f]%s"
              % (unit, dmg, n, med, lo, hi, flag))

    # ---------- D ----------
    head("D 暴击")
    dmg_crit = [abs(d) for _t, _u, d, c in hps if d < 0 and c]
    dmg_norm = [abs(d) for _t, _u, d, c in hps if d < 0 and not c]
    heal_crit = [d for _t, _u, d, c in hps if d > 0 and c]
    heal_norm = [d for _t, _u, d, c in hps if d > 0 and not c]
    total = len(dmg_crit) + len(dmg_norm)
    print("  伤害：%d 次，其中暴击 %d 次（暴击率 %.1f%%）"
          % (total, len(dmg_crit), (100.0 * len(dmg_crit) / total) if total else 0))
    for name, arr in (("非暴击", dmg_norm), ("暴击", dmg_crit)):
        if arr:
            print("    %-6s n=%-4d 中位 %-9d 最小 %-9d 最大 %d"
                  % (name, len(arr), int(statistics.median(arr)), min(arr), max(arr)))
    if dmg_crit and dmg_norm:
        print("    ⇒ 暴击/非暴击中位比 = %.3f（我们服务端的公式是 clamp(1.5 + 暴击效果/100, 1, 10)）"
              % (statistics.median(dmg_crit) / statistics.median(dmg_norm)))
    print("  治疗：%d 次，其中暴击 %d 次" % (len(heal_crit) + len(heal_norm), len(heal_crit)))

    # ---------- E ----------
    head("E 血量变化分布")
    print("  负数（受伤）%d 次，正数（治疗）%d 次" % (len(dmg_norm) + len(dmg_crit), len(heal_norm) + len(heal_crit)))
    cnt = collections.Counter(abs(d) for _t, _u, d, _c in hps if d < 0)
    print("  最常见的受伤值 Top10：%s" % cnt.most_common(10))
    print("  ⚠ 被护盾完全吸收的一击不会发任何帧（服务端 `if event.Amount > 0`），所以受伤次数偏少正常")

    # ---------- F ----------
    head("F 护盾帧带不带数值")
    shield = [items for (icon, _u), items in states.items() if icon == "bufficon_shield"]
    if not shield:
        print("  这次没抓到护盾帧")
    for items in shield:
        for t, sid, tms, ctype in items[:5]:
            print("  t=%.1fs Id=%-22s Time=%-7s Type=%s  ⇒ 只有时长，没有盾量" % (t, sid, tms, ctype))
        break

    # ---------- G ----------
    head("G 同一个 icon 的 Id 集合（重施同一 buff 时 Id 变不变）")
    for icon in sorted({i for i, _u in states}):
        ids = collections.Counter(sid for (i, _u), items in states.items() if i == icon for _t, sid, _m, _c in items)
        if len(ids) > 1:
            print("  %-20s 出现 %d 个不同 Id（前 6 个：%s）"
                  % (icon, len(ids), list(ids)[:6]))
    print("  （只出现 1 个 Id 的 icon 已省略；Id 多说明原版对每次施加都发新 Id）")

    # ---------- H ----------
    head("H 控制技能命中时用的图标")
    if not per_skill:
        print("  这次没抓到怪物控制技能")
    for skill in sorted(per_skill):
        chance, icon, kind = CONTROL[skill]
        n = sum(len(items) for (i, _u), items in states.items() if i == icon)
        print("  skill %-7d %-16s 配置图标 %-18s 该图标共出现 %d 帧" % (skill, kind, icon, n))
    for icon in sorted({i for i, _u in states if i in CONTROL_ICONS}):
        print("    ⇒ 实际抓到的控制图标：%s" % icon)

    # ---------- I ----------
    head("I 控制重施间隔 vs 持续")
    for (icon, unit), items in states.items():
        if icon not in CONTROL_ICONS:
            continue
        items.sort()
        gaps = [b[0] - a[0] for a, b in zip(items, items[1:])]
        if gaps:
            print("  %-18s unit=%-14s %d 帧  最小间隔 %.2fs 中位 %.2fs  → 持续内重施 %s"
                  % (icon, unit, len(items), min(gaps), statistics.median(gaps),
                     "有" if min(gaps) < 10 else "没有"))

    head("死亡")
    for unit, n in dead.most_common():
        print("  unit=%s 死亡 %d 次" % (unit, n))
    if not dead:
        print("  没有死亡帧")


if __name__ == "__main__":
    main()
