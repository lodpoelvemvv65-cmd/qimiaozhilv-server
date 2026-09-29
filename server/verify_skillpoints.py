# -*- coding: utf-8 -*-
"""技能点系统验证 v3（客户端不崩版 + 线上技能书）：
  客户端 Session.Call 对 Error!=0 抛异常崩溃 → 服务器任何失败返回成功（Error=0）状态不变。
  规则：首次学习只扣技能点；已学技能升级只扣升级前当前等级行的技能书。
  技能书（20216-20219/20221）在 MaterialBase（背包可显示），MultiShop Type4 有售（铜币）。
  新号无技能点/无书 → Error=0、Message 明确提示且技能不变（不崩）；
  有技能点 → 新学扣点不扣书；升级扣书不扣点；书不足升级明确拒绝。"""
import os, socket, struct, time, sys, sqlite3, json
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
HOST = os.environ.get('MHQ_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_PORT', '7756'))
DB_PATH = os.environ.get('MHQ_DB', r'data/mhq.db')

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
def skill_point_of(acct):
    conn = sqlite3.connect(DB_PATH)
    v = conn.execute("SELECT skill_point FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
    conn.close()
    return v
def skills_of(acct):
    conn = sqlite3.connect(DB_PATH)
    v = conn.execute("SELECT skills FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
    conn.close()
    return v
def bag_of(acct):
    conn = sqlite3.connect(DB_PATH)
    v = conn.execute("SELECT bag_json FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
    conn.close()
    if not v:
        return {}
    import json as _j
    out = {}
    for e in _j.loads(v):
        it = e.get('v', {})
        out[it.get('i')] = out.get(it.get('i'), 0) + it.get('c', 0)
    return out

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

# A：新号无技能点无书 → 学 110101 返回成功（Error=0）但技能不变（客户端不崩）
acctA = 'spa' + str(int(time.time()))
sa = socket.create_connection((HOST, PORT)); sa.settimeout(5)
login(sa, acctA, '技能点甲')
before = skills_of(acctA)
op, b, pushes = send_req(sa, 20243, vf(1, 110101) + vf(90, 11), 11)
print('  新号学技能 resp:', op, 'message:', fld(b, 92))
check(op == 20244 and fld(b, 91) is None and fld(b, 92), '无技能点 → Error=0 且返回明确提示（不崩）')
check(skills_of(acctA) == before, '技能状态未变')
sa.close()

# B：技能点 5；学习不需要书，升级前再买 1 本军官技能秘籍
acctB = 'spb' + str(int(time.time()))
sb = socket.create_connection((HOST, PORT)); sb.settimeout(5)
login(sb, acctB, '技能点乙')
sb.close(); time.sleep(1.5)
conn = sqlite3.connect(DB_PATH)
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acctB,)).fetchone()[0]
conn.execute('UPDATE players SET skill_point=5 WHERE id=?', (pid,))
conn.commit(); conn.close()
sb = socket.create_connection((HOST, PORT)); sb.settimeout(5)
login(sb, acctB, '技能点乙')
print('  初始技能点:', skill_point_of(acctB), '背包:', bag_of(acctB))
# 1) 无书时学 110101 → 扣技能点，不扣书
op, b, pushes = send_req(sb, 20243, vf(1, 110101) + vf(90, 21), 21)
check(op == 20244 and fld(b, 91) is None and fld(b, 92) is None and skill_point_of(acctB) == 4
      and '110101:1' in skills_of(acctB) and bag_of(acctB).get(20217, 0) == 0,
      '无书学习 110101 → 技能点 5->4，技能书不参与')
# 2) 再学 110201 → 再扣 1 技能点，仍不需要书
op, b, pushes = send_req(sb, 20243, vf(1, 110201) + vf(90, 22), 22)
check(op == 20244 and fld(b, 92) is None and skill_point_of(acctB) == 3
      and '110201:1' in skills_of(acctB), '学习 110201 → 技能点 4->3，不扣书')
# 3) MultiShop Type4 买 20217×1（idx1=14002 军官秘籍，100 铜币/本）
op, b, pushes = send_req(sb, 20417, vf(1, 4) + vf(2, 1) + vf(3, 1) + vf(90, 23), 23)
time.sleep(0.3)  # 等 saveData 落库
check(op == 20418 and fld(b, 92) is None and bag_of(acctB).get(20217) == 1, 'MultiShop 买 20217×1 入包')
# 4) 升级 110101 -> 2 级：技能点保持 3，当前等级 1 行技能书 1->0
op, b, pushes = send_req(sb, 20243, vf(1, 110101) + vf(90, 24), 24)
time.sleep(0.3)
sk = skills_of(acctB)
print('  升级 110101 后 skills:', sk, '书:', bag_of(acctB).get(20217))
check('110101:2' in sk and bag_of(acctB).get(20217, 0) == 0, '升级 110101 -> lv2 扣书（20217 1->0）')
check(skill_point_of(acctB) == 3, '升级不扣技能点（保持 3）')
# 5) 无书再升级 → 明确提示（保持 lv2、技能点不变）
op, b, pushes = send_req(sb, 20243, vf(1, 110101) + vf(90, 25), 25)
time.sleep(0.3)
check(op == 20244 and fld(b, 91) is None and fld(b, 92) and '110101:2' in skills_of(acctB)
      and skill_point_of(acctB) == 3, '无书升级 → 明确提示且等级/技能点不变')
# 6) 学前置未满足的 110305（前置 110304 未学）→ 返回成功但不变（不崩）
op, b, pushes = send_req(sb, 20243, vf(1, 110305) + vf(90, 26), 26)
sk2 = skills_of(acctB)
check(op == 20244 and fld(b, 91) is None and fld(b, 92) and '110305' not in sk2,
      '前置未满足 → Error=0、明确提示且状态不变')
sb.close()
print()
print('=== 技能点系统回归（线上技能书版）全部通过：%d 项 ===' % ok)
