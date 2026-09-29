# -*- coding: utf-8 -*-
"""所有怪物血量与客户端一致性验证：
1. 全表扫描：MonsterBase 每行 Hp×HpTimes（服务器战斗公式），检查溢出/乘数异常
2. 世界 BOSS（BossBase 25 层）：进层战斗 → 20169 MaxHp == MonsterBase.Hp
3. 家族 BOSS（FamilyBossConfig 5 个）：20139 信息 BossInfoList Hp==MaxHp == MonsterBase.Hp
4. 野外怪（荷叶球）：20169 MaxHp == 80
客户端 20169 Handler 按 UnitId 更新现有单位的 NumericComponent；20053 仅用于断线重建，
普通技能期间发送会重复创建同 ID 怪物并使客户端卡在加载界面。"""
import os, socket, struct, time, sys, sqlite3, json, re
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
HOST = os.environ.get('MHQ_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_PORT', '7756'))

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

def load(name):
    raw = open(r'../datatable_json/%s.json' % name, 'rb').read().decode('utf-8', 'replace')
    raw = re.sub(r',\s*([\]}])', r'\1', raw)
    return json.loads(raw)

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

# ============ 0) 全表扫描 ============
mb = load('MonsterBase')
mbb = {v.get('_id'): v for k, v in mb}
print('=== MonsterBase 全表扫描（%d 行）===' % len(mb))
overflow = []
non1 = []
for k, v in mb:
    hp = int(v.get('Hp') or 0)
    ht = int(v.get('HpTimes') or 1)
    calc = hp * ht
    if ht != 1:
        non1.append((v.get('_id'), v.get('NickName'), hp, ht, calc))
    if calc > 0x7fffffff:
        overflow.append((v.get('_id'), v.get('NickName'), hp, ht, calc))
print('  HpTimes≠1 行:', non1)
print('  溢出 int32 行:', overflow)
check(len(non1) == 1 and non1[0][0] == 81001, 'HpTimes≠1 仅 81001（机器狗 WorldBossConfig，客户端无玩法入口，不参与战斗）')
check(len(overflow) == 1 and overflow[0][0] == 81001, '溢出仅 81001（服务器未使用，不产生实际值）')

# ============ 1) 野外怪荷叶球（10001 Hp=80，账号 A） ============
base = 'bl' + str(int(time.time()))
s = socket.create_connection((HOST, PORT)); s.settimeout(8)
login(s, base + 'a', '血量验证A')
send_req(s, 20031, vf(1, 1000401) + vf(90, 29), 29)
op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 30), 30)
regions = [fld(bb, 1) for o, bb in pushes if o == 20047]
check(len(regions) == 1 and not any(o == 20320 for o, _ in pushes), '海滩使用原生主线怪且无重影')
op, b, pushes = send_req(s, 20048, vf(1, regions[0]) + vf(90, 31), 31)
check(op == 20049 and fld(b, 92) is None, '荷叶球开战')
monster_units = []
for o2, x in pushes:
    if o2 == 20050:
        for f, wt, v in fields(x):
            if f == 1 and wt == 'bytes':
                monster_units.append(fld(v, 1))
attrs = []
forbidden = []
damage_seen = False
for i in range(4):
    op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 32 + i), 32 + i)
    for o2, x in pushes:
        if o2 in (20053, 20079):
            forbidden.append(o2)
        if o2 == 20078:
            damage_seen = True
        if o2 == 20169 and fld(x, 1) in monster_units:
            attrs.append((fld(x, 1), fld(x, 2), fld(x, 3)))
    if any(t == 1002 for _, t, _ in attrs):
        break
    time.sleep(5.1)
maxhp = max((v for _, t, v in attrs if t == 1002), default=0)
print('  荷叶球 MaxHp:', maxhp, '期望:', mbb[10001]['Hp'], '(20169 x%d)' % len(attrs))
check(maxhp == mbb[10001]['Hp'], '野外怪荷叶球血量 == MonsterBase[10001].Hp（80）')
check(damage_seen, '野外怪技能返回 20078 受击消息')
check(not forbidden, '野外怪技能不发送 20053/20079: %s' % forbidden)
s.close()

# ============ 2) 世界 BOSS 层 3（夺命监视者 10257 Hp=1500000，账号 B 全新 session） ============
# 注：层 1/层 2 分别留给 verify_bossenergy/verify_boss（世界 BOSS 击杀后 1h 不刷新，须隔离）
s = socket.create_connection((HOST, PORT)); s.settimeout(8)
login(s, base + 'b', '血量验证B')
op, b, pushes = send_req(s, 20031, vf(1, 1001003) + vf(90, 11), 11)  # 层 3
refreshes = [fld(bb, 1) for o, bb in pushes if o == 20056]
send_info = [bb for o, bb in pushes if o == 20061]
print('  层3 SendBossInfo:', [(fld(x, 1), fld(x, 2)) for x in send_info], 'BossRefresh:', refreshes)
check(refreshes == [1003], '层 3 原生 BossRefresh{BossId=1003}')
op, b, _ = send_req(s, 20057, vf(90, 12), 12)
key = fld(b, 2)
check(op == 20058 and key and fld(b, 92) is None, '层 3 获取挑战 Key')
op, b, pushes = send_req(s, 20059, vf(2, key) + vf(90, 13), 13)
check(op == 20060 and fld(b, 92) is None, '层 3 BOSS 开战')
# 开战推 SendBossInfo{BossId=10257}（20061 是开战事件，进图不推）
send_info = [bb for o, bb in pushes if o == 20061]
print('  开战 SendBossInfo:', [(fld(x, 1), fld(x, 2)) for x in send_info])
check(len(send_info) == 1 and fld(send_info[0], 1) == 10257, '层3 开战推 SendBossInfo{BossId=10257}')
monster_units = [fld(send_info[0], 2)]
legacy_init = [o for o, _ in pushes if o in (20047, 20050)]
check(monster_units[0] is not None, 'SendBossInfo 带 BOSS 战斗 UnitId')
check(not legacy_init, '世界 BOSS 不发送主线 20047/20050: %s' % legacy_init)
# 施放一次 → 20169 NumericType=1002/1001，且不得出现 20053/20079
op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 14), 14)
attrs = [(fld(bb, 1), fld(bb, 2), fld(bb, 3)) for o, bb in pushes
         if o == 20169 and fld(bb, 1) in monster_units]
forbidden = [o for o, _ in pushes if o in (20053, 20079)]
print('  20169 怪物属性:', attrs[:4])
maxhp = max((v for _, t, v in attrs if t == 1002), default=0)
print('  层3 BOSS MaxHp:', maxhp, '期望:', mbb[10257]['Hp'])
check(maxhp == mbb[10257]['Hp'], '世界 BOSS 层3 血量 == MonsterBase[10257].Hp（%d）' % maxhp)
check(any(o == 20078 for o, _ in pushes), '世界 BOSS 技能返回 20078 受击消息')
check(not forbidden, '世界 BOSS 技能不发送 20053/20079: %s' % forbidden)
s.close()

# ============ 3) 家族 BOSS 5 个血量（账号 C） ============
s = socket.create_connection((HOST, PORT)); s.settimeout(8)
login(s, base + 'c', '血量验证C')
fname = '血验家族%d' % (int(time.time()) % 10000)
op, b, pushes = send_req(s, 20123, sf(1, fname) + vf(90, 20), 20)
check(op == 20124 and fld(b, 92) is None, '建族成功')
op, b, pushes = send_req(s, 20139, vf(90, 21), 21)
bosses = [dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var') for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
print('  家族 BOSS 信息:', [(x.get(1), x.get(2), x.get(3), x.get(4)) for x in bosses])
check(op == 20141 and len(bosses) == 5, '家族 BOSS 信息 5 个')
all_match = True
for i, x in enumerate(bosses, 1):
    expect = mbb.get(50000 + i, {})
    exp_hp = int(expect.get('Hp') or 0) * int(expect.get('HpTimes') or 1)
    got_hp = x.get(2)
    got_max = x.get(3)
    if got_hp != exp_hp or got_max != exp_hp:
        all_match = False
        print('    不一致: BOSS%d 服务器 Hp=%s MaxHp=%s 期望=%d' % (i, got_hp, got_max, exp_hp))
check(all_match, '家族 BOSS 5 个 Hp==MaxHp==MonsterBase.Hp×HpTimes（3000万/9800万/2亿/8亿/20亿）')
s.close()
print()
print('=== 怪物血量与客户端一致性验证全部通过：%d 项 ===' % ok)
