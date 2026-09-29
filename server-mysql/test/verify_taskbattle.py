# -*- coding: utf-8 -*-
"""验证：战斗 20077 EffectId + 任务完整链路（苹果JJ 10011 接取/提交）。"""
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
    for _ in range(80):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

def wait_for_push(s, pushes, opcode, timeout=3.0):
    if any(op == opcode for op, _ in pushes):
        return True
    deadline = time.monotonic() + timeout
    old_timeout = s.gettimeout()
    try:
        while time.monotonic() < deadline:
            s.settimeout(deadline - time.monotonic())
            try:
                frame = recv_one(s)
            except socket.timeout:
                return False
            pushes.append(frame)
            if frame[0] == opcode:
                return True
        return False
    finally:
        s.settimeout(old_timeout)

def npc_task_states(body):
    """取 NPCTask 列表：M2C_SendTaskState(20225) 的 NPCState 和
    M2C_GetTaskState(20224) 的 NPCStateList 都是 field1=NPCTask{Id=NPC id, TaskState}。"""
    out = {}
    for f, wt, payload in fields(body):
        if f != 1 or wt != 'bytes':
            continue
        kv = dict((ff, vv) for ff, _, vv in fields(payload))
        if kv.get(1) is not None:
            out[kv.get(1)] = kv.get(2)
    return out

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

acct = 'fx%d' % int(time.time() * 1000)
role_name = 'fx' + acct[-8:]
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
key = [v for f, w, v in fields(b) if f == 2][0]; gate = [v for f, w, v in fields(b) if f == 3][0]
send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 2), 2)
send_req(s, 20016, vf(2, 1) + sf(3, role_name) + vf(90, 3), 3)
op, b, pushes = send_req(s, 20027, vf(90, 4), 4)
print('enter game ok')

# NPC 1012 位于海滩 1000601；先切主城再回海滩，刷新场景出生点。
send_req(s, 20031, vf(1, 1000401) + vf(90, 5), 5)
send_req(s, 20031, vf(1, 1000601) + vf(90, 6), 6)

# ===== 任务链路 =====
# 1) 点击苹果JJ 1012 → OpenTaskUI 任务列表
op, b, pushes = send_req(s, 20206, vf(1, 1012) + vf(90, 10), 10)
ui = [bb for o, bb in pushes if o == 20216]
print('ClickNPC 1012 -> OpenTaskUI(20216) 推送:', len(ui))
tasklist = []
for f, w, v in fields(ui[0]):
    if f == 1 and w == 'bytes':
        d = dict((ff, vv) for ff, wt, vv in fields(v))
        tasklist.append((d.get(1), d.get(2)))
print('  TaskList:', tasklist)
assert any(t[0] == 10011 for t in tasklist), '10011 不在任务列表'
# 2) 接取 10011（如果列表里是 Waiting）
st = [ts for tid, ts in tasklist if tid == 10011][0]
print('  10011 状态:', st)
if st == 1:
    op, b, pushes = send_req(s, 20217, vf(1, 10011) + vf(90, 11), 11)
    print('  AcceptTask 10011 resp op=%d err=%s' % (op, [v for f, w, v in fields(b) if f == 92]))
    assert not [v for f, w, v in fields(b) if f == 92], '接取失败'
# 3) 提交 10011（对话任务，条件即达成）
# 原版 10011 是奖励多选任务，必须按配置数量提交选中索引，否则服务端返回
# "请选择数量的奖励！"；选 120592 等三件用于后续穿戴。
reward_fields = b''.join(vf(2, index) for index in (0, 2, 4))
op, b, pushes = send_req(s, 20219, vf(1, 10011) + reward_fields + vf(90, 12), 12)
err = [v for f, w, v in fields(b) if f == 92]
print('  CompleteTask 10011 resp op=%d err=%s' % (op, err))
assert not err, '提交失败! %s' % err
print('  -> 任务 10011 完成 OK')
# 4) 重新点击 NPC → 10012 应可接
op, b, pushes = send_req(s, 20206, vf(1, 1012) + vf(90, 13), 13)
ui = [bb for o, bb in pushes if o == 20216]
tasklist = []
for f, w, v in fields(ui[0]):
    if f == 1 and w == 'bytes':
        d = dict((ff, vv) for ff, wt, vv in fields(v))
        tasklist.append((d.get(1), d.get(2)))
print('  再点 NPC TaskList:', tasklist)
assert any(t[0] == 10012 and t[1] == 1 for t in tasklist), '10012 应可接取'

# 接取战斗任务并穿上 job1 奖励武器，后续用两次合法普攻完成战斗。
op, b, pushes = send_req(s, 20217, vf(1, 10012) + vf(90, 14), 14)
assert op == 20218 and not [v for f, w, v in fields(b) if f == 91], '接取 10012 失败'
# 任务 10012 进行中：线上抓包里接取 NPC 1012 和提交 NPC 1013 都返回
# Running(2)（灰色感叹号）。20225/20224 的 NPCTask.Id 是 NPC id，不是任务 id。
accept_states = {}
for o, bb in pushes:
    if o == 20225:
        accept_states.update(npc_task_states(bb))
assert accept_states.get(1012) == 2 and accept_states.get(1013) == 2, \
    '接取 10012 后 NPC 1012/1013 都应为 Running(2): %s' % accept_states
op, b, pushes = send_req(s, 20223, vf(90, 17), 17)
assert op == 20224, 'GetTaskState 响应错误: %s' % op
query_states = npc_task_states(b)
assert query_states.get(1012) == 2 and query_states.get(1013) == 2, \
    '20224 查询 10012 进行中时 NPC 1012/1013 都应为 Running(2): %s' % query_states
print('  -> 10012 进行中 1012/1013 均为 Running(2)')
op, b, pushes = send_req(s, 20258, vf(90, 15), 15)
assert op == 20259, 'GetBag 响应错误: %s' % op
weapon_index = next(index for index, item_id in bag_items(b) if item_id == 120592)
op, b, pushes = send_req(s, 20271, vf(1, weapon_index) + vf(90, 16), 16)
assert op == 20272 and not [v for f, w, v in fields(b) if f == 91], '装备 120592 失败'
print('  -> 已接取 10012 并装备任务武器 120592')
s.close()

# ===== 战斗特效 =====
# 重新登录（登录固定回主城，无字段怪）→ 先进海滩 1000601 再点字段怪进战斗
import socket as _s
_s2 = socket.create_connection(('127.0.0.1', 7756)); _s2.settimeout(5)
op, b, _ = send_req(_s2, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 50), 50)
key = [v for f, w, v in fields(b) if f == 2][0]; gate = [v for f, w, v in fields(b) if f == 3][0]
send_req(_s2, 20014, vf(1, key) + vf(2, gate) + vf(90, 51), 51)
op, b, pushes = send_req(_s2, 20027, vf(90, 52), 52)
# 快捷栏不随存档持久化，新会话槽 0 是空的；不先放技能会被判 bad slot 静默拒绝。
op, b, pushes = send_req(_s2, 20229, vf(1, 0) + vf(2, 100001) + vf(90, 57), 57)
assert op == 20230 and not [v for f, w, v in fields(b) if f == 91], \
    '槽 0 放入职业普攻 100001 失败'
# 重登恢复海滩存档；先切主城再切回，避免同图请求不重复初始化。
send_req(_s2, 20031, vf(1, 1000401) + vf(90, 56), 56)
op, b, pushes = send_req(_s2, 20031, vf(1, 1000601) + vf(90, 55), 55)
# 未通关海滩层不能在进场时创建传送门；20039 只在本层战斗胜利后出现。
assert not wait_for_push(_s2, pushes, 20039, timeout=1.0), '未通关海滩层提前显示传送门'
region = [v for o, bb in pushes if o == 20047 for f, w, v in fields(bb) if f == 1][0]
assert not any(o == 20320 for o, _ in pushes), '主线地图不应重复推字段怪'
assert region == 1001, '海滩第一层主线配置错误: %s' % region
op, b, pushes = send_req(_s2, 20048, vf(1, 0) + vf(90, 53), 53)
assert any(o == 20050 for o, _ in pushes), '未进入真实主线战斗'
print('click monster -> battle init:', sorted(set(o for o, _ in pushes)))
battle_info = next(bb for o, bb in pushes if o == 20050)
monster_ids = {
    next(v for f, w, v in fields(info) if f == 1)
    for f, w, info in fields(battle_info) if f == 1 and w == 'bytes'
}
assert monster_ids, '20050 missing monster UnitId'
# 发技能，抓 20077 飞行/命中特效及 20078 伤害顺序
op, b, pushes = send_req(_s2, 20233, vf(1, 0) + vf(90, 54), 54)
assert not [v for f, w, v in fields(b) if f == 91], '第一次普攻失败'


def monster_play_skills(frames):
    out = []
    for o, bb in frames:
        if o != 20075:
            continue
        d = dict((f, v) for f, w, v in fields(bb))
        if d.get(1) in monster_ids:
            assert set(d) == {1, 2}, '20075 must contain only UnitId/SkillId: %s' % d
            out.append((d.get(1), d.get(2)))
    return out


# 怪物按自己的动作时钟出手（首击在开战后数秒、之后约每 6 秒一轮），所以这里
# 轮询等到出现怪物 20075 为止，而不是固定睡一小段。
mon = []
deadline = time.monotonic() + 12.0
while time.monotonic() < deadline:
    pushes.extend(drain(_s2, 0.5))
    mon = monster_play_skills(pushes)
    if mon:
        break
# 线上顺序：2101(Time=1000) 飞行 → 2102(Time=0) 命中 → 20078 伤害。
eff = []
for index, (o, bb) in enumerate(pushes):
    if o == 20077:
        d = dict((f, v) for f, w, v in fields(bb))
        eff.append((index, d.get(1), d.get(4), d.get(6, 0), d.get(9)))
print('  20077 PlaySkillEffect (Index,UnitId,TargetId,Time,EffectId):', eff)
print('  20075 monster PlaySkill (UnitId,SkillId):', mon)
assert mon, 'monster attack missing 20075 PlaySkill'
assert not any(o == 20076 for o, _ in pushes), 'online monster attack unexpectedly emitted duplicate 20076'
monster_state_targets = [
    dict((f, v) for f, w, v in fields(bb)).get(2)
    for o, bb in pushes if o == 20080
]
assert not monster_ids.intersection(monster_state_targets), \
    '20080 targets monster units without BuffComponent: %s' % monster_state_targets
flight = next((event for event in eff if event[4] == 2101), None)
impact = next((event for event in eff if event[4] == 2102), None)
damage_index = next((index for index, (o, _) in enumerate(pushes) if o == 20078), None)
assert flight is not None, '普通攻击缺少 2101 飞行弹道'
assert impact is not None, '普通攻击缺少 2102 命中特效'
assert flight[3] == 1000, '2101 Time=%s, want 1000' % flight[3]
assert impact[3] == 0, '2102 Time=%s, want 0' % impact[3]
assert flight[1:3] == impact[1:3], '2101/2102 源或目标不一致: %s / %s' % (flight, impact)
assert damage_index is not None and flight[0] < impact[0] < damage_index, \
    '事件顺序错误: flight=%s impact=%s damage=%s' % (flight[0], impact[0], damage_index)
print('  -> 普通攻击 2101 飞行 -> 2102 命中 -> 20078 伤害顺序正常')

# 等待原版 6000ms 公共动作间隔后第二次普攻，任务武器应直接击杀第二只怪。
time.sleep(6.1)
op, b, pushes = send_req(_s2, 20233, vf(1, 0) + vf(90, 55), 55)
assert not [v for f, w, v in fields(b) if f == 91], '第二次普攻被 CD 拒绝'
# 胜利结算有 4.2s 延迟（battleVictoryDelay，匹配客户端胜利动画），再补一段 drain。
pushes += drain(_s2, 5.5)
victory_state_targets = [
    dict((f, v) for f, w, v in fields(bb)).get(2)
    for o, bb in pushes if o == 20080
]
assert not monster_ids.intersection(victory_state_targets), \
    'victory cleanup 20080 targets removed monster: %s' % victory_state_targets
assert any(o == 20054 for o, _ in pushes), '第二次合法普攻后未胜利'
print('victory after two cooldown-valid attacks')
# 击杀计数 + 已接取的 10012 完成条件
# 胜利后只有提交 NPC 1013 变成 Completed(3)，接取 NPC 1012 保持 Running(2)。
op, b, pushes = send_req(_s2, 20223, vf(90, 201), 201)
assert op == 20224, 'GetTaskState 响应错误: %s' % op
victory_states = npc_task_states(b)
assert victory_states.get(1013) == 3, \
    '胜利后提交NPC 1013 应为 Completed(3): %s' % victory_states
assert victory_states.get(1012) == 2, \
    '胜利后接取NPC 1012 应保持 Running(2): %s' % victory_states
print('  -> 胜利后 1013=Completed(3)、1012 保持 Running(2)')
send_req(_s2, 20031, vf(1, 1000602) + vf(90, 198), 198)
send_req(_s2, 20206, vf(1, 1013) + vf(90, 199), 199)
reward_fields = b''.join(vf(2, index) for index in (0, 1, 2))
op, b, pushes = send_req(_s2, 20219, vf(1, 10012) + reward_fields + vf(90, 200), 200)
err = [v for f, w, v in fields(b) if f == 91]
print('CompleteTask 10012 resp err=%s' % err)
assert not err, '10012 应可提交（击杀荷叶球达成）'
print('ALL TASK+BATTLE FIX TESTS PASSED')
_s2.close()
