# -*- coding: utf-8 -*-
"""Live TCP regression for the legacy-client trade workspace compatibility path."""

import os
import secrets
import socket
import struct
import sys
import time

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
MASK64 = (1 << 64) - 1


def varint(value):
    value &= MASK64
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
    data = value.encode("utf-8")
    return varint((tag << 3) | 2) + varint(len(data)) + data


def ff(tag, value):
    return varint((tag << 3) | 5) + struct.pack("<f", value)


def frame_click_map(x, y):
    unit = ff(2, x) + ff(3, y)
    return varint((1 << 3) | 2) + varint(len(unit)) + unit


def frame(opcode, body):
    return struct.pack("<HH", 2 + len(body), opcode) + body


def recv_one(conn):
    header = b""
    while len(header) < 4:
        chunk = conn.recv(4 - len(header))
        if not chunk:
            raise ConnectionError("connection closed while reading header")
        header += chunk
    total, opcode = struct.unpack("<HH", header)
    body = b""
    while len(body) < total - 2:
        chunk = conn.recv(total - 2 - len(body))
        if not chunk:
            raise ConnectionError("connection closed while reading body")
        body += chunk
    return opcode, body


def fields(body):
    result = []
    offset = 0
    while offset < len(body):
        key, offset = read_varint(body, offset)
        tag, wire = key >> 3, key & 7
        if wire == 0:
            value, offset = read_varint(body, offset)
            result.append((tag, wire, value))
        elif wire == 2:
            length, offset = read_varint(body, offset)
            result.append((tag, wire, body[offset : offset + length]))
            offset += length
        elif wire == 5:
            result.append((tag, wire, body[offset : offset + 4]))
            offset += 4
        elif wire == 1:
            result.append((tag, wire, body[offset : offset + 8]))
            offset += 8
        else:
            raise ValueError("unsupported protobuf wire type %d" % wire)
    return result


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


def field(body, tag):
    for current, _, value in fields(body):
        if current == tag:
            return value
    return None


def signed64(value):
    if value is None:
        return None
    return value - (1 << 64) if value >= (1 << 63) else value


def bag_items(body, tag=1):
    """Decode repeated BagMap entries as {store_index: (item_id, count)}."""
    result = {}
    for current, wire, payload in fields(body):
        if current != tag or wire != 2:
            continue
        entry = dict((nested_tag, value) for nested_tag, _, value in fields(payload))
        item_payload = entry.get(2)
        if not isinstance(item_payload, bytes):
            continue
        item = dict((nested_tag, value) for nested_tag, _, value in fields(item_payload))
        result[entry.get(1)] = (item.get(1), item.get(4, 0))
    return result


def rpc_of(body):
    return field(body, 90)


def call(conn, opcode, body, rpc):
    conn.sendall(frame(opcode, body))
    received = []
    for _ in range(500):
        response_opcode, response_body = recv_one(conn)
        received.append((response_opcode, response_body))
        if rpc_of(response_body) == rpc:
            return response_opcode, response_body, received
    raise AssertionError("RPC %d did not receive a response" % rpc)


def drain(conn, timeout=0.6):
    conn.settimeout(timeout)
    received = []
    while True:
        try:
            received.append(recv_one(conn))
        except (TimeoutError, socket.timeout):
            break
    conn.settimeout(5)
    return received


def login(conn, account, role_name):
    call(conn, 20010, sf(1, account) + sf(2, "123456") + vf(90, 1), 1)
    _, login_body, _ = call(conn, 20008, sf(1, account) + sf(2, "123456") + vf(90, 2), 2)
    key, gate = field(login_body, 2), field(login_body, 3)
    if key is None or gate is None:
        raise AssertionError("login did not return gate credentials")
    call(conn, 20014, vf(1, key) + vf(2, gate) + vf(90, 3), 3)
    call(conn, 20016, vf(2, 1) + sf(3, role_name) + vf(90, 4), 4)
    call(conn, 20014, vf(1, key) + vf(2, gate) + vf(90, 5), 5)
    _, body, received = call(conn, 20027, vf(90, 6), 6)
    player_id = field(body, 1)
    if not player_id:
        raise AssertionError("enter game did not return a player id")
    drain(conn)
    return player_id, received


def handle_body(player_id, accept, rpc):
    info = vf(1, player_id) + vf(2, 1 if accept else 0)
    return varint((1 << 3) | 2) + varint(len(info)) + info + vf(2, 1) + vf(90, rpc)


checks = 0


def check(condition, description):
    global checks
    if not condition:
        raise AssertionError("FAIL: " + description)
    checks += 1
    print("  OK", description)


token = "%08d%s" % (int(time.time()) % 100000000, secrets.token_hex(2))
connections = []
try:
    first = socket.create_connection((HOST, PORT), timeout=5)
    second = socket.create_connection((HOST, PORT), timeout=5)
    third = socket.create_connection((HOST, PORT), timeout=5)
    connections.extend((first, second, third))
    for conn in connections:
        conn.settimeout(5)

    first_id, _ = login(first, "ta" + token, "TA" + token[-6:])
    second_id, _ = login(second, "tb" + token, "TB" + token[-6:])
    third_id, _ = login(third, "tc" + token, "TC" + token[-6:])
    print("  players=%d/%d/%d" % (first_id, second_id, third_id))

    opcode, body, _ = call(first, 20174, vf(1, 0) + vf(2, 0) + vf(3, 3) + vf(90, 7), 7)
    first_bag = bag_items(body)
    first_item_index = next((index for index, item in first_bag.items() if item[0] == 110305), None)
    check(opcode == 20175 and first_item_index is not None, "requester prepares trade and warehouse items")
    opcode, body, _ = call(first, 20192,
        vf(1, first_item_index) + vf(3, 1) + vf(4, 0) + vf(90, 9), 9)
    check(opcode == 20193 and field(body, 92) is None,
        "requester stores one item before the trade")
    opcode, body, _ = call(second, 20174, vf(1, 0) + vf(2, 0) + vf(3, 1) + vf(90, 8), 8)
    second_bag = bag_items(body)
    second_item_index = next((index for index, item in second_bag.items() if item[0] == 110305), None)
    check(opcode == 20175 and second_item_index is not None, "target prepares a trade item")

    opcode, body, first_frames = call(first, 20154, vf(1, -second_id) + vf(90, 11), 11)
    check(opcode == 20155 and field(body, 92) is None, "negative RequestTeam starts trade")
    trade_invites = [(op, data) for op, data in drain(second) if op == 20158]
    check(len(trade_invites) == 1, "target receives one trade request-list entry")
    trade_invite = trade_invites[0][1]
    check(field(trade_invite, 1) == first_id, "trade request resolves the real requester")
    check(signed64(field(trade_invite, 93)) == -first_id, "trade request carries a negative ActorId marker")

    opcode, body, _ = call(third, 20154, vf(1, second_id) + vf(90, 12), 12)
    check(opcode == 20155 and field(body, 92).decode("utf-8") == "正在申请...",
          "positive RequestTeam shows the native pending status")
    team_invites = [(op, data) for op, data in drain(second) if op == 20158]
    check(len(team_invites) == 1, "trade does not suppress a team request")
    check(field(team_invites[0][1], 1) == third_id, "team request keeps a positive requester id")
    check(signed64(field(team_invites[0][1], 93)) > 0, "team request does not inherit the trade marker")

    opcode, body, second_frames = call(second, 20160, handle_body(-first_id, True, 13), 13)
    check(opcode == 20161 and field(body, 92) is None, "target accepts trade through HandleTeam")
    first_frames += drain(first)
    open_first = [(op, data) for op, data in first_frames if op == 20187]
    open_second = [(op, data) for op, data in second_frames if op == 20187]
    check(open_first and signed64(field(open_first[-1][1], 93)) == -first_id, "requester receives trade workspace open")
    check(open_second and signed64(field(open_second[-1][1], 93)) == -second_id, "target receives trade workspace open")
    check(not any(op == 20436 for op, _ in first_frames + second_frames), "legacy clients receive no unknown TradeState opcode")

    opcode, body, _ = call(first, 20192,
        vf(1, first_item_index) + vf(3, 2) + vf(4, 0) + vf(90, 14), 14)
    check(opcode == 20193 and field(body, 92) is None, "requester adds an item to its left offer")
    opcode, body, _ = call(second, 20188, vf(1, 0) + vf(90, 15), 15)
    target_after_first = bag_items(body)
    check(target_after_first.get(5) == (110305, 2), "target sees requester item in its right offer")

    opcode, body, _ = call(second, 20192,
        vf(1, second_item_index) + vf(3, 1) + vf(4, 0) + vf(90, 16), 16)
    check(opcode == 20193 and field(body, 92) is None, "target adds an item to its left offer")
    opcode, body, _ = call(first, 20188, vf(1, 0) + vf(90, 17), 17)
    requester_items = bag_items(body)
    check(requester_items.get(0) == (110305, 2), "requester sees its item in the left half")
    check(requester_items.get(5) == (110305, 1), "requester sees target item in the right half")
    opcode, body, _ = call(second, 20188, vf(1, 0) + vf(90, 18), 18)
    target_items = bag_items(body)
    check(target_items.get(0) == (110305, 1) and target_items.get(5) == (110305, 2),
        "target snapshot reverses self/other items")

    opcode, body, _ = call(first, 20194, vf(1, 5) + vf(3, 1) + vf(4, 0) + vf(90, 19), 19)
    check(opcode == 20195 and field(body, 92) is not None, "remote right-half items are read-only")
    opcode, body, _ = call(first, 20194, vf(1, 0) + vf(3, 1) + vf(4, 0) + vf(90, 20), 20)
    check(opcode == 20195 and field(body, 92) is None, "requester can partially withdraw its own left-half item")
    opcode, body, _ = call(second, 20188, vf(1, 0) + vf(90, 21), 21)
    check(bag_items(body).get(5) == (110305, 1),
        "partial own-item withdrawal updates the other client")
    opcode, body, _ = call(first, 20194, vf(1, 0) + vf(3, 1) + vf(4, 0) + vf(90, 22), 22)
    check(opcode == 20195 and field(body, 92) is None, "requester withdraws its final offered item")
    opcode, body, _ = call(second, 20188, vf(1, 0) + vf(90, 23), 23)
    check(5 not in bag_items(body), "final own-item withdrawal disappears from the other client")
    opcode, body, _ = call(first, 20192,
        vf(1, first_item_index) + vf(3, 2) + vf(4, 0) + vf(90, 24), 24)
    check(opcode == 20193 and field(body, 92) is None, "requester can re-add its item")

    for rpc, amount, total, denomination in (
        (30, 10000, 10000, "gold"), (31, 100, 10100, "silver"), (32, 1, 10101, "copper")):
        opcode, body, _ = call(first, 20196, vf(2, amount) + vf(90, rpc), rpc)
        check(opcode == 20197 and field(body, 2) == total,
            "requester adds %s with the correct conversion" % denomination)
    for rpc, amount, total, denomination in (
        (33, 1, 10100, "copper"), (34, 100, 10000, "silver"), (35, 10000, 0, "gold")):
        opcode, body, _ = call(first, 20198, vf(2, amount) + vf(90, rpc), rpc)
        check(opcode == 20199 and (field(body, 2) or 0) == total,
            "requester withdraws %s with the correct conversion" % denomination)
    opcode, body, _ = call(first, 20196, vf(2, 10101) + vf(90, 36), 36)
    check(opcode == 20197 and field(body, 2) == 10101, "requester restores 1 gold 1 silver 1 copper")

    for rpc, amount, total, denomination in (
        (37, 10000, 10000, "gold"), (38, 100, 10100, "silver"), (39, 1, 10101, "copper")):
        opcode, body, _ = call(second, 20196, vf(2, amount) + vf(90, rpc), rpc)
        check(opcode == 20197 and field(body, 2) == total,
            "target adds %s with the correct conversion" % denomination)

    opcode, body, received = call(first, 20188, vf(1, 0) + vf(90, 40), 40)
    check(opcode == 20189 and field(body, 3) == 1, "requester sees one two-sided trade page")
    check(field(body, 4) == 10101 and field(body, 5) == 10101,
        "requester snapshot contains both gold/silver/copper offers")
    check(not any(op == 20436 for op, _ in received), "trade page refresh stays on legacy opcodes")
    opcode, body, _ = call(second, 20188, vf(1, 0) + vf(90, 41), 41)
    check(opcode == 20189 and field(body, 3) == 1, "target sees the same two-sided trade page")
    check(field(body, 4) == 10101 and field(body, 5) == 10101,
        "target snapshot reverses self and other coin offers")

    opcode, body, _ = call(first, 20190, vf(1, 0) + vf(90, 42), 42)
    check(opcode == 20191 and field(body, 92) is None, "requester locks its offer")
    opcode, body, _ = call(first, 20196, vf(2, 1) + vf(90, 43), 43)
    check(opcode == 20197 and field(body, 92) is not None, "locked offers reject currency changes")
    opcode, body, _ = call(second, 20188, vf(1, 0) + vf(90, 44), 44)
    check(field(body, 7) == 1, "target snapshot sees requester locked in real time")

    opcode, body, _ = call(first, 20202, vf(90, 45), 45)
    check(opcode == 20203 and field(body, 92) is None, "cancel button closes the compatibility trade")

    opcode, body, _ = call(second, 20188, vf(1, 0) + vf(90, 46), 46)
    check(opcode == 20189 and field(body, 92) is not None, "remote refresh reports that the trade ended")

    opcode, body, first_frames = call(first, 20154, vf(1, -second_id) + vf(90, 47), 47)
    check(opcode == 20155 and field(body, 92) is None, "a second trade starts after cancellation")
    drain(second)
    opcode, body, second_frames = call(second, 20160, handle_body(-first_id, True, 48), 48)
    check(opcode == 20161 and field(body, 92) is None, "target accepts the second trade")
    first_frames += drain(first)
    check(any(op == 20187 for op, _ in first_frames), "requester receives the second workspace open")
    check(any(op == 20187 for op, _ in second_frames), "target receives the second workspace open")

    opcode, body, _ = call(first, 20192,
        vf(1, first_item_index) + vf(3, 2) + vf(4, 0) + vf(90, 49), 49)
    check(opcode == 20193 and field(body, 92) is None, "requester adds its final offer for auto-complete")
    opcode, body, _ = call(second, 20192,
        vf(1, second_item_index) + vf(3, 1) + vf(4, 0) + vf(90, 50), 50)
    check(opcode == 20193 and field(body, 92) is None, "target adds its final offer for auto-complete")
    opcode, body, _ = call(first, 20196, vf(2, 100) + vf(90, 51), 51)
    check(opcode == 20197 and field(body, 2) == 100, "requester adds the final currency offer")
    opcode, body, _ = call(second, 20196, vf(2, 40) + vf(90, 52), 52)
    check(opcode == 20197 and field(body, 2) == 40, "target adds the final currency offer")

    opcode, body, _ = call(first, 20190, vf(1, 0) + vf(90, 53), 53)
    check(opcode == 20191 and field(body, 92) is None, "first lock leaves the trade open")
    opcode, body, second_complete_frames = call(second, 20190, vf(1, 0) + vf(90, 54), 54)
    check(opcode == 20191 and field(body, 92) is not None,
        "second lock completes the trade without a confirm request")
    first_complete_frames = drain(first)
    complete_frames = first_complete_frames + second_complete_frames
    complete_tips = [data for op, data in complete_frames if op == 20262]
    check(any(field(data, 1) == "交易完成".encode("utf-8") for data in complete_tips),
        "both-lock completion pushes a visible completion state")

    opcode, body, _ = call(first, 20188, vf(1, 0) + vf(90, 55), 55)
    check(opcode == 20189 and field(body, 92) is not None, "requester trade workspace is closed after auto-complete")
    opcode, body, _ = call(second, 20188, vf(1, 0) + vf(90, 56), 56)
    check(opcode == 20189 and field(body, 92) is not None, "target trade workspace is closed after auto-complete")

    opcode, body, _ = call(first, 20031, vf(1, 1000401) + vf(90, 57), 57)
    check(opcode == 20032 and field(body, 91) in (None, 0),
        "requester enters the warehouse NPC scene after trade completion")
    first.sendall(frame(20022, frame_click_map(-12.294, -0.92)))
    time.sleep(5.0)
    opcode, body, warehouse_frames = call(first, 20206, vf(1, 1004) + vf(90, 58), 58)
    warehouse_open = [(op, data) for op, data in warehouse_frames if op == 20187]
    check(opcode == 20207 and field(body, 92) is None and warehouse_open and
          signed64(field(warehouse_open[-1][1], 93)) == first_id,
          "opening the normal warehouse clears the completed trade mode")

    opcode, body, _ = call(first, 20188, vf(1, 0) + vf(90, 59), 59)
    warehouse_items = bag_items(body)
    stored_index = next((index for index, item in warehouse_items.items()
                         if item == (110305, 1)), None)
    check(opcode == 20189 and field(body, 92) is None and stored_index is not None,
          "warehouse reopens with its real StoreList after the trade")
    opcode, body, _ = call(first, 20194,
        vf(1, stored_index) + vf(3, 1) + vf(4, 0) + vf(90, 60), 60)
    check(opcode == 20195 and field(body, 92) is None and stored_index not in bag_items(body, 2),
          "warehouse item can be taken out after the trade")
finally:
    for connection in connections:
        connection.close()

print()
print("=== trade compatibility TCP regression passed: %d checks ===" % checks)
