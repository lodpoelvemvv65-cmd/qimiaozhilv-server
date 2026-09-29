# -*- coding: utf-8 -*-
"""寄售行回归：上架→列表查询→购买→货款到账→背包校验；自购拒绝；密码错误拒绝；
装备上架必须带 EquipTransMessage（客户端 ItemType==1 渲染依赖）。"""
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
    key = None
    for attempt in range(3):
        op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
        key = fld(b, 2)
        if key is not None:
            break
        time.sleep(1)
    gate = fld(b, 3)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    send_req(s, 20016, vf(2, 1) + sf(3, name) + vf(90, 5), 5)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    op, b, pushes = send_req(s, 20027, vf(90, 7), 7)
    return pushes, fld(b, 1)

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

def coin_of_db(pid):
    import sqlite3
    conn = sqlite3.connect(r'data/mhq.db')
    v = conn.execute('SELECT coin FROM players WHERE id=?', (pid,)).fetchone()[0]
    conn.close()
    return v

base = 'cg' + str(int(time.time()))
sa = socket.create_connection(('127.0.0.1', 7756)); sa.settimeout(5)
_, pidA = login(sa, base + 'a', '卖家甲')
sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(5)
_, pidB = login(sb, base + 'b', '买家乙')
print('  A=%d B=%d' % (pidA, pidB))

# 1) A 买 3 个药水（110305）后上架 2 个，单价 1000
op, b, pushes = send_req(sa, 20174, vf(1, 0) + vf(2, 0) + vf(3, 3) + vf(90, 11), 11)
pot_idx = None
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        bag = dict((ff, vv) for ff, wt2, vv in fields(v))
        item = dict((ff, vv) for ff, wt2, vv in fields(bag.get(2, b'')))
        if item.get(1) == 110305:
            pot_idx = bag.get(1)
check(pot_idx is not None, 'A 买到药水 (背包格 %s)' % pot_idx)
time.sleep(0.4)  # 商店响应先于 saveData 落库（~100ms），等 DB 一致再读
coinA0 = coin_of_db(pidA)
op, b, pushes = send_req(sa, 20183, vf(1, pot_idx) + vf(2, 1000) + vf(3, 2) + vf(90, 12), 12)
check(op == 20184 and fld(b, 92) is None, 'A 上架 2 个药水 单价 1000')

# 2) B 查列表 → 看到 A 的寄售
op, b, pushes = send_req(sb, 20181, vf(90, 13), 13)
items = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 2 and wt == 'bytes']
print('  寄售列表:', [(x.get(1), (x.get(3) or b'').decode('utf-8', 'replace'), x.get(7)) for x in items])
check(op == 20182 and len(items) >= 1, 'B 查寄售列表（%d 条）' % len(items))
check(any((x.get(3) or b'').decode('utf-8', 'replace') == '卖家甲' for x in items), '寄售条目含卖家名')
mine = [x for x in items if x.get(2) == pidA]
check(mine and mine[0].get(7) == 1000, '寄售条目 ConsignItemId=%s Price=1000' % mine[0].get(1))
consign_id = mine[0].get(1)

# 3) A 自购拒绝
op, b, pushes = send_req(sa, 20185, vf(1, consign_id) + vf(90, 14), 14)
check(op == 20186 and '自己的' in (fld(b, 92) or b'').decode('utf-8', 'replace'), '不能购买自己的寄售')

# 4) B 购买 → 扣钱 + 药水入包
coinB0 = coin_of_db(pidB)
op, b, pushes = send_req(sb, 20185, vf(1, consign_id) + vf(90, 15), 15)
check(op == 20186 and fld(b, 92) is None, 'B 购买成功')
time.sleep(0.4)  # 等 saveData 落库
coinB1 = coin_of_db(pidB)
print('  B 铜币 %s -> %s' % (coinB0, coinB1))
check(coinB1 == coinB0 - 2000, 'B 扣款 2000')
time.sleep(0.4)
coinA1 = coin_of_db(pidA)
print('  A 铜币 %s -> %s' % (coinA0, coinA1))
check(coinA1 == coinA0 + 2000, 'A 货款到账 +2000')

# 5) 购买后该条目已删除
op, b, pushes = send_req(sb, 20181, vf(90, 16), 16)
items2 = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 2 and wt == 'bytes']
check(not any(x.get(1) == consign_id for x in items2), '购买后条目已从列表移除')

# 6) 密码上架 + 错误密码购买拒绝
op, b, pushes = send_req(sb, 20174, vf(1, 0) + vf(2, 0) + vf(3, 3) + vf(90, 17), 17)
pot2 = None
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        bag = dict((ff, vv) for ff, wt2, vv in fields(v))
        item = dict((ff, vv) for ff, wt2, vv in fields(bag.get(2, b'')))
        if item.get(1) == 110305:
            pot2 = bag.get(1)
op, b, pushes = send_req(sb, 20183, vf(1, pot2) + vf(2, 500) + vf(3, 1) + sf(4, '1234') + vf(90, 18), 18)
check(op == 20184 and fld(b, 92) is None, 'B 密码上架药水 500')
op, b, pushes = send_req(sa, 20181, vf(90, 19), 19)
items3 = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 2 and wt == 'bytes']
cid2 = None
for x in items3:
    if x.get(2) == pidB:
        cid2 = x.get(1)
        break
check(cid2 is not None, '找到 B 的密码寄售条目')
op, b, pushes = send_req(sa, 20185, vf(1, cid2) + vf(2, pidB) + sf(3, 'wrong') + vf(90, 20), 20)
check(op == 20186 and '密码' in (fld(b, 92) or b'').decode('utf-8', 'replace'), '错误密码购买被拒')
op, b, pushes = send_req(sa, 20185, vf(1, cid2) + vf(2, pidB) + sf(3, '1234') + vf(90, 21), 21)
check(op == 20186 and fld(b, 92) is None, '正确密码购买成功')

# 7) 装备上架 → 列表 ConsignMap 必须带 EquipTransMessage（tag6，客户端 ItemType==1
#    渲染 new Equip(EquipTransMessage)，缺失 NRE → 列表全空 = "上架了看不到"）
#    直接给 B 背包注入一件装备（120660 皮肤装，EquipBase 有行）。
#    ⚠ 先关旧连接（onClose 的 saveData 会用旧背包覆盖 DB），再注入，再重登。
sb.close()
time.sleep(0.5)
conn = sqlite3.connect(r'data/mhq.db')
conn.execute("UPDATE players SET bag_json=? WHERE id=?",
             (json.dumps([{"k": 1, "v": {"i": 120660, "t": 1, "s": 0, "l": False, "c": 1, "q": 3, "r": 0, "v": 1, "k": 0, "p": 0}}]), pidB))
conn.commit(); conn.close()
sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(5)
login(sb, base + 'b', '买家乙')
# 找背包里的装备格（20258 GetBag → 20259，BagMapList tag1）
sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(5)
login(sb, base + 'b', '买家乙')
op, b, pushes = send_req(sb, 20258, vf(90, 26), 26)
eq_idx = None
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        bag = dict((ff, vv) for ff, wt2, vv in fields(v))
        item = dict((ff, vv) for ff, wt2, vv in fields(bag.get(2, b'')))
        if item.get(2) == 1:
            eq_idx = bag.get(1)
            break
print('  背包装备格: idx=%s' % eq_idx)
check(eq_idx is not None, 'B 背包有装备（120660）')
op, b, pushes = send_req(sb, 20183, vf(1, eq_idx) + vf(2, 999) + vf(3, 1) + vf(90, 23), 23)
check(op == 20184 and fld(b, 92) is None, 'B 装备上架')
op, b, pushes = send_req(sa, 20181, vf(90, 24), 24)
eq_items = []
for f, wt, v in fields(b):
    if f == 2 and wt == 'bytes':
        d = dict((ff, vv) for ff, wt2, vv in fields(v))
        it = d.get(5, b'')
        itd = dict((ff, vv) for ff, wt2, vv in fields(it)) if it else {}
        if itd.get(2) == 1:
            eq_items.append(d)
print('  列表装备条目数:', len(eq_items), '含 EquipTransMessage(tag6):', [x.get(6) is not None for x in eq_items])
check(len(eq_items) >= 1 and eq_items[0].get(6) is not None, '装备条目带 EquipTransMessage（客户端可渲染）')
# 购买该装备 → 买家背包得到装备
op, b, pushes = send_req(sa, 20185, vf(1, eq_items[0].get(1)) + vf(90, 25), 25)
check(op == 20186 and fld(b, 92) is None, '买家购买装备成功')

sa.close(); sb.close()
print()
print('=== 寄售行回归全部通过：%d 项 ===' % ok)
