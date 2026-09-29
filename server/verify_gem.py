# -*- coding: utf-8 -*-
"""宝石系统验证：镶嵌 20212 → 20213（带 BagMapList+GemList），拆卸 20349 → 20350。
流程：背包放装备 120660(Type=2, MaxHole=4) + 宝石 20050/20063（琥珀石一级/二级，GemType2, GemKey3）
  镶嵌 20050@槽0 → 扣宝石1 + 扣金币50 + 20213 BagMapList 中装备 GemList=[20050,0,0,0]
  再镶 20063@槽1 → 同 GemType 拒绝（Message 同一种宝石）
  拆卸 槽0 → 扣金币200 + 宝石回包 + 槽清空
"""
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

def bag_gemlist(resp_body):
    """从 20213 响应 BagMapList(tag1) 提取装备(120660)的 GemList(tag12)"""
    out = {}
    for f, wt, v in fields(resp_body):
        if f == 1 and wt == 'bytes':
            bm = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
            idx = bm.get(1)
            gemlist = []
            for ff, ww, vv in fields(v):
                if ff == 3 and ww == 'bytes':  # EquipTransMessage
                    for g, gw, gv in fields(vv):
                        if g == 12 and gw == 'var':
                            gemlist.append(gv)
            out[idx] = gemlist
    return out

def coin_of(acct):
    conn = sqlite3.connect(r'data/mhq.db')
    c = conn.execute("SELECT coin FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
    conn.close()
    return c
def bag_of(acct):
    conn = sqlite3.connect(r'data/mhq.db')
    row = conn.execute("SELECT bag_json FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()
    bag = json.loads(row[0] or '[]')
    conn.close()
    return bag

def msg_txt_of(b):
    m = fld(b, 92)
    if m is None:
        return ''
    return m.decode('utf-8') if isinstance(m, bytes) else str(m)

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

acct = 'gem' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '宝石测试')
s.close(); time.sleep(1.5)
conn = sqlite3.connect(r'data/mhq.db')
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
bag = [{"k":0,"v":{"i":120660,"t":1,"s":0,"c":1,"l":False,"q":3,"r":0,"v":1,"k":0}},
       {"k":1,"v":{"i":20050,"t":3,"s":0,"c":5,"l":False,"q":0,"r":0,"v":0}},
       {"k":2,"v":{"i":20063,"t":3,"s":0,"c":5,"l":False,"q":0,"r":0,"v":0}}]
conn.execute('UPDATE players SET bag_json=?, coin=1000000 WHERE id=?', (json.dumps(bag), pid))
conn.commit(); conn.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '宝石测试')
print('  初始金币:', coin_of(acct))
# 镶嵌 20050@槽0（EquipIndex=0, GemIndex=1, AttributeIndex=0）
op, b, pushes = send_req(s, 20212, vf(1, 0) + vf(2, 1) + vf(3, 0) + vf(90, 11), 11)
msg_txt = msg_txt_of(b)
print('  镶嵌 20050@槽0 resp:', op, 'msg:', msg_txt)
gl = bag_gemlist(b)
print('  20213 BagMapList GemList:', gl)
check(op == 20213 and not msg_txt, '镶嵌成功（msg 空）')
check(gl.get(0) == [20050, 0, 0, 0], '装备 GemList=[20050,0,0,0]: %s' % gl.get(0))
check(coin_of(acct) == 1000000 - 50, '镶嵌扣金币 50')
nb = bag_of(acct)
g20050 = sum(e['v'].get('c', 1) for e in nb if e['v']['i'] == 20050)
check(g20050 == 4, '宝石 20050 消耗 1（剩 4）')
# 同 GemType 拒绝：20063 也是 GemType2 → 槽1 拒绝
op, b, pushes = send_req(s, 20212, vf(1, 0) + vf(2, 2) + vf(3, 1) + vf(90, 12), 12)
msg_txt = msg_txt_of(b)
print('  同 GemType 镶嵌 resp msg:', msg_txt)
check('同一种宝石' in msg_txt, '同 GemType 重复镶嵌拒绝（%s）' % msg_txt)
check(coin_of(acct) == 1000000 - 50, '拒绝未扣费')
# 拆卸 槽0（bagIndex=0, gemIndex=0）
op, b, pushes = send_req(s, 20349, vf(1, 0) + vf(2, 0) + vf(90, 13), 13)
msg_txt = msg_txt_of(b)
print('  拆卸 槽0 resp:', op, 'msg:', msg_txt)
check(op == 20350 and not msg_txt, '拆卸成功（msg 空）')
check(coin_of(acct) == 1000000 - 50 - 200, '拆卸扣金币 200')
nb = bag_of(acct)
g20050b = sum(e['v'].get('c', 1) for e in nb if e['v']['i'] == 20050)
check(g20050b == 5, '宝石回包（20050 回到 5）')
# 空槽拆卸拒绝
op, b, pushes = send_req(s, 20349, vf(1, 0) + vf(2, 0) + vf(90, 14), 14)
msg_txt = msg_txt_of(b)
check('没有宝石' in msg_txt, '空槽拆卸拒绝（%s）' % msg_txt)
s.close()
print()
print('=== 宝石系统回归全部通过：%d 项 ===' % ok)
