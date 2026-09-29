# -*- coding: utf-8 -*-
"""回城卷轴 + 登录固定回主城 验证：
1) 用回城卷轴(110344) → changeMap 回主城 10004
2) 登录（上次在海滩/主线）→ 强制回主城左光圈"""
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

acct = 'rt' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '回城测试')
s.close(); time.sleep(1.5)
# 塞回城卷轴 110344，并把地图设到主线 1001103（初心勃岛）
conn = sqlite3.connect(DB_PATH)
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
bag = [{"k":1,"v":{"i":110344,"t":2,"s":0,"c":3,"l":False,"q":0,"r":0,"v":0}}]
conn.execute('UPDATE players SET bag_json=?, map_id=1001103, pos_x=12.4, pos_y=-2.55 WHERE id=?', (json.dumps(bag), pid))
conn.commit(); conn.close()

# 重登 → 应强制回主城 10004 左光圈（ChangeMap 在 20028 响应后推，需额外排水）
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
pushes = login(s, acct, '回城测试')
pushes += drain(s, 0.8)
cm = None
for o, bb in pushes:
    if o == 20033:
        cm = (fld(bb, 4), fld(bb, 2), fld(bb, 3))
print('  登录 ChangeMap:', cm)
check(cm is not None and cm[0] == 1001103 and abs(cm[1] - 12.4) < 0.01 and abs(cm[2] + 2.55) < 0.01,
      '登录恢复数据库地图坐标: %s' % (cm,))

# 使用回城卷轴 → 回主城；登录时背包会规范化为客户端实际槽位。
op, bag_body, _ = send_req(s, 20258, vf(90, 10), 10)
scroll_index = bag_index_of(bag_body, 110344)
check(scroll_index is not None, 'return scroll slot resolved from GetBag')
op, b, pushes = send_req(s, 20275, vf(1, scroll_index) + vf(90, 11), 11)
pushes += drain(s, 0.8)
check(fld(b, 92) is None, '使用回城卷轴成功')
cm2 = None
for o, bb in pushes:
    if o == 20033:
        cm2 = (fld(bb, 4), fld(bb, 2), fld(bb, 3))
print('  回城卷轴 ChangeMap:', cm2)
check(cm2 is not None and cm2[0] == 1000401, '回城卷轴回主城: %s' % (cm2,))
s.close()
print()
print('=== 回城卷轴/登录回主城回归全部通过：%d 项 ===' % ok)
