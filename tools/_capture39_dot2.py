# -*- coding: utf-8 -*-
"""DoT 节拍二次核实：把每个 DoT 施加事件和它之后该目标的全部 20078 帧按时序摆出来。

用法：python tools/_capture39_dot2.py <jsonl>
"""
from __future__ import annotations

import collections
import json
import sys
from pathlib import Path

S2C = {7757, 7758}
REF = 61643
ICONS = ("bufficon_poisoning", "bufficon_bleed", "bufficon_light", "bufficon_constrea")
NAMES = {"bufficon_poisoning": "中毒", "bufficon_bleed": "流血",
         "bufficon_light": "点燃", "bufficon_constrea": "持续治疗"}


def i64(v):
    return v - (1 << 64) if v >= (1 << 63) else v


def main(path: Path):
    dots, dmg = [], collections.defaultdict(list)
    for line in open(path, encoding="utf-8"):
        r = json.loads(line)
        s, t, op, f = r["stream"], r["t"], r["op"], r["fields"]
        a, b = s.split(">")
        if int(a.split(":")[1]) not in S2C:
            continue
        if int(b.split(":")[1]) != REF:
            continue
        if op == 20080 and f.get("3") in ICONS:
            dots.append((t, f.get("3"), f.get("2"), f.get("7"), f.get("6"), f.get("8")))
        elif op == 20078:
            dmg[f.get("1")].append((t, i64(f.get("2"))))
    dots.sort()

    print("### 每个 DoT 施加事件之后该目标的伤害帧（完整摆开，不筛选）")
    for t0, icon, tgt, dur, typ, isbuff in dots:
        win = dmg.get(tgt, [])
        win = [(t, v) for t, v in win if t0 - 0.5 <= t <= t0 + (dur or 0) / 1000.0 + 3]
        print()
        print("  %s 目标=%s 施加 t=%.2f Time=%sms Type=%s IsBuff=%s"
              % (NAMES[icon], tgt, t0, dur, typ, isbuff))
        prev = t0
        for t, v in win:
            print("      +%6.2f  %12d" % (t - t0, v))
    print()
    print("### 全体「相邻负向伤害帧」间隔直方图（0.1s 分箱，只取 <20s）")
    hist = collections.Counter()
    for u, lst in dmg.items():
        neg = [t for t, v in lst if v < 0]
        for a, b in zip(neg, neg[1:]):
            d = b - a
            if d < 20:
                hist[round(d * 10) / 10] += 1
    for k in sorted(hist, key=lambda k: -hist[k])[:14]:
        print("      %5.1fs  ×%d" % (k, hist[k]))


if __name__ == "__main__":
    main(Path(sys.argv[1]))
