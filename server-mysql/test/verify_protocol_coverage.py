# -*- coding: utf-8 -*-
"""Real TCP coverage for requests that previously had no server route."""
import os, socket, struct, sys, time

sys.stdout.reconfigure(encoding='utf-8', errors='replace')
HOST = os.environ.get('MHQ_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_PORT', '7756'))

def varint(value):
    value = int(value)
    out = bytearray()
    while value > 127:
        out.append((value & 127) | 128); value >>= 7
    out.append(value)
    return bytes(out)

def vf(field, value): return varint(field << 3) + varint(value)
def sf(field, value):
    raw = value.encode('utf-8')
    return varint((field << 3) | 2) + varint(len(raw)) + raw
def pack(opcode, body=b''): return struct.pack('<HH', len(body) + 2, opcode) + body

def recv_one(sock):
    header = b''
    while len(header) < 4:
        chunk = sock.recv(4 - len(header))
        if not chunk: raise ConnectionError('closed')
        header += chunk
    total, opcode = struct.unpack('<HH', header)
    body = b''
    while len(body) < total - 2:
        chunk = sock.recv(total - 2 - len(body))
        if not chunk: raise ConnectionError('closed')
        body += chunk
    return opcode, body

def fields(body):
    out, pos = [], 0
    while pos < len(body):
        tag = 0; shift = 0
        while True:
            byte = body[pos]; pos += 1; tag |= (byte & 127) << shift; shift += 7
            if not byte & 128: break
        field, wire = tag >> 3, tag & 7
        if wire == 0:
            value = 0; shift = 0
            while True:
                byte = body[pos]; pos += 1; value |= (byte & 127) << shift; shift += 7
                if not byte & 128: break
            out.append((field, 'var', value))
        elif wire == 2:
            length = 0; shift = 0
            while True:
                byte = body[pos]; pos += 1; length |= (byte & 127) << shift; shift += 7
                if not byte & 128: break
            out.append((field, 'bytes', body[pos:pos+length])); pos += length
        elif wire == 5:
            out.append((field, 'float', struct.unpack('<f', body[pos:pos+4])[0])); pos += 4
        else:
            raise AssertionError('unsupported wire type %d' % wire)
    return out

def fld(body, tag, default=None):
    return next((value for field, _, value in fields(body) if field == tag), default)

def rpc_of(body): return fld(body, 90)

def drain(sock, timeout=0.5):
    sock.settimeout(timeout); out = []
    while True:
        try: out.append(recv_one(sock))
        except (socket.timeout, ConnectionError): break
    sock.settimeout(5)
    return out

def send_req(sock, opcode, body, rpc):
    sock.sendall(pack(opcode, body))
    pushes = []
    for _ in range(200):
        op, response = recv_one(sock)
        if rpc_of(response) == rpc:
            pushes += drain(sock)
            return op, response, pushes
        pushes.append((op, response))
    raise AssertionError('response timeout for opcode %d rpc %d' % (opcode, rpc))

def login(sock, account, create=False, enter=True):
    if create:
        send_req(sock, 20010, sf(1, account) + sf(2, '123456') + vf(90, 1), 1)
    op, body, _ = send_req(sock, 20008, sf(1, account) + sf(2, '123456') + vf(90, 2), 2)
    key, gate = fld(body, 2), fld(body, 3)
    op, gate_body, _ = send_req(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 3), 3)
    player_id = fld(gate_body, 1)
    if create:
        send_req(sock, 20016, vf(2, 1) + sf(3, '协议覆盖') + vf(90, 4), 4)
        # CreateRole response has no id; EnterGame returns it.
    if enter:
        op, enter_body, _ = send_req(sock, 20027, vf(90, 5), 5)
        player_id = fld(enter_body, 1)
    return player_id

checks = 0
def check(condition, message):
    global checks
    assert condition, 'FAIL: ' + message
    checks += 1
    print('  ✓', message)

account = 'coverage%d' % int(time.time() * 1000)
s = socket.create_connection((HOST, PORT)); s.settimeout(5)
player_id = login(s, account, create=True)
check(player_id and player_id > 0, '创建覆盖账号并进入游戏')

op, body, _ = send_req(s, 20085, vf(1, player_id) + vf(90, 10), 10)
check(op == 20086 and fld(body, 91) is None and not [v for f, w, v in fields(body) if f == 1 and w == 'bytes'],
      '非战斗状态查询 BattleStateBuff 返回真实空列表')

op, body, pushes = send_req(s, 20355, vf(1, 2) + vf(90, 11), 11)
check(op == 20356 and fld(body, 92) is None and any(o == 20257 for o, _ in pushes), '军官男转职为运动员男并热推角色')
op, body, _ = send_req(s, 20251, vf(1, 0) + vf(90, 12), 12)
character = fld(body, 1, b'')
check(fld(character, 4) == 3, '转职后 JobId=3（保持男性编码）')
op, body, _ = send_req(s, 20239, vf(90, 121), 121)
learned = [(fld(raw, 1), fld(raw, 2)) for field, wire, raw in fields(body)
           if field == 1 and wire == 'bytes' and fld(raw, 2, 0) > 0]
check((200001, 1) in learned, '转职后目标职业普通攻击 200001 以 1 级进入已学技能')

op, body, _ = send_req(s, 20329, sf(1, 'echo') + vf(90, 13), 13)
check(op == 20330 and fld(body, 1) == b'echo', 'TestRequest 安全回显')
op, body, _ = send_req(s, 20336, sf(1, account) + sf(2, '123456') + vf(90, 14), 14)
check(op == 20337 and fld(body, 91) is None and fld(body, 92), 'Reload 返回业务提示且不断线')
op, body, _ = send_req(s, 20101, vf(90, 15), 15)
check(op == 20102 and fld(body, 91) is None and fld(body, 92), '测试战斗未开放时明确提示')
op, body, _ = send_req(s, 20103, vf(90, 16), 16)
check(op == 20104 and fld(body, 91) is None, '结束测试战斗幂等 ACK')
op, body, _ = send_req(s, 20428, vf(1, player_id) + vf(2, 0) + vf(90, 17), 17)
check(op == 20429 and fld(body, 91) is None and fld(body, 92), '未授权 GM 删除他人背包被安全拒绝')

# The following two are one-way client messages. A subsequent RPC proves the
# connection remains usable and also captures AddRandomItem's visible tip.
s.sendall(pack(20261, vf(1, 2) + vf(90, 18)))
time.sleep(0.1)
s.sendall(pack(20369, vf(90, 19)))
time.sleep(0.1)
op, body, pushes = send_req(s, 20329, sf(1, 'alive') + vf(90, 20), 20)
check(op == 20330 and fld(body, 1) == b'alive', 'AddRandomItem/QMacro 单向消息后连接仍可用')
check(any(o == 20262 and fld(message, 1) for o, message in pushes), '随机物品调试请求收到可见提示')
s.close()

# Reconnect at role selection, delete the role, then create a replacement on
# the same account/session exactly as the client does.
s = socket.create_connection((HOST, PORT)); s.settimeout(5)
selected_id = login(s, account, create=False, enter=False)
check(selected_id == player_id, '角色选择阶段加载待删除角色')
op, body, _ = send_req(s, 20018, vf(1, player_id) + vf(90, 30), 30)
check(op == 20019 and fld(body, 91) is None and fld(body, 92) is None, '删除角色成功且账号保留')
op, body, _ = send_req(s, 20016, vf(2, 1) + sf(3, '重建角色') + vf(90, 31), 31)
check(op == 20017 and fld(body, 91) is None and fld(body, 92) is None, '同一连接立即创建新角色')
s.close()

print('\n=== 未路由协议真实 TCP 覆盖全部通过：%d 项 ===' % checks)
