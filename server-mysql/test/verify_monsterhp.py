# -*- coding: utf-8 -*-
"""怪血条验证：普攻用 20169 同步现有怪物的 Hp/MaxHp，且不得触发 20053 重建。"""
import os, socket, struct, time, sys
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
def drain(s, timeout=0.5):
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
            return op2, b, pushes + [(op2, b)]
        pushes.append((op2, b))
    return None, None, pushes
def login(s, acct, name):
    op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
    key = fld(b, 2); gate = fld(b, 3)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    send_req(s, 20016, vf(2, 1) + sf(3, name) + vf(90, 5), 5)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    op, b, pushes = send_req(s, 20027, vf(90, 7), 7)
    return pushes

acct = 'hpbar' + str(int(time.time()))
s = socket.create_connection((HOST, PORT)); s.settimeout(5)
login(s, acct, '血条测试')
# 进海滩 → 客户端原生主线怪 → 战斗
send_req(s, 20031, vf(1, 1000401) + vf(90, 40), 40)
op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 11), 11)
region = [v for o, bb in pushes if o == 20047 for f, w, v in fields(bb) if f == 1][0]
assert not any(o == 20320 for o, _ in pushes), '主线地图不应重复推字段怪'
op, b, pushes = send_req(s, 20048, vf(1, region) + vf(90, 12), 12)
# 20050 怪初始
monster_ids = []
for o, bb in pushes:
    if o == 20050:
        for f, wt, v in fields(bb):
            if f == 1 and wt == 'bytes':
                kv = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
                monster_ids.append(kv.get(1))
print('  战斗怪:', monster_ids)
hp_seq = []
max_hp_seq = []
damage_count = 0
forbidden = []
# 连打 6 次普攻（带 CD 5.5s 等待），抓怪物 20169 数值同步
for i in range(6):
    op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 20+i), 20+i)
    for o, bb in pushes:
        if o in (20053, 20079):
            forbidden.append(o)
        if o == 20078:
            damage_count += 1
        if o == 20169 and fld(bb, 1) in monster_ids:
            if fld(bb, 2) == 1001:
                hp_seq.append((fld(bb, 1), fld(bb, 3)))
            elif fld(bb, 2) == 1002:
                max_hp_seq.append((fld(bb, 1), fld(bb, 3)))
    time.sleep(5.6)
print('  20169 Hp 序列:', hp_seq)
print('  20169 MaxHp 序列:', max_hp_seq)
check1 = len(hp_seq) >= 2
max_by_unit = dict(max_hp_seq)
check2 = all((h or 0) >= 0 and unit in max_by_unit and (h or 0) <= max_by_unit[unit] for unit, h in hp_seq)
check3 = len(set((h or 0) for _, h in hp_seq)) > 1  # proto3 omits the final zero Hp value
check4 = not forbidden
check5 = damage_count >= 2
print('  ✓ 怪物 Hp(1001) 推送 ≥2 次:', check1)
print('  ✓ Hp≤MaxHp 且字段完整:', check2)
print('  ✓ 血量渐进变化:', check3)
print('  ✓ 无 20053/20079:', check4, forbidden)
print('  ✓ 20078 受击消息:', check5, damage_count)
s.close()
print()
passed = check1 and check2 and check3 and check4 and check5
print('=== 怪血条(20169) 回归：%s ===' % ('全部通过' if passed else '失败'))
if not passed:
    raise SystemExit(1)
