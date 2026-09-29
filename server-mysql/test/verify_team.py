# -*- coding: utf-8 -*-
"""组队系统回归（双玩家）：邀请→接受→TeamMember 全量同步；申请→同意；转让队长；退队；踢人。"""
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

def read_packed_varints(b):
    out = []
    i = 0
    while i < len(b):
        v = 0; sh = 0
        while True:
            x = b[i]; i += 1; v |= (x & 0x7f) << sh; sh += 7
            if not x & 0x80: break
        out.append(v)
    return out

def team_member(pushes):
    for o, b in reversed(pushes):
        if o != 20162:
            continue
        leader = fld(b, 1)
        units = []
        for f, wt, v in fields(b):
            if f == 2 and wt == 'var':
                units.append(v)
            elif f == 2 and wt == 'bytes':
                units.extend(read_packed_varints(v))
        return leader, units
    return None, None

def drain(s, timeout=0.6):
    s.settimeout(timeout)
    out = []
    while True:
        try:
            out.append(recv_one(s))
        except Exception:
            break
    s.settimeout(5)
    return out

base = 'tm' + str(int(time.time()))
sa = socket.create_connection(('127.0.0.1', 7756)); sa.settimeout(5)
pushA, pidA = login(sa, base + 'a', 'A' + base[-10:])
sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(5)
pushB, pidB = login(sb, base + 'b', 'B' + base[-10:])
print('  A=%d B=%d' % (pidA, pidB))

# 1) A 邀请 B → B 收 20159 InviteList{UnitId=A}
op, b, pushes = send_req(sa, 20156, vf(1, pidB) + vf(90, 11), 11)
check(op == 20157 and fld(b, 92).decode('utf-8') == '正在邀请...',
      'A 邀请 B 显示发送中状态 (20157)')
inv = [bb for o, bb in drain(sb) if o == 20159]
check(inv and fld(inv[0], 1) == pidA, 'B 收 20159 InviteList{UnitId=A}: %s' % [fld(x,1) for x in inv])

# 2) B 接受（HandleTeam{HandleInfo{Id=A,Bool=true}, IsRequest=false}）→ 双方收 20162 全量
def handle_team_body(target_id, agree, is_request, rpc):
    hi = vf(1, target_id) + vf(2, 1 if agree else 0)
    return varint((1 << 3) | 2) + varint(len(hi)) + hi + vf(2, 1 if is_request else 0) + vf(90, rpc)

op, b, pushes = send_req(sb, 20160, handle_team_body(pidA, True, False, 12), 12)
check(op == 20161 and fld(b, 92) is None, 'B 接受邀请成功 (20161)')
tmB = team_member(pushes + drain(sb, 1.5))
drainA = drain(sa, 1.5)
tmA = team_member(drainA)
print('  A 收 TeamMember:', tmA, ' B 收:', tmB)
check(tmB and tmB[0] == pidA and sorted(tmB[1]) == sorted([pidA, pidB]), 'B 队伍同步 Leader=A 成员[A,B]: %s' % (tmB,))
check(tmA and tmA[0] == pidA and sorted(tmA[1]) == sorted([pidA, pidB]), 'A 队伍同步 Leader=A 成员[A,B]: %s' % (tmA,))
accepted_tips = [fld(bb, 1).decode('utf-8') for o, bb in drainA if o == 20262]
check('对方已同意' in accepted_tips, '邀请方原提示框更新为对方已同意')

# 3) B 申请入队（新队伍 C 邀请场景）→ A 收 20158 RequestList → A 同意（IsRequest=true）
#    先 B 退队再申请 A（同队重复申请无意义；此处验证申请→同意入队链路）
op, b, pushes = send_req(sb, 20165, vf(90, 13), 13)  # B 退队
check(op == 20166, 'B 退队成功')
drainB = drain(sa)  # A 收剩余成员同步
op, b, pushes = send_req(sb, 20154, vf(1, pidA) + vf(90, 14), 14)  # B 申请加入 A
check(op == 20155 and fld(b, 92).decode('utf-8') == '正在申请...',
      'B 申请入队显示发送中状态 (20155)')
req = [bb for o, bb in drain(sa) if o == 20158]
check(req and fld(req[0], 1) == pidB, 'A 收 20158 RequestList{UnitId=B}')
op, b, pushes = send_req(sa, 20160, handle_team_body(pidB, True, True, 15), 15)  # A 同意（IsRequest=true）
tmA2 = team_member(pushes)
framesB2 = drain(sb)
tmB2 = team_member(framesB2)
print('  申请后 A:', tmA2, ' B:', tmB2)
check(tmA2 and tmA2[0] == pidA and sorted(tmA2[1]) == sorted([pidA, pidB]), '申请同意后 A 队伍同步')
check(tmB2 and tmB2[0] == pidA and sorted(tmB2[1]) == sorted([pidA, pidB]), '申请同意后 B 队伍同步')
request_tips = [fld(bb, 1).decode('utf-8') for o, bb in framesB2 if o == 20262]
check('对方已同意' in request_tips, '申请方原提示框更新为对方已同意')

# 3b) C applies through ordinary member B: only leader A receives the request.
sc = socket.create_connection(('127.0.0.1', 7756)); sc.settimeout(5)
pushC, pidC = login(sc, base + 'c', 'C' + base[-10:])
drain(sa); drain(sb)
op, b, pushes = send_req(sc, 20154, vf(1, pidB) + vf(90, 20), 20)
check(op == 20155 and fld(b, 92).decode('utf-8') == '正在申请...',
      'C request through member B shows pending status')
leader_requests = [bb for o, bb in drain(sa) if o == 20158]
member_requests = [bb for o, bb in drain(sb) if o == 20158]
check(leader_requests and fld(leader_requests[-1], 1) == pidC and
      fld(leader_requests[-1], 93) == pidA,
      'request through B is routed to leader A')
check(not member_requests, 'ordinary member B does not receive the application')
op, b, pushes = send_req(sb, 20160, handle_team_body(pidC, True, True, 21), 21)
check(op == 20161 and fld(b, 92) is not None,
      'ordinary member B cannot approve the application or become leader')
op, b, pushes = send_req(sa, 20160, handle_team_body(pidC, False, True, 22), 22)
check(op == 20161, 'leader A can reject the routed application')
rejected_tips = [fld(bb, 1).decode('utf-8') for o, bb in drain(sc) if o == 20262]
check('对方已拒绝' in rejected_tips, '申请方原提示框更新为对方已拒绝')

# 4) A 转让队长给 B → LeaderId=B
op, b, pushes = send_req(sa, 20163, vf(1, pidB) + vf(90, 16), 16)
check(op == 20164 and fld(b, 92) is None, 'A 转让队长给 B 成功')
tmA3 = team_member(pushes + drain(sa))
tmB3 = team_member(drain(sb))
print('  转让后 A:', tmA3, ' B:', tmB3)
check(tmB3 and tmB3[0] == pidB, '转让后 Leader=B: %s' % (tmB3,))
check(tmA3 and tmA3[0] == pidB, 'A 也收到 Leader=B')

# 5) 新队长 B 踢出 A → A 收 TeamMember{Leader=B 单成员}（A 已离队）
op, b, pushes = send_req(sb, 20167, vf(1, pidA) + vf(90, 17), 17)
check(op == 20168 and fld(b, 92) is None, 'B 踢出 A 成功')
tmA4 = team_member(drain(sa))
tmB4 = team_member(pushes)
print('  踢人后 A:', tmA4, ' B:', tmB4)
check(tmA4 and tmA4[0] == pidA and tmA4[1] == [pidA], 'A 收单人队伍同步(被踢) Leader=A: %s' % (tmA4,))
check(tmB4 and tmB4[0] == pidB and tmB4[1] == [pidB], 'B 队伍只剩自己')

# 6) 拒绝链路：C 申请 A，A 拒绝（Bool=false）→ 双方无队伍变化
op, b, pushes = send_req(sc, 20154, vf(1, pidB) + vf(90, 18), 18)
req2 = [bb for o, bb in drain(sb) if o == 20158]
check(req2 and fld(req2[0], 1) == pidC, 'C 申请 B → B 收 RequestList')
op, b, pushes = send_req(sb, 20160, handle_team_body(pidC, False, True, 19), 19)
check(op == 20161, 'B 拒绝 C 成功')
tmC = team_member(drain(sc))
check(tmC in (None, (None, None)) or (tmC[0] == pidC and tmC[1] == [pidC]), 'C 未入队（单人队伍）')
sc.close()
sa.close()
sb.close()
print()
print('=== 组队系统回归全部通过：%d 项 ===' % ok)
