# -*- coding: utf-8 -*-
"""Real TCP regression for ItemUpgrade evolution and connection survival."""

import socket
import struct
import sys
import time


sys.stdout.reconfigure(encoding="utf-8", errors="replace")


def varint(value):
    out = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        out.append(byte | 0x80 if value else byte)
        if not value:
            return bytes(out)


def vf(tag, value):
    return varint(tag << 3) + varint(value)


def sf(tag, value):
    data = value.encode("utf-8")
    return varint((tag << 3) | 2) + varint(len(data)) + data


def pack(opcode, body):
    return struct.pack("<HH", len(body) + 2, opcode) + body


def recv_one(sock):
    header = b""
    while len(header) < 4:
        chunk = sock.recv(4 - len(header))
        if not chunk:
            raise ConnectionError("server closed connection")
        header += chunk
    total, opcode = struct.unpack("<HH", header)
    body = b""
    while len(body) < total - 2:
        chunk = sock.recv(total - 2 - len(body))
        if not chunk:
            raise ConnectionError("server closed connection")
        body += chunk
    return opcode, body


def fields(body):
    result = []
    offset = 0
    while offset < len(body):
        key = 0
        shift = 0
        while True:
            byte = body[offset]
            offset += 1
            key |= (byte & 0x7F) << shift
            if not byte & 0x80:
                break
            shift += 7
        tag, wire = key >> 3, key & 7
        if wire == 0:
            value = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                value |= (byte & 0x7F) << shift
                if not byte & 0x80:
                    break
                shift += 7
            result.append((tag, "var", value))
        elif wire == 2:
            length = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                length |= (byte & 0x7F) << shift
                if not byte & 0x80:
                    break
                shift += 7
            result.append((tag, "bytes", body[offset:offset + length]))
            offset += length
        elif wire == 5:
            result.append((tag, "fixed32", body[offset:offset + 4]))
            offset += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire)
    return result


def field(body, tag):
    return next((value for item_tag, _, value in fields(body) if item_tag == tag), None)


def drain(sock, timeout=0.35):
    sock.settimeout(timeout)
    result = []
    while True:
        try:
            result.append(recv_one(sock))
        except (socket.timeout, ConnectionError):
            break
    sock.settimeout(5)
    return result


def call(sock, opcode, body, rpc):
    sock.sendall(pack(opcode, body + vf(90, rpc)))
    pushes = []
    while True:
        response_opcode, response_body = recv_one(sock)
        if field(response_body, 90) == rpc:
            pushes.extend(drain(sock))
            return response_opcode, response_body, pushes
        pushes.append((response_opcode, response_body))


def parse_bag(body):
    result = {}
    for tag, wire, bag_map in fields(body):
        if tag != 1 or wire != "bytes":
            continue
        index = field(bag_map, 1)
        net_item = field(bag_map, 2) or b""
        result[index] = {
            "item_id": field(net_item, 1),
            "count": field(net_item, 4) or 0,
            "locked": bool(field(net_item, 5)),
        }
    return result


def item_count(bag, item_id):
    return sum(item["count"] for item in bag.values() if item["item_id"] == item_id)


account = "upgrade%d" % int(time.time() * 1000)
password = "123456"
sock = socket.create_connection(("127.0.0.1", 7756))
sock.settimeout(5)

opcode, registered, _ = call(sock, 20010, sf(1, account) + sf(2, password), 1)
assert opcode == 20011 and field(registered, 91) is None, "registration failed"
key, gate = field(registered, 2), field(registered, 3)
call(sock, 20014, vf(1, key) + vf(2, gate), 2)
role_name = "Up" + account[-8:]
opcode, created, _ = call(sock, 20016, vf(2, 1) + sf(3, role_name), 3)
assert opcode == 20017 and field(created, 92) is None, "role creation failed: %r" % fields(created)
call(sock, 20027, b"", 4)
drain(sock)

# MarketBase page 1, zero-based slot 19 is ItemUpgrade material 20059.
opcode, bought, _ = call(sock, 20176, vf(1, 1) + vf(2, 19) + vf(3, 5) + vf(4, 2), 10)
assert opcode == 20177 and field(bought, 91) is None and field(bought, 92) is None, "material purchase failed"
bag = parse_bag(bought)
matches = [(index, item) for index, item in bag.items() if item["item_id"] == 20059]
assert len(matches) == 1 and matches[0][1]["count"] == 5, "five evolution materials not found"
index = matches[0][0]

opcode, upgraded, pushes = call(sock, 20277, vf(1, index) + vf(2, 1), 11)
assert opcode == 20278, "item evolution response opcode mismatch"
assert field(upgraded, 91) is None, "item evolution returned an RPC error"
assert all(push_opcode != 20278 for push_opcode, _ in pushes), "duplicate evolution response"

# A follow-up RPC proves the async client request did not lose its connection.
opcode, fetched, _ = call(sock, 20258, b"", 12)
assert opcode == 20259 and field(fetched, 91) is None, "connection did not survive item evolution"
bag = parse_bag(fetched)
source_count = item_count(bag, 20059)
target_count = item_count(bag, 20072)
if target_count == 1:
    target = next(item for item in bag.values() if item["item_id"] == 20072)
    assert source_count == 0 and target["locked"], "successful evolution result is invalid"
    outcome = "success"
else:
    assert target_count == 0 and source_count == 5, "failed evolution refund is invalid"
    assert field(upgraded, 92), "failed evolution did not return its user-facing message"
    outcome = "configured failure refund"

sock.close()
print("item evolution TCP regression passed: %s; connection remained usable" % outcome)
