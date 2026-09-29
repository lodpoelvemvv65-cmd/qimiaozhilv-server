# -*- coding: utf-8 -*-
"""抓包体检：核对文档 39 里那份线上抓包的「包数/帧数/时长/会话数」是否属实，
并独立复核解帧是否完整（是否有被静默跳过的字节）。

用法：
    python tools/_capture39_audit.py 参考数据/抓包归档/xxx.pcapng

分三部分输出：
  A. TCP 包层：包数、方向、标志位、时间跨度、空洞、重传
  B. 帧层：独立重扫字节流，统计「完整消费」还是「有跳字节/有残留」
  C. 消息层：opcode 名称表（来自 server-mysql/protocol/opcodes.go）+ 计数
"""
from __future__ import annotations

import collections
import re
import socket
import struct
import sys
from pathlib import Path

import dpkt

ROOT = Path(__file__).resolve().parent.parent
OP_GO = ROOT / "server-mysql" / "protocol" / "opcodes.go"
OP_MIN, OP_MAX = 20001, 20444

FLAGS = {0x01: "FIN", 0x02: "SYN", 0x04: "RST", 0x08: "PSH", 0x10: "ACK",
         0x20: "URG", 0x40: "ECE", 0x80: "CWR"}


def load_op_names() -> dict:
    txt = OP_GO.read_text(encoding="utf-8", errors="ignore")
    m = {}
    for name, num in re.findall(r"^\s*(Op\w+)\s*=\s*(\d+)\s*$", txt, re.M):
        m.setdefault(int(num), name)
    return m


def main(path: Path):
    op_names = load_op_names()

    # ---------- A. TCP 包层 ----------
    with open(path, "rb") as fh:
        try:
            reader = dpkt.pcapng.Reader(fh)
            off = fh.tell()
        except Exception:  # noqa: BLE001
            fh.seek(0)
            reader = dpkt.pcap.Reader(fh)

        pkts = []
        non_ip = 0
        ips = collections.Counter()
        ports = collections.Counter()
        flags = collections.Counter()
        streams = collections.defaultdict(list)
        for ts, buf in reader:
            try:
                eth = dpkt.ethernet.Ethernet(buf)
            except Exception:  # noqa: BLE001
                non_ip += 1
                continue
            if eth.type != dpkt.ethernet.ETH_TYPE_IP:
                non_ip += 1
                continue
            ip = eth.data
            ips[(socket.inet_ntoa(ip.src), socket.inet_ntoa(ip.dst))] += 1
            if ip.p != dpkt.ip.IP_PROTO_TCP:
                continue
            tcp = ip.data
            key = (socket.inet_ntoa(ip.src), tcp.sport,
                   socket.inet_ntoa(ip.dst), tcp.dport)
            ports[(key[0], key[1])] += 1
            ports[(key[2], key[3])] += 1
            fl = "".join(v for k, v in FLAGS.items() if tcp.flags & k)
            flags[fl] += 1
            streams[key].append((ts, tcp.seq, bytes(tcp.data), tcp.flags))
            pkts.append((ts, ip))

    print("=" * 78)
    print("A. TCP 包层")
    print("=" * 78)
    ts_all = [p[0] for p in pkts]
    print("  包总数          = %d  (含非 TCP/非 IP 的丢弃 %d)" % (len(pkts), non_ip))
    print("  时间跨度        = %.3f s   (%s → %s)" % (
        max(ts_all) - min(ts_all),
        __import__("datetime").datetime.fromtimestamp(min(ts_all)),
        __import__("datetime").datetime.fromtimestamp(max(ts_all))))
    print("  IP 对（按方向） :")
    for (a, b), n in ips.most_common():
        print("      %-18s → %-18s  包=%d" % (a, b, n))
    print("  出现过的 IP:端口 :")
    for (a, p), n in sorted(ports.items()):
        print("      %-18s:%-6d 包=%d" % (a, p, n))
    print("  TCP 标志位组合  :")
    for f, n in flags.most_common():
        print("      %-8s 包=%d" % (f or "-", n))

    print("  逐流：包数 / 载荷包数 / 载荷字节 / 起止时间 / 最大空洞(s)")
    for key, plist in sorted(streams.items(), key=lambda kv: -len(kv[1])):
        tss = [p[0] for p in plist]
        pl = [p for p in plist if p[2]]
        # 重传：同 seq 同长度出现多次
        seen = collections.Counter((p[1], len(p[2])) for p in pl)
        retrans = sum(c - 1 for c in seen.values() if c > 1)
        gaps = [b - a for a, b in zip(tss, tss[1:])]
        print("      %s:%d>%s:%d  包=%-5d 载荷=%-5d 字节=%-8d "
              "t=[%.3f,%.3f] 最大空洞=%.3fs 重传=%d"
              % (key[0], key[1], key[2], key[3], len(plist), len(pl), sum(len(p[2]) for p in pl),
                 min(tss), max(tss), max(gaps) if gaps else 0, retrans))

    # ---------- B. 帧层独立重扫 ----------
    print()
    print("=" * 78)
    print("B. 帧层：独立重扫（严格「消费完全部字节」才算干净）")
    print("=" * 78)
    total_frames = 0
    op_all = collections.Counter()
    dir_frames = collections.Counter()
    for key, plist in sorted(streams.items(), key=lambda kv: -len(kv[1])):
        # 只按 TCP 序号重排（不补洞），逐包拼接；遇到序号跳变就记一个断点
        base = min(p[1] for p in plist if p[2])
        segs = sorted([p for p in plist if p[2]], key=lambda p: (p[1] - base) & 0xFFFFFFFF)
        buf = bytearray()
        hard_gaps = []
        for ts, seq, data, _ in segs:
            r = (seq - base) & 0xFFFFFFFF
            if r == 0xFFFFFFFF:  # 抓包起点的前一段
                continue
            if r > len(buf):
                hard_gaps.append(r - len(buf))
                buf.extend(b"\x00" * (r - len(buf)))
            buf[r:r + len(data)] = data
        # 严格扫描
        i, n = 0, len(buf)
        frames, skipped, desync = 0, 0, 0
        while i + 4 <= n:
            ln = struct.unpack_from("<H", buf, i)[0]
            op = struct.unpack_from("<H", buf, i + 2)[0]
            if ln >= 2 and i + 2 + ln <= n and OP_MIN <= op <= OP_MAX:
                frames += 1
                op_all[op] += 1
                dir_frames[(key[0], key[3])] += 1
                i += 2 + ln
            else:
                desync += 1
                i += 1
                skipped += 1
        total_frames += frames
        print("      %s:%d>%s:%d  帧=%-6d 字节=%-8d 序号断点=%s 跳字节=%d 扫描尾残留=%d"
              % (key[0], key[1], key[2], key[3], frames, n,
                 hard_gaps if hard_gaps else "无", skipped, n - i))
    print("  —— 帧合计 = %d" % total_frames)

    # ---------- C. 消息层 ----------
    print()
    print("=" * 78)
    print("C. 消息层：opcode 计数（全流合计；S2C 会因为 5 开 ×5）")
    print("=" * 78)
    s2c = sum(v for k, v in dir_frames.items() if k[1] in (7757, 7758))
    c2s = total_frames - s2c
    print("  服务器→客户端 = %d 帧;  客户端→服务器 = %d 帧" % (s2c, c2s))
    for op, cnt in op_all.most_common():
        print("      %-34s %-6d" % (op_names.get(op, "??"), cnt))


if __name__ == "__main__":
    main(Path(sys.argv[1]))
