# -*- coding: utf-8 -*-
"""自动战斗持续验证：开自动 → 打赢当前怪 → 点下一个怪 → 自动战斗自动恢复。"""
import os, socket, struct, time, sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
HOST = os.environ.get('MHQ_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_PORT', '7756'))
DB_PATH = os.environ.get('MHQ_DB', r'data/mhq.db')

def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7f; n >>= 7
        if n: out.append(b | 0x80)
        else: out.append(b); return bytes(out)
def vf(n, v):  return varint((n << 3) | 0) + varint(v)
def sf(n, s):
    b = s.encode('utf-8'); return varint((n << 3) | 2) + varint(len(b)) + b
def packed_vf(n, values):
    b = b''.join(varint(v) for v in values)
    return varint((n << 3) | 2) + varint(len(b)) + b
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

acct = 'auto' + str(int(time.time()))
s = socket.create_connection((HOST, PORT)); s.settimeout(5)
login_pushes = login(s, acct, '自动战斗')
s.close(); time.sleep(1.5)
# 等级拉高 → 普攻伤害高，第一场几秒打完
import sqlite3
conn = sqlite3.connect(DB_PATH)
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
conn.execute("UPDATE players SET level=4000, char_point=4001, skills='100001:1,110101:1', auto_skills='' WHERE id=?", (pid,))
conn.commit(); conn.close()
s = socket.create_connection((HOST, PORT)); s.settimeout(5)
login_pushes = login(s, acct, '自动战斗')
# 按客户端流程保存自动技能，再确认已经持久化。
op, b, enter_pushes = send_req(s, 20241, packed_vf(1, [110101]) + vf(90, 10), 10)
conn = sqlite3.connect(DB_PATH)
saved_auto = conn.execute('SELECT auto_skills FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
assert op == 20242 and fld(b, 91) is None and saved_auto == '110101', \
    '自动技能保存失败: op=%s message=%s db=%r' % (op, fld(b, 92), saved_auto)
print('  自动技能已保存:', saved_auto)
# 在战斗外先打开自动战斗。客户端主界面的按钮允许这样操作，服务端必须
# 持久化偏好，并在下一场战斗创建后直接启动 worker。
op, b, _ = send_req(s, 20069, vf(1, 1) + vf(90, 13), 13)
conn = sqlite3.connect(DB_PATH)
saved_switch = conn.execute('SELECT auto_battle FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
assert op == 20070 and fld(b, 92) is None and saved_switch == 1, \
    '战斗外自动开关未持久化: op=%s message=%s db=%r' % (op, fld(b, 92), saved_switch)
print('  战斗外自动开关已保存:', saved_switch)
# 新号出生点本来就在海滩，20047 会在 EnterGame 登录推送中到达；若存档
# 不在海滩，再走 20031 换图并取换图推送。
pushes = login_pushes + enter_pushes
if not any(o == 20047 for o, _ in pushes):
    op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 11), 11)
regions = [v for o, bb in pushes if o == 20047 for f, w, v in fields(bb) if f == 1]
print('  主线 region:', regions)
assert len(regions) == 1 and not any(o == 20320 for o, _ in pushes), '海滩应只推原生主线怪: %s' % regions
op, b, pushes = send_req(s, 20048, vf(1, regions[0]) + vf(90, 12), 12)
# 不再切换按钮；进入战斗后应直接使用上面保存的开启状态。
# 等自动战斗打完（4000 级普攻秒杀 2 只 80 血怪 → 2 次普攻 ~11s + 4.2s 结算延迟）
time.sleep(16)
pa = drain(s, 1.0)
win = [o for o, _ in pa if o == 20054]
special_casts = [fld(bb, 2) for o, bb in pa if o == 20075 and fld(bb, 2) == 110101]
print('  第一场胜利 20054:', win)
print('  第一场自动释放 110101:', len(special_casts))
assert special_casts, '自动战斗没有释放已保存的 110101，只使用了普通攻击'
# 第二场：20048 StartMainStoryFight（线上主线 AI / 连续打怪路径）→ 自动战斗应自动恢复
op, b, pushes = send_req(s, 20048, vf(1, 1) + vf(90, 22), 22)
print('  第二场战斗开始，等待自动攻击...')
time.sleep(8)
pb = drain(s, 1.0)
# 自动攻击应产生 20075(PlaySkill)、20078(伤害) 或 20169(血条)，不得重建战斗或推未注册消息。
auto_attacked = any(o in (20075, 20078, 20169) for o, _ in pb)
forbidden = [o for o, _ in pb if o in (20053, 20079)]
auto_attacked = auto_attacked and not forbidden
print('  第二场自动攻击 20075/20078/20169:', sorted(set(o for o, _ in pb)))
print('  禁止消息 20053/20079:', forbidden)
print('  ✓ 第二场自动攻击恢复:', auto_attacked)
s.close()
print()
print('=== 自动战斗持续回归：%s ===' % ('全部通过' if auto_attacked else '失败'))
if not auto_attacked:
    raise SystemExit(1)
