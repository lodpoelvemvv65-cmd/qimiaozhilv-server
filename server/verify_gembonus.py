# -*- coding: utf-8 -*-
"""宝石属性加成验证：Type=1 装备 120890 镶嵌生命之晶·一级
(20046, GemKey=1最大生命, GemValue=2733) →
穿戴后 MaxHp 比无宝石时高 2733。"""
import socket, struct, time, sys, sqlite3, json
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
    for _ in range(500):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            return op2, b, pushes + [(op2, b)]
        pushes.append((op2, b))
    return None, None, pushes
def login(s, acct, name):
    op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
    key = fld(b, 2)
    if key is None:
        time.sleep(1)
        op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
        key = fld(b, 2)
    gate = fld(b, 3)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    send_req(s, 20016, vf(2, 1) + sf(3, name) + vf(90, 5), 5)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    op, b, pushes = send_req(s, 20027, vf(90, 7), 7)
    return pushes
def get_maxhp(s, rpc):
    op, b, pushes = send_req(s, 20251, vf(90, rpc), rpc)
    # 20252: UnitCharacter tag1 → MaxHp tag14
    for f, wt, v in fields(b):
        if f == 1 and wt == 'bytes':
            uc = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
            return uc.get(14)
    return None

acct = 'gemb' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '宝石加成')
s.close(); time.sleep(1.5)
conn = sqlite3.connect(r'data/mhq.db')
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
bag = [{"k":0,"v":{"i":120890,"t":1,"s":0,"c":1,"l":False,"q":3,"r":0,"v":1,"k":0}},
       {"k":1,"v":{"i":20046,"t":3,"s":0,"c":5,"l":False,"q":0,"r":0,"v":0}}]
conn.execute('UPDATE players SET bag_json=?, coin=1000000, level=4000, char_point=4001 WHERE id=?', (json.dumps(bag), pid))
conn.commit(); conn.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '宝石加成')
# 镶嵌 20046@槽0
op, b, pushes = send_req(s, 20212, vf(1, 0) + vf(2, 1) + vf(3, 0) + vf(90, 11), 11)
# 穿装备
op, b, pushes = send_req(s, 20271, vf(1, 0) + vf(90, 12), 12)
mhp_gem = get_maxhp(s, 13)
print('  带宝石 MaxHp:', mhp_gem)
# 脱下装备（20273 Takeoff，Index=穿戴槽=EquipBase.Type=1）→ 装备回背包 0
op, b, pushes = send_req(s, 20273, vf(1, 1) + vf(90, 14), 14)
# 拆卸宝石（bagIndex=0 装备已在背包）
op, b, pushes = send_req(s, 20349, vf(1, 0) + vf(2, 0) + vf(90, 15), 15)
# 再穿
op, b, pushes = send_req(s, 20271, vf(1, 0) + vf(90, 16), 16)
mhp_nogem = get_maxhp(s, 17)
print('  无宝石 MaxHp:', mhp_nogem)
diff = (mhp_gem or 0) - (mhp_nogem or 0)
print('  差值:', diff)
ok = mhp_gem is not None and mhp_nogem is not None and diff == 2733
print('  ✓ 宝石属性加成 MaxHp +2733:', ok)
s.close()
print()
print('=== 宝石属性加成回归：%s ===' % ('通过' if ok else '失败'))
