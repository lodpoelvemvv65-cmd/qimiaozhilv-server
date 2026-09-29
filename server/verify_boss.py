# -*- coding: utf-8 -*-
"""世界 BOSS 生命周期验证：
  1. 进 10010 层 2 → 字段怪 + M2C_SendBossInfo{BossId=10256(MonsterId), UnitId}
  2. 点击 BOSS 开战 → 胜利（削弱怪物 HP 保证秒杀）→ 击杀广播：
     M2C_BossDead + M2C_BossBeDefeat + 世界聊天播报"XX 击败了 XX"
  3. 死亡后重进场景 → 字段怪不生成（BOSS 刷新时间内不出现）+ 点击提示"BOSS 已阵亡"
  4. 手动把刷新时间置为极短 → 验证 BossRefresh 广播（用 SQLite 改不了内存状态，
     改为直接调 bossSweepLoop 逻辑不可行——用测试账号验证 1-3，刷新广播用单测）。"""
import os, socket, struct, time, sys, sqlite3, json
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
    for _ in range(800):
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

acct = 'boss' + str(int(time.time()))
s = socket.create_connection((HOST, PORT)); s.settimeout(5)
login(s, acct, 'BOSS测试')
s.close(); time.sleep(1.5)
conn = sqlite3.connect(DB_PATH)
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
# 给高等级 + 高攻装备（各槽位最高 PhyAtk：Type2 创世女神/Type4 富可敌国/Type5 凋零/
# Type7 奇·世界守护者项链/Type10 转·影※生者必灭手·风）保证快速击杀 BOSS
worn = [
    {"k":2,"v":{"i":120659,"t":1,"s":0,"c":1,"l":False,"q":3,"r":0,"v":1,"k":0}},
    {"k":4,"v":{"i":120853,"t":1,"s":0,"c":1,"l":False,"q":3,"r":0,"v":1,"k":0}},
    {"k":5,"v":{"i":120842,"t":1,"s":0,"c":1,"l":False,"q":3,"r":0,"v":1,"k":0}},
    {"k":7,"v":{"i":120580,"t":1,"s":0,"c":1,"l":False,"q":3,"r":0,"v":1,"k":0}},
    {"k":10,"v":{"i":121090,"t":1,"s":0,"c":1,"l":False,"q":3,"r":0,"v":1,"k":0}},
]
conn.execute('UPDATE players SET level=4000, char_point=4001, energy=2000, worn_json=? WHERE id=?', (json.dumps(worn), pid))
conn.commit(); conn.close()
s = socket.create_connection((HOST, PORT)); s.settimeout(5)
login(s, acct, 'BOSS测试')

# 1) 进 10010 层 2（BossBase 1002 足球行者 10256）。原生 BossRefresh 负责展示；
#    SendBossInfo 只能在两阶段挑战确认后推送，否则客户端会提前进入战斗态。
op, b, pushes = send_req(s, 20031, vf(1, 1001002) + vf(90, 11), 11)
send_info = [bb for o, bb in pushes if o == 20061]
refreshes = [fld(bb, 1) for o, bb in pushes if o == 20056]
print('  进 10010-2 SendBossInfo:', [(fld(bb,1), fld(bb,2)) for bb in send_info], 'BossRefresh:', refreshes)
check(len(send_info) == 0, '进场景不推 SendBossInfo（避免锁战斗态）')
check(refreshes == [1002], '原生 BossRefresh{BossId=1002} 展示足球行者')

# 2) 客户端原生两阶段挑战：20057 获取 Key，20059 确认开战。
op, b, _ = send_req(s, 20057, vf(90, 12), 12)
key = fld(b, 2)
check(op == 20058 and key and fld(b, 92) is None, '获取 BOSS 挑战 Key 成功')
op, b, pushes = send_req(s, 20059, vf(2, key) + vf(90, 13), 13)
print('  确认 BOSS resp:', op, 'err:', fld(b, 92))
check(op == 20060 and fld(b, 92) is None, '确认 BOSS 开战成功')
# 专用 BOSS 事件会自行创建战斗单位和 HUD，不能叠加主线 20047/20050。
send_info = [bb for o, bb in pushes if o == 20061]
init47 = [bb for o, bb in pushes if o == 20047]
init50 = [bb for o, bb in pushes if o == 20050]
print('  开战 SendBossInfo:', [(fld(bb,1), fld(bb,2)) for bb in send_info], '20047:', len(init47), '20050:', len(init50))
check(len(send_info) == 1 and fld(send_info[0], 1) == 10256, '开战推 SendBossInfo{BossId=10256 MonsterId}')
boss_battle_unit_id = fld(send_info[0], 2)
check(boss_battle_unit_id is not None and boss_battle_unit_id > 0, 'SendBossInfo 带唯一战斗 UnitId')
check(len(init47) == 0 and len(init50) == 0, '世界 BOSS 不叠加 20047/20050（防止重影）')

# 3) 战斗内按技能直到胜利（普攻 CD 5000ms；BOSS 800k 血，伤害 ~12万/次）
victory = False
hp_updates = []
forbidden = []
for i in range(30):
    op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 30+i), 30+i)
    forbidden.extend(o for o, _ in pushes if o in (20053, 20079))
    hp_updates.extend((fld(bb, 2), fld(bb, 3)) for o, bb in pushes
                      if o == 20169 and fld(bb, 1) == boss_battle_unit_id)
    # Native world-BOSS settlement uses BossDead/BossBeDefeat. It does not
    # emit the main-story victory opcode 20054.
    if any(o in (20065, 20066) for o, _ in pushes):
        victory = True
        break
    time.sleep(5.1)  # 等普攻 CD（100001 CD=5000ms）
check(victory, 'BOSS 战斗胜利')
boss_hp = [0 if v is None else v for numeric_type, v in hp_updates if numeric_type == 1001]
check(len(boss_hp) >= 1 and min(boss_hp) < max([800000] + boss_hp),
      '20169 更新 SendBossInfo 的同一 UnitId，且 BOSS 血量下降')
check(not forbidden, 'BOSS 技能期间不发送 20053/20079: %s' % forbidden)

# 4) 胜利 → 全服广播 BossDead(20065) + BossBeDefeat(20066) + 世界聊天播报(20300)
dead = [bb for o, bb in pushes if o == 20065]
bedefeat = [bb for o, bb in pushes if o == 20066]
chat = [bb for o, bb in pushes if o == 20300]
print('  BossDead:', [(fld(bb,1)) for bb in dead], 'BossBeDefeat:', len(bedefeat), '聊天播报:', len(chat))
check(len(dead) == 1, '广播 BossDead（客户端按 BossComponent 移除原生展示）')
check(len(bedefeat) == 1, '广播 BossBeDefeat')
check(len(chat) == 1 and '击败' in (fld(chat[0], 1) or b'').decode('utf-8','replace'), '世界聊天播报"X 击败了 X"')
chat_content = (fld(chat[0], 1) or b'').decode('utf-8','replace')
print('  播报内容:', chat_content)
check('BOSS测试' in chat_content and '足球行者' in chat_content, '播报含玩家名 + BOSS名')

# 5) BOSS 死亡后：重进场景 → 无 BossRefresh/SendBossInfo
op, b, pushes = send_req(s, 20031, vf(1, 1000401) + vf(90, 40), 40)
op, b, pushes = send_req(s, 20031, vf(1, 1001002) + vf(90, 41), 41)
refreshes2 = [bb for o, bb in pushes if o == 20056]
send_info2 = [bb for o, bb in pushes if o == 20061]
print('  死亡后重进: BossRefresh:', len(refreshes2), 'SendBossInfo:', len(send_info2))
check(len(refreshes2) == 0 and len(send_info2) == 0, 'BOSS 刷新时间内原生展示不出现')

# 6) 死亡期间发起挑战 → 提示"BOSS 已阵亡"
op, b, pushes = send_req(s, 20057, vf(90, 42), 42)
msg = fld(b, 92)
msg_txt = msg.decode('utf-8') if isinstance(msg, bytes) else str(msg)
print('  死亡期间点击:', msg_txt)
check('\u5df2\u9635\u4ea1' in msg_txt, 'dead BOSS click returns respawn countdown (%s)' % msg_txt)

s.close()
print()
print('=== 世界 BOSS 生命周期回归全部通过：%d 项 ===' % ok)
