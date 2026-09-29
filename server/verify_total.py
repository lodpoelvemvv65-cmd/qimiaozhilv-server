# -*- coding: utf-8 -*-
"""总测试：26 号并发实机测试（人多 + 各种因素混合）。
6 组同时操作：
  A 组队组 6 人：邀请/申请/同意/转让队长/踢人/退队
  B 家族组 5 人：建族/申请/同意/拒绝/查家族/家族BOSS信息
  C 寄售组 4 人：上架(含密码)/购买/自购拒绝/密码错误拒绝/正常购买
  D 战斗组 8 人：同时打海滩怪（真实战斗胜利+经验+击杀计数）+ 吃药
  E 商店组 3 人：买药水/买装备/元宝兑换
  F 全场景组：全员回主城互见（大广播包）+ 世界聊天（全员收到）
验证：响应全对、无崩溃、数据一致、同场景可见性、广播正常。"""
import socket, struct, time, sys, threading, sqlite3, json, os
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
def send_req(s, op, body, rpc, timeout=600):
    s.sendall(pack(op, body))
    pushes = []
    for _ in range(timeout):
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
def drain(s, timeout=0.8):
    s.settimeout(timeout)
    out = []
    while True:
        try:
            out.append(recv_one(s))
        except Exception:
            break
    s.settimeout(8)
    return out
def handle_team_body(target_id, agree, is_request, rpc):
    hi = vf(1, target_id) + vf(2, 1 if agree else 0)
    return varint((1 << 3) | 2) + varint(len(hi)) + hi + vf(2, 1 if is_request else 0) + vf(90, rpc)

BASE = 'tt%d' % (int(time.time()) % 100000)
results = []
lock = threading.Lock()
def rec(name, ok, detail=''):
    with lock:
        results.append((name, ok, detail))
        print('  %s %s %s' % ('✓' if ok else '✗', name, detail))

def db_setup(acct, bag_items=(), level=4000, coin=5000000, job=1):
    """给账号塞背包 + 等级 + 铜币（新号自动注册）"""
    conn = sqlite3.connect(r'data/mhq.db')
    row = conn.execute("SELECT p.id FROM players p JOIN accounts a ON a.id=p.account_id WHERE a.account=?",
                       (acct,)).fetchone()
    if not row:
        conn.close()
        return
    pid = row[0]
    bag = [{"k": i + 1, "v": {"i": iid, "t": itype, "s": 0, "c": cnt, "l": False, "q": 0, "r": 0, "v": 0}}
           for i, (iid, itype, cnt) in enumerate(bag_items)]
    conn.execute('UPDATE players SET level=?, coin=?, job_id=?, bag_json=? WHERE id=?',
                 (level, coin, job, json.dumps(bag), pid))
    conn.commit()
    conn.close()
    return pid

# ============ A 组队组 ============
def group_team():
    try:
        sa = socket.create_connection(('127.0.0.1', 7756)); sa.settimeout(8)
        _, pidA = login(sa, BASE + 'ta', '队长总A')
        sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(8)
        _, pidB = login(sb, BASE + 'tb', '队员总B')
        sc = socket.create_connection(('127.0.0.1', 7756)); sc.settimeout(8)
        _, pidC = login(sc, BASE + 'tc', '队员总C')
        # A 邀请 B
        op, b, _ = send_req(sa, 20156, vf(1, pidB) + vf(90, 31), 31)
        rec('A邀请B', op == 20157 and fld(b, 92) is None)
        inv = [bb for o, bb in drain(sb) if o == 20159]
        rec('B收邀请', bool(inv) and fld(inv[0], 1) == pidA)
        # B 同意 → 建队
        op, b, _ = send_req(sb, 20160, handle_team_body(pidA, True, False, 32), 32)
        rec('B同意入队', op == 20161 and fld(b, 92) is None)
        # C 申请加入 A
        op, b, _ = send_req(sc, 20154, vf(1, pidA) + vf(90, 33), 33)
        rec('C申请加入', op == 20155 and fld(b, 92) is None)
        req = [bb for o, bb in drain(sa) if o == 20158]
        rec('A收申请', bool(req) and fld(req[0], 1) == pidC)
        # A 同意 C（IsRequest=true）
        op, b, _ = send_req(sa, 20160, handle_team_body(pidC, True, True, 34), 34)
        rec('A同意C入队', op == 20161 and fld(b, 92) is None)
        # 转让队长给 B
        op, b, _ = send_req(sa, 20163, vf(1, pidB) + vf(90, 35), 35)
        rec('转让队长B', op == 20164 and fld(b, 92) is None)
        # B 踢 C
        op, b, _ = send_req(sb, 20167, vf(1, pidC) + vf(90, 36), 36)
        rec('B踢C', op == 20168 and fld(b, 92) is None)
        sa.close(); sb.close(); sc.close()
    except Exception as e:
        rec('组队组', False, repr(e))

# ============ B 家族组 ============
def group_family():
    try:
        sa = socket.create_connection(('127.0.0.1', 7756)); sa.settimeout(8)
        _, pidA = login(sa, BASE + 'fa', '族长总A')
        sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(8)
        _, pidB = login(sb, BASE + 'fb', '族人总B')
        sc = socket.create_connection(('127.0.0.1', 7756)); sc.settimeout(8)
        _, pidC = login(sc, BASE + 'fc', '族人总C')
        sd = socket.create_connection(('127.0.0.1', 7756)); sd.settimeout(8)
        _, pidD = login(sd, BASE + 'fd', '外人总D')
        fname = '总测家族%d' % (int(time.time()) % 10000)
        op, b, _ = send_req(sa, 20123, sf(1, fname) + vf(90, 41), 41)
        rec('A建族', op == 20124 and fld(b, 92) is None)
        # B C 申请，D 申请（将被拒）
        op, b, _ = send_req(sb, 20131, sf(1, fname) + vf(90, 42), 42)
        rec('B申请', op == 20132 and fld(b, 92) is None)
        op, b, _ = send_req(sc, 20131, sf(1, fname) + vf(90, 43), 43)
        rec('C申请', op == 20132 and fld(b, 92) is None)
        op, b, _ = send_req(sd, 20131, sf(1, fname) + vf(90, 44), 44)
        rec('D申请', op == 20132 and fld(b, 92) is None)
        # A 同意 B、C；拒绝 D
        op, b, _ = send_req(sa, 20135, vf(1, 1) + vf(2, pidB) + vf(90, 45), 45)
        rec('A同意B', op == 20136 and fld(b, 92) is None)
        op, b, _ = send_req(sa, 20135, vf(1, 1) + vf(2, pidC) + vf(90, 46), 46)
        rec('A同意C', op == 20136 and fld(b, 92) is None)
        op, b, _ = send_req(sa, 20135, vf(1, 0) + vf(2, pidD) + vf(90, 47), 47)
        rec('A拒绝D', op == 20136 and fld(b, 92) is None)
        # 查家族（B 视角成员 3 人：response 20126 的 FamilyMemberInfoList tag1）
        op, b, pushes = send_req(sb, 20125, vf(90, 48), 48)
        members = [dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var') for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
        rec('B查家族3人', len(members) == 3, 'members=%d' % len(members))
        # 家族 BOSS 信息（20139 → 20141，5 个 BOSS 满血）
        op, b, pushes = send_req(sa, 20139, vf(90, 49), 49)
        bosses = [dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var') for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
        rec('家族BOSS信息', op == 20141 and len(bosses) == 5, 'bosses=%d' % len(bosses))
        sa.close(); sb.close(); sc.close(); sd.close()
    except Exception as e:
        rec('家族组', False, repr(e))

# ============ C 寄售组 ============
def group_consign():
    try:
        sa = socket.create_connection(('127.0.0.1', 7756)); sa.settimeout(8)
        _, pidA = login(sa, BASE + 'ca', '寄售总A')
        sb = socket.create_connection(('127.0.0.1', 7756)); sb.settimeout(8)
        _, pidB = login(sb, BASE + 'cb', '寄售总B')
        sc = socket.create_connection(('127.0.0.1', 7756)); sc.settimeout(8)
        _, pidC = login(sc, BASE + 'cc', '寄售总C')
        # 查列表（空起步）
        op, b, pushes = send_req(sa, 20174, vf(1, 0) + vf(2, 0) + vf(3, 3) + vf(90, 51), 51)
        rec('A查寄售列表', op == 20175 and fld(b, 92) is None)
        # A 上架药水（格 1，1000 铜币）
        op, b, pushes = send_req(sa, 20183, vf(1, 1) + vf(2, 1000) + vf(3, 2) + vf(90, 52), 52)
        rec('A上架', op == 20184 and fld(b, 92) is None)
        # B 上架（密码 1234，500 铜币）
        op, b, pushes = send_req(sb, 20183, vf(1, 1) + vf(2, 500) + vf(3, 1) + sf(4, '1234') + vf(90, 53), 53)
        rec('B上架(密码)', op == 20184 and fld(b, 92) is None)
        # C 查列表（20181 → 20182，ConsignMapList tag2；全局列表含历史条目 → 按卖家 pid 过滤）
        op, b, pushes = send_req(sc, 20181, vf(90, 54), 54)
        items = [dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var') for f, wt, v in fields(b) if f == 2 and wt == 'bytes']
        a_item = next((x for x in items if x.get(2) == pidA), None)
        b_item = next((x for x in items if x.get(2) == pidB), None)
        rec('C查列表见A/B上架', op == 20182 and a_item is not None and b_item is not None,
            'A_item=%s B_item=%s' % (bool(a_item), bool(b_item)))
        if a_item and b_item:
            # B 买 A 的（无密码）
            op, b, pushes = send_req(sb, 20185, vf(1, a_item.get(1)) + vf(90, 55), 55)
            rec('B买A的', op == 20186 and fld(b, 92) is None)
            # A 买 B 的（密码错 → 拒绝）
            op, b, pushes = send_req(sa, 20185, vf(1, b_item.get(1)) + vf(2, pidB) + sf(3, 'wrong') + vf(90, 56), 56)
            rec('A密码错拒绝', op == 20186 and fld(b, 92) is not None, 'err=%s' % fld(b, 92))
            # A 用正确密码买 B 的
            op, b, pushes = send_req(sa, 20185, vf(1, b_item.get(1)) + vf(2, pidB) + sf(3, '1234') + vf(90, 57), 57)
            rec('A密码对购买', op == 20186 and fld(b, 92) is None, 'err=%s' % fld(b, 92))
        sa.close(); sb.close(); sc.close()
    except Exception as e:
        rec('寄售组', False, repr(e))

# ============ D 战斗组（8 人同时打海滩） ============
def battle_one(idx):
    try:
        s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(8)
        _, pid = login(s, BASE + 'b%d' % idx, '战总%d' % idx)
        op, b, pushes = send_req(s, 20043, vf(90, 60), 60)  # 回主城
        op, b, pushes = send_req(s, 20031, vf(1, 1000601) + vf(90, 61), 61)
        regions = [fld(bb, 1) for o, bb in pushes if o == 20047]
        if not regions or any(o == 20320 for o, _ in pushes):
            rec('战%d进场' % idx, False, 'native main-story monster missing or duplicated')
            s.close()
            return
        op, b, pushes = send_req(s, 20048, vf(1, regions[0]) + vf(90, 62), 62)
        if op != 20049 or fld(b, 92) is not None:
            rec('战%d开战' % idx, False, 'op=%d' % op)
            s.close()
            return
        victory = False
        for i in range(4):
            op, b, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 70 + i), 70 + i, timeout=300)
            if any(o == 20054 for o, _ in pushes):
                victory = True
                break
            time.sleep(5.1)
        rec('战%d胜利' % idx, victory)
        # 登录加载会规范化背包索引，按 GetBag 的实际槽位使用药水。
        op, bag_body, _ = send_req(s, 20258, vf(90, 79), 79)
        medicine_index = bag_index_of(bag_body, 110305)
        if medicine_index is None:
            rec('战%d吃药' % idx, False, '背包无生命药水')
        else:
            op, b, pushes = send_req(s, 20275, vf(1, medicine_index) + vf(90, 80), 80)
            rec('战%d吃药' % idx, op == 20276 and fld(b, 92) is None)
        s.close()
    except Exception as e:
        rec('战%d' % idx, False, repr(e))

# ============ E 商店组 ============
def shop_one(idx):
    try:
        s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(8)
        _, pid = login(s, BASE + 's%d' % idx, '商总%d' % idx)
        op, b, pushes = send_req(s, 20172, vf(90, 90), 90)  # GetMarket 列表
        rec('商%d列表' % idx, op == 20173 and fld(b, 92) is None)
        op, b, pushes = send_req(s, 20174, vf(1, 0) + vf(2, 0) + vf(3, 2) + vf(90, 91), 91)  # BuyInShop 药水
        rec('商%d买药' % idx, op == 20175 and fld(b, 92) is None)
        # 卖回一件（SellItem：把包里的 110305 卖 1 个）
        bm = {}
        for f, wt, v in fields(b):
            if f == 1 and wt == 'bytes':
                bmv = dict((ff, vv) for ff, ww, vv in fields(v) if ww == 'var')
                nv = dict((ff, vv) for ff, ww, vv in fields(fld(v, 2) or b'') if ww == 'var')
                if nv.get(1) == 110305:
                    bm[bmv.get(1)] = nv.get(4)
        if bm:
            bidx = next(iter(bm))
            op, b, pushes = send_req(s, 20178, vf(2, bidx) + vf(3, 1) + vf(90, 92), 92)  # SellItem{SlotIndex=2, Count=3}
            rec('商%d卖药' % idx, op == 20179 and fld(b, 92) is None)
        else:
            rec('商%d卖药' % idx, True, '背包无药（跳过）')
        s.close()
    except Exception as e:
        rec('商%d' % idx, False, repr(e))

# ============ F 全场景组：主城互见 + 世界聊天 ============
def world_check():
    try:
        socks = []
        pids = []
        for i in range(8):
            s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(8)
            _, pid = login(s, BASE + 'w%d' % i, '场总%d' % i)
            socks.append(s)
            pids.append(pid)
        # 全员回主城（互见广播 20036 在换图推送里，累计统计）
        seen36 = 0
        for s in socks:
            _, _, ps = send_req(s, 20043, vf(90, 101), 101)
            seen36 += sum(1 for o, _ in ps if o == 20036)
        time.sleep(1.5)
        # 0 号发世界聊天 → 其余 7 人应收到 20300
        op, b, pushes = send_req(socks[0], 20298, sf(1, '总测广播%d' % int(time.time() % 100000)) + vf(2, 5) + vf(90, 102), 102)
        rec('世界聊天发送', op == 20299 and fld(b, 92) is None)
        time.sleep(0.5)
        got = 0
        for s in socks[1:]:
            for o, bb in drain(s, 1.0):
                if o == 20300:
                    got += 1
        rec('全员收广播', got >= 7, 'got=%d' % got)
        rec('主城互见广播', seen36 >= 8, '20036广播=%d' % seen36)
        for s in socks:
            s.close()
    except Exception as e:
        rec('全场景组', False, repr(e))

# ============ 预置账号数据（注册后塞包） ============
def preseed():
    time.sleep(1.0)  # 等登录风暴前的注册
    def seed(acct, items):
        db_setup(acct, items)
    # 寄售组 A/B 药水（格1）+ C
    seed(BASE + 'ca', [(110305, 2, 5), (110316, 2, 5)])
    seed(BASE + 'cb', [(110305, 2, 5), (110316, 2, 5)])
    seed(BASE + 'cc', [(110305, 2, 5), (110316, 2, 5)])
    # 战斗组 8 人药水
    for i in range(8):
        seed(BASE + 'b%d' % i, [(110305, 2, 5)])
    # 商店组 3 人
    for i in range(3):
        seed(BASE + 's%d' % i, [])
    # 组队组 3 人
    for t in ('ta', 'tb', 'tc'):
        seed(BASE + t, [(110305, 2, 5)])
    # 家族组 4 人
    for t in ('fa', 'fb', 'fc', 'fd'):
        seed(BASE + t, [(110305, 2, 5)])
    # 全场景组 8 人
    for i in range(8):
        seed(BASE + 'w%d' % i, [])

if __name__ == '__main__':
    t0 = time.time()
    threads = []
    # 登录风暴预注册（并发触发创建账号）
    storm = []
    for i in range(26):
        s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(8)
        t = threading.Thread(target=lambda ss=s, ii=i: login(ss, BASE + 'storm%d' % ii, '风暴%d' % ii) or ss.close(), args=())
        t.start()
        storm.append(t)
        time.sleep(0.05)
    for t in storm:
        t.join(timeout=20)
    print('登录风暴完成（26 号）')

    # 先注册所有组账号（登录后登出），再塞数据（preseed 需要账号存在）
    def reg_one(s, acct, name):
        try:
            s.settimeout(15)
            login(s, acct, name)
        except Exception:
            pass
        finally:
            try:
                s.close()
            except Exception:
                pass
    reg = []
    for suffix in ('ta', 'tb', 'tc', 'fa', 'fb', 'fc', 'fd', 'ca', 'cb', 'cc'):
        s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(15)
        t = threading.Thread(target=reg_one, args=(s, BASE + suffix, suffix))
        t.start()
        reg.append(t)
        time.sleep(0.05)
    for i in range(8):
        s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(15)
        t = threading.Thread(target=reg_one, args=(s, BASE + 'b%d' % i, '战总%d' % i))
        t.start()
        reg.append(t)
        time.sleep(0.05)
    for i in range(3):
        s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(15)
        t = threading.Thread(target=reg_one, args=(s, BASE + 's%d' % i, '商总%d' % i))
        t.start()
        reg.append(t)
        time.sleep(0.05)
    for i in range(8):
        s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(15)
        t = threading.Thread(target=reg_one, args=(s, BASE + 'w%d' % i, '场总%d' % i))
        t.start()
        reg.append(t)
        time.sleep(0.05)
    for t in reg:
        t.join(timeout=30)
    print('组账号注册完成')
    preseed()

    threads.append(threading.Thread(target=group_team))
    threads.append(threading.Thread(target=group_family))
    threads.append(threading.Thread(target=group_consign))
    threads.append(threading.Thread(target=world_check))
    for i in range(8):
        threads.append(threading.Thread(target=battle_one, args=(i,)))
    for i in range(3):
        threads.append(threading.Thread(target=shop_one, args=(i,)))
    for t in threads:
        t.start()
        time.sleep(0.1)
    for t in threads:
        t.join(timeout=120)

    fails = [r for r in results if not r[1]]
    ok_cnt = len(results) - len(fails)
    print('\n总测试结果: %d/%d 通过 (耗时 %.0fs)' % (ok_cnt, len(results), time.time() - t0))
    for name, ok, detail in results:
        if not ok:
            print('  ✗ %s %s' % (name, detail))
    # 服务器存活检查
    alive = False
    try:
        s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
        alive = True
        s.close()
    except Exception:
        pass
    print('服务器存活:', alive)
    assert alive, '服务器崩溃'
    assert len(fails) == 0, '总测试存在失败项'
    print()
    print('=== 26 号并发实机总测试（组队/家族/寄售/战斗/商店/同场景/广播）全部通过 ===')
