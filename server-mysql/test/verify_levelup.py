# -*- coding: utf-8 -*-
"""验证升级系统：战斗胜利 → 升级 → 属性/等级推送 + 加点。"""
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
    for _ in range(60):
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
    # 走真实新手流程：点击苹果JJ后先接受 Waiting 的 10011，再提交并装备 120592。
    op, b, pushes = send_req(s, 20206, vf(1, 1012) + vf(90, 10), 10)
    task_ui = next((body for opcode, body in pushes if opcode == 20216), None)
    task_state = next((next((value for field, wire, value in fields(raw) if field == 2), None)
                       for field, wire, raw in fields(task_ui or b)
                       if field == 1 and wire == 'bytes' and
                       next((value for child, _, value in fields(raw) if child == 1), None) == 10011), None)
    if task_state == 1:
        op, b, _ = send_req(s, 20217, vf(1, 10011) + vf(90, 11), 11)
        assert op == 20218 and not [v for f, _, v in fields(b) if f == 91], '接受 10011 失败'
    op, b, _ = send_req(s, 20219, vf(1, 10011) + vf(90, 12), 12)
    assert op == 20220 and not [v for f, _, v in fields(b) if f == 91], '完成 10011 失败'
    op, b, _ = send_req(s, 20258, vf(90, 13), 13)
    assert op == 20259, 'GetBag 响应错误: %s' % op
    weapon_index = next(index for index, item_id in bag_items(b) if item_id == 120592)
    op, b, _ = send_req(s, 20271, vf(1, weapon_index) + vf(90, 14), 14)
    assert op == 20272 and not [v for f, _, v in fields(b) if f == 91], '装备 120592 失败'
    print('equipped task weapon 120592 at bag index', weapon_index)

acct = 'lv%d' % int(time.time() * 1000)
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
key = [v for f, w, v in fields(b) if f == 2][0]; gate = [v for f, w, v in fields(b) if f == 3][0]
op, b, _ = send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 2), 2)
op, b, _ = send_req(s, 20016, vf(2, 1) + sf(3, '升级测') + vf(90, 3), 3)
op, b, pushes = send_req(s, 20027, vf(90, 4), 4)
# 新号出生在海滩；先回主城再进海滩，确保取得本次换图的原生初始化。
send_req(s, 20031, vf(1, 1000401) + vf(90, 40), 40)
op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 6), 6)
equip_task_weapon(s)
# 点击客户端原生主线怪进战斗
region = [v for o, bb in pushes if o == 20047 for f, w, v in fields(bb) if f == 1][0]
assert not any(o == 20320 for o, _ in pushes), '主线地图不应重复推字段怪'
op, b, pushes = send_req(s, 20048, vf(1, region) + vf(90, 5), 5)
# 任务武器使每次普攻击杀一只怪；两次合法出手之间遵守服务端 5000ms CD。
op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 100), 100)
assert not [v for f, _, v in fields(b) if f == 91], '第一次普攻失败'
assert not any(o == 20054 for o, _ in pushes), '第一击不应提前结束双怪战斗'
time.sleep(5.1)
op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 101), 101)
assert not [v for f, _, v in fields(b) if f == 91], '第二次普攻被 CD 拒绝'
# 胜利结算有 4.2s 延迟（battleVictoryDelay，匹配客户端胜利动画），再补一段 drain。
pushes += drain(s, 5.5)
assert any(o == 20054 for o, _ in pushes), '第二次合法普攻后未胜利'
print('victory after two cooldown-valid attacks; 推送:', sorted(set(o for o, _ in pushes)))
# 升级推送：SendUnitInfo(20025) Level + SyncUnitAttribute(20169) 属性
su = [bb for o, bb in pushes if o == 20025]
if su:
    uc = [v for f, w, v in fields(su[0]) if f == 1][0]
    print('  SendUnitInfo.UnitCharacter:', fields(uc))
for o, bb in pushes:
    if o == 20169:
        fs = fields(bb)
        print('  SyncUnitAttribute: type=%s value=%s' % ([v for f, w, v in fs if f == 2], [v for f, w, v in fs if f == 3]))
# 加点测试（Trans=1 力量）
op, b, pushes = send_req(s, 20253, vf(2, 1) + vf(90, 300), 300)
print('AddPoint resp op=%d err=%s msg=%s' % (op, [v for f, w, v in fields(b) if f == 91], [v for f, w, v in fields(b) if f == 92]))
# 洗点
op, b, pushes = send_req(s, 20255, vf(2, 1) + vf(90, 301), 301)
print('ResetPoint resp op=%d err=%s' % (op, [v for f, w, v in fields(b) if f == 91]))
s.close()
print('ALL OK')
