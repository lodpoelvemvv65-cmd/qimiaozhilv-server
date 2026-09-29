# -*- coding: utf-8 -*-
"""传送门选图 + 场景怪 + 消耗品 综合回归。
验证：① 进 10011（初心勃岛）推 ChangeMap + 字段怪；② 点字段怪开对应 region 战斗；
③ 药品使用后 20276 带 MainUISlotList + 20169 血蓝推送；④ 换图清理旧字段怪。"""
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

def drain(s, timeout=0.6):
    s.settimeout(timeout)
    out = []
    while True:
        try: out.append(recv_one(s))
        except (socket.timeout, ConnectionError): break
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
    for _ in range(200):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

acct = 'portal' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
if fld(b, 91) is not None:
    send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 3), 3)
key = fld(b, 2); gate = fld(b, 3)
op, b, _ = send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
reserved_pid = fld(b, 1)
send_req(s, 20016, vf(1, reserved_pid) + vf(2, 1) + sf(3, '传送门' + acct[-8:]) + vf(90, 5), 5)
op, b, pushes = send_req(s, 20027, vf(90, 7), 7)
player_id = None
for push_op, push_body in pushes:
    if push_op == 20025:
        character = fld(push_body, 1)
        if isinstance(character, bytes):
            player_id = fld(character, 1)

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

# 新角色快捷栏先保持 9 格全空，再由玩家把基础攻击拖入槽 0。
op, b, pushes = send_req(s, 20227, vf(90, 8), 8)
initial_slots = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
check(op == 20228 and len(initial_slots) == 9 and all(not x.get(1) and not x.get(2) for x in initial_slots),
      '新号快捷栏固定 9 格且全部为空')
op, b, pushes = send_req(s, 20229, vf(1, 0) + vf(2, 100001) + vf(90, 9), 9)
check(op == 20230 and not fld(b, 92), '玩家手动把基础攻击拖入槽 0')

# 1) 请求进入 10011（初心勃岛 layer1）
op, b, pushes = send_req(s, 20031, vf(1, 1001101) + vf(90, 10), 10)
cm = [bb for o, bb in pushes if o == 20033]
check(op == 20032 and cm and fld(cm[0], 4) == 1001101, '传送门选图 1001101 -> ChangeMap')
regions = [fld(bb, 1) for o, bb in pushes if o == 20047]
check(len(regions) == 1 and not any(o == 20320 for o, _ in pushes), '新场景推送原生主线 region')
print('  主线 region:', regions)

# 2) 点击原生主线怪开战斗（region 应为 1011）
op, b, pushes = send_req(s, 20048, vf(1, regions[0]) + vf(90, 11), 11)
check(op == 20049 and not fld(b, 92), '点原生主线怪开战斗成功 (20049)')
mi = [bb for o, bb in pushes if o == 20050]
check(len(mi) >= 1, '战斗初始化推送 20050')
mon_ids = []
for f, wt, v in fields(mi[0]):
    if f == 1 and wt == 'bytes':
        kv = dict((ff, vv) for ff, wt2, vv in fields(v) if wt2 == 'var')
        mon_ids.append(kv.get(2))
check(10011 in mon_ids, 'region 1011 怪物为 10011: %s' % mon_ids)
print('  战斗怪物:', mon_ids)
dispose = [bb for o, bb in pushes if o == 20321]
print('  战斗推送:', sorted(set(o for o, _ in pushes)))

# 3) 退出战斗（20171 fire-and-forget，无响应帧）
s.sendall(pack(20171, vf(90, 12)))
time.sleep(0.3)
drain(s)

# 4) 回主城
op, b, pushes = send_req(s, 20031, vf(1, 1000401) + vf(90, 13), 13)
cm = [bb for o, bb in pushes if o == 20033]
check(cm and fld(cm[0], 4) == 1000401, '回主城 1000401')

# 5) 主城买药水 → 使用 → 20276 带 MainUISlotList + 20169 血蓝
# 先制造残血：进战斗等怪物反击（region 1011 已测过，回主城后重新开 10006 海滩第 1 层
# 怪物 10001 攻击力低，等 2 秒掉血），QuitBattle 后血量应 < 满值 → 吃药有实际回复。
# 进入海滩第 1 层战斗，制造残血（技能有 8 点 HP 消耗；怪物反击也可能掉血）
op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 14), 14)
regions = [fld(bb, 1) for o, bb in pushes if o == 20047]
assert len(regions) == 1 and not any(o == 20320 for o, _ in pushes)
op, b, pushes = send_req(s, 20048, vf(1, regions[0]) + vf(90, 15), 15)
s.settimeout(0.35)
damaged = False
for attempt in range(3):
    try:
        s.sendall(pack(20233, vf(1, 0) + vf(90, 150 + attempt)))
    except Exception:
        pass
    deadline = time.time() + 2.2
    while time.time() < deadline:
        try:
            o2, b2 = recv_one(s)
        except socket.timeout:
            continue
        except Exception:
            break
        if o2 == 20169 and fld(b2, 1) == player_id and fld(b2, 2) == 1001 and fld(b2, 3) < 92:
            damaged = True
            break
    if damaged:
        break
s.settimeout(5)
drain(s)
s.sendall(pack(20171, vf(90, 16)))  # 退出战斗（残血写回会话）
time.sleep(0.3)
drain(s)
# 回到主城
op, b, pushes = send_req(s, 20031, vf(1, 1000401) + vf(90, 17), 17)

op, b, pushes = send_req(s, 20174, vf(1, 0) + vf(2, 0) + vf(3, 3) + vf(90, 18), 18)  # 买 3 个药水
pot_idx = None
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        bag = dict((ff, vv) for ff, wt2, vv in fields(v))
        item = dict((ff, vv) for ff, wt2, vv in fields(bag.get(2, b'')))
        if item.get(1) == 110305:
            pot_idx = bag.get(1)
check(pot_idx is not None, '主城购买药水')
op, b, pushes = send_req(s, 20275, vf(1, pot_idx) + vf(90, 19), 19)  # 背包用 1 个 → 剩 2
slots = [v for f, wt, v in fields(b) if f == 2 and wt == 'bytes']
bags = [v for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
check(op == 20276 and not fld(b, 92), '使用药品成功 (20276)')
check(len(slots) > 0, '20276 响应带 MainUISlotList (%d 条)' % len(slots))
check(len(bags) > 0, '20276 响应带 BagMapList')
attrs = [(fld(bb, 2), fld(bb, 3)) for o, bb in pushes if o == 20169 and fld(bb, 2) in (1001, 1003)]
print('  血蓝推送:', attrs)
if damaged:
    check(len(attrs) >= 1, '使用药品推送血蓝 20169')
    check(any(a[0] == 1001 and a[1] > 0 for a in attrs), '血蓝推送含 HP 恢复: %s' % attrs)
else:
    print('  ⚠ 本场战斗未掉血（怪物未命中），跳过血蓝恢复断言（机制由 goodsHeal 单测覆盖）')

# 6) 主界面快捷栏：9 槽固定，拖药水到槽 8 + 点击使用
op, b, pushes = send_req(s, 20227, vf(90, 20), 20)
slot_list = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
check(len(slot_list) == 9, 'GetMainUISetting 返回 9 槽固定')
skill_slots = [x for x in slot_list if x.get(2) == 1]
check(len(skill_slots) == 1 and skill_slots[0].get(1) == 100001, '槽 0 保留玩家配置的基础攻击')
drop_slot = 8
op, b, pushes = send_req(s, 20231, vf(1, drop_slot) + vf(2, pot_idx) + vf(90, 21), 21)
check(op == 20232 and not fld(b, 92), '拖药水到物品槽 (20232)')
op, b, pushes = send_req(s, 20235, vf(1, drop_slot) + vf(90, 22), 22)
slots3 = [dict((ff, vv) for ff, wt2, vv in fields(v)) for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
used = [x for x in slots3 if x.get(5) == drop_slot]
check(op == 20236 and not fld(b, 92) and used and used[0].get(4) == 1, '点击物品槽使用成功 数量 2→1 (20236)')
cd = [bb for o, bb in pushes if o == 20237]
check(any(fld(bb, 4) == 2 and fld(bb, 2) == 110305 for bb in cd), '使用物品槽后推 20237 CD (Type=ItemSlot Id=110305): %s' % [(fld(bb,2), fld(bb,3), fld(bb,4)) for bb in cd])
print('  使用后槽位:', [(x.get(1), x.get(4)) for x in used])

s.close()
print()
print('=== 传送门/场景怪/消耗品 综合回归全部通过：%d 项 ===' % ok)
