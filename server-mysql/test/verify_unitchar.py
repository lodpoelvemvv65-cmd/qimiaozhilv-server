# -*- coding: utf-8 -*-
"""抓 20025 SendUnitInfo 的原始字节并解码字段。"""
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

def fields(body, depth=0):
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
            sub = body[i:i+l]; i += l
            out.append((f, 'bytes', sub))
        elif wt == 5:
            out.append((f, 'float', struct.unpack('<f', body[i:i+4])[0])); i += 4
        else: break
    return out

def send_req(s, op, body, rpc):
    s.sendall(pack(op, body))
    pushes = []
    for _ in range(40):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

acct = 'hp%d' % int(time.time() * 1000)
role_name = 't' + acct[-8:]
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
assert op == 20011 and not [v for f, w, v in fields(b) if f == 91], 'register failed: %s' % fields(b)
key = [v for f, w, v in fields(b) if f == 2][0]; gate = [v for f, w, v in fields(b) if f == 3][0]
op, b, _ = send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 2), 2)
assert op == 20015 and not [v for f, w, v in fields(b) if f == 91], 'login gate failed: %s' % fields(b)
op, b, _ = send_req(s, 20016, vf(2, 1) + sf(3, role_name) + vf(90, 3), 3)
assert op == 20017 and not [v for f, w, v in fields(b) if f == 91], 'create role failed: %s' % fields(b)
op, b, pushes = send_req(s, 20027, vf(90, 4), 4)
assert op == 20028, 'EnterGame response opcode=%s' % op
assert not [v for f, w, v in fields(b) if f == 91], 'enter game failed: %s' % fields(b)
title = None
for o, bb in pushes:
    if o == 20025:
        print('20025 SendUnitInfo body len', len(bb))
        for f, wt, v in fields(bb):
            if wt == 'bytes':
                print('  field %d (UnitCharacter, %d bytes):' % (f, len(v)))
                for f2, wt2, v2 in fields(v):
                    print('     field %d %s %s' % (f2, wt2, repr(v2)[:80]))
                    if f == 1 and f2 == 5 and wt2 == 'var':
                        title = v2
            else:
                print('  field %d %s %s' % (f, wt, v))
numeric_title = []
for o, bb in pushes:
    if o != 20169:
        continue
    d = dict((f, v) for f, w, v in fields(bb))
    if d.get(2) == 1038:
        numeric_title.append(d.get(3))
print('title sync: UnitCharacter.Title=%s NumericType1038=%s' % (title, numeric_title))
assert title == 120889, 'UnitCharacter.Title=%s, want 120889' % title
assert numeric_title and numeric_title[-1] == 120889, 'NumericType 1038=%s, want 120889' % numeric_title
print('TITLE UNIT+NUMERIC TCP TEST PASSED')
s.close()
