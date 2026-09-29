# -*- coding: utf-8 -*-
"""技能学习链路验证：GetSkill 返回全部职业技能（未学 lv0）→ LearnSkill 全部可学。"""
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

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

acct = 'sk' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '技能学习')
# 给测试号技能点 + 技能书（新学扣技能点、升级扣技能书）
s.close(); time.sleep(1.5)
import sqlite3, json as _json
conn = sqlite3.connect(r'data/mhq.db')
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
conn.execute('UPDATE players SET skill_point=50 WHERE id=?', (pid,))
row = conn.execute('SELECT bag_json FROM players WHERE id=?', (pid,)).fetchone()
bag = _json.loads(row[0] or '[]')
bag.append({'k': 99, 'v': {'i': 20217, 't': 2, 's': 0, 'c': 50, 'l': False, 'q': 0, 'r': 0, 'v': 0}})
conn.execute('UPDATE players SET bag_json=? WHERE id=?', (_json.dumps(bag), pid))
conn.commit(); conn.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '技能学习')
# GetSkill → 全部职业技能
op, b, pushes = send_req(s, 20239, vf(90, 10), 10)
skills = []
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        kv = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
        skills.append((kv.get(1), kv.get(2)))
print('  GetSkill 返回技能数:', len(skills), '前 6:', skills[:6])
check(len(skills) == 22, '返回职业全部 22 个技能: %d' % len(skills))
check(any(i == 110101 for i, _ in skills), '含未学技能 110101')
# proto3 零值省略 → 未学技能 SkillLevel tag 缺省（客户端解析为 0）
lv0 = [i for i, l in skills if l in (0, None)]
check(len(lv0) == 21, '未学技能 SkillLevel=0（21 个）: %d' % len(lv0))
# 学习 110101（前置 100001 已学，扣技能点）
op, b, pushes = send_req(s, 20243, vf(1, 110101) + vf(90, 11), 11)
check(op == 20244 and fld(b, 92) is None, '学习 110101 成功')
sk2 = []
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        kv = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
        sk2.append((kv.get(1), kv.get(2)))
check(any(i == 110101 and l == 1 for i, l in sk2), '学习后 110101 等级 1: %s' % [x for x in sk2 if x[0] == 110101])
# 学 110201（前置 110101 已学）
op, b, pushes = send_req(s, 20243, vf(1, 110201) + vf(90, 12), 12)
check(op == 20244 and fld(b, 92) is None, '学习 110201 成功（前置 110101 满足）')
# 未学前置失败：110202 前置是 110201？110202 前置 110201 已学 → 学 110301 需要 110202？跳过，测试 120101 大招
op, b, pushes = send_req(s, 20243, vf(1, 120101) + vf(90, 13), 13)
check(op == 20244 and fld(b, 92) is None, '学习 120101 大招成功')
s.close()
print()
print('=== 技能学习链路回归全部通过：%d 项 ===' % ok)
