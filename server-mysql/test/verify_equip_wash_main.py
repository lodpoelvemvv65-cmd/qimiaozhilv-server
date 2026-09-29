# -*- coding: utf-8 -*-
"""洗练主属性（20345）回归：手工产物不下发可洗练主属性，普通装备照常洗练。

手工产物（月语 120877 / 忘语 120856 / 时之皇冠 120854 / 流光 120855）在 EquipBase
里只有 SpecialKey/SpecialValue、没有任何属性列。客户端洗练面板显示的是
`TabHelper.GetValueFromConfig(EquipBase, key) * (1 + delta)`，而 GetValueFromConfig
是 `switch(key)` 只读那 26 个属性列、没有 SpecialKey 回退；基础值恒为 0，
于是不管服务端下发什么 delta 都恒显示「最大生命:0 (19.64%)」。
所以服务端不给手工产物下发可洗练主属性，客户端按空列表显示"无法洗练"。

Run from server-mysql while the local server is listening on 127.0.0.1:7756:
    python .\\test\\verify_equip_wash_main.py
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

# 月语：EquipBase 只有 SpecialKey=1(最大生命)/SpecialValue=20000，属性列全空。
MANUAL_EQUIP_ID = 120877
# 普通装备：EquipBase 有 PhyAtk/SpiAtk 列，洗练主属性是这两个键。
NORMAL_EQUIP_ID = 120638
NORMAL_EQUIP_KEYS = {7, 8}
MANUAL_SLOT = 40
NORMAL_SLOT = 41
# Gameplay.yaml 的 equipment.minimum/maximum_main_attribute_percent。
DELTA_LIMIT = 0.2501

OP_GET_BAG = 20258
OP_SEND_BAG = 20260
OP_WASH_MAIN = 20345
OP_WASH_MAIN_RESP = 20346


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
            value = body[offset:offset + length]
            offset += length
        elif wire_type == 5:
            value = body[offset:offset + 4]
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


def drain(conn, timeout=0.4):
    conn.settimeout(timeout)
    result = []
    while True:
        try:
            result.append(recv_frame(conn))
        except (socket.timeout, ConnectionError):
            break
    conn.settimeout(5)
    return result


def call(conn, opcode, body, rpc):
    conn.sendall(pack(opcode, body + vf(90, rpc)))
    pushes = []
    for _ in range(300):
        response_opcode, response_body = recv_frame(conn)
        if rpc_id(response_body) == rpc:
            pushes.extend(drain(conn))
            return response_opcode, response_body, pushes
        pushes.append((response_opcode, response_body))
    raise AssertionError("RPC %d did not receive a response" % rpc)


def login(account, password, character_name=None):
    conn = socket.create_connection((HOST, PORT), timeout=5)
    conn.settimeout(5)
    opcode, body, _ = call(conn, 20008, sf(1, account) + sf(2, password), 1)
    if opcode != 20009 or first(body, 91):
        if character_name is None:
            raise AssertionError("login failed for existing account")
        opcode, body, _ = call(conn, 20010, sf(1, account) + sf(2, password), 2)
        assert opcode == 20011 and not first(body, 91), "registration failed"
        opcode, body, _ = call(conn, 20008, sf(1, account) + sf(2, password), 3)
        assert opcode == 20009 and not first(body, 91), "login after registration failed"
    key, gate_id = first(body, 2), first(body, 3)
    opcode, body, _ = call(conn, 20014, vf(1, key) + vf(2, gate_id), 4)
    if not first(body, 2):
        assert character_name is not None, "existing account has no role"
        opcode, body, _ = call(conn, 20016, vf(2, 1) + sf(3, character_name), 5)
        assert opcode == 20017 and not first(body, 91), "role creation failed"
    opcode, body, _ = call(conn, 20027, b"", 7)
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


def bag_equips(body):
    """M2C_SendBag 的 BagMapList → {格子号: (ItemId, EquipTransMessage 原文)}。"""
    result = {}
    for tag, wire_type, value in fields(body):
        if tag == 1 and wire_type == 2:
            net = first(value, 2, b"")
            result[first(value, 1)] = (first(net, 1), first(value, 3, b""))
    return result


def equip_slot(equips, item_id):
    """服务端装载背包时会按顺序压实格子，所以只能按 ItemId 反查实际槽位。"""
    for slot, (current, _) in equips.items():
        if current == item_id:
            return slot
    raise AssertionError("bag has no item %d (slots %r)" % (item_id, sorted(equips)))


def equip_main_attribute(equip_raw):
    """EquipTransMessage.mainAttribute(5) → {AttributeType: delta}。"""
    result = {}
    for tag, wire_type, value in fields(equip_raw):
        if tag == 5 and wire_type == 2:
            result[first(value, 1)] = struct.unpack("<f", first(value, 2, b"\0\0\0\0"))[0]
    return result


def last_bag_push(pushes):
    for opcode, body in reversed(pushes):
        if opcode == OP_SEND_BAG:
            return body
    return None


stamp = "%d%d" % (int(time.time()), random.randint(100, 999))
account = "wash" + stamp
character = "洗练主属性测试" + stamp[-5:]
password = "abc12345"
player_id = None
conn = None

print("=== 洗练主属性 20345 TCP 回归 ===")
try:
    first_conn = login(account, password, character)
    first_conn.close()
    time.sleep(0.3)
    player_id = int(mysql(
        "SELECT p.id FROM players p JOIN accounts a ON a.id=p.account_id WHERE a.account='%s'" % account
    ))
    mysql(
        "INSERT INTO player_items "
        "(player_id,location,slot_index,item_id,item_type,server_id,item_count,is_locked,quality,star,"
        "strength_level,special_key,special_id,get_source) VALUES "
        "(%d,1,%d,%d,1,0,1,0,4,6,0,0,0,'WashMainProbe'),(%d,1,%d,%d,1,0,1,0,4,6,0,0,0,'WashMainProbe')"
        % (player_id, MANUAL_SLOT, MANUAL_EQUIP_ID, player_id, NORMAL_SLOT, NORMAL_EQUIP_ID)
    )
    seeded = mysql(
        "SELECT COUNT(*) FROM player_items WHERE player_id=%d AND location=1 AND slot_index IN (%d,%d)"
        % (player_id, MANUAL_SLOT, NORMAL_SLOT)
    )
    assert seeded == "2", "灌数据失败，player_items 里只有 %s 行" % seeded
    conn = login(account, password)

    opcode, body, _ = call(conn, OP_GET_BAG, b"", 10)
    assert opcode == 20259, "get bag opcode = %d" % opcode
    equips = bag_equips(body)
    manual_slot = equip_slot(equips, MANUAL_EQUIP_ID)
    normal_slot = equip_slot(equips, NORMAL_EQUIP_ID)
    # 服务端装载背包时 ensureEquipmentVariation 会给空主属性的装备自动摇一次，
    # 所以普通装备出厂就带主属性行；手工产物没有属性列，一条都不该有。
    assert equip_main_attribute(equips[manual_slot][1]) == {}, "手工产物没有属性列，不该有主属性行"
    normal_before = equip_main_attribute(equips[normal_slot][1])
    assert set(normal_before) == NORMAL_EQUIP_KEYS, "普通装备主属性键 = %r" % sorted(normal_before)
    for key, delta in normal_before.items():
        assert -DELTA_LIMIT <= delta <= DELTA_LIMIT, "主属性 %d delta=%r 超出 ±25%%" % (key, delta)

    # 手工产物：拒绝，且不产生任何主属性行、不推背包快照。
    opcode, body, pushes = call(conn, OP_WASH_MAIN, vf(1, manual_slot), 20)
    assert opcode == OP_WASH_MAIN_RESP, "wash opcode = %d" % opcode
    assert not first(body, 91), "拒绝不应带 Error"
    manual_message = first(body, 92)
    assert manual_message, "手工产物洗练应被拒绝（客户端拿空属性列当基础值，恒显示 0）"
    assert last_bag_push(pushes) is None, "被拒绝的洗练不该推背包快照"
    rows = mysql(
        "SELECT COUNT(*) FROM player_item_main_attributes "
        "WHERE player_id=%d AND location=1 AND slot_index=%d" % (player_id, manual_slot)
    )
    assert rows == "0", "手工产物落库主属性行数 = %s, want 0" % rows

    # 普通装备：照常洗练，键只能是它有值的属性列，delta 落在配置区间内。
    opcode, body, pushes = call(conn, OP_WASH_MAIN, vf(1, normal_slot), 21)
    assert opcode == OP_WASH_MAIN_RESP, "wash opcode = %d" % opcode
    assert not first(body, 91) and not first(body, 92), "普通装备洗练被拒绝：%r" % first(body, 92)
    pushed = last_bag_push(pushes)
    assert pushed is not None, "洗练成功应推背包快照"
    attrs = equip_main_attribute(bag_equips(pushed)[normal_slot][1])
    assert set(attrs) == NORMAL_EQUIP_KEYS, "普通装备洗练主属性键 = %r, want %r" % (sorted(attrs), sorted(NORMAL_EQUIP_KEYS))
    for key, delta in attrs.items():
        assert -DELTA_LIMIT <= delta <= DELTA_LIMIT, "主属性 %d delta=%r 超出 ±25%%" % (key, delta)

    persisted = mysql(
        "SELECT attribute_type FROM player_item_main_attributes "
        "WHERE player_id=%d AND location=1 AND slot_index=%d ORDER BY attribute_type" % (player_id, normal_slot)
    )
    assert [int(row) for row in persisted.split()] == sorted(NORMAL_EQUIP_KEYS), "落库主属性 = %r" % persisted

    print("手工 120877 月语 → 20345 被拒绝：%s（无主属性行，客户端显示「无法洗练」）" % manual_message)
    print("普通 120638 → 主属性键 %r，delta %r，已落库" % (sorted(attrs), attrs))
    print("PASS: 手工产物不下发可洗练主属性；普通装备照常洗练")
finally:
    if conn is not None:
        conn.close()
    if player_id is not None:
        mysql(
            "DELETE FROM player_item_main_attributes WHERE player_id=%d;"
            "DELETE FROM player_items WHERE player_id=%d;"
            "DELETE FROM players WHERE id=%d;" % (player_id, player_id, player_id)
        )
    mysql("DELETE FROM accounts WHERE account='%s'" % account)
