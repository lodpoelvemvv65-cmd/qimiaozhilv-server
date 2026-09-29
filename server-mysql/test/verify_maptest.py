# -*- coding: utf-8 -*-
"""验证：原生主线怪战斗、系统奖励通知、手动换层及地图持久化。"""
import socket, struct, time, select, sys
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

def drain(s, timeout=0.8):
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

def send_req(s, op, body, rpc):
    s.sendall(pack(op, body))
    pushes = []
    for _ in range(50):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

def bag_items(body):
    out = []
    for f, wt, payload in fields(body):
        if f != 1 or wt != 'bytes':
            continue
        bag = fields(payload)
        index = next(v for ff, _, v in bag if ff == 1)
        net = next(v for ff, w, v in bag if ff == 2 and w == 'bytes')
        item_id = next(v for ff, _, v in fields(net) if ff == 1)
        out.append((index, item_id))
    return out

def equip_task_weapon(s):
    # 走真实新手流程领取并装备 job1 的 120592，避免用快速连发绕过战斗 CD。
    op, b, pushes = send_req(s, 20206, vf(1, 1012) + vf(90, 10), 10)
    task_ui = next((body for opcode, body in pushes if opcode == 20216), None)
    message = next((value.decode('utf-8', errors='replace') for field, wire, value in fields(b)
                    if field == 92 and wire == 'bytes'), None)
    assert op == 20207 and task_ui is not None, \
        '点击 NPC 1012 未打开任务界面: op=%s message=%s pushes=%s' % \
        (op, message, [opcode for opcode, _ in pushes])
    task_state = next((next((value for field, _, value in fields(raw) if field == 2), None)
                       for field, wire, raw in fields(task_ui)
                       if field == 1 and wire == 'bytes' and
                       next((value for child, _, value in fields(raw) if child == 1), None) == 10011), None)
    assert task_state in (1, 3), '10011 状态异常: %s' % task_state
    if task_state == 1:
        op, b, _ = send_req(s, 20217, vf(1, 10011) + vf(90, 14), 14)
        assert op == 20218 and not [v for f, _, v in fields(b) if f == 91], '接取 10011 失败'
    # Task 10011 requires exactly three selections; index 4 is the officer
    # weapon used by this combat regression.
    selection = vf(2, 0) + vf(2, 1) + vf(2, 4)
    op, b, _ = send_req(s, 20219, vf(1, 10011) + selection + vf(90, 11), 11)
    message = next((v.decode('utf-8', errors='replace') for f, w, v in fields(b)
                    if f == 92 and w == 'bytes'), '')
    assert op == 20220 and not [v for f, _, v in fields(b) if f == 91] and not message, \
        '完成 10011 失败: %s' % message
    op, b, _ = send_req(s, 20258, vf(90, 12), 12)
    assert op == 20259, 'GetBag 响应错误: %s' % op
    weapon_index = next(index for index, item_id in bag_items(b) if item_id == 120592)
    op, b, _ = send_req(s, 20271, vf(1, weapon_index) + vf(90, 13), 13)
    assert op == 20272 and not [v for f, _, v in fields(b) if f == 91], '装备 120592 失败'
    print('equipped task weapon 120592 at bag index', weapon_index)

acct = 'fmap%d' % int(time.time() * 1000)
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
assert op == 20011 and not [v for f, _, v in fields(b) if f == 91], '注册失败'
op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
assert op == 20009 and not [v for f, _, v in fields(b) if f == 91], '首次登录失败'
key = [v for f, w, v in fields(b) if f == 2][0]; gate = [v for f, w, v in fields(b) if f == 3][0]
op, b, _ = send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 3), 3)
assert op == 20015 and not [v for f, _, v in fields(b) if f == 91], '首次 Gate 登录失败'
op, b, _ = send_req(s, 20016, vf(2, 1) + sf(3, acct[-10:]) + vf(90, 4), 4)
assert op == 20017 and not [v for f, _, v in fields(b) if f == 91], '创建唯一昵称角色失败'
# Gate Key 是一次性凭证；建角后重新登录并取得新 Key，再进入游戏。
op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 5), 5)
assert op == 20009 and not [v for f, _, v in fields(b) if f == 91], '建角后重新登录失败'
key = [v for f, w, v in fields(b) if f == 2][0]; gate = [v for f, w, v in fields(b) if f == 3][0]
op, b, _ = send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
assert op == 20015 and not [v for f, _, v in fields(b) if f == 91], '建角后 Gate 登录失败'
op, b, pushes = send_req(s, 20027, vf(90, 7), 7)
assert op == 20028 and not [v for f, _, v in fields(b) if f == 91], '进入游戏失败'
print('enter game pushes:', sorted(set(o for o, _ in pushes)))
# 新号出生在海滩；先回主城再进海滩，确保取得本次换图的原生初始化。
send_req(s, 20031, vf(1, 1000401) + vf(90, 40), 40)
op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 6), 6)
equip_task_weapon(s)
# Newly created roles learn the basic attack but start with empty shortcut
# slots. Assign the officer attack to slot zero through the native RPC.
op, b, _ = send_req(s, 20229, vf(1, 0) + vf(2, 100001) + vf(90, 41), 41)
assert op == 20230 and not [v for f, _, v in fields(b) if f in (91, 92)], \
    '初始化普攻技能槽失败'

# 点击客户端按 20047 创建的原生主线怪（20048）。20047.MainStoryId 是配置行，
# 20048.Region 是同一层两个展示怪的本地槽位（0/1），两者不能混用。
main_story_id = [v for o, bb in pushes if o == 20047 for f, w, v in fields(bb) if f == 1][0]
assert main_story_id == 1001, '海滩 1 层 MainStoryId=%s, want 1001' % main_story_id
assert not any(o == 20320 for o, _ in pushes), '主线地图不应重复推字段怪'
op, b, pushes = send_req(s, 20048, vf(1, 0) + vf(90, 5), 5)
print('click monster resp op=%d, pushes:' % op, sorted(set(o for o, _ in pushes)))
start_message = next((value.decode('utf-8', errors='replace') for field, wire, value in fields(b)
                      if field == 92 and wire == 'bytes'), '')
assert op == 20049 and not start_message, \
    '主线开战失败: op=%s message=%s body=%s' % (op, start_message, fields(b))
assert any(o == 20050 for o, _ in pushes), '应推 MainStoryMonsterInfo(20050)'
print('  -> 点击原生主线怪进入战斗 OK')

# 任务武器使每次普攻击杀一只怪；两次合法出手之间遵守服务端 6000ms CD。
op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 100), 100)
assert not [v for f, _, v in fields(b) if f == 91], '第一次普攻失败'
assert not any(o == 20054 for o, _ in pushes), '第一击不应提前结束双怪战斗'
time.sleep(6.1)
op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 101), 101)
# The hit lands after the cast/projectile phases, then the client victory HUD
# waits 4.2 seconds before the server emits the final result.
time.sleep(6.6)
pushes.extend(drain(s))
assert not [v for f, _, v in fields(b) if f == 91], '第二次普攻被 CD 拒绝'
assert any(o == 20054 for o, _ in pushes), '第二次合法普攻后未胜利'
print('victory after two cooldown-valid attacks; pushes:', sorted(set(o for o, _ in pushes)))
assert any(o == 20084 for o, _ in pushes), '应推 SendReward(20084)'
reward_body = next(bb for o, bb in pushes if o == 20084)
reward_coin = next((v for f, wire, v in fields(reward_body) if f == 3 and wire == 'var'), 0)
assert reward_coin >= 1000, '主线胜利未执行 MonsterBase 掉落铜币链: %s' % reward_coin
print('  -> 主线 MonsterBase 掉落铜币:', reward_coin)
system_notices = [body for opcode, body in pushes if opcode == 20301]
assert len(system_notices) == 1, '战斗结算应推一条 SendSystemChat(20301)'
notice = system_notices[0]
notice_lines = [value.decode('utf-8') for field, wire, value in fields(notice)
                if field == 1 and wire == 'bytes']
assert next((value for field, wire, value in fields(notice) if field == 2 and wire == 'var'), None) == 7, \
    '奖励通知必须进入 System(7) 频道'
assert any('获得经验' in line for line in notice_lines), '系统频道缺少经验奖励'
assert any('获得铜币' in line for line in notice_lines), '系统频道缺少铜币奖励'
print('  -> 系统频道实时奖励:', notice_lines)
cleanup_maps = [v for o, body in pushes if o == 20033
                for f, w, v in fields(body) if f == 4 and w == 'var']
assert all(map_id == 1000601 for map_id in cleanup_maps), \
    '胜利后不应自动切入其他层（须手动走光圈）: %s' % cleanup_maps
print('  -> 胜利不自动切层 OK（手动走光圈进下一层）')
# 20031 模拟玩家战后走入光圈，按客户端流程只进入相邻下一层。
op, b, pushes = send_req(s, 20031, vf(1, 1000602) + vf(90, 200), 200)
map_message = next((value.decode('utf-8', errors='replace') for field, wire, value in fields(b)
                    if field == 92 and wire == 'bytes'), '')
assert op == 20032 and not map_message, \
    '进入海滩 2 层失败: op=%s message=%s body=%s' % (op, map_message, fields(b))
cm = [bb for o, bb in pushes if o == 20033]
assert len(cm) == 1, '进入海滩 2 层未推 ChangeMap(20033)'
print('request enter map 1000602 -> ChangeMap:', [fields(c) for c in cm])

# Disconnect and log in again: authoritative map and position are restored from MySQL.
s.close()
time.sleep(0.3)
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 201), 201)
key = [v for f, w, v in fields(b) if f == 2][0]
gate = [v for f, w, v in fields(b) if f == 3][0]
send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 202), 202)
op, b, pushes = send_req(s, 20027, vf(90, 203), 203)
restored = [v for o, bb in pushes if o == 20033
            for f, w, v in fields(bb) if f == 4]
assert restored == [1000602], 're-login map %s, want persisted map 1000602' % restored
print('  -> re-login restored persisted map 1000602')

# EnterGame already initialized the persisted map, so reuse its native region push.
second_story_ids = [v for o, bb in pushes if o == 20047 for f, w, v in fields(bb) if f == 1]
assert second_story_ids == [1002], \
    're-login did not initialize persisted main-story map 1002: %s' % second_story_ids
op, b, pushes = send_req(s, 20048, vf(1, 0) + vf(90, 204), 204)
second_battle_ids = [v for o, bb in pushes if o == 20050
                     for f, w, item in fields(bb) if f == 1 and w == 'bytes'
                     for ff, ww, v in fields(item) if ff == 1]
assert op == 20049 and not [v for f, _, v in fields(b) if f == 92], \
    'map 1000602 + local Region=0 start failed: op=%s body=%s' % (op, fields(b))
assert len(second_battle_ids) == 2, 'stage 1002 monster count is %d, want 2' % len(second_battle_ids)
print('  -> layer 2 resolved to stage 1002 with unit IDs:', second_battle_ids)
s.close()
print('ALL OK')
