# -*- coding: utf-8 -*-
"""兑换星币/元宝兑换代金券/换格子/金币怪 验证：全部有响应不再卡死。"""
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
            return op2, b, pushes + [(op2, b)]
        pushes.append((op2, b))
    return None, None, pushes
def login(s, acct, name):
    op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
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

acct = 'cx' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '兑换测试')
# 先塞元宝
import sqlite3, json, os
s.close(); time.sleep(1.5)
conn = sqlite3.connect(r'data/mhq.db')
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
conn.execute('UPDATE players SET yuan_bao=10000 WHERE id=?', (pid,))
conn.commit(); conn.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '兑换测试')
# 兑换星币 20314（Gem=5 → 5000 铜币）
op, b, pushes = send_req(s, 20314, vf(1, 5) + vf(90, 10), 10)
check(op == 20315 and fld(b, 92) is None, '兑换星币 20314→20315 成功')
coins = [(fld(bb,2), fld(bb,3)) for o, bb in pushes if o == 20169 and fld(bb,2) == 1028]
print('  兑换后铜币推送:', coins)
check(any(v == 105000.0 for _, v in coins), '铜币 100000 +5000 = 105000: %s' % coins)
# 元宝兑换代金券 20312（5 元宝 → 5 代金券）
op, b, pushes = send_req(s, 20312, vf(1, 5) + vf(90, 11), 11)
check(op == 20313 and fld(b, 92) is None, '元宝兑换代金券 20312→20313 成功')
# 交换格子 20265（需要背包有 2 格 → 买 2 瓶药水再换）
op, b, pushes = send_req(s, 20174, vf(1, 0) + vf(2, 0) + vf(3, 2) + vf(90, 12), 12)
op, b, pushes = send_req(s, 20265, vf(1, 1) + vf(2, 3) + vf(90, 13), 13)
check(op == 20266 and fld(b, 92) is None, '交换格子 20265→20266 成功')
# 金币怪 20351
op, b, pushes = send_req(s, 20351, vf(90, 14), 14)
check(op == 20352 and fld(b, 92) is None, '金币怪 20351→20352 成功')
# 金币怪次数 20353
op, b, pushes = send_req(s, 20353, vf(90, 15), 15)
check(op == 20354 and fld(b, 92) is None, '金币怪次数 20353→20354 成功')
# 任务官 NPC 空列表也推面板
op, b, pushes = send_req(s, 20206, vf(1, 1005) + vf(90, 16), 16)
push_ops = sorted(set(o for o, _ in pushes))
check(20216 in push_ops, '练级任务官点击推 20216 任务面板（空列表）: %s' % push_ops)
op, b, pushes = send_req(s, 20206, vf(1, 1004) + vf(90, 17), 17)
push_ops = sorted(set(o for o, _ in pushes))
check(20187 in push_ops, '万能管家点击推 20187 仓库面板: %s' % push_ops)
s.close()
print()
print('=== 兑换星币/代金券/换格/金币怪/任务官NPC 回归全部通过：%d 项 ===' % ok)
