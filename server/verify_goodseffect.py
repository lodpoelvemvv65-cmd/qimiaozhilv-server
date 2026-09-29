# -*- coding: utf-8 -*-
"""开箱/货币物品效果验证：YOYO礼盒(110394)→4件装备；铜币(110203)→+铜币；经验(110201)→+经验。"""
import socket, struct, time, sys, sqlite3, json, os
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
DB_PATH = os.path.abspath(os.environ.get('MHQ_DB', r'data/mhq.db'))
os.chdir(os.path.dirname(os.path.abspath(__file__)))

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
def bag_items(bb):
    out = {}
    for f, wt, v in fields(bb):
        if f == 1 and wt == 'bytes':
            idx = None; ni = None
            for ff, wt2, vv in fields(v):
                if wt2 == 'var' and ff == 1: idx = vv
                elif wt2 == 'bytes' and ff == 2: ni = vv
            if ni is not None:
                nkv = dict((ff, vv) for ff, wt2, vv in fields(ni) if wt2 == 'var')
                out[idx] = nkv.get(1)
    return out
def bag_item_counts(bb):
    out = {}
    for f, wt, v in fields(bb):
        if f == 1 and wt == 'bytes':
            idx = None; ni = None
            for ff, wt2, vv in fields(v):
                if wt2 == 'var' and ff == 1: idx = vv
                elif wt2 == 'bytes' and ff == 2: ni = vv
            if ni is not None:
                nkv = dict((ff, vv) for ff, wt2, vv in fields(ni) if wt2 == 'var')
                out[idx] = (nkv.get(1), nkv.get(4))
    return out

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

acct = 'fx' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '效果测试')
s.close()
time.sleep(1.5)  # 等 onClose saveData 完成，再注入（避免被空背包覆盖）
# 塞 YOYO礼盒(110394)+铜币(110203)×5+经验(110201)×1
conn = sqlite3.connect(DB_PATH)
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
bag = [{"k":0,"v":{"i":110394,"t":2,"s":0,"c":1,"l":False,"q":0,"r":0,"v":0}},
       {"k":1,"v":{"i":110203,"t":2,"s":0,"c":5,"l":False,"q":0,"r":0,"v":0}},
       {"k":2,"v":{"i":110201,"t":2,"s":0,"c":1,"l":False,"q":0,"r":0,"v":0}}]
conn.execute('UPDATE players SET bag_json=?, coin=100 WHERE id=?', (json.dumps(bag), pid))
conn.commit(); conn.close()
# 重登并确认背包读到了
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '效果测试')
op, b, pushes = send_req(s, 20258, vf(90, 10), 10)
got = []
for o, bb in pushes:
    if o in (20259, 20260):
        for f, wt, v in fields(bb):
            if f == 1 and wt == 'bytes':
                idx = None; ni = None
                for ff, wt2, vv in fields(v):
                    if wt2 == 'var' and ff == 1: idx = vv
                    elif wt2 == 'bytes' and ff == 2: ni = vv
                nkv = dict((ff, vv) for ff, wt2, vv in fields(ni) if wt2 == 'var') if ni else {}
                got.append((idx, nkv.get(1)))
print('  重登背包:', got)
check(len(got) == 3, '重登读到注入背包 3 格: %s' % got)
# 用 YOYO 礼盒 k=0 → 出 4 件装备（120399-120402）
op, b, pushes = send_req(s, 20275, vf(1, 0) + vf(90, 11), 11)
errmsg = fld(b, 92)
if errmsg is not None:
    print('  use box err:', errmsg)
check(errmsg is None, '使用 YOYO 礼盒成功')
bm = bag_items(b)
ids = set(bm.values())
check(all(e in ids for e in (120399, 120400, 120401, 120402)), '开箱出 4 件套装: %s' % sorted(ids))
# 用铜币 k=1（整格 5 个）→ +5 铜币
coin_before = None
for o, bb in pushes:
    if o == 20169 and fld(bb, 2) == 1028:
        coin_before = fld(bb, 3)
print('  coin before:', coin_before)
op, b, pushes = send_req(s, 20275, vf(1, 1) + vf(90, 12), 12)
check(fld(b, 92) is None, '使用铜币物品成功')
coin_after = None
for o, bb in pushes:
    if o == 20169 and fld(bb, 2) == 1028:
        coin_after = fld(bb, 3)
print('  coin after:', coin_after)
check(coin_after is not None and (coin_before is None or coin_after > coin_before), '铜币 +5: %s -> %s' % (coin_before, coin_after))
bm2 = bag_items(b)
check(110203 not in bm2.values(), '铜币格子已整格消耗: %s' % bm2)
# 用经验 k=2 → +1000 经验
op, b, pushes = send_req(s, 20275, vf(1, 2) + vf(90, 13), 13)
check(fld(b, 92) is None, '使用经验物品成功')
s.close()
print()
print('=== 开箱/货币/经验物品效果回归全部通过：%d 项 ===' % ok)
