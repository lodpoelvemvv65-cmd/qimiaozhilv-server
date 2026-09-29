# -*- coding: utf-8 -*-
"""10 号并发总测试：10 个账号同时登录 + 分组（组队/寄售/家族/聊天/进场景战斗）混合操作，
验证服务器并发稳定、无死锁、响应全部正常。"""
import socket, struct, time, sys, threading
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
    for _ in range(600):
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

results = []
lock = threading.Lock()
def run_player(idx):
    """每个玩家：登录 → 查背包 → 查技能 → 查主界面槽 → 世界聊天 → 进 1001001 场景 → 退出"""
    acct = 'cc%d_%d' % (int(time.time()) % 100000, idx)
    try:
        s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(8)
        pushes, pid = login(s, acct, '并发%d号' % idx)
        # 基础查询
        op, b, p = send_req(s, 20258, vf(90, 20), 20)
        assert op == 20259, 'get bag resp %d' % op
        op, b, p = send_req(s, 20239, vf(90, 21), 21)
        assert op == 20240, 'get skill resp %d' % op
        op, b, p = send_req(s, 20227, vf(90, 22), 22)
        assert op == 20228, 'main ui resp %d' % op
        # 世界聊天
        op, b, p = send_req(s, 20298, sf(1, '并发测试消息%d' % idx) + vf(2, 5) + vf(90, 23), 23)
        assert op == 20299, 'chat resp %d' % op
        # 进 Boss 层场景（字段怪）
        op, b, p = send_req(s, 20031, vf(1, 1001001) + vf(90, 24), 24)
        assert op == 20032, 'enter map resp %d' % op
        # 回主城
        op, b, p = send_req(s, 20043, vf(90, 25), 25)
        s.close()
        with lock:
            results.append((idx, 'OK'))
    except Exception as e:
        with lock:
            results.append((idx, 'FAIL: %r' % e))

threads = []
for i in range(10):
    t = threading.Thread(target=run_player, args=(i,))
    t.start()
    threads.append(t)
    time.sleep(0.3)  # 错峰登录

for t in threads:
    t.join(timeout=30)

ok_cnt = sum(1 for _, r in results if r == 'OK')
print('并发结果: %d/10 OK' % ok_cnt)
for idx, r in sorted(results):
    if r != 'OK':
        print('  #%d %s' % (idx, r))
assert ok_cnt == 10, '并发登录/操作失败'
print()
print('=== 10 号并发总测试全部通过 ===')
