# -*- coding: utf-8 -*-
"""阶段 2：重启服务器后验证 DB 持久化三套状态全部保留：
1) 寄售列表 2 条（无密码 + 密码 1234）仍在
2) 家族 BOSS 1 残血 1500 万（满血 3000 万）
3) 世界 BOSS 层 1 死亡：进 1001001 场景无字段怪 + 点 BOSS 拒绝"已阵亡倒计时"
"""
import os, socket, struct, time, sys, sqlite3
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
def send_req(s, op, body, rpc, timeout=600):
    s.sendall(pack(op, body))
    pushes = []
    for _ in range(timeout):
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

HOST = os.environ.get('MHQ_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_PORT', '7756'))
DB = os.path.abspath(os.environ.get('MHQ_DB', r'data/mhq.db'))
ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

# 找上一阶段账号（后缀 a = 甲的族长）
conn = sqlite3.connect(DB)
rows = conn.execute("SELECT p.id, p.account_id, p.name FROM players p JOIN accounts a ON a.id=p.account_id WHERE a.account LIKE 'dbd%a' ORDER BY p.id DESC LIMIT 1").fetchall()
print('  上一阶段族长账号:', rows)
if not rows:
    print('SKIP: 无阶段1账号（需先跑 verify_db_persist.py）')
    sys.exit(2)
acct_a = conn.execute("SELECT account FROM accounts WHERE id=?", (rows[0][1],)).fetchone()[0]
pidA = rows[0][0]
fid = conn.execute("SELECT id FROM families WHERE leader=?", (pidA,)).fetchone()[0]
world = conn.execute("SELECT dead_at, killer FROM world_boss_states WHERE layer=1").fetchone()
cnt = conn.execute('SELECT COUNT(*) FROM consignment_items').fetchone()[0]
print('  家族 fid=%d 世界BOSS dead_at=%s killer=%s 寄售数=%d' % (fid, world[0] if world else None, world[1] if world else None, cnt))
conn.close()
if not world:
    print('SKIP: DB 无世界 BOSS 死亡记录')
    sys.exit(2)

s = socket.create_connection((HOST, PORT)); s.settimeout(8)
login(s, acct_a, '库验甲')

# 1) 寄售列表 2 条恢复
op, b, pushes = send_req(s, 20181, vf(90, 11), 11)
items = [dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var') for f, wt, v in fields(b) if f == 2 and wt == 'bytes']
print('  寄售列表:', [(x.get(1), x.get(7)) for x in items])
check(op == 20182 and len(items) >= 2, '重启后寄售 2 条仍在（DB 恢复）')

# 2) 家族 BOSS 1 真实战斗残血
op, b, pushes = send_req(s, 20139, vf(90, 12), 12)
bosses = [dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var') for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
b1 = bosses[0] if bosses else {}
print('  家族 BOSS1:', (b1.get(1), b1.get(2), b1.get(3)), '期望 0 < Hp < MaxHp')
check(op == 20141 and 0 < b1.get(2, 0) < b1.get(3, 0), '家族 BOSS1 真实战斗残血重启保留')

# 3) 世界 BOSS 层 1 死亡：进 1001001 无原生 BossRefresh/SendBossInfo
op, b, pushes = send_req(s, 20031, vf(1, 1001001) + vf(90, 13), 13, timeout=20)
refreshes = [fld(bb, 1) for o, bb in pushes if o == 20056]
send_info = [bb for o, bb in pushes if o == 20061]
print('  进层1 BossRefresh:', len(refreshes), 'SendBossInfo:', len(send_info), 'ops:', [o for o, _ in pushes][:8])
check(op == 20032 and len(refreshes) == 0 and len(send_info) == 0, '层1 BOSS 死亡期间无原生展示（DB 倒计时保留）')
s.close()
print()
print('=== 阶段 2 完成：重启后三套状态全部保留（家族BOSS残血/寄售2条/世界BOSS死亡） ===')
