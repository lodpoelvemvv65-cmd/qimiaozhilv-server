# -*- coding: utf-8 -*-
"""从一份战斗抓包里，判断「原版服务端到底有没有拿玩家的抗性去削怪物控制」。

实验思路（结论见 文档/40-线上抓包实测结论-技能-盾-治疗-抗性-2026-09-18.md）：
  配置里怪物控制技能都写死了几率（35/65/85/95%）。抓一份原版战斗包，
  数「怪物放了几次控制技能」（20075）与「玩家身上出现几次控制图标」（20080），
  得到**实测落地率**，再和配置几率比：
    * 实测 ≈ 配置          ⇒ 抗性不参与（按配置原值）
    * 实测 明显 < 配置     ⇒ 抗性参与，且可以反推倍率
  线上抓包**不推 1033（抵抗）**，但会推 1007（精神）/1035（耐力），
  用 CharacterGrowth 行 261-264 可以算出**成长那部分的抗性**（装备部分未知）。

用法：
    python tools/_control_resist_report.py <capture.pcapng|frames.jsonl>
"""
from __future__ import annotations

import collections
import io
import json
import struct
import sys
from pathlib import Path

sys.stdout.reconfigure(encoding="utf-8")
ROOT = Path(__file__).resolve().parent.parent

# 9 个控制技能 → (配置几率%, 落地图标名, 控制类型)
# 注意 500013「泥石击」是**石化**但用的是 bufficon_silence 图标（原版配置如此）
CONTROL = {
    500006: (35, "bufficon_vertigo", "眩晕"),
    500020: (35, "bufficon_vertigo", "眩晕"),
    500039: (65, "bufficon_vertigo", "眩晕"),
    500040: (85, "bufficon_vertigo", "眩晕"),
    500041: (95, "bufficon_vertigo", "眩晕"),
    500011: (35, "bufficon_silence", "沉默"),
    500025: (35, "bufficon_silence", "沉默"),
    500013: (35, "bufficon_silence", "石化(用沉默图标)"),
    500027: (35, "bufficon_silence", "石化(用沉默图标)"),
}

# CharacterGrowth 行 261-264：抵抗 = Spi×a + Sta×b（行号 = attributeType*10 + 职业族）
RESIST_GROWTH = {
    1: ("军官", 0.00028, 1.2),
    2: ("运动员", 0.00014, 0.35),
    3: ("护士", 0.00018, 0.65),
    4: ("超能力", 0.00014, 0.4),
}

NUMERIC = {1001: "当前HP", 1002: "最大HP", 1007: "精神Spi", 1035: "耐力Sta", 1033: "抵抗Res"}


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


def main() -> None:
    path = Path(sys.argv[1])
    casts = collections.Counter()        # (unit, skill) -> n   （怪物施法）
    states = collections.defaultdict(list)  # (icon, unit) -> [t]
    numeric = {}                         # (unit, numeric) -> value
    for t, op, f in read_frames(path):
        if op == 20075:
            casts[(int((f.get(1) or [0])[0]), int((f.get(2) or [0])[0]))] += 1
        elif op == 20080:
            icon = txt((f.get(3) or [""])[0])
            if icon:
                states[(icon, int((f.get(2) or [0])[0]))].append(t)
        elif op == 20169:
            numeric[(int((f.get(1) or [0])[0]), int((f.get(2) or [0])[0]))] = f32((f.get(3) or [0])[0])

    print("=" * 78)
    print("抓包：%s" % path.name)
    print("=" * 78)

    print("\n[1] 怪物放过的控制技能（20075）")
    per_skill = collections.Counter()
    per_unit = collections.defaultdict(collections.Counter)
    for (unit, skill), n in casts.items():
        if skill in CONTROL:
            per_skill[skill] += n
            per_unit[unit][skill] += n
    if not per_skill:
        print("    **这份抓包里没有任何怪物控制技能施法** —— 实验无效，需要重打。")
        return
    for skill, n in per_skill.most_common():
        chance, icon, kind = CONTROL[skill]
        print("    skill %-7d %-14s 配置 %3d%% → %-18s 施法 %d 次"
              % (skill, kind, chance, icon, n))
    for unit, skills in per_unit.items():
        print("      怪物 unit=%-14s 施法：%s"
              % (unit, ", ".join("%d×%d" % (s, c) for s, c in skills.most_common())))

    print("\n[2] 玩家身上的控制图标（20080）")
    if not states:
        print("    一次都没落地")
    for (icon, unit), ts in sorted(states.items(), key=lambda kv: -len(kv[1])):
        ts.sort()
        gaps = ["%.1f" % (b - a) for a, b in zip(ts, ts[1:])]
        print("    %-18s unit=%-14s x%-3d 间隔=[%s]"
              % (icon, unit, len(ts), ",".join(gaps[:8])))

    print("\n[3] 按技能算落地率（实测 vs 配置）")
    icons_total = collections.Counter()
    for (icon, _unit), ts in states.items():
        icons_total[icon] += len(ts)
    # 「独立落地次数」：相邻帧间隔 < 1.5s 视为同一段控制（Refresh 会重复发帧）
    windows = collections.Counter()
    for (icon, _unit), ts in states.items():
        ts = sorted(ts)
        n = 0
        prev = None
        for t in ts:
            if prev is None or t - prev > 1.5:
                n += 1
            prev = t
        windows[icon] += n
    print("    （图标帧数 vs 独立控制段数 —— Refresh 重施会让帧数偏多，段数更接近真实落地次数）")
    for icon, n in icons_total.most_common():
        print("      %-18s 帧数 %-4d 独立段数 %d" % (icon, n, windows.get(icon, 0)))
    for skill, n in per_skill.most_common():
        chance, icon, kind = CONTROL[skill]
        landed = windows.get(icon, 0)   # 用独立段数，避免 Refresh 重复计数
        rate = 100.0 * landed / n if n else 0.0
        verdict = ""
        if n >= 20:
            if rate > chance * 0.8:
                verdict = "≈ 配置 ⇒ **抗性不参与**"
            elif rate < chance * 0.6:
                verdict = "明显低于配置 ⇒ **抗性参与**"
            else:
                verdict = "略低于配置（样本偏少，不好下结论）"
        else:
            verdict = "样本不足（<20 次）"
        print("    skill %-7d 配置 %3d%%  施法 %-4d 该图标落地 %-4d 实测 %5.1f%%   %s"
              % (skill, chance, n, landed, rate, verdict))
    print("    ⚠ 多个怪物同时攻击时，图标无法归属到具体某次施法；")
    print("      500013/500011/500025/500027 共用 bufficon_silence 图标，也会互相串。")

    print("\n[4] 面板里的六维（用于反推成长抗性）")
    units = sorted({u for (u, k) in numeric})
    if not units:
        print("    抓包里没有 20169")
    for u in units:
        spi = numeric.get((u, 1007))
        sta = numeric.get((u, 1035))
        if spi is None and sta is None:
            continue
        print("    unit=%-14s 精神=%s 耐力=%s" % (u, spi, sta))
        if spi is None or sta is None:
            continue
        print("        按 CharacterGrowth 行 261-264 推算**成长抗性**（装备部分未知）：")
        for fam, (name, a, b) in RESIST_GROWTH.items():
            res = spi * a + sta * b
            print("          职业族%d %-5s 抵抗≈%-10.1f ⇒ 配置95%% → %.1f%%（100/(100+抵抗)=%.4f）"
                  % (fam, name, res, 95 * 100 / (100 + res), 100 / (100 + res)))

    print("\n[5] 怎么读这个结果")
    print("    · 若 [3] 里实测率 ≈ 配置值  ⇒ **原版不拿抗性削控制**（我们的实现是加戏）")
    print("    · 若实测率 明显 < 配置值    ⇒ 原版确实削，再用 [4] 的成长抗性反推公式形状")
    print("    · 样本量：一个技能至少 20 次施法才有意义；抗性越高控制越少，越难测出来")


if __name__ == "__main__":
    main()
