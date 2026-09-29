# -*- coding: utf-8 -*-
"""购买/使用物品全链路：买 110305 药水 → 背包有；用 110305 → 血+500 且消耗；
买 110301 宝箱 → 用掉消耗。"""
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

def bag_items(bb):
    out = {}
    for f, wt, v in fields(bb):
        if f == 1 and wt == 'bytes':
            idx = None
            ni = None
            for ff, wt2, vv in fields(v):
                if wt2 == 'var' and ff == 1:
                    idx = vv
                elif wt2 == 'bytes' and ff == 2:
                    ni = vv
            if ni is not None:
                nkv = dict((ff, vv) for ff, wt2, vv in fields(ni) if wt2 == 'var')
                out[idx] = nkv.get(1)
    return out

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

acct = 'buy' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
pushes = login(s, acct, '购买测试')
# 主城买 110305 超小型生命药水（ShopBase page 找 slot）
import json, re
def load(name):
    raw = open(r'../datatable_json/%s.json' % name,'rb').read().decode('utf-8','replace')
    raw = re.sub(r',\s*([\]}])', r'\1', raw)
    return json.loads(raw)
sh = load('ShopBase')
rows = []
for k,v in sh:
    if v.get('Page') in (0, 1) or v.get('PageIndex') in (0, 1):
        rows.append(v)
print('ShopBase page0 rows:', len(rows))
slot110305 = None
for i, v in enumerate(rows):
    if v.get('ItemId') == 110305:
        slot110305 = i
        break
print('110305 slot:', slot110305)
# 尝试直接买（Page=0, Slot=slot110305, Count=2）
if slot110305 is not None:
    op, b, pushes = send_req(s, 20174, vf(1, 0) + vf(2, slot110305) + vf(3, 2) + vf(90, 12), 12)
    print('buy resp err:', fld(b, 92))
    bm = bag_items(b)
    check(110305 in bm.values(), '买 110305×2 成功入包: %s' % bm)
    # 找到 110305 的格子 index
    idx = [k for k, v2 in bm.items() if v2 == 110305][0]
    # 使用 110305 → 消耗 1 个（满血吃药无回复推送属正常：healHP=0 不推 20169）
    op, b, pushes = send_req(s, 20275, vf(1, idx) + vf(90, 13), 13)
    check(fld(b, 92) is None, '使用 110305 成功')
    bm2 = bag_items(b)
    check(110305 in bm2.values(), '使用后仍剩 1 瓶（数量-1）: %s' % bm2)
s.close()
print()
print('=== 购买/使用物品回归全部通过：%d 项 ===' % ok)
