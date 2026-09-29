# -*- coding: utf-8 -*-
"""主界面物品槽回归：拖入物品槽 → 点击使用 → 槽位数量变化、背包减少、血蓝推送。"""
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

def fld(body, tag):
    for f, _, v in fields(body):
        if f == tag: return v
    return None

def send_req(s, op, body, rpc):
    s.sendall(pack(op, body))
    pushes = []
    for _ in range(200):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

acct = 'mui' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
if fld(b, 91) is not None:
    send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 3), 3)
key = fld(b, 2); gate = fld(b, 3)
send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
send_req(s, 20016, vf(2, 1) + sf(3, '物品槽测试') + vf(90, 5), 5)
send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
op, b, pushes = send_req(s, 20027, vf(90, 7), 7)

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

# 1) 先买药水（ShopBase 10001 = 110305），再 GetMainUISetting → 物品槽应自动显示药水
op, b, pushes = send_req(s, 20174, vf(1, 0) + vf(2, 0) + vf(3, 3) + vf(90, 11), 11)
check(op == 20175 and not fld(b, 92), '购买 3 个药水')
# 找背包里的药水格子
pot_idx = None
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        bag = dict((ff, vv) for ff, wt2, vv in fields(v))
        item = dict((ff, vv) for ff, wt2, vv in fields(bag.get(2, b'')))
        if item.get(1) == 110305:
            pot_idx = bag.get(1)
check(pot_idx is not None, '背包找到 110305')

# 2) GetMainUISetting → 9 槽固定，槽 0 = 基础攻击，其余 NoneSlot
op, b, pushes = send_req(s, 20227, vf(90, 12), 12)
slots = []
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        d = dict((ff, vv) for ff, wt2, vv in fields(v))
        slots.append(d)
check(op == 20228 and len(slots) == 9, 'GetMainUISetting 返回 9 槽固定')
check(slots[0].get(2) == 1 and slots[0].get(1) == 100001, '槽 0 = 基础攻击 SkillSlot')
none_slots = [x for x in slots if x.get(2) in (None, 0) and x.get(5) is not None]
check(len(none_slots) == 8, '其余 8 槽 NoneSlot(0)（不越界）')
print('  槽位列表:', [(x.get(1), x.get(2), x.get(4), x.get(5)) for x in slots])

# 3) 拖药水到物品槽（slot=8）
drop_slot = 8
op, b, pushes = send_req(s, 20231, vf(1, drop_slot) + vf(2, pot_idx) + vf(90, 12), 12)
slots2 = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
check(op == 20232 and not fld(b, 92), '拖入物品槽成功 (20232)')
dropped = [x for x in slots2 if x.get(5) == drop_slot]
check(dropped and dropped[0].get(1) == 110305 and dropped[0].get(4) == 3, '槽位显示 item=%s count=%s' % (dropped[0].get(1), dropped[0].get(4)))

# 4) 点击物品槽使用 1 个
op, b, pushes = send_req(s, 20235, vf(1, drop_slot) + vf(90, 13), 13)
slots3 = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
bags3 = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 2 and wt == 'bytes']
print('  点击物品槽响应:', op, 'message:', fld(b, 92))
check(op == 20236 and not fld(b, 92), '点击物品槽使用成功 (20236)')
used = [x for x in slots3 if x.get(5) == drop_slot]
check(used and used[0].get(4) == 2, '使用后槽位数量 3→2')
check(len(bags3) > 0, '响应带 BagMapList 全量')
hp_push = [v for o, bb in pushes if o == 20169 and fld(bb, 2) == 1001]
print('  血蓝推送:', [(fld(bb, 2), fld(bb, 3)) for o, bb in pushes if o == 20169 and fld(bb, 2) in (1001, 1003)])

# 5) 背包数量同步减少
op, b, pushes = send_req(s, 20258, vf(90, 14), 14)
total = 0
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        bag = dict((ff, vv) for ff, wt2, vv in fields(v))
        item = dict((ff, vv) for ff, wt2, vv in fields(bag.get(2, b'')))
        if item.get(1) == 110305:
            total += item.get(4)
check(total == 2, '背包剩余药水 2')

s.close()
print()
print('=== 主界面物品槽回归全部通过：%d 项 ===' % ok)
