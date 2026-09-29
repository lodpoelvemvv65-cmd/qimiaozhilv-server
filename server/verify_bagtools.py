# -*- coding: utf-8 -*-
"""背包整理/拆分/升级/锻造 验证：发请求必须收到完整响应（不再掉线）。"""
import socket, struct, time, sys, sqlite3, json, os
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
DB_PATH = os.path.abspath(os.environ.get('MHQ_DB', r'data/mhq.db'))
os.chdir(os.path.dirname(os.path.abspath(__file__)))

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
def bag_items(bb):
    out = {}
    for f, wt, v in fields(bb):
        if f == 1 and wt == 'bytes':
            idx = None; ni = None
            for ff, wt2, vv in fields(v):
                if wt2 == 'var' and ff == 1: idx = vv
                elif wt2 == 'bytes' and ff == 2: ni = vv
            if ni is not None:
                nkv = dict((ff, vv) for ff, wt2, vv in fields(ni) if wt2 == 'var')
                out[idx] = (nkv.get(1), nkv.get(4))
    return out

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

acct = 'bt' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '背包工具')
s.close(); time.sleep(1.5)
# 塞：药水 110305×5(k1)、药水 110305×3(k8 打散)、武器 120553(k3)、宝箱 110394(k9)、
# 打造材料 20001×10(k11)/20002×3(k12)（配方 1017：JobType=1 → 120227）
conn = sqlite3.connect(DB_PATH)
row = None
for _ in range(20):
    row = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()
    if row is not None:
        break
    time.sleep(0.25)
check(row is not None, 'new account was persisted before bag setup')
pid = row[0]
bag = [{"k":1,"v":{"i":110305,"t":2,"s":0,"c":5,"l":False,"q":0,"r":0,"v":0}},
       {"k":8,"v":{"i":110305,"t":2,"s":0,"c":3,"l":False,"q":0,"r":0,"v":0}},
       {"k":3,"v":{"i":120553,"t":1,"s":0,"c":1,"l":False,"q":0,"r":0,"v":0}},
       {"k":9,"v":{"i":110394,"t":2,"s":0,"c":1,"l":False,"q":0,"r":0,"v":0}},
       {"k":11,"v":{"i":20001,"t":3,"s":0,"c":10,"l":False,"q":0,"r":0,"v":0}},
       {"k":12,"v":{"i":20002,"t":3,"s":0,"c":3,"l":False,"q":0,"r":0,"v":0}}]
conn.execute('UPDATE players SET level=4000, job_id=1, coin=1000000, bag_json=? WHERE id=?', (json.dumps(bag), pid))
conn.commit(); conn.close()

s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '背包工具')
op, b, pushes = send_req(s, 20258, vf(90, 10), 10)
initial_bag = bag_items(b)
print('  原始背包:', initial_bag)
check(len(initial_bag) == 6, '6 格背包就位（含打造材料）')

# 登录加载时会按客户端规则规范化稀疏索引，必须使用 GetBag 返回的真实槽位。
split_idx = [k for k, v in initial_bag.items() if v == (110305, 5)][0]
op, b, pushes = send_req(s, 20267, vf(1, split_idx) + vf(2, 2) + vf(90, 11), 11)
check(op == 20268 and fld(b, 92) is None, '拆分响应 20268 成功')
bm = bag_items(b)
check(any(v == (110305, 2) for v in bm.values()), '拆出 2 瓶新格子: %s' % bm)
check(any(v == (110305, 3) for v in bm.values()), '原格剩 3 瓶: %s' % bm)

# 整理 → 重排到 1..n + 同 Id 堆叠（110305 全合到一起；材料 20001/20002 各自合并）
op, b, pushes = send_req(s, 20269, vf(90, 12), 12)
check(op == 20270 and fld(b, 92) is None, '整理响应 20270 成功')
bm2 = bag_items(b)
counts = [v[1] for v in bm2.values() if v[0] == 110305]
check(sum(counts) == 8 and len(bm2) == 5, '整理后 110305 合并 8 瓶 + 武器 + 宝箱 + 2 材料 = 5 格: %s' % bm2)

# 升级 k1 装备 120553（若整理后格子变了，找武器格子）
widx = [k for k, v in bm2.items() if v[0] == 120553][0]
op, b, pushes = send_req(s, 20277, vf(1, widx) + vf(90, 13), 13)
check(op == 20278 and fld(b, 92) is None, '升级响应 20278 成功')

# 锻造：配方 1017（JobType=1，材料 20001×5+20002×1，铜币 20 万）→ 产出 120227
op, b, pushes = send_req(s, 20204, vf(2, 1017) + vf(90, 14), 14)
check(op == 20205 and fld(b, 92) is None, '锻造响应 20205 成功')
bm3 = bag_items(b)
check(any(v[0] == 120227 for v in bm3.values()), '锻造产出 120227 入包: %s' % bm3)
# 第二次锻造：材料恰好够一次（20001 剩 5、20002 剩 2）→ 也成功
op, b, pushes = send_req(s, 20204, vf(2, 1017) + vf(90, 15), 15)
check(op == 20205 and fld(b, 92) is None, '第二次锻造成功')
# 第三次：20001 不足 → 材料不足业务拒绝（不崩）
op, b, pushes = send_req(s, 20204, vf(2, 1017) + vf(90, 16), 16)
check(op == 20205 and '材料不足' in (fld(b, 92) or b'').decode('utf-8', 'replace'), '材料不足业务拒绝（不崩）')
s.close()
print()
print('=== 背包整理/拆分/升级/锻造回归全部通过：%d 项 ===' % ok)
