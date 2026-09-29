# -*- coding: utf-8 -*-
"""验证背包系统：GetBag/GetCharacter/PutOn/Takeoff/UseGoods + 任务奖励入包 + 线上属性公式。"""
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

def parse_bagmap(body):
    """解析 BagMapList（repeated BagMap{Index, NetItem, EquipTrans}）。"""
    items = []
    for f, w, v in fields(body):
        if f == 1 and w == 'bytes':
            idx = None; ni = None; et = None
            for f2, w2, v2 in fields(v):
                if f2 == 1: idx = v2
                elif f2 == 2: ni = v2
                elif f2 == 3: et = v2
            entry = {'index': idx}
            if ni:
                d = dict((f, v) for f, w, v in fields(ni))
                entry['net'] = d
            if et:
                entry['equip'] = fields(et)
            items.append(entry)
    return items

acct = 'bg%d' % int(time.time() * 1000)
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
key = [v for f, w, v in fields(b) if f == 2][0]; gate = [v for f, w, v in fields(b) if f == 3][0]
send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 2), 2)
send_req(s, 20016, vf(2, 1) + sf(3, '背包测') + vf(90, 3), 3)
op, b, pushes = send_req(s, 20027, vf(90, 4), 4)
print('enter game ok')

# 1) GetBag：初始空包
op, b, pushes = send_req(s, 20258, vf(90, 10), 10)
items = parse_bagmap(b)
print('1) GetBag resp op=%d items=%d (期望 0)' % (op, len(items)))
assert len(items) == 0
assert any(o == 20260 for o, _ in pushes), '缺少 M2C_SendBag 推送'

# 2) GetCharacter：线上公式 + CharacterGrowth（job1: MaxHp=93, MaxMp=31 + CharacterGrowth[21].Spi=54 = 85）
op, b, pushes = send_req(s, 20251, vf(1, 0) + vf(90, 11), 11)
uc = [v for f, w, v in fields(b) if f == 1][0]
ucd = dict((f, v) for f, w, v in fields(uc))
worn = [v for f, w, v in fields(b) if f == 3]
print('2) GetCharacter UnitCharacter:', {k: v for k, v in ucd.items() if k in (11, 13, 14, 15, 16, 9)}, 'worn:', len(worn), '(期望 MaxHp=93 MaxMp=31 worn=0)')
assert ucd.get(14) == 93, '线上 MaxHp 公式错误: %s' % ucd.get(14)
assert ucd.get(16) == 85, '线上 MaxMp + CharacterGrowth 公式错误: %s' % ucd.get(16)
assert len(worn) == 0

# 3) 完成主线任务 10011（对话苹果JJ，无前置自动接取）→ 奖励入包
op, b, pushes = send_req(s, 20206, vf(1, 1012) + vf(90, 12), 12)
op, b, pushes = send_req(s, 20219, vf(1, 10011) + vf(90, 13), 13)
print('3) complete task 10011 resp op=%d err=%s msg=%s' % (op, [v for f, w, v in fields(b) if f == 91], [v for f, w, v in fields(b) if f == 92]))
assert op == 20220 and not [v for f, w, v in fields(b) if f == 92], '任务提交失败'
sb = [bb for o, bb in pushes if o == 20260]
if sb:
    items = parse_bagmap(sb[0])
    print('   任务奖励入包推送:', [(it['index'], it['net'].get(1), it['net'].get(4)) for it in items])
    assert len(items) >= 3, '奖励未全部入包'

# 4) GetBag 验证背包物品
op, b, pushes = send_req(s, 20258, vf(90, 14), 14)
items = parse_bagmap(b)
print('4) GetBag items:', [(it['index'], it['net'].get(1), it['net'].get(2), it['net'].get(4)) for it in items])
assert len(items) >= 3

# 5) PutOn 穿马桶塞（120592 武器 EquipBase.Type 缺省 → 枚举槽位 0）
weapon = [it for it in items if it['net'].get(1) == 120592][0]
widx = weapon['index']
op, b, pushes = send_req(s, 20271, vf(1, widx) + vf(90, 15), 15)
err = [v for f, w, v in fields(b) if f == 92]
print('5) PutOn idx=%d resp op=%d err=%s' % (widx, op, err))
assert not err, '穿戴失败'
bagitems = parse_bagmap(b)
wornitems = []
for f, w, v in fields(b):
    if f == 2 and w == 'bytes':
        # v = 单个 BagMap{Index=1, NetItem=2, EquipTrans=3}
        idx = None; ni = None
        for f2, w2, v2 in fields(v):
            if f2 == 1: idx = v2
            elif f2 == 2: ni = v2
        entry = {'index': idx}
        if ni:
            entry['net'] = dict((f, v) for f, w, v in fields(ni))
        wornitems.append(entry)
print('   穿后背包:', [(it['index'], it['net'].get(1)) for it in bagitems], '穿戴:', [(it['index'], it['net'].get(1)) for it in wornitems])
assert len(wornitems) == 1 and wornitems[0]['index'] == 0, '穿戴槽位错误'
attrs = [(dict((f, v) for f, w, v in fields(bb)), bb) for o, bb in pushes if o == 20169]
patk = [a.get(3) for a, _ in attrs if a.get(2) == 1009]
print('   穿戴后 PhyAtk 推送:', patk, '(期望 152 = RoleGrowth 6 + CharacterGrowth 46 + 装备 100)')
assert patk and patk[-1] == 152.0, '装备攻击加成错误'

# 6) Takeoff 脱武器
op, b, pushes = send_req(s, 20273, vf(1, 0) + vf(90, 16), 16)
err = [v for f, w, v in fields(b) if f == 92]
print('6) Takeoff slot=0 resp op=%d err=%s' % (op, err))
assert not err, '脱下失败'
attrs = [(dict((f, v) for f, w, v in fields(bb)), bb) for o, bb in pushes if o == 20169]
patk = [a.get(3) for a, _ in attrs if a.get(2) == 1009]
print('   脱下后 PhyAtk 推送:', patk, '(期望 52 = RoleGrowth 6 + CharacterGrowth 46)')
assert patk and patk[-1] == 52.0, '脱下后攻击未回落'

# 客户端可能因残留 CharacterUI 实例连续触发两次双击回调。第二次空槽
# 必须作为普通业务提示返回，不能用非零 Error 让 Session.Call 断线。
op, b, pushes = send_req(s, 20273, vf(1, 0) + vf(90, 116), 116)
rpc_error = [v for f, w, v in fields(b) if f == 91]
message = [v for f, w, v in fields(b) if f == 92]
print('   repeated Takeoff slot=0 op=%d rpc_error=%s message=%s' % (op, rpc_error, message))
assert op == 20274 and not rpc_error and message, '重复脱装不应触发 RPC 断线'
op, b, pushes = send_req(s, 20258, vf(90, 117), 117)
assert op == 20259, '重复脱装后连接应保持可用'

# 7) UseGoods 使用生命药水（110305）
items = parse_bagmap(b)
pot = [it for it in items if it['net'].get(1) == 110305]
print('7) 药水在包:', [(it['index'], it['net'].get(4)) for it in pot])
if pot:
    op, b, pushes = send_req(s, 20275, vf(1, pot[0]['index']) + vf(90, 17), 17)
    err = [v for f, w, v in fields(b) if f == 92]
    print('   UseGoods resp op=%d err=%s' % (op, err))
    assert not err, '使用物品失败'
    items2 = parse_bagmap(b)
    pot2 = [it for it in items2 if it['net'].get(1) == 110305]
    print('   使用后药水剩余:', [(it['index'], it['net'].get(4)) for it in pot2], '(期望 9)')
    assert pot2 and pot2[0]['net'].get(4) == 9, '药水数量未减少'

print('\nALL BAG TESTS PASSED')
s.close()
