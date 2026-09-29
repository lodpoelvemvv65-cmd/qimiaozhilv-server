# -*- coding: utf-8 -*-
"""实测任务奖励少选/多选提示不会触发 RPC 异常或卡死任务窗口。"""

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
        if value:
            out.append(byte | 0x80)
        else:
            out.append(byte)
            return bytes(out)


def vf(field, value):
    return varint((field << 3) | 0) + varint(value)


def sf(field, value):
    raw = value.encode("utf-8")
    return varint((field << 3) | 2) + varint(len(raw)) + raw


def pack(opcode, body):
    return struct.pack("<HH", 2 + len(body), opcode) + body


def recv_one(conn):
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
        field, wire = tag >> 3, tag & 7
        if wire == 0:
            value = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                value |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((field, wire, value))
        elif wire == 2:
            length = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                length |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((field, wire, body[offset:offset + length]))
            offset += length
        elif wire == 5:
            result.append((field, wire, body[offset:offset + 4]))
            offset += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire)
    return result


def var_field(body, wanted):
    return next((value for field, wire, value in fields(body)
                 if field == wanted and wire == 0), None)


def bytes_field(body, wanted):
    return next((value for field, wire, value in fields(body)
                 if field == wanted and wire == 2), None)


def rpc_of(body):
    return var_field(body, 90)


def drain(conn, timeout=0.6):
    conn.settimeout(timeout)
    result = []
    while True:
        try:
            result.append(recv_one(conn))
        except (socket.timeout, ConnectionError):
            break
    conn.settimeout(5)
    return result


def send_request(conn, opcode, body, rpc):
    conn.sendall(pack(opcode, body))
    pushes = []
    for _ in range(100):
        response_opcode, response_body = recv_one(conn)
        if rpc_of(response_body) == rpc:
            pushes.extend(drain(conn))
            return response_opcode, response_body, pushes
        pushes.append((response_opcode, response_body))
    raise AssertionError("RPC %d response not received" % rpc)


def selection_body(task_id, indexes, rpc):
    return (vf(1, task_id) +
            b"".join(vf(2, index) for index in indexes) +
            vf(90, rpc))


def bag_item_ids(body):
    result = set()
    for field, wire, bag_raw in fields(body):
        if field != 1 or wire != 2:
            continue
        net_item = bytes_field(bag_raw, 2)
        if net_item is not None:
            item_id = var_field(net_item, 1)
            if item_id is not None:
                result.add(item_id)
    return result


account = "tasksel%d" % int(time.time() * 1000)
password = "123456"
conn = socket.create_connection((HOST, PORT))
conn.settimeout(5)

try:
    opcode, body, _ = send_request(
        conn, 20010, sf(1, account) + sf(2, password) + vf(90, 1), 1)
    assert opcode == 20011 and not var_field(body, 91), "注册失败"
    key, gate = var_field(body, 2), var_field(body, 3)

    opcode, body, _ = send_request(
        conn, 20014, vf(1, key) + vf(2, gate) + vf(90, 2), 2)
    player_id = var_field(body, 1)
    assert opcode == 20015 and player_id and not var_field(body, 91), "LoginGate 失败"

    role_name = "奖励测" + account[-6:]
    opcode, body, _ = send_request(
        conn, 20016,
        vf(1, player_id) + vf(2, 1) + sf(3, role_name) + vf(90, 3), 3)
    assert opcode == 20017 and not var_field(body, 91), "创建角色失败"

    opcode, body, _ = send_request(conn, 20027, vf(90, 4), 4)
    assert opcode == 20028 and not var_field(body, 91), "进入游戏失败"

    opcode, body, _ = send_request(
        conn, 20206, vf(1, 1012) + vf(90, 5), 5)
    assert opcode == 20207 and not var_field(body, 91), "点击苹果JJ失败"

    opcode, body, _ = send_request(
        conn, 20217, vf(1, 10011) + vf(90, 6), 6)
    assert opcode == 20218 and not var_field(body, 91), "接受任务10011失败"

    expected_tip = "请选择数量的奖励！".encode("utf-8")
    for rpc, indexes in ((7, (2,)), (8, (2, 3, 4, 5))):
        opcode, body, pushes = send_request(
            conn, 20219, selection_body(10011, indexes, rpc), rpc)
        assert opcode == 20220, "完成任务响应 opcode 错误"
        assert var_field(body, 91) in (None, 0), "错误数量触发了 RPC 异常"
        assert bytes_field(body, 92) == expected_tip, "错误数量提示不匹配"
        assert not any(push_opcode == 20225 for push_opcode, _ in pushes), \
            "错误数量不应完成任务"
        print("错误选择 %s：普通提示返回，连接未卡死" % (indexes,))

    selected = (0, 2, 4)
    opcode, body, pushes = send_request(
        conn, 20219, selection_body(10011, selected, 9), 9)
    assert opcode == 20220 and not var_field(body, 91), "正确选择提交失败"
    assert bytes_field(body, 92) is None, "正确选择不应返回错误提示"
    assert any(push_opcode == 20225 for push_opcode, _ in pushes), "任务状态未同步"

    opcode, body, _ = send_request(conn, 20258, vf(90, 10), 10)
    ids = bag_item_ids(body)
    assert opcode == 20259, "背包响应 opcode 错误"
    assert {110305, 120590, 120592}.issubset(ids), "所选奖励未完整入包: %s" % ids
    assert not ({110316, 120591, 120593} & ids), "未选奖励被错误发放: %s" % ids
    print("PASS：少选/多选均提示且可继续操作，选满3个后任务正常完成")
finally:
    conn.close()
