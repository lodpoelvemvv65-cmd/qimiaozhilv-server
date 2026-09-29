# -*- coding: utf-8 -*-
"""Real TCP regression for drag-out bag item discard and persistence."""

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
        body += sock.recv(total - 2 - len(body))
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


def rpc_id(body):
    return field(body, 90)


def drain(sock, timeout=0.4):
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
        if rpc_id(response_body) == rpc:
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
            "count": field(net_item, 4),
            "locked": bool(field(net_item, 5)),
        }
    return result


def connect_existing(account, password, rpc_base):
    sock = socket.create_connection(("127.0.0.1", 7756))
    sock.settimeout(5)
    _, login, _ = call(sock, 20008, sf(1, account) + sf(2, password), rpc_base)
    key, gate = field(login, 2), field(login, 3)
    assert key is not None and gate is not None, "login did not return gate credentials"
    call(sock, 20014, vf(1, key) + vf(2, gate), rpc_base + 1)
    call(sock, 20027, b"", rpc_base + 2)
    drain(sock)
    return sock


account = "discard%d" % int(time.time() * 1000)
password = "123456"
sock = socket.create_connection(("127.0.0.1", 7756))
sock.settimeout(5)

opcode, registered, _ = call(sock, 20010, sf(1, account) + sf(2, password), 1)
assert opcode == 20011 and field(registered, 91) is None, "registration failed"
key, gate = field(registered, 2), field(registered, 3)
call(sock, 20014, vf(1, key) + vf(2, gate), 2)
opcode, created, _ = call(sock, 20016, vf(2, 1) + sf(3, "DiscardTest"), 3)
assert opcode == 20017 and field(created, 92) is None, "role creation failed"
call(sock, 20027, b"", 4)
drain(sock)

opcode, bought, _ = call(sock, 20174, vf(1, 0) + vf(2, 0) + vf(3, 3), 10)
assert opcode == 20175 and field(bought, 92) is None, "test stack purchase failed"
bag = parse_bag(bought)
matches = [(index, item) for index, item in bag.items() if item["item_id"] == 110305]
assert matches, "purchased item 110305 not found"
index, item = matches[0]
assert item["count"] == 3, "purchased stack count is not 3"

opcode, deleted, pushes = call(sock, 20263, vf(1, index) + vf(2, item["count"]), 11)
assert opcode == 20264, "discard response opcode mismatch"
assert field(deleted, 91) is None and field(deleted, 92) is None, "discard returned an error"
assert all(tag != 1 for tag, _, _ in fields(deleted)), "M2C_DeleteItem contains a non-protocol bag field"
bag_pushes = [body for push_opcode, body in pushes if push_opcode == 20260]
assert bag_pushes, "discard did not send M2C_SendBag"
assert all(entry["item_id"] != 110305 for entry in parse_bag(bag_pushes[-1]).values()), "push still contains discarded item"

opcode, fetched, _ = call(sock, 20258, b"", 12)
assert opcode == 20259
assert all(entry["item_id"] != 110305 for entry in parse_bag(fetched).values()), "discarded item remained in session bag"
sock.close()

sock = connect_existing(account, password, 20)
opcode, fetched, _ = call(sock, 20258, b"", 23)
assert opcode == 20259
assert all(entry["item_id"] != 110305 for entry in parse_bag(fetched).values()), "discarded item returned after relogin"
sock.close()

print("discard TCP regression passed: response, push refresh, full-stack delete, persistence")
