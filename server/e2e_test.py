# -*- coding: utf-8 -*-
import os, socket, struct, time, select

HOST = os.environ.get('MHQ_TEST_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_TEST_PORT', '7756'))

def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7f; n >>= 7
        if n: out.append(b | 0x80)
        else: out.append(b); return bytes(out)

def vf(n, v):  return varint((n<<3)|0) + varint(v)
def sf(n, s):
    b = s.encode('utf-8')
    return varint((n<<3)|2) + varint(len(b)) + b

def pack(op, body): return struct.pack('<HH', 2+len(body), op) + body

sock = socket.create_connection((HOST, PORT)); sock.settimeout(5)
buf = []

def recv_exact(n, timeout=0.3):
    data = b''
    end = time.time() + timeout
    while len(data) < n:
        left = end - time.time()
        if left <= 0:
            raise socket.timeout()
        r, _, _ = select.select([sock], [], [], left)
        if not r:
            raise socket.timeout()
        chunk = sock.recv(n - len(data))
        if not chunk:
            raise ConnectionError('closed')
        data += chunk
    return data

def recv_one():
    if buf: return buf.pop(0)
    hdr = recv_exact(4)
    total, op = struct.unpack('<HH', hdr)
    body = recv_exact(total - 2)
    return op, body

def drain_following():
    """响应帧之后跟随的推送帧（如 EnterGame/StartMainStoryFight 先回响应再推 20025/20047/20050）。"""
    out = []
    while True:
        try:
            hdr = recv_exact(4, 0.15)
        except (socket.timeout, ConnectionError):
            break
        total, op = struct.unpack('<HH', hdr)
        body = recv_exact(total - 2, 0.15)
        out.append((op, body))
    return out

def collect_until(required, timeout=2.0):
    """收集异步推送，直到收到全部目标 opcode 或超时。"""
    required = set(required)
    out = []
    end = time.time() + timeout
    while not required.issubset({op for op, _ in out}):
        left = end - time.time()
        if left <= 0:
            break
        try:
            hdr = recv_exact(4, left)
            total, op = struct.unpack('<HH', hdr)
            body = recv_exact(total - 2, max(0.01, end - time.time()))
        except (socket.timeout, ConnectionError):
            break
        out.append((op, body))
    return out

def decode(body):
    out = []; i = 0; n = len(body)
    while i < n:
        t = 0; s = 0
        while True:
            b = body[i]; i += 1; t |= (b & 0x7f) << s; s += 7
            if not b & 0x80: break
        field, wt = t >> 3, t & 7
        if wt == 0:
            v = 0; s = 0
            while True:
                b = body[i]; i += 1; v |= (b & 0x7f) << s; s += 7
                if not b & 0x80: break
            out.append((field, wt, v))
        elif wt == 2:
            l = 0; s = 0
            while True:
                b = body[i]; i += 1; l |= (b & 0x7f) << s; s += 7
                if not b & 0x80: break
            out.append((field, wt, body[i:i+l])); i += l
        elif wt == 5:
            out.append((field, wt, struct.unpack('<I', body[i:i+4])[0])); i += 4
        else:
            break
    return out

def fld(body, field):
    for f, wt, v in decode(body):
        if f == field and wt == 0: return v
    return None

def bytes_fld(body, field):
    for f, wt, v in decode(body):
        if f == field and wt == 2:
            return v
    return None

def all_fld(body, field):
    out = []
    for f, wt, v in decode(body):
        if f != field:
            continue
        if wt == 0:
            out.append(v)
        elif wt == 2:
            i = 0
            while i < len(v):
                value = 0; shift = 0
                while True:
                    b = v[i]; i += 1
                    value |= (b & 0x7f) << shift; shift += 7
                    if not b & 0x80: break
                out.append(value)
    return out

def bag_entries(body, field=1):
    """解析 repeated BagMap，并保留装备详情协议字段。"""
    out = []
    for f, wt, payload in decode(body):
        if f != field or wt != 2:
            continue
        entry = {'index': None, 'item': {}, 'equip': {}, 'attributes': []}
        for ef, ewt, value in decode(payload):
            if ef == 1 and ewt == 0:
                entry['index'] = value
            elif ef == 2 and ewt == 2:
                entry['item'] = dict(
                    (nf, nv) for nf, nwt, nv in decode(value) if nwt == 0
                )
            elif ef == 3 and ewt == 2:
                for tf, twt, tv in decode(value):
                    if twt == 0:
                        entry['equip'][tf] = tv
                    elif tf == 5 and twt == 2:
                        attr = {}
                        for af, awt, av in decode(tv):
                            attr[af] = av
                        entry['attributes'].append(attr)
        out.append(entry)
    return out

def send_req(op, body, rpc):
    sock.sendall(pack(op, body))
    pushes = []
    while True:
        op2, b = recv_one()
        r2 = fld(b, 90)
        if r2 == rpc:
            pushes.extend(drain_following())
            return op2, b, pushes
        pushes.append((op2, b))

def show(name, op, body):
    ds = decode(body)
    print('  <- %s op=%d fields=%s' % (name, op, ds))

rpc = 0
def next_rpc():
    global rpc; rpc += 1; return rpc

# 1) 注册
acct = 'tskill%d' % int(time.time()*1000)
r = next_rpc()
op2, b, pushes = send_req(20010, sf(1, acct) + sf(2, '123456') + vf(90, r), r)
print('[1] 注册', acct)
show('R2C_Regist', op2, b)
key = fld(b, 2); gate = fld(b, 3)
assert key, '注册应返回 Key'
print('    key=', key, 'gateId=', gate)

# 2) LoginGate
r = next_rpc()
op2, b, pushes = send_req(20014, vf(1, key) + vf(2, gate) + vf(90, r), r)
print('[2] LoginGate(无角色)')
show('G2C_LoginGate', op2, b)
has_role = fld(b, 2)
print('    HasRole=', has_role, '(应 0/None)')

# 3) 创建角色 Officer
r = next_rpc()
op2, b, pushes = send_req(20016, vf(2, 1) + sf(3, '测试员') + vf(90, r), r)
print('[3] CreateRole job=1 Officer')
show('R2C_CreateRole', op2, b)
err = fld(b, 91)
assert err is None or err == 0, '创建角色失败 err=%s' % (err,)

# 4) CreateRole 后同连接直接 EnterGame（LoginGate Key 一次性，无需/不能再 LoginGate）
r = next_rpc()
op2, b, pushes = send_req(20027, vf(90, r), r)
print('[4] EnterGame')
show('G2C_EnterGame', op2, b)
pid = fld(b, 1)
print('    PlayerId=', pid, ' pushes:', [(o, fld(bb, 90)) for o, bb in pushes])
assert any(o == 20025 for o, _ in pushes), '应推送 SendUnitInfo(20025)'
assert any(o == 20033 for o, _ in pushes), '应推送 ChangeMap(20033)'

# 5) 场景恢复完成：服务器应同步单人队伍，避免 ClickTarget 在射线检测前空引用
sock.sendall(pack(20030, b''))
op2, b = recv_one()
print('[5] GetStateReback -> TeamMember')
show('M2C_TeamMember', op2, b)
assert op2 == 20162, '应推送 TeamMember(20162), 实际 op=%s' % (op2,)
assert fld(b, 1) == pid, 'LeaderId 应为当前玩家'
assert all_fld(b, 2) == [pid], 'UnitIds 应只含当前玩家'

# 6) GetSkill：应只有基础攻击 100001
r = next_rpc()
op2, b, pushes = send_req(20239, vf(90, r), r)
print('[6] GetSkill')
show('M2C_GetSkill', op2, b)
skills = []
for f, wt, v in decode(b):
    if f == 1 and wt == 2:
        kv = dict((ff, vv) for ff, wt2, vv in decode(v) if wt2 == 0)
        skills.append(kv)
    if f == 2 and wt == 2:
        pl = [x[2] for x in decode(v) if x[1] == 0]
        print('    AutoSkillList=', pl)
learned = [skill for skill in skills if skill.get(2, 0) > 0]
print('    learned skills=', learned, 'all panel entries=', len(skills))
assert len(learned) == 1 and learned[0].get(1) == 100001, '新号应只学基础攻击, 实际 %s' % (learned,)

# 7) GetMainUISetting：应只有 1 个槽
r = next_rpc()
op2, b, pushes = send_req(20227, vf(90, r), r)
print('[7] GetMainUISetting')
show('M2C_GetMainUISetting', op2, b)
slots = []
for f, wt, v in decode(b):
    if f == 1 and wt == 2:
        kv = dict((ff, vv) for ff, wt2, vv in decode(v) if wt2 == 0)
        slots.append(kv)
print('    slots=', slots)
filled_slots = [slot for slot in slots if slot.get(1)]
assert len(slots) == 9 and len(filled_slots) == 1 and filled_slots[0].get(1) == 100001, \
    '技能栏应为9格且只有基础攻击已填充, 实际 %s' % (slots,)

# 8) 点击 NPC 1012：应推送 OpenTaskUI(20216)，含已自动接取的 10011 (Running)
r = next_rpc()
op2, b, pushes = send_req(20206, vf(1, 1012) + vf(90, r), r)
print('[8] ClickNPC 1012')
show('M2C_ClickNPC', op2, b)
openui = [bb for o, bb in pushes if o == 20216]
assert openui, '应推送 OpenTaskUI(20216)'
tl = []
for f, wt, v in decode(openui[0]):
    if f == 1 and wt == 2:
        kv = dict((ff, vv) for ff, wt2, vv in decode(v) if wt2 == 0)
        tl.append(kv)
print('    TaskList=', tl)
assert any(kv.get(1) == 10011 for kv in tl), '应含任务 10011'
assert next(kv.get(2) for kv in tl if kv.get(1) == 10011) == 3, '苹果JJ处的 10011 应显示为可提交 Completed'
assert next(kv.get(2) for kv in tl if kv.get(1) == 10086) == 1, '10086 应保持 Waiting，不能误自动接取'
assert [kv.get(1) for kv in tl[:2]] == [10011, 10086], '苹果JJ任务顺序必须稳定'

# 9) 完成对话任务 10011
r = next_rpc()
op2, b, pushes = send_req(20219, vf(1, 10011) + vf(90, r), r)
print('[9] CompleteTask 10011')
show('M2C_CompleteTask', op2, b)
err = fld(b, 91)
assert err is None or err == 0, '完成10011失败 err=%s' % (err,)
assert any(o == 20225 for o, _ in pushes), '完成任务后应推送 SendTaskState(20225)'

# 任务奖励入包后，详情字段和双击装备必须在真实协议链路上可用。
r = next_rpc()
op2, b, pushes = send_req(20258, vf(90, r), r)
items = bag_entries(b)
print('[9.1] GetBag 奖励装备=', [
    (it['index'], it['item'].get(1), it['equip'].get(3)) for it in items
])
assert op2 == 20259, 'GetBag 响应 opcode 错误: %s' % (op2,)
weapon = next(it for it in items if it['item'].get(1) == 120592)
reward_weapons = [it for it in items if it['item'].get(1) in (120590, 120591, 120592, 120593)]
assert len(reward_weapons) == 4, '四个职业的任务奖励装备必须全部入包'
assert all(it['equip'] for it in reward_weapons), '任务奖励装备必须全部携带 EquipTransMessage'
assert weapon['item'].get(2) == 1, '120592 的 ItemType 应为 EquipItem(1)'
assert weapon['equip'].get(1) == 120592, '详情 EquipId 应关联客户端 EquipBase 模板'
assert weapon['equip'].get(3) == 1, '装备详情 specialKey 应为 1'
assert weapon['equip'].get(7) == 5, '装备详情 Star 应为 5'
assert weapon['equip'].get(8) == 4, '装备详情 Quality 应为 4'
assert weapon['equip'].get(9) == 1, '新装备详情 Level 应为 1'
assert weapon['equip'].get(10) in (None, 0), '普通模板装备 specialId 应为 0'
assert weapon['item'].get(4) == 1, '装备实例 Count 应为 1'
assert weapon['attributes'], '装备详情应包含 mainAttribute 条目'
assert all(1 <= attr.get(1, 0) <= 31 for attr in weapon['attributes']), \
    'mainAttribute Key 必须使用 AttributeType(1..31): %s' % (weapon['attributes'],)
assert {attr.get(1) for attr in weapon['attributes']} == {7, 8, 11, 12, 19}, \
    '120592 mainAttribute 键错误: %s' % (weapon['attributes'],)
assert all(attr.get(2) == 0 for attr in weapon['attributes']), \
    '模板基础属性 Value 必须为 0，避免客户端重复叠加: %s' % (weapon['attributes'],)

# 先双击错误职业装备，必须返回正常 20272 错误响应且连接不断开。
wrong_weapon = next(it for it in items if it['item'].get(1) == 120590)
r = next_rpc()
op2, b, pushes = send_req(20271, vf(1, wrong_weapon['index']) + vf(90, r), r)
reject_message = bytes_fld(b, 92)
print('[9.2] PutOn 错误职业装备 op=%d err=%s message=%r' % (
    op2, fld(b, 91), reject_message
))
assert op2 == 20272, '错误职业装备应返回 20272'
assert fld(b, 90) == r, '错误职业响应 RpcId 不匹配'
assert fld(b, 91) in (None, 0), '业务拒绝不能使用会触发客户端异常的 Error'
assert reject_message, '业务拒绝必须通过 Message 提示原因'
rejected_bag = bag_entries(b)
assert rejected_bag, '业务拒绝也必须携带当前 BagMapList'
assert {it['item'].get(1) for it in rejected_bag}.issuperset({120590, 120592}), \
    '业务拒绝后错误和正确职业装备都应仍在背包'
assert not any(it['item'].get(1) == 120590 for it in bag_entries(b, 2)), \
    '错误职业装备不能进入穿戴栏'

# 双击 job1 对应的 120592，检查背包减少、穿戴栏增加。
r = next_rpc()
op2, b, pushes = send_req(20271, vf(1, weapon['index']) + vf(90, r), r)
worn = bag_entries(b, 2)
print('[9.3] PutOn 120592 op=%d worn=%s' % (
    op2, [(it['index'], it['item'].get(1)) for it in worn]
))
assert op2 == 20272 and not fld(b, 91), '双击装备 120592 失败'
assert any(it['index'] == 0 and it['item'].get(1) == 120592 for it in worn), \
    '120592 未进入武器穿戴槽'
equipped_bag_ids = {it['item'].get(1) for it in bag_entries(b)}
assert 120592 not in equipped_bag_ids and 120590 in equipped_bag_ids, \
    '正确穿戴后应只从背包移除 120592'

if os.environ.get('MHQ_TEST_SCOPE') == 'equip':
    print('[EQUIP PASS] 详情字段、业务拒绝与错误后继续正确穿戴全部通过')
    sock.close()
    raise SystemExit(0)

# 10) 再点击 NPC 1012：应显示 10012 可接
r = next_rpc()
op2, b, pushes = send_req(20206, vf(1, 1012) + vf(90, r), r)
print('[10] ClickNPC 1012 (10011完成后)')
openui = [bb for o, bb in pushes if o == 20216]
assert op2 == 20207 and not fld(b, 91), '再次点击苹果JJ失败'
assert openui, '10011 完成后点击苹果JJ应推送 OpenTaskUI(20216)'
tl = []
for f, wt, v in decode(openui[0]):
    if f == 1 and wt == 2:
        kv = dict((ff, vv) for ff, wt2, vv in decode(v) if wt2 == 0)
        tl.append(kv)
print('    TaskList=', tl)
assert any(kv.get(1) == 10012 for kv in tl), '应显示可接 10012'
assert next(kv.get(2) for kv in tl if kv.get(1) == 10012) == 1, \
    '10012 在接取前应为 Waiting(1)'

# 11) 接取 10012
r = next_rpc()
op2, b, pushes = send_req(20217, vf(1, 10012) + vf(90, r), r)
print('[11] AcceptTask 10012')
show('M2C_AcceptTask', op2, b)
err = fld(b, 91)
assert err is None or err == 0, '接取10012失败 err=%s' % (err,)
task_state_pushes = [bb for o, bb in pushes if o == 20225]
assert task_state_pushes, '接取任务后应推送 SendTaskState(20225)'
assert any(fld(bytes_fld(bb, 1), 1) == 10012 and fld(bytes_fld(bb, 1), 2) == 2
           for bb in task_state_pushes), '接取后 10012 应同步为 Running(2)'

# 12) 开始主线战斗 1001（怪物 10001 x2）
r = next_rpc()
op2, b, pushes = send_req(20048, vf(1, 1001) + vf(90, r), r)
print('[12] StartMainStoryFight region=1001')
show('M2C_StartMainStoryFight', op2, b)
msgi = [bb for o, bb in pushes if o == 20050]
assert msgi, '应推送 MainStoryMonsterInfo(20050)'
mon = []
for f, wt, v in decode(msgi[0]):
    if f == 1 and wt == 2:
        kv = dict((ff, vv) for ff, wt2, vv in decode(v) if wt2 == 0)
        mon.append(kv)
print('    怪物=', mon, ' (应2只 10001)')
assert len(mon) == 2 and all(kv.get(2) == 10001 for kv in mon)

# 活跃战斗不能被第二次开始请求覆盖
# 注：服务器对重复开始战斗返回幂等成功（Error=0，无 20047/20050 重建推送），
# 避免 Session.Call 因 RPC 异常断开客户端（battle.go 注释）。
r = next_rpc()
op2, b, pushes = send_req(20048, vf(1, 1001) + vf(90, r), r)
assert not any(o in (20047, 20050) for o, _ in pushes), '重复开始战斗不能重建战场'

# 选择第二只怪，首个单体技能必须命中它
selected_monster = mon[1][1]
r = next_rpc()
op2, b, pushes = send_req(20071, vf(1, selected_monster) + vf(90, r), r)
assert not fld(b, 91), '选择存活敌人应成功'

# 13) 开启自动战斗，第一发必须自动使用普攻并进入服务端权威 5 秒 CD。
r = next_rpc()
op2, b, pushes = send_req(20069, vf(1, 1) + vf(90, r), r)
assert op2 == 20070 and not fld(b, 91), '开启自动战斗失败'
pushes.extend(collect_until({20075, 20237}, 2.0))
play = next(bb for o, bb in pushes if o == 20075)
cdpush = next(bb for o, bb in pushes if o == 20237)
assert fld(play, 3) == 5000, '普攻必须读取 xx00 战斗配置的 CD'
assert fld(play, 4) == selected_monster, '单体技能未命中选择的敌人'
assert fld(cdpush, 3) == 5000, '客户端技能槽 CD 应为 5000ms'

r = next_rpc()
op2, b, pushes = send_req(20069, vf(1, 0) + vf(90, r), r)
assert op2 == 20070 and not fld(b, 91), '关闭自动战斗失败'

# 绕过客户端按钮直接重发，服务器也必须拒绝且不能产生战斗伤害推送。
r = next_rpc()
op2, b, pushes = send_req(20233, vf(1, 0) + vf(90, r), r)
assert op2 == 20234 and fld(b, 90) == r, 'CD 拒绝响应 opcode/RpcId 错误'
assert not fld(b, 91), 'CD 中重复普攻应以 Error=0 静默拒绝，避免客户端掉线'
assert not any(o in (20075, 20077, 20078, 20237) for o, _ in pushes), \
    '被拒绝的普攻不能产生播放、命中、伤害或新 CD 推送'

time.sleep(5.1)
r = next_rpc()
op2, b, pushes = send_req(20233, vf(1, 0) + vf(90, r), r)
# 胜利结算有 4.2s 延迟（battleVictoryDelay，匹配客户端胜利动画），等够后再 drain。
time.sleep(4.5)
pushes.extend(drain_following())
victory = any(o == 20054 for o, _ in pushes)
print('[13] 第二次合法普攻后 BattleVictory(20054) =', victory)
assert op2 == 20234 and not fld(b, 91) and victory, '装备后两次合法普攻应完成首战'

# 胜利结算后查询任务状态，10012 应已达到可提交状态。
r = next_rpc()
op2, b, task_pushes = send_req(20223, vf(90, r), r)
assert op2 == 20224 and not fld(b, 91), '查询任务状态失败'
completed_10012 = [bb for o, bb in task_pushes if o == 20225 and
                   fld(bytes_fld(bb, 1), 1) == 10012 and
                   fld(bytes_fld(bb, 1), 2) == 3]
assert completed_10012, '首战胜利后 10012 应同步为 Completed(3)'

# 14) 完成 10012（击杀计数验证）
r = next_rpc()
op2, b, pushes = send_req(20219, vf(1, 10012) + vf(90, r), r)
print('[14] CompleteTask 10012')
show('M2C_CompleteTask', op2, b)
err = fld(b, 91)
assert err is None or err == 0, '完成10012失败 err=%s (击杀计数未生效)' % (err,)

# 15) 点击 NPC 1012 验证 10013 可接、10012 不再显示可接
r = next_rpc()
op2, b, pushes = send_req(20206, vf(1, 1012) + vf(90, r), r)
openui = [bb for o, bb in pushes if o == 20216]
tl = []
for f, wt, v in decode(openui[0]):
    if f == 1 and wt == 2:
        kv = dict((ff, vv) for ff, wt2, vv in decode(v) if wt2 == 0)
        tl.append(kv)
print('[15] ClickNPC 1012 TaskList=', tl)
assert any(kv.get(1) == 10013 and kv.get(2) == 1 for kv in tl), \
    '10012 完成后应显示 Waiting(1) 的 10013'
assert not any(kv.get(1) == 10012 for kv in tl), '已完成的 10012 不应再次显示'

print()
print('=== 全部通过 ===')
