# -*- coding: utf-8 -*-
"""Boss 扣体力验证：10010 Boss 层挑战 → 体力(1039) 减少；体力不足 → 拒绝。"""
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
    for _ in range(600):
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

def get_energy(acct):
    conn = sqlite3.connect(r'data/mhq.db')
    e = conn.execute("SELECT energy FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
    conn.close()
    return e

def boss_fight(sock):
    """选择一个当前存活的 Boss 层并走客户端原生两阶段挑战流程。"""
    for layer in range(1, 26):
        rpc = 100 + layer
        op, b, pushes = send_req(sock, 20031, vf(1, 1001000 + layer) + vf(90, rpc), rpc)
        time.sleep(0.35)
        pushes += drain(sock, 0.2)
        sock.settimeout(5)
        refreshes = [fld(bb, 1) for o, bb in pushes if o == 20056]
        if not refreshes:
            continue
        print('  选择存活 Boss 层:', layer)
        op, body, request_pushes = send_req(sock, 20057, vf(90, rpc + 100), rpc + 100)
        key = fld(body, 2)
        if fld(body, 92) is not None or not key:
            return op, body, pushes + request_pushes
        op, body, start_pushes = send_req(sock, 20059, vf(2, key) + vf(90, rpc + 200), rpc + 200)
        return op, body, pushes + request_pushes + start_pushes
    raise AssertionError('25 个 Boss 层当前均无可挑战原生 BossRefresh')

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

# ===== A：正常挑战扣体力 =====
acctA = 'bossA' + str(int(time.time()))
sa = socket.create_connection(('127.0.0.1', 7756)); sa.settimeout(5)
login(sa, acctA, 'Boss甲')
e0 = get_energy(acctA)
print('  初始体力:', e0)
op, b, pushes = boss_fight(sa)
print('  点 Boss resp:', op, 'err:', fld(b, 92))
e1 = get_energy(acctA)
print('  点 Boss 后体力:', e1)
check(e1 == e0 - 1, 'Boss 挑战按 CopyConfig.NeedBossEnergy 扣 1：%d -> %d' % (e0, e1))
energy_push = [(fld(bb,2), fld(bb,3)) for o, bb in pushes if o == 20169 and fld(bb,2) == 1039]
print('  20169 体力推送:', energy_push)
check(any(v == float(e1) for _, v in energy_push), '推送 20169 体力 %d' % e1)
sa.close()

# ===== B：体力不足拒绝 =====
acctB = 'bossB' + str(int(time.time()))
sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(5)
login(sb, acctB, 'Boss乙')
sb.close(); time.sleep(1.5)  # onClose saveData（体力 2000 落库）
conn = sqlite3.connect(r'data/mhq.db')
pidB = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acctB,)).fetchone()[0]
conn.execute('UPDATE players SET energy=0 WHERE id=?', (pidB,))
conn.commit(); conn.close()
sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(5)
login(sb, acctB, 'Boss乙')
print('  B 体力:', get_energy(acctB))
op, b, pushes = boss_fight(sb)
msg = fld(b, 92)
try:
    msg_txt = msg.decode('utf-8') if isinstance(msg, bytes) else str(msg)
except Exception:
    msg_txt = str(msg)
print('  B 体力不足点 Boss resp:', op, 'err:', msg_txt)
check(fld(b, 92) is not None and '体力' in msg_txt, '体力不足拒绝挑战（msg=%s）' % msg_txt)
sb.close()
print()
print('=== Boss 扣体力回归全部通过：%d 项 ===' % ok)
