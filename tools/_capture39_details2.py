# -*- coding: utf-8 -*-
"""文档 39 抓包 · 第二轮挖掘（针对第一轮暴露出的新疑点）。

用法：
    python tools/_capture39_details2.py <jsonl> <同名.pcapng>

覆盖：
  1) 每条流的 t=0 对应哪个墙钟时刻（跨流比时间必须先对齐，第一轮发现的坑）
  2) 20169（单推）与 20170（列表）分别推了什么；BOSS 属性序列 → 叠层节奏/上限
  3) 20080 的 Id 是否真的「同一 Id 连发几帧」（核对文档 39 §5 坑#7）
  4) DoT：玩家负向伤害帧的时间序列与间隔（DoT 跳伤到底走不走 20078）
  5) C2S：客户端到底发了什么（有没有技能请求）
"""
from __future__ import annotations

import collections
import datetime
import json
import re
import socket
import struct
import sys
from pathlib import Path

import dpkt

ROOT = Path(__file__).resolve().parent.parent
S2C_PORTS = {7757, 7758}
BOSS = 3640454171745994031
PLAYERS = [18937, 22233, 22234, 22235, 22240]


def i64(v):
    return v - (1 << 64) if v >= (1 << 63) else v


def op_names():
    txt = (ROOT / "server-mysql/protocol/opcodes.go").read_text(encoding="utf-8", errors="ignore")
    m = {}
    for name, num in re.findall(r"^\s*(Op\w+)\s*=\s*(\d+)\s*$", txt, re.M):
        m.setdefault(int(num), name)
    return m


def load(p: Path):
    recs = []
    with open(p, "r", encoding="utf-8") as fh:
        for line in fh:
            r = json.loads(line)
            recs.append((r["stream"], r["t"], r["op"], r["fields"]))
    return recs


def sp(stream):
    a, b = stream.split(">")
    pa, pb = int(a.split(":")[1]), int(b.split(":")[1])
    return pb if pa in S2C_PORTS else pa


def s2c(stream):
    return int(stream.split(">")[0].split(":")[1]) in S2C_PORTS


def main(jsonl: Path, pcapng: Path):
    names = op_names()
    recs = load(jsonl)

    # ---- 1. 各流 t0 墙钟 ----
    print("### 1. 每条流 t=0 对应的墙钟（跨流对齐用）")
    t0 = {}
    with open(pcapng, "rb") as fh:
        rd = dpkt.pcapng.Reader(fh)
        for ts, buf in rd:
            try:
                eth = dpkt.ethernet.Ethernet(buf)
                ip = eth.data
                tcp = ip.data
            except Exception:  # noqa: BLE001
                continue
            if not isinstance(tcp, dpkt.tcp.TCP) or not bytes(tcp.data):
                continue
            key = ("%s:%d>%s:%d" % (socket.inet_ntoa(ip.src), tcp.sport,
                                    socket.inet_ntoa(ip.dst), tcp.dport))
            t0[key] = min(t0.get(key, ts), ts)
    base = min(t0.values())
    for k, v in sorted(t0.items(), key=lambda kv: kv[1]):
        print("      %-40s t0=+%.3fs  (%s)" % (
            k, v - base, datetime.datetime.fromtimestamp(v).strftime("%H:%M:%S.%f")[:-3]))
    print("      ⇒ 跨流同一广播的 t 最大相差 %.3fs" % (max(t0.values()) - min(t0.values())))

    ref = None
    for st, _, _, _ in recs:
        if s2c(st):
            ref = sp(st)
            break
    print("      基准流客户端端口 = %d" % ref)

    # ---- 2. 20169 / 20170 ----
    print()
    print("### 2. 属性推送：20169（单推） vs 20170（列表）")
    single = collections.defaultdict(list)      # (unit, nt) -> [(t, val)]
    listpush = collections.defaultdict(list)    # unit -> [(t, {nt:val})]
    for st, t, op, f in recs:
        if not s2c(st) or sp(st) != ref:
            continue
        if op == 20169:
            raw3 = f.get("3", 0.0)
            if isinstance(raw3, str) and raw3.startswith("<f32 "):
                val = struct.unpack("<f", bytes.fromhex(raw3[5:-1]))[0]
            elif isinstance(raw3, str) and raw3.startswith("<"):
                val = raw3
            else:
                val = raw3
            single[f["1"]].append((t, f["2"], val))
        elif op == 20170:
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
                    nt = sh = 0
                    while True:
                        c = raw[j]
                        j += 1
                        nt |= (c & 0x7F) << sh
                        sh += 7
                        if not c & 0x80:
                            break
                    if j + 5 > len(raw) or raw[j] != 0x15:
                        break
                    snap[nt] = struct.unpack_from("<f", raw, j + 1)[0]
                    j += 5
            listpush[f["1"]].append((t, snap))
    print("  20169 单推：单位 %d 个" % len(single))
    for u, lst in sorted(single.items()):
        types = collections.Counter(nt for _, nt, _ in lst)
        print("      %-20s 帧=%-5d 类型=%s" % (u, len(lst), dict(types)))
    print("  20170 列表：单位 %d 个" % len(listpush))
    for u, lst in sorted(listpush.items()):
        types = collections.Counter()
        for _, snap in lst:
            types.update(snap)
        print("      %-20s 帧=%-5d 类型=%s" % (u, len(lst), dict(types)))

    # BOSS 属性重推：单推 20169 的连续值本身没有时间戳，只能用 20170 的快照排时间
    print()
    print("  --- BOSS 关键属性的时间序列（20170 快照，只列变化点）")
    if BOSS in listpush:
        for nt in (1009, 1011, 1012, 1013, 1017, 1002):
            ser = []
            for t, snap in listpush[BOSS]:
                if nt in snap:
                    ser.append((t, snap[nt]))
            uniq = []
            for t, v in ser:
                if not uniq or abs(uniq[-1][1] - v) > 1e-6:
                    uniq.append((t, v))
            print("      %d：%d 个变化点" % (nt, len(uniq)))
            for t, v in uniq:
                print("          %8.2f  %s" % (t, v))

    # ---- 3. 状态帧 Id 复用 ----
    print()
    print("### 3. 20080 的 Id 复用核查（文档 39 §5 坑#7 声称「同一 Id 连发几帧、间隔 0.5s」）")
    ids = collections.defaultdict(list)   # id -> [(stream_port, t, icon, target)]
    for st, t, op, f in recs:
        if op == 20080 and s2c(st):
            ids[f["1"]].append((sp(st), t, f.get("3"), f.get("2")))
    multi = {k: v for k, v in ids.items() if len(v) > 1}
    print("  状态帧独立 Id 总数 = %d（帧数 %d），出现 >1 次的 Id = %d"
          % (len(ids), sum(len(v) for v in ids.values()), len(multi)))
    same_stream = sum(1 for k, v in multi.items() if len({p for p, _, _, _ in v}) < len(v))
    print("  其中「同一 Id 在同一条流里重复出现」= %d" % same_stream)
    for k, v in list(multi.items())[:6]:
        print("      Id=%d  %s" % (k, [(p, round(t, 2), i) for p, t, i, _ in v]))
    if multi:
        spreads = []
        for k, v in multi.items():
            ts = sorted(t for _, t, _, _ in v)
            spreads.append(ts[-1] - ts[0])
        spreads.sort()
        print("  重复 Id 的首末帧时间差中位数 = %.3fs  最大 = %.3fs"
              % (spreads[len(spreads) // 2], spreads[-1]))

    # ---- 4. DoT ----
    print()
    print("### 4. DoT 核查：玩家身上的负向伤害帧时间序列")
    dmg = collections.defaultdict(list)
    for st, t, op, f in recs:
        if op == 20078 and s2c(st) and sp(st) == ref:
            dmg[f["1"]].append((t, i64(f["2"]), f.get("3")))
    for u in PLAYERS:
        neg = [(t, v) for t, v, _ in dmg.get(u, []) if v < 0]
        if not neg:
            continue
        gaps = [b[0] - a[0] for a, b in zip(neg, neg[1:])]
        print("      %-8d 负向帧=%-3d 首=%.1fs 末=%.1fs  间隔(排序后)=%s"
              % (u, len(neg), neg[0][0], neg[-1][0],
                 [round(g, 2) for g in sorted(gaps)][:14]))
    # 全包内「同一数值重复出现且间隔规律」的候选 DoT
    print("  --- 候选周期伤害（同值出现 ≥4 次）")
    for u, lst in sorted(dmg.items()):
        byval = collections.defaultdict(list)
        for t, v, _ in lst:
            if v < 0:
                byval[v].append(t)
        for v, ts in sorted(byval.items(), key=lambda kv: -len(kv[1])):
            if len(ts) < 4:
                continue
            gaps = [round(b - a, 2) for a, b in zip(ts, ts[1:])]
            print("      %-20s 值=%-10d 次数=%-3d 间隔=%s" % (u, v, len(ts), gaps[:16]))

    # ---- 5. C2S ----
    print()
    print("### 5. 客户端 → 服务器 明细")
    for st, t, op, f in recs:
        if s2c(st):
            continue
        nm = names.get(op, str(op))
        if op in (20333,):     # Ping 太多，只给间隔统计
            continue
        print("      port=%-6d t=%8.2f  %-28s %s"
              % (sp(st), t, nm, json.dumps(f, ensure_ascii=False)[:100]))
    for port in sorted({sp(s) for s, _, _, _ in recs if not s2c(s)}):
        ts = sorted(t for s, t, op, _ in recs if not s2c(s) and sp(s) == port and op == 20333)
        if len(ts) > 2:
            g = [b - a for a, b in zip(ts, ts[1:])]
            gs = sorted(g)
            print("      C2G_Ping port=%d n=%d 间隔中位=%.3f 最小=%.3f 最大=%.3f"
                  % (port, len(ts), gs[len(gs) // 2], gs[0], gs[-1]))


if __name__ == "__main__":
    main(Path(sys.argv[1]), Path(sys.argv[2]))
