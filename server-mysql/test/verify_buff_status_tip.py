# -*- coding: utf-8 -*-
"""Real TCP regression for native top-left right-click status TipUI."""

import os
import socket
import struct
import sys
import time


sys.stdout.reconfigure(encoding="utf-8", errors="replace")
HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))


def varint(value):
    out = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        out.append(byte | 0x80 if value else byte)
        if not value:
            return bytes(out)


def vf(field, value):
    return varint((field << 3) | 0) + varint(value)


def sf(field, value):
    raw = value.encode("utf-8")
    return varint((field << 3) | 2) + varint(len(raw)) + raw


def pack(opcode, body):
    return struct.pack("<HH", 2 + len(body), opcode) + body


def recv_exact(sock, size):
    data = b""
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise ConnectionError("connection closed")
        data += chunk
    return data


def recv_frame(sock):
    total, opcode = struct.unpack("<HH", recv_exact(sock, 4))
    return opcode, recv_exact(sock, total - 2)


def fields(body):
    result = []
    offset = 0
    while offset < len(body):
        tag = 0
        shift = 0
        while True:
            byte = body[offset]
            offset += 1
            tag |= (byte & 0x7F) << shift
            shift += 7
            if not byte & 0x80:
                break
        field, wire_type = tag >> 3, tag & 7
        if wire_type == 0:
            value = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                value |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
        elif wire_type == 2:
            length = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                length |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            value = body[offset:offset + length]
            offset += length
        elif wire_type == 5:
            value = body[offset:offset + 4]
            offset += 4
        else:
            raise ValueError("unsupported wire type %d" % wire_type)
        result.append((field, wire_type, value))
    return result


def field(body, tag, default=None):
    for current, _, value in fields(body):
        if current == tag:
            return value
    return default


def request(sock, opcode, body, rpc_id):
    sock.sendall(pack(opcode, body))
    for _ in range(200):
        response_opcode, response_body = recv_frame(sock)
        if field(response_body, 90) == rpc_id:
            return response_opcode, response_body
    raise TimeoutError("response not found for rpc %d" % rpc_id)


account = "bufftip%d" % int(time.time() * 1000)
password = "CorrectPass123"
role_name = "状态" + account[-6:]
sock = socket.create_connection((HOST, PORT), timeout=5)
sock.settimeout(5)

opcode, body = request(sock, 20010, sf(1, account) + sf(2, password) + vf(90, 1), 1)
assert opcode == 20011 and field(body, 91, 0) == 0, (opcode, fields(body))
gate_key = field(body, 2)
opcode, body = request(sock, 20014, vf(1, gate_key) + vf(2, 1) + vf(90, 2), 2)
assert opcode == 20015 and field(body, 91, 0) == 0, (opcode, fields(body))
opcode, body = request(sock, 20016, vf(2, 1) + sf(3, role_name) + vf(90, 3), 3)
assert opcode == 20017 and field(body, 91, 0) == 0, (opcode, fields(body))
opcode, body = request(sock, 20027, vf(90, 4), 4)
assert opcode == 20028 and field(body, 91, 0) == 0, (opcode, fields(body))

sock.sendall(pack(20045, vf(90, 5)))
tip_body = None
for _ in range(200):
    pushed_opcode, pushed_body = recv_frame(sock)
    if pushed_opcode == 20262:
        tip_body = pushed_body
        break
assert tip_body is not None, "M2C_SendTip(20262) was not pushed"
message = field(tip_body, 1, b"").decode("utf-8")
for expected in (
    "血球Buff剩余量：0",
    "蓝球Buff剩余量：0",
    "普通跑图剩余量：00:00:00:00",
    "畅爽跑图剩余量：00:00:00:00",
    "时空旅行战斗次数：50",
    "死亡之塔战斗次数：10",
    "今日boss行动值：1000",
    "今日家族boss钥匙：2",
    "战斗经验Buff剩余时间：0.00分钟",
):
    assert expected in message, (expected, message)

sock.close()
print("PASS native 20045 -> 20262 status tip contains authoritative daily state")
