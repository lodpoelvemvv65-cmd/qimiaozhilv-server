# -*- coding: utf-8 -*-
"""穿皮肤装备 → SkinId 变化 + 持久化 验证（用新号：背包塞皮肤装备 120594）。"""
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
def drain(s, timeout=0.4):
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
    for _ in range(300):
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

def bag_index_of(body, item_id):
    for f, wt, pair in fields(body):
        if f != 1 or wt != 'bytes':
            continue
        pair_fields = list(fields(pair))
        index = next((v for ff, ww, v in pair_fields if ff == 1 and ww == 'var'), None)
        item = next((v for ff, ww, v in pair_fields if ff == 2 and ww == 'bytes'), None)
        if item is not None and fld(item, 1) == item_id:
            return index
    return None

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

acct = 'skin' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '皮肤测试')
# 先关连接触发 saveData（否则旧连接 onClose 会用内存背包覆盖 DB 注入）
s.close(); time.sleep(1.0)
# 找 playerID（accounts → players）
conn = sqlite3.connect(DB_PATH)
row = None
for _ in range(20):
    row = conn.execute('SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=? LIMIT 1)', (acct,)).fetchone()
    if row is not None:
        break
    time.sleep(0.25)
check(row is not None, 'new account was persisted before skin setup')
pid = row[0]
# 背包塞皮肤装备 120594（k=5）+ 一件普通武器 120553（k=6）
bag = [{"k":5,"v":{"i":120594,"t":1,"s":0,"c":1,"l":False,"q":0,"r":0,"v":0}},
       {"k":6,"v":{"i":120553,"t":1,"s":0,"c":1,"l":False,"q":0,"r":0,"v":0}}]
conn.execute('UPDATE players SET bag_json=?, level=200 WHERE id=?', (json.dumps(bag), pid))
conn.commit()
conn.close()
# 重登加载新背包
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '皮肤测试')

# 登录会规范化背包槽位，按 GetBag 返回值定位装备。
op, bag_body, _ = send_req(s, 20258, vf(90, 10), 10)
skin_index = bag_index_of(bag_body, 120594)
weapon_index = bag_index_of(bag_body, 120553)
check(skin_index is not None and weapon_index is not None, 'skin and weapon slots resolved from GetBag')

# 穿皮肤装备 → SkinId 应变 120594
op, b, pushes = send_req(s, 20271, vf(1, skin_index) + vf(90, 11), 11)
errmsg = fld(b, 92)
if errmsg is not None:
    # 调试：打印服务器日志侧原因
    print('  put on err:', errmsg)
check(errmsg is None, '穿皮肤装备 120594 成功')
skin_after = None
for o, bb in pushes:
    if o == 20257:
        for f, wt, v in fields(bb):
            if f == 1 and wt == 'bytes':
                kv = dict((ff, vv) for ff, wt2, vv in fields(v) if wt2 == 'var')
                skin_after = kv.get(8)
check(skin_after == 120594, '穿上后 SkinId=120594: %s' % skin_after)

# 穿普通装备 → SkinId 保持 120594（普通装备不改皮肤）
op, bag_body, _ = send_req(s, 20258, vf(90, 115), 115)
weapon_index = bag_index_of(bag_body, 120553)
check(weapon_index is not None, 'weapon slot resolved after skin equip')
op, b, pushes = send_req(s, 20271, vf(1, weapon_index) + vf(90, 12), 12)
check(fld(b, 92) is None, '穿普通装备 120553 成功')
skin2 = None
for o, bb in pushes:
    if o == 20257:
        for f, wt, v in fields(bb):
            if f == 1 and wt == 'bytes':
                kv = dict((ff, vv) for ff, wt2, vv in fields(v) if wt2 == 'var')
                skin2 = kv.get(8)
check(skin2 == 120594, '再穿普通装备 SkinId 仍 120594: %s' % skin2)

# 脱下皮肤（Type=2 槽位）→ SkinId 恢复 jobID
# 皮肤装备 Type=2 → slot=2。脱下 slot=2
op, b, pushes = send_req(s, 20273, vf(1, 2) + vf(90, 13), 13)
check(fld(b, 92) is None, '脱下皮肤装备成功')
skin3 = None
for o, bb in pushes:
    if o == 20257:
        for f, wt, v in fields(bb):
            if f == 1 and wt == 'bytes':
                kv = dict((ff, vv) for ff, wt2, vv in fields(v) if wt2 == 'var')
                skin3 = kv.get(8)
check(skin3 == 1, '脱下后 SkinId 恢复职业 1: %s' % skin3)
s.close()
print()
print('=== 皮肤装备穿戴回归全部通过：%d 项 ===' % ok)
