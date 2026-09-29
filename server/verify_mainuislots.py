# -*- coding: utf-8 -*-
"""快捷栏（玩家自定义）验证：9 格固定、默认槽 0=基础攻击、
DropSkill 拖技能到槽（20229→20230 带 MainUISlotList）、DropItem 拖物品（20231→20232）、
主城按技能 Error=0 不崩。"""
import os, socket, struct, time, sys, sqlite3, json
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

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

def parse_slots(b):
    out = []
    for f, wt, v in fields(b):
        if f == 1 and wt == 'bytes':
            kv = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
            # Index=5, MainUIType=2, Id=1, Level=3, ItemCount=4
            out.append((kv.get(5) or 0, kv.get(2) or 0, kv.get(1) or 0, kv.get(4) or 0, kv.get(3) or 0))
    return sorted(out)

def bag_index_of(body, item_id):
    for f, wt, pair in fields(body):
        if f != 1 or wt != 'bytes':
            continue
        pair_fields = list(fields(pair))
        index = next((v for ff, ww, v in pair_fields if ff == 1 and ww == 'var'), None)
        item = next((v for ff, ww, v in pair_fields if ff == 2 and ww == 'bytes'), None)
        if item is not None and fld(item, 1) == item_id:
            return index
    return None

acct = 'bar' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '快捷栏测试')
s.close(); time.sleep(1.5)
conn = sqlite3.connect(DB_PATH)
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
conn.execute('UPDATE players SET skill_point=50, level=4000 WHERE id=?', (pid,))
bag = [{"k":1,"v":{"i":110305,"t":2,"s":0,"c":5,"l":False,"q":0,"r":0,"v":0}},
       {"k":2,"v":{"i":20217,"t":2,"s":0,"c":10,"l":False,"q":0,"r":0,"v":0}}]
conn.execute('UPDATE players SET bag_json=? WHERE id=?', (json.dumps(bag), pid))
conn.commit(); conn.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '快捷栏测试')
# 初始：9 槽，槽 0 = 基础攻击 100001
op, b, pushes = send_req(s, 20227, vf(90, 11), 11)
slots = parse_slots(b)
print('  初始槽:', slots)
check(len(slots) == 9, '9 槽固定（%d）' % len(slots))
check(slots[0][:3] == (0, 1, 100001) and slots[0][4] == 1, '槽 0 = 基础攻击 SkillSlot(100001) Lv1: %s' % (slots[0],))
check(all(sl[1] == 0 and sl[2] == 0 for sl in slots[1:]), '其余槽 NoneSlot 空')
idxs = [x[0] for x in slots]
check(idxs == list(range(9)), 'Index 连续 0..8')
# 学 110101 → 拖到槽 2（DropSkill）
op, b, pushes = send_req(s, 20243, vf(1, 110101) + vf(90, 12), 12)
op, b, pushes = send_req(s, 20229, vf(1, 2) + vf(2, 110101) + vf(90, 13), 13)
slots2 = parse_slots(b)
print('  拖技能后槽:', [(x[0], x[1], x[2]) for x in slots2])
check(op == 20230 and fld(b, 92) is None, 'DropSkill 20230 成功（msg 空）')
check(slots2[2][1] == 1 and slots2[2][2] == 110101, '槽 2 = SkillSlot(110101)')
check(len(slots2) == 9, '仍 9 槽不越界')
# 拖物品到槽 8（DropItem）；学习技能已消耗书，背包索引可能被规范化。
op, bag_body, _ = send_req(s, 20258, vf(90, 135), 135)
goods_index = bag_index_of(bag_body, 110305)
check(goods_index is not None, 'medicine slot resolved from GetBag')
op, b, pushes = send_req(s, 20231, vf(1, 8) + vf(2, goods_index) + vf(90, 14), 14)
slots3 = parse_slots(b)
print('  拖物品后槽:', [(x[0], x[1], x[2], x[3]) for x in slots3])
check(op == 20232 and slots3[8][1] == 2 and slots3[8][2] == 110305, '槽 8 = ItemSlot(110305)')
check(slots3[8][3] == 5, '物品槽数量 = 5')
# 主城按技能（槽 2）→ 20234 Error=0 不崩
op, b, pushes = send_req(s, 20233, vf(1, 2) + vf(90, 15), 15)
print('  主城按技能 resp:', op, 'err:', fld(b, 92))
check(op == 20234 and fld(b, 92) in (None, b''), '主城按技能 Error=0（不崩）')
# 学 9+ 技能 → 槽不变（固定 9，无越界）
for i, sid in enumerate((110201, 110202, 110301, 110302, 110303, 110304, 110401, 110402)):
    op, b, pushes = send_req(s, 20243, vf(1, sid) + vf(90, 20+i), 20+i)
op, b, pushes = send_req(s, 20227, vf(90, 30), 30)
slots4 = parse_slots(b)
print('  学 9 技能后槽:', [(x[0], x[1], x[2]) for x in slots4])
check(len(slots4) == 9 and [x[0] for x in slots4] == list(range(9)), '学满仍 9 槽 Index 0..8（不越界）')
s.close()
print()
print('=== 快捷栏自定义回归全部通过：%d 项 ===' % ok)
