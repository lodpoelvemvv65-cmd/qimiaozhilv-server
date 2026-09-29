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
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
key = [v for f, w, v in fields(b) if f == 2][0]; gate = [v for f, w, v in fields(b) if f == 3][0]
send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 2), 2)
send_req(s, 20016, vf(2, 1) + sf(3, '修复测') + vf(90, 3), 3)
op, b, pushes = send_req(s, 20027, vf(90, 4), 4)
print('enter game ok')

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
op, b, pushes = send_req(s, 20219, vf(1, 10011) + vf(90, 12), 12)
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
# 重登恢复海滩存档；先切主城再切回，避免同图请求不重复初始化。
send_req(_s2, 20031, vf(1, 1000401) + vf(90, 56), 56)
op, b, pushes = send_req(_s2, 20031, vf(1, 1000601) + vf(90, 55), 55)
region = [v for o, bb in pushes if o == 20047 for f, w, v in fields(bb) if f == 1][0]
assert not any(o == 20320 for o, _ in pushes), '主线地图不应重复推字段怪'
op, b, pushes = send_req(_s2, 20048, vf(1, region) + vf(90, 53), 53)
print('click monster -> battle init:', sorted(set(o for o, _ in pushes)))
# 发技能，抓 20077 EffectId
op, b, pushes = send_req(_s2, 20233, vf(1, 0) + vf(90, 54), 54)
assert not [v for f, w, v in fields(b) if f == 91], '第一次普攻失败'
# 20077 已停推（客户端 DOTween 缺 DOMove 重载会刷 MissingMethodException 拖掉线）；
# 伤害/施法走 20078/20075/20076。断言不再出现 20077。
eff = []
for o, bb in pushes:
    if o == 20077:
        d = dict((f, v) for f, w, v in fields(bb))
        eff.append((d.get(1), d.get(4), d.get(9)))
mon = []
for o, bb in pushes:
    if o == 20076:
        d = dict((f, v) for f, w, v in fields(bb))
        mon.append((d.get(1), d.get(2), d.get(3)))
print('  20077 PlaySkillEffect (应无):', eff)
print('  20076 MonsterPlaySkill (UnitId,SkillId,TargetId):', mon)
assert not eff, '20077 已停推（客户端 DOTween 缺陷），不应出现'
print('  -> 无 20077 特效推送（避免客户端 MissingMethodException 刷屏）')

# 等待真实 5000ms CD 后第二次普攻，任务武器应直接击杀第二只怪。
time.sleep(5.1)
op, b, pushes = send_req(_s2, 20233, vf(1, 0) + vf(90, 55), 55)
assert not [v for f, w, v in fields(b) if f == 91], '第二次普攻被 CD 拒绝'
# 胜利结算有 4.2s 延迟（battleVictoryDelay，匹配客户端胜利动画），再补一段 drain。
pushes += drain(_s2, 5.5)
assert any(o == 20054 for o, _ in pushes), '第二次合法普攻后未胜利'
print('victory after two cooldown-valid attacks')
# 击杀计数 + 已接取的 10012 完成条件
op, b, pushes = send_req(_s2, 20219, vf(1, 10012) + vf(90, 200), 200)
err = [v for f, w, v in fields(b) if f == 91]
print('CompleteTask 10012 resp err=%s' % err)
assert not err, '10012 应可提交（击杀荷叶球达成）'
print('ALL TASK+BATTLE FIX TESTS PASSED')
_s2.close()
