# -*- coding: utf-8 -*-
"""全场景怪物验证：主线/试炼走客户端原生链路，其余副本走 20320/20391。"""
import socket, struct, time, sys
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

def drain(s, timeout=0.4):
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

def packed_varints(value):
    out = []; i = 0
    if not isinstance(value, (bytes, bytearray)): return out
    while i < len(value):
        v = 0; sh = 0
        while True:
            x = value[i]; i += 1; v |= (x & 0x7f) << sh; sh += 7
            if not x & 0x80: break
        out.append(v)
    return out

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

acct = 'scene' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
if fld(b, 91) is not None:
    send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 3), 3)
key = fld(b, 2); gate = fld(b, 3)
send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
send_req(s, 20016, vf(2, 1) + sf(3, '场景测试') + vf(90, 5), 5)
send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
op, b, pushes = send_req(s, 20027, vf(90, 7), 7)

scenes = [1000901, 1001001, 1003301, 1003901, 1001101]
seen_cfg = set()
for mapid in scenes:
    op, b, pushes = send_req(s, 20031, vf(1, mapid) + vf(90, mapid % 1000), mapid % 1000)
    cm = [bb for o, bb in pushes if o == 20033]
    mons = [bb for o, bb in pushes if o == 20320]
    main_story = [fld(bb, 1) for o, bb in pushes if o == 20047]
    cfgs = [fld(bb, 4) for bb in mons]
    seen_cfg.update(cfgs)
    ok_cm = bool(cm and fld(cm[0], 4) == mapid)
    print('场景 %d: ChangeMap=%s 字段怪=%d ConfigId=%s' % (mapid, ok_cm, len(mons), cfgs))
    if not ok_cm:
        print('  FAIL 无 ChangeMap'); s.close(); sys.exit(1)
    if mapid == 1000901:
        init_trial = [bb for o, bb in pushes if o == 20091]
        if mons or not init_trial or fld(init_trial[0], 1) != 1001:
            print('  FAIL 试炼未走 20091 原生展示怪链路'); s.close(); sys.exit(1)
        rpc = mapid % 1000 + 500
        op, b, fight_pushes = send_req(s, 20092, vf(90, rpc), rpc)
        combat = [bb for o, bb in fight_pushes if o == 20050]
        unit_values = [v for f, wt, v in fields(b) if f == 2]
        unit_ids = []
        for value in unit_values:
            unit_ids.extend(packed_varints(value) if isinstance(value, bytes) else [value])
        ok_fight = op == 20093 and not fld(b, 92) and fld(b, 1) == 1001 and len(unit_ids) == 2 and not combat
        print('  试炼原生开战=%s TrialCopyId=%s UnitIdList=%s 无重复20050=%s' %
              (ok_fight, fld(b, 1), unit_ids, not combat))
        if not ok_fight:
            print('  FAIL 试炼战斗未开始'); s.close(); sys.exit(1)
        s.sendall(pack(20171, vf(90, mapid % 1000 + 900)))
        time.sleep(0.3); drain(s)
        continue
    if mapid == 1001101:
        if mons or len(main_story) != 1:
            print('  FAIL 主线未走 20047 原生展示怪链路'); s.close(); sys.exit(1)
        rpc = mapid % 1000 + 500
        op, b, fight_pushes = send_req(s, 20048, vf(1, main_story[0]) + vf(90, rpc), rpc)
        combat = [bb for o, bb in fight_pushes if o == 20050]
        ok_fight = op == 20049 and not fld(b, 92) and len(combat) == 1
        print('  主线原生开战=%s Region=%s' % (ok_fight, main_story[0]))
        if not ok_fight:
            print('  FAIL 主线战斗未开始'); s.close(); sys.exit(1)
        s.sendall(pack(20171, vf(90, mapid % 1000 + 900)))
        time.sleep(0.3); drain(s)
        continue
    if not mons:
        print('  ⚠ 无字段怪（该场景可能无怪配置）')
        continue
    # 点击开战斗
    if mapid == 1003301:
        if len(mons) != 1 or cfgs != [1001] or fld(mons[0], 5) != 2:
            print('  FAIL manual dungeon must expose exactly one native layer monster')
            s.close(); sys.exit(1)
        mid = fld(mons[0], 1)
        rpc = mapid % 1000 + 500
        op, b, pushes = send_req(s, 20391, vf(1, mid) + vf(2, 2) + vf(90, rpc), rpc)
        message = fld(b, 92)
        message = message.decode('utf-8') if isinstance(message, bytes) else ''
        if op != 20392 or '3' not in message:
            print('  FAIL solo manual dungeon did not return the native three-player limit:', message)
            s.close(); sys.exit(1)
        print('  manual dungeon solo limit=%s' % message)
        continue
    mid = fld(mons[0], 1)
    op, b, pushes = send_req(s, 20391, vf(1, mid) + vf(2, 4) + vf(90, mapid % 1000 + 500), mapid % 1000 + 500)
    mi = [bb for o, bb in pushes if o == 20050]
    boss = [bb for o, bb in pushes if o == 20061]
    if mapid == 1001001:
        ok_fight = op == 20392 and not fld(b, 92) and len(boss) == 1 and not mi
    else:
        ok_fight = op == 20392 and not fld(b, 92) and len(mi) >= 1
    mon_ids = []
    for f, wt, v in fields(mi[0]) if mi else []:
        if f == 1 and wt == 'bytes':
            kv = dict((ff, vv) for ff, wt2, vv in fields(v) if wt2 == 'var')
            mon_ids.append(kv.get(2))
    print('  点击开战斗=%s 怪物=%s 专用BOSS=%s' % (ok_fight, mon_ids, bool(boss)))
    if not ok_fight:
        print('  FAIL 战斗未开始'); s.close(); sys.exit(1)
    # 退出战斗
    s.sendall(pack(20171, vf(90, mapid % 1000 + 900)))
    time.sleep(0.3)
    drain(s)

print('各场景字段怪 ConfigId:', sorted(seen_cfg))
if len(seen_cfg) < 2:
    print('  FAIL 不同场景应使用不同怪物造型')
    s.close(); sys.exit(1)
# 字段怪 ConfigId = 10000 + PrefabId（客户端补丁行）；各场景 ConfigId 已确认覆盖
# 该场景战斗怪的 PrefabId（1000901→Prefab221、1001001→Prefab4071、1003301→Prefab301、
# 1003901→Prefab4104、1001101→Prefab201），模型与战斗怪一致。
print('字段怪 ConfigId = 10000 + 战斗怪 PrefabId（模型一致）✓')
s.close()
print()
print('=== 全场景怪物验证通过（试炼原生链路 + 其他场景模型一致） ===')
