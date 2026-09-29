# -*- coding: utf-8 -*-
"""验证主线地图只走客户端原生 20047/20048，不再叠加 20320 字段怪。"""
import os, socket, struct, time, select, sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

HOST = os.environ.get('MHQ_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_PORT', '7756'))

def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7f; n >>= 7
        if n: out.append(b | 0x80)
        else: out.append(b); return bytes(out)
def vf(n, v):  return varint((n << 3) | 0) + varint(v)
def sf(n, s):
    b = s.encode('utf-8'); return varint((n << 3) | 2) + varint(len(b)) + b
def pack(op, body): return struct.pack('<HH', 2 + len(body), op) + body

def recv_one(s):
    hdr = b''
    while len(hdr) < 4:
        c = s.recv(4 - len(hdr))
        if not c: raise ConnectionError('closed')
        hdr += c
    total, op = struct.unpack('<HH', hdr)
    body = b''
    while len(body) < total - 2:
        c = s.recv(total - 2 - len(body))
        if not c: raise ConnectionError('closed')
        body += c
    return op, body

def drain(s, timeout=0.6):
    s.settimeout(timeout)
    out = []
    while True:
        try: out.append(recv_one(s))
        except (socket.timeout, ConnectionError): break
    return out

def rpc_of(b):
    i = 0
    while i < len(b):
        t = 0; sh = 0
        while True:
            x = b[i]; i += 1; t |= (x & 0x7f) << sh; sh += 7
            if not x & 0x80: break
        f, wt = t >> 3, t & 7
        if wt == 0:
            v = 0; sh = 0
            while True:
                x = b[i]; i += 1; v |= (x & 0x7f) << sh; sh += 7
                if not x & 0x80: break
            if f == 90: return v
        elif wt == 2:
            l = 0; sh = 0
            while True:
                x = b[i]; i += 1; l |= (x & 0x7f) << sh; sh += 7
                if not x & 0x80: break
            i += l
        elif wt == 5: i += 4
        else: break
    return None

def fields(body):
    out = []; i = 0
    while i < len(body):
        t = 0; sh = 0
        while True:
            x = body[i]; i += 1; t |= (x & 0x7f) << sh; sh += 7
            if not x & 0x80: break
        f, wt = t >> 3, t & 7
        if wt == 0:
            v = 0; sh = 0
            while True:
                x = body[i]; i += 1; v |= (x & 0x7f) << sh; sh += 7
                if not x & 0x80: break
            out.append((f, 'var', v))
        elif wt == 2:
            l = 0; sh = 0
            while True:
                x = body[i]; i += 1; l |= (x & 0x7f) << sh; sh += 7
                if not x & 0x80: break
            out.append((f, 'bytes', body[i:i+l])); i += l
        elif wt == 5:
            out.append((f, 'float', struct.unpack('<f', body[i:i+4])[0])); i += 4
        else: break
    return out

def send_req(s, op, body, rpc):
    s.sendall(pack(op, body))
    pushes = []
    for _ in range(40):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

acct = 'clk%d' % int(time.time() * 1000)
s = socket.create_connection((HOST, PORT)); s.settimeout(5)
op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
key = [v for f, w, v in fields(b) if f == 2][0]; gate = [v for f, w, v in fields(b) if f == 3][0]
op, b, _ = send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 2), 2)
op, b, _ = send_req(s, 20016, vf(2, 1) + sf(3, '点怪测') + vf(90, 3), 3)
op, b, pushes = send_req(s, 20027, vf(90, 4), 4)
# 新号持久化出生点就是海滩；先回主城再进海滩，避免同图请求不重复初始化。
send_req(s, 20031, vf(1, 1000401) + vf(90, 40), 40)
op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 5), 5)
print('entergame pushes:', sorted(set(o for o, _ in pushes)))
regions = [v for o, bb in pushes if o == 20047 for f, w, v in fields(bb) if f == 1]
legacy = [bb for o, bb in pushes if o == 20320]
assert len(regions) == 1, regions
assert not legacy, 'main-story map emitted duplicate 20320 monsters'
op, b, battle_pushes = send_req(s, 20048, vf(1, regions[0]) + vf(90, 6), 6)
assert op == 20049 and not [v for f, _, v in fields(b) if f == 92], (op, fields(b))
assert any(o == 20050 for o, _ in battle_pushes), sorted(set(o for o, _ in battle_pushes))
print('main story native monster flow: OK (20047 -> 20048, no 20320 duplicate)')
s.close()
