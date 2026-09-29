# -*- coding: utf-8 -*-
"""LoginVoucher / Voucher 免密重连 / 邮件空列表 验证。"""
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

def drain(s, timeout=0.4):
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
    for _ in range(300):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

# 1) 登录响应带 LoginVoucher(tag4)
acct = 'voucher' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
voucher = None
for f, wt, v in fields(b):
    if f == 4 and wt == 'bytes':
        voucher = v.decode('utf-8', 'replace')
check(voucher and '|' in voucher, '登录响应含 LoginVoucher: %r' % voucher)
key = fld(b, 2)
s.close()

# 2) Voucher 免密重连（LoginType=3，Password=key）
time.sleep(0.3)
s2 = socket.create_connection(('127.0.0.1', 7756)); s2.settimeout(5)
op, b, _ = send_req(s2, 20008, sf(1, acct) + sf(2, str(key)) + vf(3, 3) + vf(90, 3), 3)
check(fld(b, 91) is None, 'Voucher 免密重连成功（LoginType=3）')
voucher2 = None
for f, wt, v in fields(b):
    if f == 4 and wt == 'bytes':
        voucher2 = v.decode('utf-8', 'replace')
check(voucher2 and '|' in voucher2, '重连响应也带 LoginVoucher')
s2.close()

# 3) 邮件空列表 MailCount=0（新号领光欢迎邮件后）
s3 = socket.create_connection(('127.0.0.1', 7756)); s3.settimeout(5)
acct3 = 'mail0' + str(int(time.time()))
op, b, _ = send_req(s3, 20010, sf(1, acct3) + sf(2, '123456') + vf(90, 10), 10)
op, b, _ = send_req(s3, 20008, sf(1, acct3) + sf(2, '123456') + vf(90, 11), 11)
k3 = fld(b, 2); g3 = fld(b, 3)
send_req(s3, 20014, vf(1, k3) + vf(2, g3) + vf(90, 12), 12)
send_req(s3, 20016, vf(2, 1) + sf(3, '邮件测') + vf(90, 13), 13)
send_req(s3, 20014, vf(1, k3) + vf(2, g3) + vf(90, 14), 14)
op, b, pushes = send_req(s3, 20027, vf(90, 15), 15)
# 打开邮件：20281
op, b, pushes = send_req(s3, 20281, vf(90, 16), 16)
check(op == 20282, '邮件列表响应 20282')
mc = fld(b, 2)
check(mc == 1, '首次打开 MailCount=1（欢迎邮件）: %s' % mc)
# 领走
op, b, pushes = send_req(s3, 20283, vf(1, 1) + vf(90, 17), 17)
op, b, pushes = send_req(s3, 20285, vf(1, 1) + vf(90, 18), 18)
# 再打开：空列表（proto3 中 MailCount=0 为零值，序列化省略 tag2 → 读缺省 0）
op, b, pushes = send_req(s3, 20281, vf(90, 19), 19)
mc2 = fld(b, 2)
check(mc2 in (None, 0), '删光后 MailCount=0（缺省0）: %s' % mc2)
s3.close()
print()
print('=== 登录凭证/免密重连/邮件空列表 回归全部通过：%d 项 ===' % ok)
