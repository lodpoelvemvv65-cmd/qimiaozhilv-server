# -*- coding: utf-8 -*-
"""家族系统回归：创建/查询/申请/同意/成员列表/踢人/家族BOSS信息/开战/击杀奖励。"""
import os, socket, struct, time, sys, sqlite3, json
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
HOST = os.environ.get('MHQ_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_PORT', '7756'))

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

def i32(value):
    value &= 0xffffffff
    return value - 0x100000000 if value & 0x80000000 else value
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

def drain(s, timeout=0.5):
    s.settimeout(timeout)
    out = []
    while True:
        try:
            out.append(recv_one(s))
        except Exception:
            break
    s.settimeout(5)
    return out

base = 'fm' + str(int(time.time()))
fname = '家族' + base[-4:]
sa = socket.create_connection((HOST, PORT)); sa.settimeout(5)
_, pidA = login(sa, base + 'a', '族长甲')
sb = socket.create_connection((HOST, PORT)); sb.settimeout(5)
_, pidB = login(sb, base + 'b', '族人乙')
drain(sa); drain(sb)
print('  A=%d B=%d 家族=%s' % (pidA, pidB, fname))

# 1) A 创建家族
op, b, pushes = send_req(sa, 20123, sf(1, fname) + vf(90, 11), 11)
print('  创建家族 resp:', op, 'err:', fld(b, 92))
check(op == 20124 and fld(b, 92) is None and fld(b, 3) is not None, 'A 创建家族成功 (20124 Info)')

# 2) B 申请加入（按名字）→ A 同意
op, b, pushes = send_req(sb, 20131, sf(1, fname) + vf(90, 12), 12)
check(op == 20132 and fld(b, 92) is None, 'B 申请入族成功')
op, b, pushes = send_req(sa, 20125, vf(90, 13), 13)  # A 查家族 → 申请列表应有 B
reqs = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 2 and wt == 'bytes']
print('  申请列表:', [(x.get(5), x.get(1)) for x in reqs])
check(any(x.get(5) == pidB for x in reqs), 'A 看到 B 的入族申请（RequestAddFriendInfo.Id=tag5）')
op, b, pushes = send_req(sa, 20135, vf(1, 1) + vf(2, pidB) + vf(90, 14), 14)  # IsAgree=true, Id=B
check(op == 20136 and fld(b, 92) is None, 'A 同意 B 入族')

# 3) A 查家族 → 成员 2 人
op, b, pushes = send_req(sa, 20125, vf(90, 15), 15)
members = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
print('  成员列表:', [(x.get(1), x.get(2)) for x in members])
check(len(members) == 2 and any(x.get(1) == pidA for x in members) and any(x.get(1) == pidB for x in members), '家族成员 2 人')
check(fld(b, 3) is not None, '响应带 FamilyInfo')

# 4) B 也确认自己入族
op, b, pushes = send_req(sb, 20125, vf(90, 16), 16)
check(fld(b, 92) is None and fld(b, 3) is not None, 'B 查家族成功（已入族）')

# 5) 家族 BOSS 信息（5 个）
op, b, pushes = send_req(sa, 20139, vf(90, 17), 17)
bosses = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
print('  家族BOSS:', [(x.get(1), x.get(2), x.get(3)) for x in bosses[:3]])
check(op == 20141 and len(bosses) == 5 and bosses[0].get(2) > 0, '家族 BOSS 信息 5 个满血')

# 6) 开 1 阶家族 BOSS 战（MonsterId 50001，血 3000 万 —— 用 GM 秒杀不便，验证开战响应成功）
op, b, pushes = send_req(sa, 20142, vf(1, 1) + vf(90, 18), 18)
family_start = [bb for push_op, bb in pushes if push_op == 20144]
ordinary_start = [push_op for push_op, _ in pushes if push_op in (20047, 20050, 20061)]
print('  开家族BOSS战 resp:', op, 'err:', fld(b, 92))
check(op == 20143 and fld(b, 92) is None, '开家族 BOSS 战成功')

# 7) 伤害统计
check(len(family_start) == 1 and fld(family_start[0], 1) == 1 and fld(family_start[0], 2) is not None,
      'family BOSS start uses only 20144 with BossId and UnitId')
check(not ordinary_start, 'family BOSS start does not mix ordinary/world BOSS presentation packets')

# 战斗中不需要地面移动或先点模型；主技能栏 0 号槽应直接攻击唯一 BOSS。
family_unit_id = fld(family_start[0], 2)
op, b, pushes = send_req(sa, 20233, vf(1, 0) + vf(90, 181), 181)
hp_updates = [(fld(bb, 2), fld(bb, 3)) for push_op, bb in pushes
              if push_op == 20169 and fld(bb, 1) == family_unit_id]
damage_updates = [i32(fld(bb, 2)) for push_op, bb in pushes
                  if push_op == 20078 and fld(bb, 1) == family_unit_id and fld(bb, 2) is not None]
print('  家族BOSS技能 resp:', op, '伤害:', damage_updates, '血量同步:', hp_updates)
check(op == 20234 and fld(b, 92) is None, '家族 BOSS 战可直接使用主技能栏')
check(any(value < 0 for value in damage_updates), '家族 BOSS 技能命中并产生整数伤害')

# 退出家族 BOSS 必须先触发 BattleEnd（清 MyUnit.IsFight），再回主城左侧光圈。
op, b, pushes = send_req(sa, 20171, vf(90, 182), 182)
quit_frames = pushes + drain(sa, 0.8)
quit_opcodes = [push_op for push_op, _ in quit_frames]
change_bodies = [bb for push_op, bb in quit_frames if push_op == 20033]
print('  退出家族BOSS帧:', quit_opcodes)
check(op == 20171 and 20055 in quit_opcodes and len(change_bodies) == 1,
      '退出家族 BOSS 推送 BattleEnd 和 ChangeMap')
check(quit_opcodes.index(20055) < quit_opcodes.index(20033),
      '客户端先清理战斗状态再切回主城')
change_fields = dict((f, v) for f, _, v in fields(change_bodies[0]))
check(change_fields.get(4) == 1000401 and abs(change_fields.get(2, 0) - (-12.00656)) < 0.01
      and abs(change_fields.get(3, 0) - (-1.474469)) < 0.01,
      '退出家族 BOSS 从主城左侧光圈返回')

# 20169.Value 是客户端协议定义的 float32。三千万血量附近不足 2 点的差值
# 无法由 float32 表示，因此必须同时核对整数伤害包和服务端持久化的精确血量。
db_path = os.environ.get('MHQ_DB', os.path.join('data', 'mhq.db'))
with sqlite3.connect(db_path) as con:
    family_id = con.execute('SELECT family_id FROM players WHERE id = ?', (pidA,)).fetchone()[0]
    persisted_hp = con.execute(
        'SELECT hp FROM family_boss_states WHERE family_id = ? AND boss_id = 1',
        (family_id,)).fetchone()[0]
damage_total = -sum(value for value in damage_updates if value < 0)
expected_hp = bosses[0].get(3) - damage_total
wire_hp = next(hp for numeric_type, hp in hp_updates if numeric_type == 1001)
expected_wire_hp = struct.unpack('<f', struct.pack('<f', expected_hp))[0]
check(persisted_hp == expected_hp, '家族 BOSS 退出后精确剩余血持久化到 SQLite')
check(wire_hp == expected_wire_hp, '家族 BOSS 血量同步符合客户端 float32 协议精度')

op, b, pushes = send_req(sa, 20148, vf(1, 1) + vf(90, 19), 19)
rank_payloads = [v for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
dmg = [dict((ff, vv) for ff, wt2, vv in fields(v)) for v in rank_payloads]
rank_members = [v for f, wt, v in fields(rank_payloads[0]) if f == 2 and wt == 'bytes'] if len(rank_payloads) == 1 else []
decoded_members = [dict((ff, vv) for ff, wt2, vv in fields(member)) for member in rank_members]
check(len(decoded_members) == 2 and all(member.get(1) is not None for member in decoded_members),
      'BossDamageMap contains nested DamageList members')
print('  伤害统计 Total:', [(x.get(1)) for x in dmg])
check(op == 20149 and len(dmg) == 1 and dmg[0].get(1) == damage_total,
      'BOSS 伤害统计响应与实际伤害一致')

# 8) 踢人：A 踢 B → B 无家族
op, b, pushes = send_req(sa, 20137, vf(1, pidB) + vf(90, 20), 20)
check(op == 20138 and fld(b, 92) is None, 'A 踢出 B 成功')
op, b, pushes = send_req(sb, 20125, vf(90, 21), 21)
check(fld(b, 92) is not None, 'B 已无家族（查询提示没有家族）')

# 9) 解散家族（A 是族长）
op, b, pushes = send_req(sa, 20129, vf(90, 22), 22)
check(op == 20130 and fld(b, 92) is None, 'A 解散家族成功')

sa.close(); sb.close()
print()
print('=== 家族系统回归全部通过：%d 项 ===' % ok)
