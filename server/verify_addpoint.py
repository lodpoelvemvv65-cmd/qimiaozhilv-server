# -*- coding: utf-8 -*-
"""属性加点回归（客户端不崩版）：
  客户端 CharacterUI b__16_0（反汇编 1060）：读取输入框数值 TryParse → C2M_AddPoint.PointList
  （行序 Str/Quk/Spi/Wim）→ Session.Call(20253)。响应 Error!=0 → RpcException → 会话销毁 → 掉线。
  因此服务器【任何失败必须返回成功（Error=0）+ 当前 Character】（客户端
  ClientUnitCharacterComponent.Update(msg.Character) 首步 get_Id，nil 必 NRE）。
  验证：零和/空 PointList/无效 Trans → 返回成功 + Character；有效加点扣点；洗点返还。"""
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
    for _ in range(500):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            return op2, b, pushes + [(op2, b)]
        pushes.append((op2, b))
    return None, None, pushes
def login(s, acct, name):
    send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
    key = fld(b, 2)
    if key is None:
        time.sleep(1)
        op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
        key = fld(b, 2)
    gate = fld(b, 3)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    send_req(s, 20016, vf(2, 1) + sf(3, name) + vf(90, 5), 5)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    send_req(s, 20027, vf(90, 7), 7)
def char_point_of(acct):
    conn = sqlite3.connect(r'data/mhq.db')
    v = conn.execute("SELECT char_point FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
    conn.close()
    return v

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

acct = 'ap' + str(int(time.time()))
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '加点测试')
s.close(); time.sleep(1.5)
conn = sqlite3.connect(r'data/mhq.db')
pid = conn.execute("SELECT id FROM players WHERE account_id=(SELECT id FROM accounts WHERE account=?)", (acct,)).fetchone()[0]
conn.execute('UPDATE players SET char_point=10 WHERE id=?', (pid,))
conn.commit(); conn.close()
s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
login(s, acct, '加点测试')
print('  初始属性点:', char_point_of(acct))

# 1) PointList 全 0（sum=0）→ 成功响应 + Character（客户端不崩）
op, b, pushes = send_req(s, 20253, vf(1, 0) + vf(1, 0) + vf(1, 0) + vf(1, 0) + vf(90, 11), 11)
has_char = fld(b, 1) is not None
check(op == 20254 and fld(b, 91) is None and has_char, '零和 PointList → Error=0 + Character')
# 2) 空 PointList + Trans=0 → 成功响应 + Character
op, b, pushes = send_req(s, 20253, vf(90, 12), 12)
check(op == 20254 and fld(b, 91) is None and fld(b, 1) is not None, '空 PointList/Trans=0 → Error=0 + Character')
# 3) 有效加点 PointList=[1,0,0,0] → 扣 1 点
op, b, pushes = send_req(s, 20253, vf(1, 1) + vf(1, 0) + vf(1, 0) + vf(1, 0) + vf(90, 13), 13)
time.sleep(0.3)
check(op == 20254 and fld(b, 91) is None and fld(b, 1) is not None and char_point_of(acct) == 9,
      '加点 [1,0,0,0] 扣点 10->9 + Character')
# 4) 超过剩余点 [5,5,5,5] sum=20 > 9 → Error=0 + Character + 状态不变
op, b, pushes = send_req(s, 20253, vf(1, 5) + vf(1, 5) + vf(1, 5) + vf(1, 5) + vf(90, 14), 14)
time.sleep(0.3)
check(op == 20254 and fld(b, 91) is None and fld(b, 1) is not None and char_point_of(acct) == 9,
      '超量加点 → Error=0 + Character（不崩不扣）')
# 5) 洗点 → 返还全部（strAdd=1 → 10）
op, b, pushes = send_req(s, 20255, vf(90, 15), 15)
time.sleep(0.3)
check(op == 20256 and fld(b, 91) is None and fld(b, 1) is not None and char_point_of(acct) == 10,
      '洗点返还 9->10 + Character')
s.close()
print()
print('=== 属性加点回归（客户端不崩版）全部通过：%d 项 ===' % ok)
