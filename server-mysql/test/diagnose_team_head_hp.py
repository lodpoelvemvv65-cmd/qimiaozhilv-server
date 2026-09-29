# -*- coding: utf-8 -*-
"""只读诊断：把组队头像 1001/1002/1003/1004 的推送序列按收件人打出来。

用于定位「队友头顶血条显示别人血量」的问题：服务端到底给每个 UnitId 推了什么值、
推了几次、顺序如何。脚本只登录既有角色并读取推送，不发送任何写操作，不修改数据。
"""

import os
import socket
import struct
import sys
import time

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

HOST = os.getenv("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.getenv("MHQ_TEST_PORT", "7756"))

# 现有测试队伍：leader + 4 名成员（账号密码取自本机 MySQL，仅本地测试用）
PARTY = [
    (2, "a123123", "b123123"),
    (3902, "a12312301", "b123123"),
    (3903, "a12312302", "b123123"),
    (3904, "a12312303", "b123123"),
    (3905, "a12312304", "b123123"),
]

NUMERIC_NAMES = {1001: "hp", 1002: "maxHp", 1003: "mp", 1004: "maxMp", 1026: "level"}


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


def sf(tag, value):
    raw = value.encode("utf-8")
    return varint((tag << 3) | 2) + varint(len(raw)) + raw


def frame(opcode, body):
    return struct.pack("<HH", len(body) + 2, opcode) + body


def recv_exact(conn, length):
    output = bytearray()
    while len(output) < length:
        chunk = conn.recv(length - len(output))
        if not chunk:
            raise ConnectionError("connection closed")
        output.extend(chunk)
    return bytes(output)


def recv_frame(conn):
    total, opcode = struct.unpack("<HH", recv_exact(conn, 4))
    return opcode, recv_exact(conn, total - 2)


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


def field(body, tag, default=None):
    for field_tag, _, value in fields(body):
        if field_tag == tag:
            return value
    return default


def rpc_id(body):
    return field(body, 90)


def as_float(raw):
    return struct.unpack("<f", raw)[0]


def as_int(value):
    # int64/uint64 走 varint；负数在 proto 里是 10 字节补码
    if value >= 1 << 63:
        value -= 1 << 64
    return value


def call(conn, opcode, body, expected_rpc):
    conn.sendall(frame(opcode, body))
    received = []
    for _ in range(500):
        response_opcode, response_body = recv_frame(conn)
        received.append((response_opcode, response_body))
        if rpc_id(response_body) == expected_rpc:
            return response_opcode, response_body, received
    raise AssertionError("RPC %d did not receive a response" % expected_rpc)


def drain(conn, timeout=1.2):
    conn.settimeout(timeout)
    output = []
    while True:
        try:
            output.append(recv_frame(conn))
        except (TimeoutError, socket.timeout):
            break
    conn.settimeout(5)
    return output


def login_router(conn, account, password, rpc):
    opcode, body, _ = call(conn, 20008, sf(1, account) + sf(2, password) + vf(90, rpc), rpc)
    assert opcode == 20009 and field(body, 91, 0) == 0, fields(body)
    return field(body, 2), field(body, 3)


def enter_game(account, password, rpc_base):
    conn = socket.create_connection((HOST, PORT), timeout=5)
    conn.settimeout(5)
    key, gate = login_router(conn, account, password, rpc_base)
    opcode, body, _ = call(conn, 20014, vf(1, key) + vf(2, gate) + vf(90, rpc_base + 1), rpc_base + 1)
    assert opcode == 20015 and field(body, 91, 0) == 0, fields(body)
    opcode, body, received = call(conn, 20027, vf(90, rpc_base + 2), rpc_base + 2)
    assert opcode == 20028 and field(body, 91, 0) == 0, fields(body)
    return conn, field(body, 1), received


def decode_team_member(body):
    leader = field(body, 1)
    members = []
    for tag, wire_type, value in fields(body):
        if tag != 2:
            continue
        if wire_type == 0:
            members.append(as_int(value))
        elif wire_type == 2:
            offset = 0
            while offset < len(value):
                item, offset = read_varint(value, offset)
                members.append(as_int(item))
    return leader, members


def dump(recipient, received):
    print("\n=== 收件人 %s 收到的推送（按顺序） ===" % recipient)
    start = None
    finals = {}
    for opcode, body in received:
        if start is None:
            start = time.time()
        offset = time.time() - start
        if opcode == 20162:
            leader, members = decode_team_member(body)
            print("[+%6.3fs] 20162 M2C_TeamMember leader=%s members=%s" % (offset, leader, members))
        elif opcode == 20169:
            unit = as_int(field(body, 1, 0))
            numeric = field(body, 2, 0)
            raw = field(body, 3)
            value = as_float(raw) if raw is not None else 0.0
            actor = as_int(field(body, 93, 0))
            print(
                "[+%6.3fs] 20169 SyncUnitAttribute unit=%-5s %-5s=%-12s (actor=%s)%s"
                % (
                    offset,
                    unit,
                    NUMERIC_NAMES.get(numeric, numeric),
                    ("%.0f" % value) if raw is not None else "<字段被省略=0>",
                    actor,
                    "   <-- 该字段在报文中缺失" if raw is None else "",
                )
            )
            finals[(unit, numeric)] = value
    print("--- 最终值（按 unit 汇总） ---")
    for unit in sorted({key[0] for key in finals}):
        line = ["unit=%-5s" % unit]
        for numeric in (1001, 1002, 1003, 1004):
            if (unit, numeric) in finals:
                line.append("%s=%d" % (NUMERIC_NAMES[numeric], finals[(unit, numeric)]))
        print("  " + " ".join(line))
    return finals


def submit_plan(leader_account, members):
    body = vf(1, 1) + sf(2, leader_account)
    for account, password in members:
        entry = sf(1, account) + sf(2, password)
        body += varint((3 << 3) | 2) + varint(len(entry)) + entry
    conn = socket.create_connection((HOST, PORT), timeout=5)
    conn.settimeout(5)
    try:
        conn.sendall(frame(65000, body))
        opcode, response = recv_frame(conn)
        assert opcode == 65001, opcode
        return field(response, 1, 0) == 1, field(response, 2, b"").decode("utf-8")
    finally:
        conn.close()


def main():
    connections = {}
    received = {}
    try:
        if os.getenv("MHQ_DIAG_FORM_PLAN") == "1":
            leader_account = next(account for pid, account, _ in PARTY if pid == 2)
            ok, message = submit_plan(leader_account, [(a, p) for _, a, p in PARTY])
            print("队伍计划：ok=%s message=%s" % (ok, message))
            assert ok, message
            time.sleep(0.5)
        for index, (player_id, account, password) in enumerate(PARTY):
            conn, entered, pushes = enter_game(account, password, 100 + index * 10)
            connections[player_id] = conn
            received[player_id] = pushes
            print("登录 player=%s（协议返回 %s）" % (player_id, entered))
            time.sleep(0.8)
        for player_id, conn in connections.items():
            received[player_id] += drain(conn, 1.5)
        for player_id in (2, 3902, 3904):
            if player_id in received:
                dump(player_id, received[player_id])
    finally:
        for conn in connections.values():
            conn.close()


main()
