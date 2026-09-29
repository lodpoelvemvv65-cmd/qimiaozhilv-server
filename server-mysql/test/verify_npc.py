# -*- coding: utf-8 -*-
"""主城 NPC 点击验证：点击 1004-1011 各 NPC，检查服务器响应/UI 推送。"""
import socket, struct, time, sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7f; n >>= 7
        if n: out.append(b | 0x80)
        else: out.append(b); return bytes(out)
def vf(n, v):  return varint((n << 3) | 0) + varint(v)
def ff(n, v):  return varint((n << 3) | 5) + struct.pack('<f', v)
def frame_click_map(x, y):
    unit = ff(2, x) + ff(3, y)
    return varint(10) + varint(len(unit)) + unit
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
    op, b, _ = send_req(s, 20016, vf(2, 1) + sf(3, name) + vf(90, 5), 5)
    if op != 20017 or (fld(b, 91) or 0) != 0:
        raise AssertionError('create role failed: opcode=%s fields=%s' % (op, fields(b)))

    # LoginGate keys are single-use. Obtain a fresh key after role creation.
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 6), 6)
    key = fld(b, 2); gate = fld(b, 3)
    if key is None or gate is None:
        raise AssertionError('second login did not return a key and gate id')
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 7), 7)
    op, b, pushes = send_req(s, 20027, vf(90, 8), 8)
    if op != 20028 or fld(b, 91) not in (None, 0):
        raise AssertionError('enter game failed: opcode=%s fields=%s' % (op, fields(b)))
    return pushes

npc_names = {1003:'奇妙大博士', 1004:'万能管家', 1005:'练级任务官', 1006:'每日任务官', 1007:'冲级任务官',
             1008:'普通商店', 1009:'寄售商人', 1010:'装备合成大师', 1011:'炼化大师'}
acct = 'npc' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, 'NPC' + acct[-8:])
# New characters spawn on the beach. Move to the plaza before exercising its
# resident quiz NPC; RequestEnterMap is the same native client transition.
op, b, _ = send_req(s, 20031, vf(1, 1000501) + vf(90, 100), 100)
if op != 20032 or (fld(b, 91) or 0) != 0:
    raise AssertionError('move to plaza failed: opcode=%s fields=%s' % (op, fields(b)))
# Walk to the quiz NPC's online position before clicking it. The server
# validates interaction radius against authoritative movement progress.
s.sendall(pack(20022, frame_click_map(8.5, -1.37)))
time.sleep(5.0)
passed = 0
current_map = 1000501
positions = {
    1003: (8.5, -1.37),
    1004: (-12.294, -0.92),
    1005: (-9.378, -0.92),
    1006: (-7.632, -0.94),
    1007: (-5.724, -0.94),
    1008: (-2.862, -0.94),
    1009: (0.126, -0.94),
    1010: (3.762, -0.94),
    1011: (8.298, -0.94),
}
for npc in range(1003, 1012):
    target_map = 1000501 if npc == 1003 else 1000401
    if target_map != current_map:
        op, b, _ = send_req(s, 20031, vf(1, target_map) + vf(90, 1000 + npc), 1000 + npc)
        if op != 20032 or (fld(b, 91) or 0) != 0:
            raise AssertionError('move to NPC map failed: npc=%d opcode=%s fields=%s' %
                                 (npc, op, fields(b)))
        current_map = target_map
        time.sleep(1.0)
    x, y = positions[npc]
    s.sendall(pack(20022, frame_click_map(x, y)))
    time.sleep(2.0)
    op, b, pushes = send_req(s, 20206, vf(1, npc) + vf(90, npc), npc)
    push_ops = sorted(set(o for o, _ in pushes))
    error = fld(b, 91) or 0
    message = fld(b, 92) or b''
    if op != 20207 or error != 0 or message:
        raise AssertionError('NPC %d failed: opcode=%s error=%s message=%r' % (npc, op, error, message))
    if npc == 1003:
        quest_ui = next((body for push_op, body in pushes if push_op == 20302), None)
        if quest_ui is None or fld(quest_ui, 1) != 10:
            raise AssertionError('quiz NPC did not push OpenQuestUI Card=10: %s' % fields(quest_ui or b''))
    passed += 1
    print('PASS NPC %d %-8s response=%d pushes=%s' % (npc, npc_names.get(npc, '?'), op, push_ops))
s.close()
print('PASS: %d NPC click checks' % passed)
