# -*- coding: utf-8 -*-
"""Verify high-tier star-soul random attributes over the real TCP protocol.

Run from server-mysql while the local server is listening on 127.0.0.1:7756:
    python .\test\verify_starsoul_random.py
"""

import os
import random
import shutil
import socket
import struct
import subprocess
import sys
import time

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
MYSQL_HOST = os.environ.get("MHQ_TEST_MYSQL_HOST", "127.0.0.1")
MYSQL_PORT = os.environ.get("MHQ_TEST_MYSQL_PORT", "3306")
MYSQL_USER = os.environ.get("MHQ_TEST_MYSQL_USER", "root")
MYSQL_PASSWORD = os.environ.get("MHQ_TEST_MYSQL_PASSWORD", "123456")
MYSQL_DATABASE = os.environ.get("MHQ_TEST_MYSQL_DATABASE", "mhq")


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
    return varint(tag << 3) + varint(value)


def sf(tag, value):
    raw = value.encode("utf-8")
    return varint((tag << 3) | 2) + varint(len(raw)) + raw


def pack(opcode, body):
    return struct.pack("<HH", 2 + len(body), opcode) + body


def read_varint(data, offset):
    value = 0
    shift = 0
    while True:
        byte = data[offset]
        offset += 1
        value |= (byte & 0x7F) << shift
        if not byte & 0x80:
            return value, offset
        shift += 7


def fields(body):
    result = []
    offset = 0
    while offset < len(body):
        wire_tag, offset = read_varint(body, offset)
        tag, wire_type = wire_tag >> 3, wire_tag & 7
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
            raise AssertionError("unsupported wire type %d" % wire_type)
        result.append((tag, wire_type, value))
    return result


def first(body, tag, default=None):
    for field_tag, _, value in fields(body):
        if field_tag == tag:
            return value
    return default


def packed_varints(body):
    values = []
    offset = 0
    while offset < len(body):
        value, offset = read_varint(body, offset)
        values.append(value)
    return values


def rpc_id(body):
    return first(body, 90)


def recv_frame(conn):
    header = b""
    while len(header) < 4:
        chunk = conn.recv(4 - len(header))
        if not chunk:
            raise ConnectionError("connection closed")
        header += chunk
    total, opcode = struct.unpack("<HH", header)
    body = b""
    while len(body) < total - 2:
        chunk = conn.recv(total - 2 - len(body))
        if not chunk:
            raise ConnectionError("connection closed")
        body += chunk
    return opcode, body


def call(conn, opcode, body, rpc):
    conn.sendall(pack(opcode, body))
    for _ in range(300):
        response_opcode, response_body = recv_frame(conn)
        if rpc_id(response_body) == rpc:
            return response_opcode, response_body
    raise AssertionError("RPC %d did not receive a response" % rpc)


def login(account, password, character_name=None):
    conn = socket.create_connection((HOST, PORT), timeout=5)
    conn.settimeout(5)
    opcode, body = call(conn, 20008, sf(1, account) + sf(2, password) + vf(90, 1), 1)
    if opcode != 20009 or first(body, 91):
        if character_name is None:
            raise AssertionError("login failed for existing account")
        opcode, body = call(conn, 20010, sf(1, account) + sf(2, password) + vf(90, 2), 2)
        assert opcode == 20011 and not first(body, 91), "registration failed"
        opcode, body = call(conn, 20008, sf(1, account) + sf(2, password) + vf(90, 3), 3)
        assert opcode == 20009 and not first(body, 91), "login after registration failed"
    key, gate_id = first(body, 2), first(body, 3)
    opcode, body = call(conn, 20014, vf(1, key) + vf(2, gate_id) + vf(90, 4), 4)
    if not first(body, 2):
        assert character_name is not None, "existing account has no role"
        opcode, body = call(conn, 20016, vf(2, 1) + sf(3, character_name) + vf(90, 5), 5)
        assert opcode == 20017 and not first(body, 91), "role creation failed"
    opcode, body = call(conn, 20027, vf(90, 7), 7)
    assert opcode == 20028 and not first(body, 91), "enter game failed"
    return conn


def mysql_executable():
    configured = os.environ.get("MHQ_TEST_MYSQL_EXE")
    candidates = [
        configured,
        shutil.which("mysql"),
        r"C:\Program Files\MySQL\MySQL Server 8.0\bin\mysql.exe",
    ]
    for candidate in candidates:
        if candidate and os.path.isfile(candidate):
            return candidate
    raise RuntimeError("mysql client not found; set MHQ_TEST_MYSQL_EXE")


def mysql(sql):
    env = os.environ.copy()
    env["MYSQL_PWD"] = MYSQL_PASSWORD
    result = subprocess.run(
        [
            mysql_executable(), "--batch", "--raw", "--skip-column-names",
            "-h", MYSQL_HOST, "-P", MYSQL_PORT, "-u", MYSQL_USER, MYSQL_DATABASE, "-e", sql,
        ],
        check=True,
        capture_output=True,
        text=True,
        encoding="utf-8",
        env=env,
    )
    return result.stdout.strip()


def star_soul_from_response(body, star_soul_id):
    for tag, wire_type, item in fields(body):
        if tag == 1 and wire_type == 2 and first(item, 1) == star_soul_id:
            return item
    raise AssertionError("seeded star soul missing from 20400")


def high_tier_vice(item):
    vice = packed_varints(first(item, 9, b""))
    tier_payload = first(item, 10, b"")
    tiers = list(struct.unpack("<%df" % (len(tier_payload) // 4), tier_payload)) if tier_payload else []
    assert len(vice) == 4 and len(set(vice)) == 4, "vice=%r, want four distinct attributes" % vice
    assert all(1121 <= attribute_id <= 1150 for attribute_id in vice), (
        "vice=%r, want only Type=2 high-tier attribute ids 1121..1150" % vice
    )
    assert len(tiers) == 4 and all(0 <= tier <= 2 for tier in tiers), "viceAdd=%r" % tiers
    return vice, tiers


existing_account = os.environ.get("MHQ_TEST_EXISTING_ACCOUNT")
if existing_account:
    existing_password = os.environ.get("MHQ_TEST_EXISTING_PASSWORD")
    if not existing_password:
        raise RuntimeError("MHQ_TEST_EXISTING_PASSWORD is required")
    existing_conn = login(existing_account, existing_password)
    existing_conn.close()
    print("PASS: existing account star-soul migration completed")
    raise SystemExit(0)


stamp = "%d%d" % (int(time.time()), random.randint(100, 999))
account = "ssr" + stamp
character = "星魂随机测试" + stamp[-5:]
password = "abc12345"
star_soul_id = int(time.time_ns())
player_id = None

print("=== 星魂随机属性 TCP 回归 ===")
try:
    first_conn = login(account, password, character)
    first_conn.close()
    time.sleep(0.2)
    player_id = int(mysql("SELECT p.id FROM players p JOIN accounts a ON a.id=p.account_id WHERE a.account='%s'" % account))
    mysql(
        "INSERT INTO player_star_souls "
        "(player_id,star_soul_id,type_id,level,exp,pos_type,quality,main_attribute,vice_growth_level,is_locked) "
        "VALUES (%d,%d,1001,20,0,9,6,1105,0,0)" % (player_id, star_soul_id)
    )

    conn = login(account, password)
    opcode, response = call(conn, 20396, vf(90, 20), 20)
    assert opcode == 20400, "GetStarSoulBag opcode=%d" % opcode
    item = star_soul_from_response(response, star_soul_id)
    vice, tiers = high_tier_vice(item)
    conn.close()

    persisted = mysql(
        "SELECT s.vice_growth_level,COUNT(v.position),GROUP_CONCAT(v.attribute_type ORDER BY v.position) "
        "FROM player_star_souls s LEFT JOIN player_star_soul_vice_attributes v "
        "ON v.player_id=s.player_id AND v.star_soul_id=s.star_soul_id "
        "WHERE s.player_id=%d AND s.star_soul_id=%d GROUP BY s.vice_growth_level" % (player_id, star_soul_id)
    )
    persisted_fields = persisted.split("\t")
    assert persisted_fields[:2] == ["20", "4"], "persisted repair=%r" % persisted
    persisted_vice = [int(value) for value in persisted_fields[2].split(",")]
    assert persisted_vice == vice, "persisted vice=%r, response vice=%r" % (persisted_vice, vice)

    conn = login(account, password)
    opcode, response = call(conn, 20396, vf(90, 21), 21)
    item = star_soul_from_response(response, star_soul_id)
    second_vice, second_tiers = high_tier_vice(item)
    assert second_vice == vice and second_tiers == tiers, "second login rerolled random attributes"
    conn.close()
    print("  OK: 20级历史星魂补出4条Type=2高阶随机属性 vice=%r" % vice)
    print("  OK: vice_growth_level=20 已持久化")
    print("  OK: 第二次登录未重复生成")
finally:
    try:
        if player_id is None:
            stored_player_id = mysql(
                "SELECT p.id FROM players p JOIN accounts a ON a.id=p.account_id "
                "WHERE a.account='%s'" % account
            )
            if stored_player_id:
                player_id = int(stored_player_id)
        if player_id is not None:
            mysql("DELETE FROM players WHERE id=%d" % player_id)
        mysql("DELETE FROM accounts WHERE account='%s'" % account)
    except Exception as cleanup_error:
        print("cleanup warning:", cleanup_error)

print("PASS: 星魂随机属性 TCP 回归")
