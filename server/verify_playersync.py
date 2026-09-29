# -*- coding: utf-8 -*-
"""双玩家同场景可见性验证：A/B 先后进入同一地图，互相收到 20036（Units/PvpUnits 等长含对方）。"""
import socket, struct, time, sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

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
        except Exception: break
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

def fld(body, tag):
    for f, _, v in fields(body):
        if f == tag: return v
    return None

def send_req(s, op, body, rpc):
    s.sendall(pack(op, body))
    pushes = []
    for _ in range(400):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

def login(s, acct, name):
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    if fld(b, 91) is not None:
        send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
        op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 3), 3)
    key = fld(b, 2); gate = fld(b, 3)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    send_req(s, 20016, vf(2, 1) + sf(3, name) + vf(90, 5), 5)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    op, b, pushes = send_req(s, 20027, vf(90, 7), 7)
    return pushes

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

# A 登录
sa = socket.create_connection(('127.0.0.1', 7756)); sa.settimeout(5)
acctA = 'pa' + str(int(time.time()))
pushA = login(sa, acctA, '玩家甲')
# A 进 1001101
op, b, p = send_req(sa, 20031, vf(1, 1001101) + vf(90, 11), 11)
units = [bb for o, bb in p if o == 20036]
check(len(units) >= 1, 'A 进图收到 20036')
# 解析 A 的 20036：Units(tag1) 与 PvpUnits(tag2) 等长；不含自己（客户端
# M2C_UnitsInMapHandler 对 Units 逐项创建单位，含自己会重复 key 崩溃）
u = 0; pv = 0
for f, wt, v in fields(units[0]):
    if f == 1 and wt == 'bytes': u += 1
    elif f == 2 and wt == 'bytes': pv += 1
check(u == 0 and u == pv, 'A 单人 20036 Units=%d PvpUnits=%d 等长（不含自己）' % (u, pv))

# B 登录
sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(5)
acctB = 'pb' + str(int(time.time()))
pushB = login(sb, acctB, '玩家乙')
# B 进 1001101
op, b, p = send_req(sb, 20031, vf(1, 1001101) + vf(90, 21), 21)
unitsB = [bb for o, bb in p if o == 20036]
check(len(unitsB) >= 1, 'B 进图收到 20036')
pvp_ids = []
for f, wt, v in fields(unitsB[0]):
    if f == 2 and wt == 'bytes':
        kv = dict((ff, vv) for ff, wt2, vv in fields(v) if wt2 == 'var')
        pvp_ids.append(kv.get(1))
check(len(pvp_ids) == 1, 'B 的 20036 仅含对方 A（不含自己）: %s' % pvp_ids)

# A 应收到 B 进场的原生 20034 EnterMap。客户端只有该 handler 会创建新单位；
# 20025 SendUnitInfo 只刷新已经存在的单位，不能用于首次进场。
pa2 = drain(sa, 1.0)
suA2 = [bb for o, bb in pa2 if o == 20034]
check(len(suA2) >= 1, 'A 收到 B 进场的 20034 EnterMap 广播')
b_id = None
for bb in suA2:
    for f, wt, v in fields(bb):
        if f == 2 and wt == 'bytes':
            kv = dict((ff, vv) for ff, wt2, vv in fields(v) if wt2 == 'var')
            b_id = kv.get(1)
check(b_id is not None, 'A 收到的 20034 含 B 的 PvpUnitCharacter: %s' % b_id)
check(any(f == 1 and wt == 'bytes' for bb in suA2 for f, wt, _ in fields(bb)),
      'A 收到的 20034 含 B 的 UnitInfo 坐标')
check(all(o != 20036 for o, _ in pa2), 'A 未收到会清场的 20036 广播（NPC 不被清）')

def ff32(n, v):
    import struct as _s
    return varint((n << 3) | 5) + _s.pack('<f', v)

def frame_click(x, y, rpc):
    # Frame_ClickMap{UnitInfo=1(LEN), RpcId=90}; UnitPosInfo{X=2 fixed32, Y=3 fixed32}
    unit = ff32(2, x) + ff32(3, y)
    return vf(1, 0) + sf2(unit) + vf(90, rpc)

def sf2(b):
    return varint((1 << 3) | 2) + varint(len(b)) + b

# A 移动 → B 收到 20024 广播（20022 是 fire-and-forget，无响应帧，直接发+排水）
sa.sendall(pack(20022, frame_click(12.4, -2.5, 31)))
pa2b = drain(sa, 0.5)
pb2 = drain(sb, 1.0)
mv = [bb for o, bb in pb2 if o == 20024]
check(len(mv) >= 1, 'B 收到 A 的移动广播 20024')
mv_id = [fld(bb, 1) for bb in mv if fld(bb, 1)]
print('  移动广播 Id:', mv_id)

# B 离开（关闭连接）→ A 收到 20037 LeaveMap（服务器检测断开需要一点时间）
sb.close()
time.sleep(1.5)
pa3 = drain(sa, 3.0)
lv = [bb for o, bb in pa3 if o == 20037]
check(len(lv) >= 1, 'A 收到 B 离线的 20037 LeaveMap')
sa.close()
print()
print('=== 双玩家同场景可见性回归全部通过：%d 项 ===' % ok)
