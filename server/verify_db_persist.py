# -*- coding: utf-8 -*-
"""走数据库验证：世界 BOSS / 家族 BOSS / 寄售 三套状态重启后必须保留（DB 持久化）。
流程：
1) 建账号 A/B → A 建族 → B 入族 → 家族 BOSS 1 开战 → 残血（不击杀）
   → 寄售：A 上架 2 条（无密码 + 密码）
   → 世界 BOSS：击杀层 1 —— 直接往 DB 插 world_boss_states 模拟击杀（等价 markBossDead 落库）
2) 重启服务器（本脚本只测到落库；重启由外部执行）
3) 重启后：家族 BOSS 信息仍显示残血、寄售列表 2 条仍在、层 1 BOSS 仍提示已阵亡倒计时
"""
import socket, struct, time, sys, sqlite3, json, re, subprocess, os
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
    for _ in range(600):
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

base = 'dbd' + str(int(time.time()))

# ===== 阶段 1：造数据 =====
# 注册 A/B（先登录注册）
def reg(acct, name):
    s = socket.create_connection((HOST, PORT)); s.settimeout(8)
    try:
        login(s, acct, name)
    finally:
        s.close()
    time.sleep(1)
    conn = sqlite3.connect(DB)
    pid = conn.execute("SELECT p.id FROM players p JOIN accounts a ON a.id=p.account_id WHERE a.account=?",
                       (acct,)).fetchone()[0]
    conn.close()
    return pid

pidA = reg(base + 'a', '库验甲')
pidB = reg(base + 'b', '库验乙')
print('  A=%d B=%d' % (pidA, pidB))

s = socket.create_connection((HOST, PORT)); s.settimeout(8)
login(s, base + 'a', '库验甲')
# A 建族
fname = '库验家族%d' % (int(time.time()) % 10000)
op, b, pushes = send_req(s, 20123, sf(1, fname) + vf(90, 11), 11)
check(op == 20124 and fld(b, 92) is None, 'A 建族')
# B 申请 + A 同意
s2 = socket.create_connection((HOST, PORT)); s2.settimeout(8)
login(s2, base + 'b', '库验乙')
op, b, pushes = send_req(s2, 20131, sf(1, fname) + vf(90, 12), 12)
check(op == 20132 and fld(b, 92) is None, 'B 申请')
op, b, pushes = send_req(s, 20135, vf(1, 1) + vf(2, pidB) + vf(90, 13), 13)
check(op == 20136 and fld(b, 92) is None, 'A 同意 B')

# 家族 BOSS 1 开战 → 真实攻击一次 → 退出战斗触发服务端落库
op, b, pushes = send_req(s, 20142, vf(1, 1) + vf(90, 14), 14)
check(op == 20143 and fld(b, 92) is None, '开家族 BOSS 1 战')
op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 15), 15)
check(op == 20234 and fld(b, 92) is None, '家族 BOSS 真实攻击一次')
op, b, pushes = send_req(s, 20171, vf(90, 16), 16)
check(op == 20171, '退出家族 BOSS 战触发落库')
conn = sqlite3.connect(DB)
fid = conn.execute("SELECT id FROM families WHERE name=?", (fname,)).fetchone()[0]
print('  家族 id=%d' % fid)
family_hp, family_max = conn.execute(
    'SELECT hp, max_hp FROM family_boss_states WHERE family_id=? AND boss_id=1', (fid,)).fetchone()
check(0 < family_hp < family_max, '真实战斗后的家族 BOSS 残血已落库')
print('  DB 家族 BOSS1 残血 %d/%d 已写入' % (family_hp, family_max))

# 寄售：A 上架 2 条（格1 药水；A 需先有药水）
op, b, pushes = send_req(s, 20174, vf(1, 0) + vf(2, 0) + vf(3, 3) + vf(90, 15), 15)  # 买 3 药水
pot = None
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        bm = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
        ni = dict((ff, vv) for ff, ww, vv in fields(fld(v, 2) or b'') if ww == 'var')
        if ni.get(1) == 110305:
            pot = bm.get(1)
check(pot is not None, 'A 买到药水（格 %s）' % pot)
op, b, pushes = send_req(s, 20183, vf(1, pot) + vf(2, 1000) + vf(3, 2) + vf(90, 16), 16)
check(op == 20184 and fld(b, 92) is None, 'A 上架 1（无密码）')
op, b, pushes = send_req(s, 20183, vf(1, pot) + vf(2, 500) + vf(3, 1) + sf(4, '1234') + vf(90, 17), 17)
check(op == 20184 and fld(b, 92) is None, 'A 上架 2（密码 1234）')
# DB 检查
conn = sqlite3.connect(DB)
cnt = conn.execute('SELECT COUNT(*) FROM consignment_items').fetchone()[0]
seq = conn.execute("SELECT value FROM server_meta WHERE name='consign_seq'").fetchone()
print('  DB 寄售条目:', cnt, '序列:', seq)
check(cnt >= 2 and seq and seq[0] >= 2, '寄售 2 条已落库 + 序列持久化')

# 世界 BOSS：往 DB 直接插死亡记录（等价 markBossDead 落库，层 1）
import time as _t
dead_ms = int(_t.time() * 1000)
conn.execute('''INSERT INTO world_boss_states (layer, dead_at, killer) VALUES (1, ?, '库验甲')
                ON CONFLICT(layer) DO UPDATE SET dead_at=excluded.dead_at''', (dead_ms,))
conn.commit()
conn.close()
print('  DB 世界 BOSS 层1 死亡已写入（dead_at=%d）' % dead_ms)

# 服务端内存此刻未重启（不读 DB）——s 可能在家族 BOSS 战中被占用或断连，直接关闭进入下一阶段
try:
    s.close()
except Exception:
    pass
try:
    s2.close()
except Exception:
    pass
# 服务器当前进程内存态已包含 A 上架 2 条（重启后才会走 DB 恢复）；此处仅确认 DB 落库完成
print()
print('=== 阶段 1 完成：数据已全部落库（家族BOSS残血/寄售2条/世界BOSS死亡） ===')
print('下一步：重启服务器后跑 verify_db_persist_after.py 验证重启保留 ===')
