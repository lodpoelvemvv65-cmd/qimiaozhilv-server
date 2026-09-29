# -*- coding: utf-8 -*-
"""双人交互验证：窥探(PK挑战 20094) + 查看对方角色(20251 他人Id)。"""
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

# A 登录
acctA = 'pka' + str(int(time.time()))
sa = socket.create_connection(('127.0.0.1', 7756)); sa.settimeout(5)
login(sa, acctA, '窥探甲')
# B 登录
acctB = 'pkb' + str(int(time.time()))
sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(5)
login(sb, acctB, '窥探乙')
# B 的 playerID = A 看到 20036 UnitsInMap 里其他玩家 id
# 直接查 DB 拿 B 的 id
import sqlite3
conn = sqlite3.connect(r'data/mhq.db')
bid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acctB,)).fetchone()[0]
aid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acctA,)).fetchone()[0]
conn.close()
# A 窥探 B：20094 StartPKFight TargetId=bid
op, b, pushes = send_req(sa, 20094, vf(1, bid) + vf(90, 20), 20)
check(op == 20095 and fld(b, 92) is None, 'A 窥探 B（20094→20095）成功')
# B 收到 20096 挑战通知
b_pushes = drain(sb, 0.5)
check(any(o == 20096 for o, _ in b_pushes), 'B 收到 20096 挑战通知: %s' % sorted(set(o for o, _ in b_pushes)))
# PK 结束后双方都应收到胜负包，且服务端追加单人 TeamMember 清理临时敌方头像。
sa.sendall(pack(20171, b''))
sb.sendall(pack(20171, b''))
pa_end = drain(sa, 1.0)
pb_end = drain(sb, 1.0)
check(any(o in (20097, 20098) for o, _ in pa_end), 'A PK 退出收到胜负结算')
check(any(o in (20097, 20098) for o, _ in pb_end), 'B PK 退出收到胜负结算')
def singleton_team(frames, pid):
    snapshots = [bb for o, bb in frames if o == 20162]
    return any(fld(bb, 1) == pid and len([v for f, w, v in fields(bb) if f == 2 and w == 'bytes']) == 1
               for bb in snapshots)
check(singleton_team(pa_end, aid), 'A PK 退出后恢复单人队伍')
check(singleton_team(pb_end, bid), 'B PK 退出后恢复单人队伍')
# A 查看 B 角色：20251 GetCharacter{Id=bid}
op, b, pushes = send_req(sa, 20251, vf(1, bid) + vf(90, 21), 21)
check(op == 20252 and fld(b, 92) is None, 'A 查看 B 角色（20251 他人 Id）成功')
uc_present = any(f == 1 and w == 'bytes' for f, w, v in fields(b))
check(uc_present, '响应含对方 UnitCharacter')
sa.close(); sb.close()
print()
print('=== 双人互动（窥探/查看角色）回归全部通过：%d 项 ===' % ok)
