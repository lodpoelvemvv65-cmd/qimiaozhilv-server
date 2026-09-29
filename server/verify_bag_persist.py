# -*- coding: utf-8 -*-
"""验证背包持久化：重登后背包/穿戴保留。复用 verify_bag.py 的协议工具。"""
import socket, struct, time, sys, sqlite3
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
    items = []
    for f, w, v in fields(body):
        if f == 1 and w == 'bytes':
            idx = None; ni = None
            for f2, w2, v2 in fields(v):
                if f2 == 1: idx = v2
                elif f2 == 2: ni = v2
            entry = {'index': idx}
            if ni: entry['net'] = dict((f, v) for f, w, v in fields(ni))
            items.append(entry)
    return items

def login_game(account, create=False):
    sock = socket.create_connection(('127.0.0.1', 7756)); sock.settimeout(5)
    if create:
        send_req(sock, 20010, sf(1, account) + sf(2, '123456') + vf(90, 1), 1)
    op, body, _ = send_req(sock, 20008, sf(1, account) + sf(2, '123456') + vf(90, 2), 2)
    key = [v for f, w, v in fields(body) if f == 2][0]
    gate = [v for f, w, v in fields(body) if f == 3][0]
    send_req(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 3), 3)
    if create:
        send_req(sock, 20016, vf(2, 1) + sf(3, '背包持久化') + vf(90, 4), 4)
        send_req(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 5), 5)
    send_req(sock, 20027, vf(90, 6), 6)
    return sock

def bagmap_item_id(payload):
    net = next((v for f, w, v in fields(payload) if f == 2 and w == 'bytes'), None)
    if net is None:
        return None
    return next((v for f, w, v in fields(net) if f == 1), None)

acct = 'persist%d' % int(time.time() * 1000)
s = login_game(acct, create=True)

# Buy through the real shop flow, then obtain and wear the task weapon.
op, b, _ = send_req(s, 20174, vf(1, 0) + vf(2, 0) + vf(3, 3) + vf(90, 10), 10)
items = parse_bagmap(b)
pot = [it for it in items if it['net'].get(1) == 110305]
assert pot and pot[0]['net'].get(4) == 3, '购买 110305×3 失败'
send_req(s, 20206, vf(1, 1012) + vf(90, 11), 11)
op, b, _ = send_req(s, 20219, vf(1, 10011) + vf(90, 12), 12)
assert op == 20220 and not [v for f, w, v in fields(b) if f == 92], '任务奖励领取失败'
op, b, _ = send_req(s, 20258, vf(90, 13), 13)
items = parse_bagmap(b)
weapon = next(it for it in items if it['net'].get(1) == 120592)
op, b, _ = send_req(s, 20271, vf(1, weapon['index']) + vf(90, 14), 14)
assert op == 20272 and not [v for f, w, v in fields(b) if f == 92], '穿戴任务武器失败'
s.close(); time.sleep(0.3)

# Worn equipment must survive relogin and must not also remain in the bag.
s = login_game(acct)
op, char_body, _ = send_req(s, 20251, vf(1, 0) + vf(90, 20), 20)
worn_ids = [bagmap_item_id(payload) for f, w, payload in fields(char_body) if f == 3 and w == 'bytes']
op, bag_body, _ = send_req(s, 20258, vf(90, 21), 21)
items = parse_bagmap(bag_body)
assert 120592 in worn_ids, '穿戴状态未持久化'
assert not any(it['net'].get(1) == 120592 for it in items), '已穿装备仍重复留在背包'
op, _, _ = send_req(s, 20273, vf(1, 0) + vf(90, 22), 22)
assert op == 20274, '脱下任务武器失败'
s.close(); time.sleep(0.3)

# After takeoff and a second relogin, the item is only in the bag.
s = login_game(acct)
op, char_body, _ = send_req(s, 20251, vf(1, 0) + vf(90, 30), 30)
worn_ids = [bagmap_item_id(payload) for f, w, payload in fields(char_body) if f == 3 and w == 'bytes']
op, bag_body, _ = send_req(s, 20258, vf(90, 31), 31)
items = parse_bagmap(bag_body)
pot = [it for it in items if it['net'].get(1) == 110305]
assert 120592 not in worn_ids, '脱装后个人信息仍显示装备'
assert any(it['net'].get(1) == 120592 for it in items), '脱装后装备未回到背包'
assert pot and pot[0]['net'].get(4) == 13, '购买物品和任务奖励数量未持久化'
print('背包/穿戴/脱装/重登持久化 OK:', [(it['net'].get(1), it['net'].get(4)) for it in items])
s.close()
