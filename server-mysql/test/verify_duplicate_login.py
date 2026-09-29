# -*- coding: utf-8 -*-
"""Live TCP regression for duplicate-login eviction and old-session isolation."""

import os
import secrets
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
        if value:
            out.append(byte | 0x80)
        else:
            out.append(byte)
            return bytes(out)


def vf(tag, value):
    return varint((tag << 3) | 0) + varint(value)


def sf(tag, value):
    raw = value.encode("utf-8")
    return varint((tag << 3) | 2) + varint(len(raw)) + raw


def frame(opcode, body):
    return struct.pack("<HH", 2 + len(body), opcode) + body


def recv_exact(conn, size):
    data = b""
    while len(data) < size:
        chunk = conn.recv(size - len(data))
        if not chunk:
            raise ConnectionError("connection closed")
        data += chunk
    return data


def recv_frame(conn):
    total, opcode = struct.unpack("<HH", recv_exact(conn, 4))
    return opcode, recv_exact(conn, total - 2)


def read_varint(body, offset):
    value = 0
    shift = 0
    while True:
        byte = body[offset]
        offset += 1
        value |= (byte & 0x7F) << shift
        if not byte & 0x80:
            return value, offset
        shift += 7


def fields(body):
    result = []
    offset = 0
    while offset < len(body):
        key, offset = read_varint(body, offset)
        tag, wire = key >> 3, key & 7
        if wire == 0:
            value, offset = read_varint(body, offset)
        elif wire == 2:
            length, offset = read_varint(body, offset)
            value = body[offset : offset + length]
            offset += length
        elif wire == 5:
            value = body[offset : offset + 4]
            offset += 4
        elif wire == 1:
            value = body[offset : offset + 8]
            offset += 8
        else:
            raise ValueError("unsupported protobuf wire type %d" % wire)
        result.append((tag, value))
    return result


def field(body, tag, default=None):
    for current, value in fields(body):
        if current == tag:
            return value
    return default


def call(conn, opcode, body, rpc_id):
    conn.sendall(frame(opcode, body))
    received = []
    for _ in range(500):
        response_opcode, response_body = recv_frame(conn)
        received.append((response_opcode, response_body))
        if field(response_body, 90) == rpc_id:
            return response_opcode, response_body, received
    raise AssertionError("RPC %d did not receive a response" % rpc_id)


def drain(conn, timeout=0.4):
    conn.settimeout(timeout)
    received = []
    while True:
        try:
            received.append(recv_frame(conn))
        except (TimeoutError, socket.timeout):
            break
    conn.settimeout(5)
    return received


def login_router(conn, account, password, rpc_id):
    opcode, body, _ = call(
        conn,
        20008,
        sf(1, account) + sf(2, password) + vf(90, rpc_id),
        rpc_id,
    )
    assert opcode == 20009 and field(body, 91, 0) == 0, fields(body)
    key, gate = field(body, 2), field(body, 3)
    assert key and gate, fields(body)
    return key, gate


checks = 0


def check(condition, description):
    global checks
    if not condition:
        raise AssertionError("FAIL: " + description)
    checks += 1
    print("  OK", description)


token = "%08d%s" % (int(time.time()) % 100000000, secrets.token_hex(2))
account = "dup" + token
password = "Pass123456"
role_name = "Dup" + token[-7:]
connections = []

try:
    old = socket.create_connection((HOST, PORT), timeout=5)
    replacement = socket.create_connection((HOST, PORT), timeout=5)
    connections.extend((old, replacement))
    for conn in connections:
        conn.settimeout(5)

    opcode, body, _ = call(
        old,
        20010,
        sf(1, account) + sf(2, password) + vf(90, 1),
        1,
    )
    check(opcode == 20011 and field(body, 91, 0) == 0, "account registration succeeds")

    key, gate = login_router(old, account, password, 2)
    opcode, body, _ = call(old, 20014, vf(1, key) + vf(2, gate) + vf(90, 3), 3)
    check(opcode == 20015 and field(body, 91, 0) == 0, "first LoginGate succeeds")
    opcode, body, _ = call(
        old,
        20016,
        vf(2, 1) + sf(3, role_name) + vf(90, 4),
        4,
    )
    check(opcode == 20017 and field(body, 91, 0) == 0, "role creation succeeds")
    opcode, body, _ = call(old, 20027, vf(90, 5), 5)
    player_id = field(body, 1)
    check(opcode == 20028 and player_id, "old session enters the game")
    drain(old)

    new_key, new_gate = login_router(replacement, account, password, 6)
    opcode, body, _ = call(
        replacement,
        20014,
        vf(1, new_key) + vf(2, new_gate) + vf(90, 7),
        7,
    )
    check(opcode == 20015 and field(body, 91, 0) == 0, "replacement LoginGate succeeds")

    old_frames = drain(old, timeout=1.0)
    force_offline = [(op, data) for op, data in old_frames if op == 20328]
    check(len(force_offline) == 1, "old session receives exactly one ForceOffLine push")

    opcode, body, _ = call(replacement, 20027, vf(90, 8), 8)
    check(
        opcode == 20028 and field(body, 1) == player_id,
        "replacement session enters the same role",
    )

    opcode, body, _ = call(old, 20333, vf(90, 9), 9)
    check(opcode == 20334, "old connection remains alive for confirmation and ping")

    old.sendall(frame(20221, vf(90, 10)))
    old.settimeout(0.8)
    try:
        recv_frame(old)
        old_gameplay_ignored = False
    except (TimeoutError, socket.timeout):
        old_gameplay_ignored = True
    finally:
        old.settimeout(5)
    check(old_gameplay_ignored, "superseded gameplay requests are ignored")

    opcode, body, _ = call(replacement, 20221, vf(90, 11), 11)
    check(opcode == 20222, "replacement session remains authoritative")
finally:
    for connection in connections:
        connection.close()

print()
print("=== duplicate-login TCP regression passed: %d checks ===" % checks)
