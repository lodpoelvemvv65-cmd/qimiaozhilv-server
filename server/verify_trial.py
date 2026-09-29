# -*- coding: utf-8 -*-
"""在线验证试炼原生协议：20091 展示怪、20092/20093 开战和 BattleType=2。"""
import socket
import struct
import sys
import time

sys.stdout.reconfigure(encoding='utf-8', errors='replace')

HOST, PORT = '127.0.0.1', 7756


def varint(value):
    out = bytearray()
    while True:
        byte = value & 0x7f
        value >>= 7
        if value:
            out.append(byte | 0x80)
        else:
            out.append(byte)
            return bytes(out)


def vf(tag, value):
    return varint(tag << 3) + varint(value)


def sf(tag, value):
    raw = value.encode('utf-8')
    return varint((tag << 3) | 2) + varint(len(raw)) + raw


def pack(opcode, body):
    return struct.pack('<HH', len(body) + 2, opcode) + body


def recv_one(sock):
    header = b''
    while len(header) < 4:
        chunk = sock.recv(4 - len(header))
        if not chunk:
            raise ConnectionError('server closed connection')
        header += chunk
    total, opcode = struct.unpack('<HH', header)
    body = b''
    while len(body) < total - 2:
        chunk = sock.recv(total - 2 - len(body))
        if not chunk:
            raise ConnectionError('server closed connection')
        body += chunk
    return opcode, body


def fields(body):
    result = []
    index = 0
    while index < len(body):
        token = 0
        shift = 0
        while True:
            byte = body[index]
            index += 1
            token |= (byte & 0x7f) << shift
            shift += 7
            if not byte & 0x80:
                break
        tag, wire = token >> 3, token & 7
        if wire == 0:
            value = 0
            shift = 0
            while True:
                byte = body[index]
                index += 1
                value |= (byte & 0x7f) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((tag, wire, value))
        elif wire == 2:
            length = 0
            shift = 0
            while True:
                byte = body[index]
                index += 1
                length |= (byte & 0x7f) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((tag, wire, body[index:index + length]))
            index += length
        elif wire == 5:
            result.append((tag, wire, body[index:index + 4]))
            index += 4
        else:
            raise ValueError('unsupported protobuf wire type %d' % wire)
    return result


def field(body, tag):
    for current, _, value in fields(body):
        if current == tag:
            return value
    return None


def rpc_id(body):
    value = field(body, 90)
    return value if isinstance(value, int) else None


def packed_varints(raw):
    values = []
    index = 0
    while index < len(raw):
        value = 0
        shift = 0
        while True:
            byte = raw[index]
            index += 1
            value |= (byte & 0x7f) << shift
            shift += 7
            if not byte & 0x80:
                break
        values.append(value)
    return values


def drain(sock, timeout=0.35):
    sock.settimeout(timeout)
    messages = []
    while True:
        try:
            messages.append(recv_one(sock))
        except (socket.timeout, ConnectionError):
            break
    sock.settimeout(5)
    return messages


def request(sock, opcode, body, rpc):
    sock.sendall(pack(opcode, body))
    pushes = []
    for _ in range(400):
        response_opcode, response_body = recv_one(sock)
        if rpc_id(response_body) == rpc:
            pushes.extend(drain(sock))
            return response_opcode, response_body, pushes
        pushes.append((response_opcode, response_body))
    raise RuntimeError('rpc %d response not received' % rpc)


account = 'trial%d' % int(time.time() * 1000)
sock = socket.create_connection((HOST, PORT), timeout=5)
sock.settimeout(5)

opcode, body, _ = request(sock, 20010, sf(1, account) + sf(2, '123456') + vf(90, 1), 1)
if opcode != 20011:
    raise RuntimeError('register opcode %s' % opcode)
key, gate = field(body, 2), field(body, 3)
request(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 2), 2)
request(sock, 20016, vf(2, 1) + sf(3, '试炼验证') + vf(90, 3), 3)
_, _, enter_pushes = request(sock, 20027, vf(90, 4), 4)

_, _, map_pushes = request(sock, 20031, vf(1, 1000901) + vf(90, 5), 5)
all_map_pushes = enter_pushes + map_pushes
change_maps = [message for op, message in all_map_pushes if op == 20033 and field(message, 4) == 1000901]
trial_inits = [message for op, message in all_map_pushes if op == 20091]
legacy_monsters = [message for op, message in all_map_pushes if op == 20320]
assert change_maps, 'missing ChangeMap(1000901)'
assert trial_inits and field(trial_inits[-1], 1) == 1001, 'missing M2C_InitTrialCopyMap(1001)'
assert not legacy_monsters, 'trial still emitted legacy 20320 field monster'

opcode, response, fight_pushes = request(sock, 20092, vf(90, 6), 6)
assert opcode == 20093, 'start response opcode=%s' % opcode
assert field(response, 1) == 1001 and field(response, 92) is None, 'trial start rejected'
unit_ids = []
for tag, wire, value in fields(response):
    if tag == 2:
        unit_ids.extend(packed_varints(value) if wire == 2 else [value])
combat = [message for op, message in fight_pushes if op == 20050]
assert len(unit_ids) == 2, 'UnitIdList=%s' % unit_ids
assert len(combat) == 1, 'trial did not emit exactly one M2C_MainStoryMonsterInfo combat presentation'

print('20091 TrialCopyId:', field(trial_inits[-1], 1))
print('20093 UnitIdList:', unit_ids)
print('20050 combat presentations:', len(combat))

# Trial quit is a failure path and must return to the left side of the main city.
sock.sendall(pack(20171, vf(90, 7)))
quit_messages = drain(sock, 1.0)
city = [message for op, message in quit_messages if op == 20033 and field(message, 4) in (10004, 1000401)]
assert city, 'trial quit did not return to main city'
sock.close()
print('TEST_ACCOUNT:', account)
print('=== 试炼原生协议验证通过 ===')
