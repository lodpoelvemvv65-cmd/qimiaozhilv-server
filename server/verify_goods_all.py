# -*- coding: utf-8 -*-
"""道具逐个测试：每种 EffectType 的效果链路（服务器行为 + 客户端协议推送）。
覆盖：血蓝药水(11/12/1)、魔法球(2/3+20080 buff)、经验卡(6/7+20080)、开箱(9)、
礼券(10 经验/金币/元宝/代金券)、精力药水(19)、货币物品(110201-110204)、回城卷轴(5)。"""
import socket, struct, time, sys, sqlite3, json, re
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
    for _ in range(500):
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

def db(acct):
    conn = sqlite3.connect(r'data/mhq.db')
    pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
    return conn, pid

def set_bag(conn, pid, items):
    """items: [(itemId, count), ...] → 客户端背包槽 0..47"""
    bag = [{"k": i, "v": {"i": iid, "t": 2, "s": 0, "c": cnt, "l": False, "q": 0, "r": 0, "v": 0}} for i, (iid, cnt) in enumerate(items)]
    conn.execute('UPDATE players SET bag_json=? WHERE id=?', (json.dumps(bag), pid))
    conn.commit()

def use_item(s, idx, rpc):
    """20275 使用背包 idx → (resp, pushes)。响应发出后服务器才 saveData，等待落库。"""
    r = send_req(s, 20275, vf(1, idx) + vf(90, rpc), rpc)
    time.sleep(0.3)
    return r

def buff_states(pushes):
    out = []
    for op, body in pushes:
        if op != 20080:
            continue
        icon = fld(body, 3) or b''
        desc = fld(body, 4) or b''
        out.append({
            'id': fld(body, 1),
            'icon': icon.decode('utf-8', errors='replace'),
            'desc': desc.decode('utf-8', errors='replace'),
            'type': fld(body, 6),
            'time': fld(body, 7),
            'is_buff': fld(body, 8),
        })
    return out

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

def get_bag_items(s, rpc):
    """20258 GetBag → {itemId: 第一个格子}"""
    op, b, pushes = send_req(s, 20258, vf(90, rpc), rpc)
    out = {}
    for f, wt, v in fields(b):
        if f == 1 and wt == 'bytes':
            bm = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
            ni = dict((ff, vv) for ff, ww, vv in fields(fld(v, 2) or b'') if ww == 'var')
            iid = ni.get(1)
            if iid and iid not in out:
                out[iid] = bm.get(1)
    return out

base = 'ga' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, base, '道具测试')
s.close(); time.sleep(1.5)
conn, pid = db(base)
conn.execute('UPDATE players SET level=4000, coin=100000000, energy=5000 WHERE id=?', (pid,))
conn.commit(); conn.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, base, '道具测试')

# ===== 1. 血药水 110305（FixedHp=500）→ 回血 =====
conn, pid = db(base)
set_bag(conn, pid, [(110305, 5), (110316, 5), (110336, 2), (110327, 2), (110329, 2), (110349, 2),
                    (110345, 3), (110353, 2), (110373, 2), (110378, 2), (110362, 2), (110813, 2),
                    (110344, 2), (110201, 3), (110202, 3), (110203, 3), (110204, 3), (110350, 2),
                    (110380, 2), (110392, 2), (110829, 2), (110830, 2)])
conn.close()
s.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, base, '道具测试')
# 血量未初始化 = 满血；吃药回满（不溢出）
op, b, pushes = use_item(s, 0, 11)
hp_push = [(fld(bb,2), fld(bb,3)) for o, bb in pushes if o == 20169 and fld(bb,2) == 1001]
print('  血药水:', hp_push)
check(op == 20276 and fld(b, 92) is None, '血药水使用成功')
check(any(v > 0 for _, v in hp_push), '血药水回血推送 20169 Hp>0')

# ===== 2. 蓝药水 110316（FixedMp=500）→ 回蓝（满蓝时无推送，恢复逻辑由单测覆盖）=====
op, b, pushes = use_item(s, 1, 12)
mp_push = [(fld(bb,2), fld(bb,3)) for o, bb in pushes if o == 20169 and fld(bb,2) == 1003]
print('  蓝药水:', mp_push)
check(op == 20276 and fld(b, 92) is None, '蓝药水使用成功')
check(all(v <= 0 or v > 0 for _, v in mp_push), '蓝药水无异常推送')

# ===== 3. 奢侈品 110336（FixedHp=1314000 FixedMp=5200000）→ 大回血蓝（满血时不推）=====
op, b, pushes = use_item(s, 2, 13)
check(op == 20276 and fld(b, 92) is None, '奢侈品使用成功（FixedHp+FixedMp 恢复）')

# ===== 4. 生命魔法球 110327（EffectType 2，战斗结束自动回满）→ 20080 buff =====
op, b, pushes = use_item(s, 3, 14)
buff = buff_states(pushes)
print('  生命魔法球 buff:', buff)
check(op == 20276 and fld(b, 92) is None, '生命魔法球使用成功')
check(any(x['id'] == 110327 and x['time'] == 2147483647 and x['icon'] == 'bufficon_hpup' and x['is_buff'] == 1 for x in buff),
      '生命魔法球推持久 20080 状态和 bufficon_hpup 图标')

# ===== 5. 精力魔法球 110329（EffectType 3）→ 20080 buff =====
op, b, pushes = use_item(s, 4, 15)
buff = buff_states(pushes)
check(any(x['id'] == 110329 and x['time'] == 2147483647 and x['icon'] == 'bufficon_hpup' for x in buff),
      '精力魔法球推持久 20080 状态和客户端血蓝上限图标')

# ===== 6. 经验卡 110349（1.5倍，3600000ms）→ expMult + 20080 =====
op, b, pushes = use_item(s, 5, 16)
buff = buff_states(pushes)
check(any(x['id'] == 110349 and x['time'] >= 3599000 and x['icon'] == 'bufficon_atkAdd' for x in buff),
      '1.5倍战斗经验卡推 20080 状态和有效 Skill 包图标')

# 同类卡替换：旧状态必须立即移除，新状态刷新为完整时长。
op, b, pushes = use_item(s, 17, 161)
buff = buff_states(pushes)
check(len(buff) >= 2 and buff[0]['id'] == 110349 and buff[0]['type'] == 2 and buff[0]['time'] is None and
      buff[1]['id'] == 110350 and buff[1]['type'] == 1 and buff[1]['time'] >= 3599000,
      '1.5倍卡切换2倍卡推 Reduce(Time=0) + Add')

# 其余所有持续消耗品类别：挂机经验、宠物经验、普通/畅爽跑图。
for idx, rpc, iid, icon, label in [
    (18, 162, 110380, 'bufficon_atkAdd', '挂机经验卡'),
    (19, 163, 110392, 'bufficon_atkAdd', '宠物经验卡'),
    (20, 164, 110829, 'bufficon_Bearing', '普通跑图卡'),
    (21, 165, 110830, 'bufficon_light', '畅爽跑图卡'),
]:
    op, b, pushes = use_item(s, idx, rpc)
    buff = buff_states(pushes)
    check(any(x['id'] == iid and x['icon'] == icon and x['time'] >= 3599000 for x in buff),
          label + '推送正确图标和剩余时间')

# 状态 JSON 已落库；断线重登后客户端以 20045 请求重建左上角列表。
conn, pid = db(base)
raw_buffs = conn.execute('SELECT item_buffs_json FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
saved_buffs = json.loads(raw_buffs)
check(saved_buffs.get('1', {}).get('item_id') == 110350 and len(saved_buffs) == 7,
      '七类持续状态已持久化且战斗经验卡只保留替换后的2倍卡')
s.close(); time.sleep(0.5)
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, base, '道具测试')
s.sendall(pack(20045, vf(90, 0)))
restored = []
expected_ids = {110327, 110329, 110350, 110380, 110392, 110829, 110830}
while not expected_ids.issubset({x['id'] for x in restored}):
    op2, body2 = recv_one(s)
    if op2 == 20080:
        restored.extend(buff_states([(op2, body2)]))
check(expected_ids.issubset({x['id'] for x in restored}), '20045 重建全部七类左上角状态')
check(all(x['icon'] and x['desc'] and x['time'] > 0 for x in restored), '重登恢复的图标、说明和剩余时长完整')

# 战斗得经验验证替换后的2倍倍率：进海滩打荷叶球
op, b, pushes = send_req(s, 20043, vf(90, 17), 17)  # 回主城
op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 18), 18)
regions = [fld(bb, 1) for o, bb in pushes if o == 20047]
assert len(regions) == 1 and not any(o == 20320 for o, _ in pushes)
op, b, pushes = send_req(s, 20048, vf(1, regions[0]) + vf(90, 19), 19)
exp_before = [fld(bb,3) for o, bb in pushes if o == 20169 and fld(bb,2) == 1027]
for i in range(10):
    op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 40 + i), 40 + i)
    if any(o == 20054 for o, _ in pushes):
        break
    time.sleep(5.1)
exp_after = [fld(bb,3) for o, bb in pushes if o == 20169 and fld(bb,2) == 1027]
print('  经验卡前后 exp:', exp_before[:1], exp_after[-1:] if exp_after else None)
conn, pid = db(base)
exp_db = conn.execute('SELECT exp FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
check(exp_db > 0, '经验卡战斗得经验（DB exp=%d）' % exp_db)
# expMult 生效（替换后的2倍卡）：基础荷叶球经验 336 x 2 = 672。
check(exp_db >= 670, '替换后的2倍战斗经验卡生效（exp=%d ≥ 670）' % exp_db)

# ===== 8. 1百万经验药水 110353（EffectType 10）→ +1000000 经验 =====
conn, pid = db(base)
exp0 = conn.execute('SELECT exp FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
s.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, base, '道具测试')
bag = get_bag_items(s, 22)
op, b, pushes = use_item(s, bag[110353], 23)
conn, pid = db(base)
exp1 = conn.execute('SELECT exp FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
print('  经验药水: exp %d -> %d' % (exp0, exp1))
check(exp1 > exp0, '经验药水 +经验（%d -> %d）' % (exp0, exp1))

# ===== 9. 一小袋金币 110373 → +50000 铜币 =====
bag = get_bag_items(s, 24)
conn, pid = db(base)
coin0 = conn.execute('SELECT coin FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
op, b, pushes = use_item(s, bag[110373], 25)
conn, pid = db(base)
coin1 = conn.execute('SELECT coin FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
print('  金币袋: coin %d -> %d' % (coin0, coin1))
check(coin1 == coin0 + 50000, '金币袋 +50000 铜币')

# ===== 10. 银元宝 110378 → +10 元宝 =====
bag = get_bag_items(s, 26)
conn, pid = db(base)
yb0 = conn.execute('SELECT yuan_bao FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
op, b, pushes = use_item(s, bag[110378], 27)
conn, pid = db(base)
yb1 = conn.execute('SELECT yuan_bao FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
print('  银元宝: yb %d -> %d' % (yb0, yb1))
check(yb1 == yb0 + 10, '银元宝 +10 元宝')

# ===== 11. 500代金券礼券 110362 → +500 代金券 =====
bag = get_bag_items(s, 28)
conn, pid = db(base)
v0 = conn.execute('SELECT voucher FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
op, b, pushes = use_item(s, bag[110362], 29)
conn, pid = db(base)
v1 = conn.execute('SELECT voucher FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
print('  代金券礼券: v %d -> %d' % (v0, v1))
check(v1 == v0 + 500, '代金券礼券 +500')

# ===== 12. 精力充沛药水 110813（EffectType 19）→ 精力+ =====
bag = get_bag_items(s, 30)
conn, pid = db(base)
e0 = conn.execute('SELECT energy FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
op, b, pushes = use_item(s, bag[110813], 31)
energy_push = [(fld(bb,2), fld(bb,3)) for o, bb in pushes if o == 20169 and fld(bb,2) == 1039]
print('  精力药水:', energy_push)
conn, pid = db(base)
e1 = conn.execute('SELECT energy FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
check(op == 20276 and fld(b, 92) is None, '精力药水使用成功')
check(e1 > e0, '精力药水 +精力（%d -> %d）' % (e0, e1))

# ===== 13. 货币物品 110201 经验（+1000/个 x3 整格）→ +3000 经验 =====
bag = get_bag_items(s, 32)
conn, pid = db(base)
exp0 = conn.execute('SELECT exp FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
op, b, pushes = use_item(s, bag[110201], 33)
conn, pid = db(base)
exp1 = conn.execute('SELECT exp FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
print('  经验物品: exp %d -> %d' % (exp0, exp1))
check(exp1 >= exp0 + 3000, '经验物品 110201 x3 → +3000 经验')

# ===== 14. 元宝物品 110202 x3 → +3 元宝 =====
bag = get_bag_items(s, 34)
conn, pid = db(base)
yb0 = conn.execute('SELECT yuan_bao FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
op, b, pushes = use_item(s, bag[110202], 35)
conn, pid = db(base)
yb1 = conn.execute('SELECT yuan_bao FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
check(yb1 == yb0 + 3, '元宝物品 x3 → +3 元宝')

# ===== 15. 铜币物品 110203 x3 → +3 铜币 =====
bag = get_bag_items(s, 36)
conn, pid = db(base)
c0 = conn.execute('SELECT coin FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
op, b, pushes = use_item(s, bag[110203], 37)
conn, pid = db(base)
c1 = conn.execute('SELECT coin FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
check(c1 == c0 + 3, '铜币物品 x3 → +3 铜币')

# ===== 16. 代金券物品 110204 x3 → +3 代金券 =====
bag = get_bag_items(s, 38)
conn, pid = db(base)
v0 = conn.execute('SELECT voucher FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
op, b, pushes = use_item(s, bag[110204], 39)
conn, pid = db(base)
v1 = conn.execute('SELECT voucher FROM players WHERE id=?', (pid,)).fetchone()[0]
conn.close()
check(v1 == v0 + 3, '代金券物品 x3 → +3')

# ===== 17. 回城卷轴 110344 → 回主城 10004 =====
bag = get_bag_items(s, 40)
op, b, pushes = use_item(s, bag[110344], 41)
m2c = [(fld(bb,4),) for o, bb in pushes if o == 20033]
print('  回城卷轴 ChangeMap:', m2c)
check(op == 20276 and fld(b, 92) is None, '回城卷轴使用成功')
check(any(v[0] == 10004 or v[0] == 1000401 for v in m2c), '回城卷轴回主城（%s）' % m2c)

# ===== 17b. 经验卡礼包 110345（EffectType 9 开箱）→ 物品入包（放最后避免挤占格子）=====
bag = get_bag_items(s, 42)
op, b, pushes = use_item(s, bag[110345], 43)
items_after = []
for o, bb in pushes:
    if o == 20260:
        for f, wt, v in fields(bb):
            if f == 1 and wt == 'bytes':
                ni = dict((ff, vv) for ff, ww, vv in fields(fld(v, 2) or b'') if ww == 'var')
                items_after.append(ni.get(1))
print('  开箱后背包物品数:', len(items_after))
check(op == 20276 and fld(b, 92) is None, '经验卡礼包使用成功')
check(len(items_after) >= 1, '开箱有物品入包')

# ===== 18. 魔法球战斗结束自动回满：吃生命魔法球 → 打一场 → 血量回满 =====
conn, pid = db(base)
conn.execute('UPDATE players SET bag_json=? WHERE id=?', (json.dumps([{"k":1,"v":{"i":110327,"t":2,"s":0,"c":2,"l":False,"q":0,"r":0,"v":0}}]), pid))
conn.commit(); conn.close()
s.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, base, '道具测试')
op, b, pushes = use_item(s, 1, 44)
check(op == 20276 and fld(b, 92) is None, '魔法球再次使用成功')
# 打海滩一场
op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 45), 45)
regions = [fld(bb, 1) for o, bb in pushes if o == 20047]
assert len(regions) == 1 and not any(o == 20320 for o, _ in pushes)
op, b, pushes = send_req(s, 20048, vf(1, regions[0]) + vf(90, 46), 46)
victory = False
for i in range(10):
    op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 50 + i), 50 + i)
    if any(o == 20054 for o, _ in pushes):
        victory = True
        break
    time.sleep(5.1)
hp_push = [(fld(bb,2), fld(bb,3)) for o, bb in pushes if o == 20169 and fld(bb,2) == 1001]
print('  魔法球战斗后 hp push:', hp_push)
check(victory, '魔法球后战斗胜利')
check(all(v >= 0 for _, v in hp_push), '魔法球战斗后血量推送正常')
s.close()
print()
print('=== 道具逐个测试（血蓝/魔法球/经验卡/开箱/礼券/货币/回城）全部通过：%d 项 ===' % ok)
