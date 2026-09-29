# -*- coding: utf-8 -*-
"""退出战斗验证：进战斗 → 发 20171 → 应收到 20171 响应帧(RpcId) + 20169 血蓝 + 20033 ChangeMap。"""
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
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

def login(s, acct, name):
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    if fld(b, 91) is not None:
        send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
        op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 3), 3)
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

acct = 'qb' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
pushes = login(s, acct, '退战测试')
# 新号默认在海滩 1000601，login 推送里已有原生主线 region
region = None
for o, bb in pushes:
    if o == 20047:
        for f, wt, v in fields(bb):
            if f == 1 and wt == 'var':
                region = v
                break
    if region is not None:
        break
if region is None:
    # 保险：重新进图
    op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 11), 11)
    for o, bb in pushes:
        if o == 20047:
            for f, wt, v in fields(bb):
                if f == 1 and wt == 'var':
                    region = v
                    break
        if region is not None:
            break
check(region is not None and not any(o == 20320 for o, _ in pushes), '有原生主线怪可点: %s' % region)
op, b, pushes = send_req(s, 20048, vf(1, region) + vf(90, 12), 12)
check(fld(b, 92) is None, '进入战斗成功')

# 发退出战斗 20171（带 RpcId）
s.sendall(pack(20171, vf(90, 30)))
pushes = drain(s, 1.0)
ops = [o for o, _ in pushes]
print('  退出战斗推送 opcodes:', sorted(set(ops)))
# 1) 响应帧（opcode=20171 带 RpcId 30）
resp_frames = [bb for o, bb in pushes if o == 20171 and rpc_of(bb) == 30]
check(len(resp_frames) >= 1, '收到 20171 响应帧（RpcId=30 完成 Call）')
# 2) 血蓝推送
hp = [(fld(bb, 2), fld(bb, 3)) for o, bb in pushes if o == 20169]
hpv = dict(hp)
check(1001 in hpv and 1003 in hpv, '推送当前血蓝: %s' % hpv)
# 3) ChangeMap 重进场景
cm = [bb for o, bb in pushes if o == 20033]
check(len(cm) >= 1, '收到 20033 ChangeMap（重进场景退出战斗）')
s.close()
print()
print('=== 退出战斗回归全部通过：%d 项 ===' % ok)
