# -*- coding: utf-8 -*-
"""任务系统回归：进度记录（GetTask 1/3 进度）、击杀计数（杀多次才算完成）、
killCount 持久化（重登进度保留）、未达标拒绝提交、达标提交成功。"""
import socket, struct, time, sys, sqlite3, json
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
    return pushes

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

def get_task_list(s, rpc):
    """20221 GetTask → 解析 TaskList(tag1) 里 TansferTask{Id, CurrCompleteList}"""
    op, b, pushes = send_req(s, 20221, vf(90, rpc), rpc)
    out = []
    for f, wt, v in fields(b):
        if f == 1 and wt == 'bytes':
            tt = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
            t_id = tt.get(1)
            # CurrCompleteList 是 packed repeated int32（tag2）→ 解析 bytes
            cur = []
            for ff, ww, vv in fields(v):
                if ff == 2 and ww == 'bytes':
                    i = 0
                    while i < len(vv):
                        x = 0; sh = 0
                        while True:
                            b2 = vv[i]; i += 1; x |= (b2 & 0x7f) << sh; sh += 7
                            if not b2 & 0x80: break
                        cur.append(x)
                elif ff == 2 and ww == 'var':
                    cur.append(vv)
            out.append((t_id, cur))
    return out

base = 'tp' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, base, '任务进度测试')
s.close(); time.sleep(1.5)
conn = sqlite3.connect(r'data/mhq.db')
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (base,)).fetchone()[0]
# 注入：任务 10081（杀 10255 x2）Running + 已杀 1 只（killCount 10255=1）
conn.execute('UPDATE players SET level=4000, tasks=? WHERE id=?', ('10081:2@k:10255=1', pid))
conn.commit(); conn.close()

s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, base, '任务进度测试')

# 1) GetTask → 进度 [1/2]
tl = get_task_list(s, 11)
print('  GetTask 任务列表:', tl)
check(any(t_id == 10081 for t_id, _ in tl), 'GetTask 返回进行中任务 10081')
t10081 = [cur for tid, cur in tl if tid == 10081][0]
check(t10081 == [1], '进度 CurrCompleteList=[1]（已杀 1/2）: %s' % t10081)

# 2) 未达标提交 → 拒绝
op, b, pushes = send_req(s, 20219, vf(1, 10081) + vf(90, 12), 12)
print('  杀 1/2 提交 resp:', op, 'err:', fld(b, 92))
check(op == 20220 and '未达成' in (fld(b, 92) or b'').decode('utf-8', 'replace'), '杀 1 只不能提交（条件未达成）')

# 3) 补足 10255=2 → 进度 [2/2]（先登出让 session 写回旧值，再注入新值，重登读取）
s.close()
time.sleep(1)
conn = sqlite3.connect(r'data/mhq.db')
conn.execute('UPDATE players SET tasks=? WHERE id=?', ('10081:2@k:10255=2', pid))
conn.commit(); conn.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, base, '任务进度测试')
tl2 = get_task_list(s, 13)
print('  重登后进度:', [(tid, cur) for tid, cur in tl2 if tid == 10081])
check(any(tid == 10081 and cur == [2] for tid, cur in tl2), '重登后 killCount 持久化（进度 [2/2]）')

# 4) 达标提交 → 成功（任务置 Completed）
op, b, pushes = send_req(s, 20219, vf(1, 10081) + vf(90, 14), 14)
print('  杀 2/2 提交 resp:', op, 'err:', fld(b, 92))
check(op == 20220 and fld(b, 92) is None, '杀 2 只后提交成功')
tl3 = get_task_list(s, 15)
check(not any(tid == 10081 for tid, _ in tl3), '已完成任务不在 GetTask 列表')

# 5) 真实战斗链路：进海滩层 1 → 点原生荷叶球战斗胜利 → killCount[10001]++ → 持久化
op, b, pushes = send_req(s, 20043, vf(90, 16), 16)  # 回主城
op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 17), 17)
regions = [fld(bb, 1) for o, bb in pushes if o == 20047]
check(len(regions) == 1 and not any(o == 20320 for o, _ in pushes), '进 1000601 只有原生主线怪')
op, b, pushes = send_req(s, 20048, vf(1, regions[0]) + vf(90, 18), 18)
check(op == 20049 and fld(b, 92) is None, '点击荷叶球开战')
victory = False
for i in range(10):
    op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 30 + i), 30 + i)
    if any(o == 20054 for o, _ in pushes):
        victory = True
        break
    time.sleep(5.1)
check(victory, '荷叶球战斗胜利')
conn = sqlite3.connect(r'data/mhq.db')
tasks_db = conn.execute('SELECT tasks FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
print('  战斗后 DB tasks:', tasks_db)
check('10001=' in tasks_db, '战斗胜利累加 killCount[10001] 并持久化（海滩层 1 多只怪 → +N）')
s.close()
print()
print('=== 任务系统回归（进度记录/击杀计数/持久化）全部通过：%d 项 ===' % ok)
