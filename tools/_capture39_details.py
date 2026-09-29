# -*- coding: utf-8 -*-
"""文档 39 那份抓包的「二次挖掘」：在已验证 100% 完整的前提下，再榨出可用细节。

用法：
    python tools/_capture39_details.py _work/pcap_frames/online-control-resist-20260918.jsonl

输出：
  1) 双向 opcode 计数（S2C / C2S 分开）
  2) 5 条流是否互为同一份广播（去重必要性量化）
  3) 关键事件时间线
  4) 20078 伤害帧：负值解码 / 按单位分组 / 同值跳伤间隔（DoT 节拍）
  5) 20170 属性序列：BOSS 叠层节奏与上限、面板 NumericType 普查
  6) 20080 状态帧：图标普查 / 目标 / 时长 / Id 复用
"""
from __future__ import annotations

import collections
import json
import re
import struct
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
OP_GO = ROOT / "server-mysql" / "protocol" / "opcodes.go"

S2C_PORTS = {7757, 7758}


def load_op_names() -> dict:
    txt = OP_GO.read_text(encoding="utf-8", errors="ignore")
    m = {}
    for name, num in re.findall(r"^\s*(Op\w+)\s*=\s*(\d+)\s*$", txt, re.M):
        m.setdefault(int(num), name)
    return m


def f32(s: str) -> float:
    return struct.unpack("<f", bytes.fromhex(s[4:-1]))[0]


def i64(v: int) -> int:
    return v - (1 << 64) if v >= (1 << 63) else v


def load(path: Path):
    """返回 [(stream, t, op, fields)]，并给出 stream → 角色端口映射。"""
    recs = []
    with open(path, "r", encoding="utf-8") as fh:
        for line in fh:
            r = json.loads(line)
            recs.append((r["stream"], r["t"], r["op"], r["fields"]))
    return recs


def stream_port(stream: str) -> int:
    """返回该流中「客户端那一侧」的端口（S2C 时是目的端口，C2S 时是源端口）。"""
    a, b = stream.split(">")
    pa = int(a.split(":")[1])
    pb = int(b.split(":")[1])
    return pb if pa in S2C_PORTS else pa


def is_s2c(stream: str) -> bool:
    return int(stream.split(">")[0].split(":")[1]) in S2C_PORTS


def sec(t):
    return t if t is not None else float("nan")


def main(path: Path):
    names = load_op_names()
    recs = load(path)
    print("总帧 = %d" % len(recs))

    # ---------- 1. 双向 opcode 计数 ----------
    print()
    print("### 1. 双向 opcode 计数")
    s2c_cnt = collections.Counter()
    c2s_cnt = collections.Counter()
    for st, t, op, f in recs:
        (s2c_cnt if is_s2c(st) else c2s_cnt)[op] += 1
    print("  --- 服务器 → 客户端（合计 %d）" % sum(s2c_cnt.values()))
    for op, n in s2c_cnt.most_common():
        print("      %-34s %-6d  (单流均值 %.1f)" % (names.get(op, "??"), n, n / 5))
    print("  --- 客户端 → 服务器（合计 %d）" % sum(c2s_cnt.values()))
    for op, n in c2s_cnt.most_common():
        print("      %-34s %-6d  (单流均值 %.1f)" % (names.get(op, "??"), n, n / 5))

    # ---------- 2. 五条流是否同一份广播 ----------
    print()
    print("### 2. 五条流的内容比对（S2C）")
    per_stream = collections.defaultdict(list)
    for st, t, op, f in recs:
        if is_s2c(st):
            per_stream[stream_port(st)].append((op, json.dumps(f, sort_keys=True)))
    ports = sorted(per_stream)
    ref_port = ports[0]
    ref = per_stream[ref_port]
    print("  以客户端端口 %d 为基准：%d 帧" % (ref_port, len(ref)))
    ref_set = collections.Counter(x[1] for x in ref)
    for p in ports[1:]:
        cur = per_stream[p]
        cur_set = collections.Counter(x[1] for x in cur)
        same = sum((ref_set & cur_set).values())
        print("      vs 端口 %-6d 帧=%-6d 完全相同帧=%-6d (%.1f%%)  仅基准有=%-5d 仅它有=%d"
              % (p, len(cur), same, 100.0 * same / max(len(ref), 1),
                 len(ref) - same, len(cur) - same))
    print("  按 opcode 逐流计数差（基准 − 其他）：")
    ref_op = collections.Counter(op for op, _ in ref)
    for p in ports[1:]:
        cur_op = collections.Counter(op for op, _ in per_stream[p])
        d = {names.get(o, o): ref_op[o] - cur_op[o]
             for o in set(ref_op) | set(cur_op) if ref_op[o] != cur_op[o]}
        print("      vs %-6d %s" % (p, d if d else "完全一致"))
    # 基准流里「私有帧」（在其余 4 条流里都找不到）按 opcode 统计
    others = [set(x[1] for x in per_stream[p]) for p in ports[1:]]
    priv = collections.Counter()
    shared_all = collections.Counter()
    for op, key in ref:
        cnt = sum(1 for s in others if key in s)
        if cnt == 0:
            priv[op] += 1
        elif cnt == len(others):
            shared_all[op] += 1
    print("  基准流私有帧（其余 4 流都没有）按 opcode：")
    for op, n in priv.most_common():
        print("      %-34s %d" % (names.get(op, "??"), n))
    print("  五流共享帧（其余 4 流都有）按 opcode：")
    for op, n in shared_all.most_common():
        print("      %-34s %d" % (names.get(op, "??"), n))

    # ---------- 3. 关键事件时间线 ----------
    print()
    print("### 3. 关键事件时间线（取基准流）")
    watch = {20146: "StartFamilyBossFight", 20047: "InitMainStoryMap",
             20328: "ForceOffLine", 20083: "UnitDead", 20140: "BattleDefeat",
             20355: "FamilyBossInfo", 20142: "GetBossDamageMap"}
    for st, t, op, f in recs:
        nm = names.get(op, "")
        if op in (20083, 20084, 20171) or "Defeat" in nm or "Dead" in nm \
           or "EnterMap" in nm or "LeaveMap" in nm or "FamilyBoss" in nm:
            print("      t=%8.2f  %-32s %s" % (sec(t), nm, json.dumps(f, ensure_ascii=False)[:110]))

    # ---------- 4. 伤害帧 ----------
    print()
    print("### 4. 20078 伤害帧（hp 变化）")
    dmg = collections.defaultdict(list)
    for st, t, op, f in recs:
        if op == 20078 and is_s2c(st) and stream_port(st) == ref_port:
            dmg[f["1"]].append((sec(t), i64(f["2"]), f.get("3")))
    for uid, lst in sorted(dmg.items(), key=lambda kv: -len(kv[1])):
        vals = [v for _, v, _ in lst]
        neg = [v for v in vals if v < 0]
        pos = [v for v in vals if v > 0]
        crit = sum(1 for _, _, c in lst if c)
        print("      单位 %-20d 帧=%-5d 负(掉血)=%-5d 正(治疗)=%-5d 暴击=%d 掉血区间=[%d, %d]"
              % (uid, len(lst), len(neg), len(pos), crit, min(neg) if neg else 0, max(neg) if neg else 0))
    # 同值连跳（DoT 节拍）：同一单位上重复出现同一伤害值的时间间隔
    print("  --- 同一单位「同值伤害」的相邻间隔（中位数排序，候选 DoT 节拍）")
    for uid, lst in sorted(dmg.items(), key=lambda kv: -len(kv[1])):
        byval = collections.defaultdict(list)
        for t, v, _ in lst:
            if v < 0:
                byval[v].append(t)
        best = []
        for v, ts in byval.items():
            if len(ts) < 5:
                continue
            gaps = [round(b - a, 3) for a, b in zip(ts, ts[1:])]
            g = sorted(gaps)
            best.append((len(ts), v, g[len(g) // 2], g[0], g[-1]))
        best.sort(reverse=True)
        for n, v, med, lo, hi in best[:4]:
            print("      %-20d 值=%-10d 次数=%-4d 间隔中位=%.3fs 区间[%.3f, %.3f]"
                  % (uid, v, n, med, lo, hi))

    # ---------- 5. 20170 属性序列 ----------
    print()
    print("### 5. 20170 属性列表（打包下发）")
    series = collections.defaultdict(lambda: collections.defaultdict(list))
    for st, t, op, f in recs:
        if op == 20170 and is_s2c(st) and stream_port(st) == ref_port:
            uid = f["1"]
            blobs = f.get("2")
            if not isinstance(blobs, list):
                blobs = [blobs]
            snap = {}
            for b in blobs:
                if not (isinstance(b, str) and b.startswith("<")):
                    continue
                raw = bytes.fromhex(b.split(" ", 1)[1][:-1])
                j = 0
                while j < len(raw):
                    if raw[j] != 0x08:
                        break
                    j += 1
                    nt = 0
                    sh = 0
                    while True:
                        c = raw[j]
                        j += 1
                        nt |= (c & 0x7F) << sh
                        sh += 7
                        if not c & 0x80:
                            break
                    if j + 5 > len(raw) or raw[j] != 0x15:
                        break
                    v = struct.unpack_from("<f", raw, j + 1)[0]
                    j += 5
                    snap[nt] = v
            if snap:
                series[uid][t].append(snap)
                for nt, v in snap.items():
                    if uid in (50005,) or nt not in (1001,):
                        pass
    for uid in sorted(series):
        pts = sorted(series[uid].items())
        types = collections.Counter()
        for t, snaps in pts:
            for s in snaps:
                for nt in s:
                    types[nt] += 1
        print("      单位 %-20d 推送次数=%-4d 类型=%s"
              % (uid, len(pts), sorted(types)))
    # BOSS 物攻/物防时间序列 → 叠层节奏
    boss = 50005
    if boss in series:
        print("  --- BOSS 50005 关键属性时间序列（每帧取该类型最后值）")
        for nt in (1009, 1011, 1012, 1002, 1017):
            rows = []
            for t, snaps in sorted(series[boss].items()):
                for s in snaps:
                    if nt in s:
                        rows.append((sec(t), s[nt]))
                        break
            uniq = []
            for t, v in rows:
                if not uniq or uniq[-1][1] != v:
                    uniq.append((t, v))
            print("      NumericType %d 变化%d次：" % (nt, len(uniq)))
            for t, v in uniq[:40]:
                print("          t=%8.2f  %s" % (t, v))
            if len(uniq) > 40:
                print("          ... 共 %d 段" % len(uniq))

    # ---------- 6. 状态帧 ----------
    print()
    print("### 6. 20080 状态帧（图标 / 目标 / 时长）")
    states = collections.defaultdict(lambda: collections.defaultdict(list))
    for st, t, op, f in recs:
        if op == 20080 and is_s2c(st) and stream_port(st) == ref_port:
            states[f.get("3")][f.get("2")].append((sec(t), f.get("7"), f.get("1")))
    for icon in sorted(states, key=lambda k: -sum(len(v) for v in states[k].values())):
        tot = sum(len(v) for v in states[icon].values())
        times = collections.Counter()
        for tgt, lst in states[icon].items():
            for _, tm, _ in lst:
                times[tm] += 1
        ids = {i for lst in states[icon].values() for _, _, i in lst}
        print("      %-24s 目标数=%-3d 帧=%-5d 独立Id=%-4d 时长=%s"
              % (icon, len(states[icon]), tot, len(ids), dict(times)))


if __name__ == "__main__":
    main(Path(sys.argv[1]))
