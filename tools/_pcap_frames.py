# -*- coding: utf-8 -*-
"""pcapng → JSONL 帧 dump：把抓包里的游戏 TCP 帧解出来（含每帧时间戳）。

帧格式（与 server-mysql/testclient 一致）：
    [u16 LE 总长(含 opcode 2 字节)][u16 LE opcode][protobuf body]

用法：
    python tools/_pcap_frames.py <a.pcapng> [b.pcapng ...]
输出：_work/pcap_frames/<文件名>.jsonl 与 .stat.txt
"""
from __future__ import annotations

import dpkt
import json
import socket
import struct
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
OUT_DIR = ROOT / "_work" / "pcap_frames"

OP_MIN, OP_MAX = 20001, 20444          # opcodes.go 全部 444 个定义


def decode_protobuf(data: bytes) -> dict:
    """通用 protobuf 解码（无 schema）：{field_number: value}。"""
    out = {}
    i = 0
    while i < len(data):
        key = 0
        shift = 0
        while i < len(data):
            b = data[i]
            i += 1
            key |= (b & 0x7F) << shift
            shift += 7
            if not b & 0x80:
                break
        field, wire = key >> 3, key & 7
        value = None
        if wire == 0:
            val = 0
            shift = 0
            while i < len(data):
                b = data[i]
                i += 1
                val |= (b & 0x7F) << shift
                shift += 7
                if not b & 0x80:
                    break
            value = val
        elif wire == 2:
            n = 0
            shift = 0
            while i < len(data):
                b = data[i]
                i += 1
                n |= (b & 0x7F) << shift
                shift += 7
                if not b & 0x80:
                    break
            raw = data[i:i + n]
            i += n
            try:
                text = raw.decode("utf-8")
                if any(ord(c) < 32 and c not in "\t\n\r" for c in text):
                    raise ValueError
                value = text
            except (UnicodeDecodeError, ValueError):
                value = "<%dB %s>" % (n, raw.hex())
        elif wire == 5:
            value = "<f32 %s>" % data[i:i + 4].hex()
            i += 4
        elif wire == 1:
            value = "<f64 %s>" % data[i:i + 8].hex()
            i += 8
        else:
            break
        if field in out:
            if not isinstance(out[field], list):
                out[field] = [out[field]]
            out[field].append(value)
        else:
            out[field] = value
    return out


def reassemble(packets):
    """packets=[(seq,payload,ts)] -> (buf, segs, gaps, t0)。
    segs = [(start,end,ts)] 把字节偏移映回包时间。"""
    base = packets[0][0]

    def rel(seq):
        d = (seq - base) & 0xFFFFFFFF
        return d - (1 << 32) if d >= (1 << 31) else d

    ordered = sorted(packets, key=lambda p: rel(p[0]))
    buf = bytearray()
    segs = []
    gaps = 0
    for seq, payload, ts in ordered:
        r = rel(seq)
        if r < 0:      # 抓包从流中间开始前的重传/乱序，丢弃
            continue
        if r > len(buf):
            gaps += 1
            buf.extend(b"\x00" * (r - len(buf)))   # 缺口补零占位，帧扫描会跳过
        start = r
        end = r + len(payload)
        if end > len(buf):
            buf.extend(b"\x00" * (end - len(buf)))
        buf[start:end] = payload
        segs.append((start, end, ts))
    t0 = min(p[2] for p in packets)
    return bytes(buf), segs, gaps, t0


def ts_at(segs, offset, t0):
    for start, end, ts in segs:
        if start <= offset < end:
            return ts - t0
    return None


def frame_scan(stream: bytes):
    """贪婪扫描游戏帧，返回 [(opcode, body, offset)]。"""
    frames = []
    i = 0
    n = len(stream)
    while i + 4 <= n:
        ln = struct.unpack_from("<H", stream, i)[0]
        if ln < 2 or i + 2 + ln > n:
            i += 1
            continue
        op = struct.unpack_from("<H", stream, i + 2)[0]
        if OP_MIN <= op <= OP_MAX:
            frames.append((op, stream[i + 4:i + 2 + ln], i))
            i += 2 + ln
        else:
            i += 1
    return frames


def process(path: Path, out: Path, stat: Path):
    with open(path, "rb") as fh:
        try:
            fh.seek(0)
            reader = dpkt.pcapng.Reader(fh)
        except Exception:  # noqa: BLE001
            fh.seek(0)
            reader = dpkt.pcap.Reader(fh)
        streams = {}
        count = 0
        for ts, buf in reader:
            count += 1
            try:
                eth = dpkt.ethernet.Ethernet(buf)
            except Exception:  # noqa: BLE001
                continue
            if eth.type != dpkt.ethernet.ETH_TYPE_IP:
                continue
            ip = eth.data
            if getattr(ip, "p", 0) != dpkt.ip.IP_PROTO_TCP:
                continue
            tcp = ip.data
            payload = bytes(tcp.data)
            if not payload:
                continue
            key = (socket.inet_ntoa(ip.src), tcp.sport, socket.inet_ntoa(ip.dst), tcp.dport)
            streams.setdefault(key, []).append((tcp.seq, payload, ts))

        records = []
        stats = []
        for key, pkts in streams.items():
            data, segs, gaps, t0 = reassemble(pkts)
            frames = frame_scan(data)
            if len(frames) < 5:
                continue
            stats.append((key, len(frames), gaps, len(data)))
            for op, body, off in frames:
                records.append({
                    "stream": "%s:%d>%s:%d" % key,
                    "t": ts_at(segs, off, t0),
                    "op": op,
                    "fields": decode_protobuf(body),
                })

    records.sort(key=lambda r: (r["stream"], r["t"] if r["t"] is not None else 1e9))
    out.write_text("\n".join(json.dumps(r, ensure_ascii=False) for r in records),
                   encoding="utf-8")
    lines = ["## %s  包=%d  帧=%d" % (path.name, count, len(records))]
    for key, nf, gaps, nbytes in sorted(stats, key=lambda s: -s[1]):
        lines.append("   %-36s 帧=%-7d 缺口=%-3d 字节=%d"
                     % ("%s:%d>%s:%d" % key, nf, gaps, nbytes))
    stat.write_text("\n".join(lines), encoding="utf-8")
    print("\n".join(lines))


def main():
    OUT_DIR.mkdir(exist_ok=True)
    for arg in sys.argv[1:]:
        p = Path(arg)
        if not p.exists():
            print("!! missing", arg)
            continue
        process(p, OUT_DIR / (p.stem + ".jsonl"), OUT_DIR / (p.stem + ".stat.txt"))


if __name__ == "__main__":
    main()
