# -*- coding: utf-8 -*-
"""Launcher-only 65000/65001 plan: authenticate accounts, enter game, auto-form a native team."""

import os
import secrets
import socket
import struct
import sys
import time

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

HOST = os.getenv("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.getenv("MHQ_TEST_PORT", "7756"))
PASSWORD = "Pass123456"


def varint(value):
    output = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        output.append(byte | (0x80 if value else 0))
        if not value:
            return bytes(output)


def vf(tag, value):
    return varint(tag << 3) + varint(value)


def bf(tag, value):
    return varint((tag << 3) | 2) + varint(len(value)) + value


def sf(tag, value):
    return bf(tag, value.encode("utf-8"))


def frame(opcode, body):
    return struct.pack("<HH", len(body) + 2, opcode) + body


def recv_frame(conn):
    header = recv_exact(conn, 4)
    total, opcode = struct.unpack("<HH", header)
    if total < 2:
        raise AssertionError("invalid frame length %d" % total)
    return opcode, recv_exact(conn, total - 2)


def recv_exact(conn, length):
    output = bytearray()
    while len(output) < length:
        chunk = conn.recv(length - len(output))
        if not chunk:
            raise ConnectionError("connection closed")
        output.extend(chunk)
    return bytes(output)


def fields(body):
    output = []
    offset = 0
    while offset < len(body):
        key, offset = read_varint(body, offset)
        tag, wire_type = key >> 3, key & 7
        if wire_type == 0:
            value, offset = read_varint(body, offset)
        elif wire_type == 2:
            length, offset = read_varint(body, offset)
            value = body[offset : offset + length]
            offset += length
        elif wire_type == 5:
            value = body[offset : offset + 4]
            offset += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire_type)
        output.append((tag, wire_type, value))
    return output


def read_varint(body, offset):
    value = 0
    shift = 0
    while offset < len(body):
        byte = body[offset]
        offset += 1
        value |= (byte & 0x7F) << shift
        if not byte & 0x80:
            return value, offset
        shift += 7
    raise AssertionError("truncated varint")


def field(body, tag, default=None):
    for field_tag, _, value in fields(body):
        if field_tag == tag:
            return value
    return default


def rpc_id(body):
    return field(body, 90)


def call(conn, opcode, body, expected_rpc):
    conn.sendall(frame(opcode, body))
    received = []
    for _ in range(500):
        response_opcode, response_body = recv_frame(conn)
        received.append((response_opcode, response_body))
        if rpc_id(response_body) == expected_rpc:
            return response_opcode, response_body, received
    raise AssertionError("RPC %d did not receive a response" % expected_rpc)


def drain(conn, timeout=0.6):
    conn.settimeout(timeout)
    output = []
    while True:
        try:
            output.append(recv_frame(conn))
        except (TimeoutError, socket.timeout):
            break
    conn.settimeout(5)
    return output


def login_router(conn, account, rpc):
    opcode, body, _ = call(
        conn, 20008, sf(1, account) + sf(2, PASSWORD) + vf(90, rpc), rpc
    )
    assert opcode == 20009 and field(body, 91, 0) == 0, fields(body)
    return field(body, 2), field(body, 3)


def provision(account, role_name, rpc_base):
    conn = socket.create_connection((HOST, PORT), timeout=5)
    conn.settimeout(5)
    try:
        opcode, body, _ = call(
            conn,
            20010,
            sf(1, account) + sf(2, PASSWORD) + vf(90, rpc_base),
            rpc_base,
        )
        assert opcode == 20011 and field(body, 91, 0) == 0, fields(body)
        key, gate = login_router(conn, account, rpc_base + 1)
        opcode, body, _ = call(
            conn,
            20014,
            vf(1, key) + vf(2, gate) + vf(90, rpc_base + 2),
            rpc_base + 2,
        )
        assert opcode == 20015 and field(body, 91, 0) == 0, fields(body)
        opcode, body, _ = call(
            conn,
            20016,
            vf(2, 1) + sf(3, role_name) + vf(90, rpc_base + 3),
            rpc_base + 3,
        )
        assert opcode == 20017 and field(body, 91, 0) == 0, fields(body)
    finally:
        conn.close()


def enter_existing(account, rpc_base):
    conn = socket.create_connection((HOST, PORT), timeout=5)
    conn.settimeout(5)
    key, gate = login_router(conn, account, rpc_base)
    opcode, body, _ = call(
        conn,
        20014,
        vf(1, key) + vf(2, gate) + vf(90, rpc_base + 1),
        rpc_base + 1,
    )
    assert opcode == 20015 and field(body, 91, 0) == 0, fields(body)
    player_id = field(body, 1)
    opcode, body, received = call(conn, 20027, vf(90, rpc_base + 2), rpc_base + 2)
    assert opcode == 20028 and field(body, 91, 0) == 0, fields(body)
    return conn, player_id, received


def submit_plan(leader, accounts, passwords):
    body = vf(1, 1) + sf(2, leader)
    for account, password in zip(accounts, passwords):
        body += bf(3, sf(1, account) + sf(2, password))
    conn = socket.create_connection((HOST, PORT), timeout=5)
    conn.settimeout(5)
    try:
        conn.sendall(frame(65000, body))
        opcode, response = recv_frame(conn)
        assert opcode == 65001, opcode
        message = field(response, 2, b"").decode("utf-8")
        return field(response, 1, 0) == 1, message, field(response, 3, 0)
    finally:
        conn.close()


def packed_varints(value):
    output = []
    offset = 0
    while offset < len(value):
        item, offset = read_varint(value, offset)
        output.append(item)
    return output


def team_snapshot(received):
    for opcode, body in reversed(received):
        if opcode != 20162:
            continue
        members = []
        for tag, wire_type, value in fields(body):
            if tag == 2 and wire_type == 0:
                members.append(value)
            elif tag == 2 and wire_type == 2:
                members.extend(packed_varints(value))
        return field(body, 1), members
    return None


checks = 0


def check(condition, description):
    global checks
    if not condition:
        raise AssertionError("FAIL: " + description)
    checks += 1
    print("  OK", description)


suffix = "%08d%s" % (int(time.time()) % 100000000, secrets.token_hex(2))
leader_account = "ltl" + suffix
member_account = "ltm" + suffix
leader_conn = member_conn = None

try:
    provision(leader_account, "L" + suffix[-7:], 10)
    provision(member_account, "M" + suffix[-7:], 20)

    ok, message, _ = submit_plan(
        leader_account,
        [leader_account, member_account],
        [PASSWORD, "Wrong123"],
    )
    check(not ok and message == "账号或密码错误", "wrong password cannot create a team plan")

    ok, message, expires = submit_plan(
        leader_account,
        [leader_account, member_account],
        [PASSWORD, PASSWORD],
    )
    check(ok and expires == 120, "authenticated two-minute team plan is accepted")

    leader_conn, leader_id, leader_received = enter_existing(leader_account, 30)
    member_conn, member_id, member_received = enter_existing(member_account, 40)
    leader_received += drain(leader_conn, 1.5)
    member_received += drain(member_conn, 1.5)
    expected = [leader_id, member_id]
    check(team_snapshot(leader_received) == (leader_id, expected), "leader receives native team snapshot")
    check(team_snapshot(member_received) == (leader_id, expected), "member receives native team snapshot")
finally:
    if leader_conn is not None:
        leader_conn.close()
    if member_conn is not None:
        member_conn.close()

print("PASS launcher auto-team TCP: %d checks" % checks)
