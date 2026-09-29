# -*- coding: utf-8 -*-
"""转生系统验证：20342 C2M_TransLevel{Level=1/2} → 20343 空 Message + 20169(1029) + 20257 SendCharacter。
新号默认能量 2000；先加等级（用 20046 AddExp 无法直接加等级，改为直接验证转生链路本身）。"""
import os, socket, sqlite3, struct, time, sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
DB_PATH = os.path.abspath(os.environ.get('MHQ_DB', r'data/mhq.db'))

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
    for _ in range(300):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

def login(s, acct):
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    if fld(b, 91) is not None:
        send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
        op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 3), 3)
    key = fld(b, 2); gate = fld(b, 3)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    send_req(s, 20016, vf(2, 1) + sf(3, '转生测试') + vf(90, 5), 5)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    op, b, pushes = send_req(s, 20027, vf(90, 7), 7)
    return pushes

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

# 1) 新号转生：Level=1 → 成功，推 1029=1 + SendCharacter.Trans=1
acct = 'trans' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct)
# The online client uses cumulative total-level boundaries
# [0,6000,13000,21500,31500]. Seed this isolated regression account high
# enough to exercise +1 and +2 success paths; ordinary players must level to
# the requested boundary before the server accepts transmigration.
s.close()
time.sleep(0.2)
with sqlite3.connect(DB_PATH) as db:
    db.execute('''UPDATE players SET level=31500
                  WHERE account_id=(SELECT id FROM accounts WHERE account=?)''', (acct,))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
baseline_pushes = login(s, acct)
baseline_attrs = dict((fld(bb, 2), fld(bb, 3)) for o, bb in baseline_pushes if o == 20169)
op, b, pushes = send_req(s, 20342, vf(1, 1) + vf(90, 10), 10)
check(op == 20343 and not fld(b, 92), '转生 +1 成功 (20343 Message 空)')
trans_push = [fld(bb, 3) for o, bb in pushes if o == 20169 and fld(bb, 2) == 1029]
check(trans_push and trans_push[0] == 1.0, '推 20169 转生等级 1029=1: %s' % trans_push)
sc = [bb for o, bb in pushes if o == 20257]
check(len(sc) >= 1, '推 20257 SendCharacter')
# 解析 UnitCharacter 的 Trans (tag12)
trans_in_char = None
for o, bb in pushes:
    if o == 20257:
        for f, wt, v in fields(bb):
            if f == 1 and wt == 'bytes':
                kv = dict((ff, vv) for ff, wt2, vv in fields(v) if wt2 == 'var')
                trans_in_char = kv.get(12)
check(trans_in_char == 1, 'SendCharacter.UnitCharacter.Trans=1: %s' % trans_in_char)

# 2) 再转生 +2 → 3（上限），属性加成应用
op, b, pushes = send_req(s, 20342, vf(1, 2) + vf(90, 11), 11)
check(op == 20343 and not fld(b, 92), '转生 +2 成功 → 3 阶')
trans_push = [fld(bb, 3) for o, bb in pushes if o == 20169 and fld(bb, 2) == 1029]
check(trans_push and trans_push[0] == 3.0, '推 20169 转生等级 1029=3: %s' % trans_push)
attrs = dict((fld(bb, 2), fld(bb, 3)) for o, bb in pushes if o == 20169)
print('  转生后属性:', {k: v for k, v in attrs.items() if k in (1002, 1004, 1005, 1006, 1007, 1008, 1009, 1010, 1011, 1012, 1017, 1034, 1035)})
check(attrs.get(1002, 0) > 0, '高等级生命不会 int32 溢出为负数: %s' % attrs.get(1002))
check(abs(attrs.get(1005, 0) - 302.0) < 0.01, '3转军官力量加成生效: %s' % attrs.get(1005))
check(abs(attrs.get(1017, 0) - baseline_attrs.get(1017, 0) - 0.96) < 0.001,
      '3转军官闪避增加 0.96（配置直接 0.9 + CharacterGrowth 派生 0.06；总值=%s 初始=%s）' %
      (attrs.get(1017), baseline_attrs.get(1017)))
check(abs(attrs.get(1035, 0) - 3000.0) < 0.01, '3转军官体质加成生效: %s' % attrs.get(1035))

# 3) 超出上限：再转生 → 拒绝
op, b, pushes = send_req(s, 20342, vf(1, 1) + vf(90, 12), 12)
check(not fld(b, 91), 'business rejection keeps Error=0')
check(fld(b, 92), '已达上限再转生被拒: %s' % fld(b, 92))

# 4) 非法参数：Level=3 → 拒绝
op, b, pushes = send_req(s, 20342, vf(1, 3) + vf(90, 13), 13)
check(not fld(b, 91), 'invalid level keeps Error=0')
check(fld(b, 92), '非法 Level=3 被拒')

# 5) 重登持久化
s.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct)
op, b, pushes = send_req(s, 20342, vf(1, 1) + vf(90, 14), 14)
check(not fld(b, 91), 'persisted-limit rejection keeps Error=0')
check(fld(b, 92), '重登后仍 3 阶（持久化），再转被拒: %s' % fld(b, 92))
s.close()
print()
print('=== 转生系统回归全部通过：%d 项 ===' % ok)
