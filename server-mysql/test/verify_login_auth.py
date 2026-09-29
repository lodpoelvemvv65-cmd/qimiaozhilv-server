# -*- coding: utf-8 -*-
"""Real TCP regression for credentials, reconnect voucher, and role deletion."""

import os
import socket
import struct
import sys
import time


sys.stdout.reconfigure(encoding="utf-8", errors="replace")
HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
CLIENT_VISIBLE_CREDENTIAL_ERROR = 200001
REG_ACCOUNT_REQUIRED = 200003
REG_PASSWORD_REQUIRED = 200004
REG_ACCOUNT_FORMAT = 200005
REG_PASSWORD_FORMAT = 200006
REG_ACCOUNT_EXISTS = 200008


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
        else:
            raise ValueError("unsupported wire type %d" % wire_type)
        result.append((field, value))
    return result


def field(body, tag, default=None):
    for current, value in fields(body):
        if current == tag:
            return value
    return default


def request(sock, opcode, body, rpc_id):
    sock.sendall(pack(opcode, body))
    for _ in range(100):
        response_opcode, response_body = recv_frame(sock)
        if field(response_body, 90) == rpc_id:
            return response_opcode, response_body
    raise TimeoutError("response not found for rpc %d" % rpc_id)


def connect():
    sock = socket.create_connection((HOST, PORT), timeout=5)
    sock.settimeout(5)
    return sock


def assert_response(opcode, body, expected_opcode, expected_error=0, message=None):
    error = field(body, 91, 0)
    actual_message = field(body, 92, b"").decode("utf-8")
    assert opcode == expected_opcode, (opcode, expected_opcode, fields(body))
    assert error == expected_error, (error, expected_error, fields(body))
    if message is not None:
        assert actual_message == message, (actual_message, message)


account = "auth%d" % int(time.time() * 1000)
password = "CorrectPass123"

registration_rejections = [
    ("", "Pass123", REG_ACCOUNT_REQUIRED, "请输入账号"),
    (account + "empty", "", REG_PASSWORD_REQUIRED, "请输入密码"),
    (account + "-bad", "Pass123", REG_ACCOUNT_FORMAT, "账号格式不正确"),
    (account + "short", "12345", REG_PASSWORD_FORMAT, "密码格式不正确"),
    (account + "punct", "Pass_123", REG_PASSWORD_FORMAT, "密码格式不正确"),
]
for index, (rejected_account, rejected_password, error, message) in enumerate(registration_rejections, 20):
    sock = connect()
    body = vf(90, index)
    if rejected_account:
        body += sf(1, rejected_account)
    if rejected_password:
        body += sf(2, rejected_password)
    opcode, body = request(sock, 20010, body, index)
    assert_response(opcode, body, 20011, error, message)
    assert field(body, 2, 0) == 0
    sock.close()
    if rejected_account:
        sock = connect()
        opcode, body = request(sock, 20008, sf(1, rejected_account) + sf(2, rejected_password or "Pass123") + vf(90, index + 20), index + 20)
        assert_response(opcode, body, 20009, CLIENT_VISIBLE_CREDENTIAL_ERROR, "账号或密码错误")
        sock.close()
print("PASS invalid registration fields are rejected without creating accounts")

sock = connect()
opcode, body = request(sock, 20010, sf(1, account) + sf(2, password) + vf(90, 1), 1)
assert_response(opcode, body, 20011)
initial_gate_key = field(body, 2)
initial_voucher = field(body, 4).decode("utf-8")
assert initial_gate_key > 0 and initial_voucher.startswith(account + "|")
sock.close()
print("PASS registration returns independent gate and reconnect credentials")

sock = connect()
opcode, body = request(sock, 20010, sf(1, account) + sf(2, password) + vf(90, 60), 60)
assert_response(opcode, body, 20011, REG_ACCOUNT_EXISTS, "注册失败，账号已存在")
sock.close()
print("PASS duplicate registration uses the original client-visible message")

sock = connect()
opcode, body = request(sock, 20008, sf(1, account) + sf(2, "wrong-password") + vf(90, 2), 2)
assert_response(opcode, body, 20009, CLIENT_VISIBLE_CREDENTIAL_ERROR, "账号或密码错误")
assert field(body, 2, 0) == 0
sock.close()
print("PASS wrong password is rejected with the client-visible message")

sock = connect()
opcode, body = request(sock, 20008, sf(1, account + "-missing") + sf(2, password) + vf(90, 14), 14)
assert_response(opcode, body, 20009, CLIENT_VISIBLE_CREDENTIAL_ERROR, "账号或密码错误")
assert field(body, 2, 0) == 0
sock.close()
print("PASS unknown account uses the same client-visible credential error")

sock = connect()
bogus = "%s|9223372036854770000" % account
opcode, body = request(sock, 20008, sf(1, account) + sf(2, bogus) + vf(3, 3) + vf(90, 3), 3)
assert_response(opcode, body, 20009, CLIENT_VISIBLE_CREDENTIAL_ERROR, "账号或密码错误")
assert field(body, 2, 0) == 0
sock.close()
print("PASS forged reconnect voucher cannot fall back to account-only login")

sock = connect()
opcode, body = request(sock, 20008, sf(1, account) + sf(2, initial_voucher) + vf(3, 3) + vf(90, 4), 4)
assert_response(opcode, body, 20009)
voucher_gate_key = field(body, 2)
next_voucher = field(body, 4).decode("utf-8")
assert voucher_gate_key > 0 and next_voucher != initial_voucher
opcode, body = request(sock, 20014, vf(1, voucher_gate_key) + vf(2, 1) + vf(90, 5), 5)
assert_response(opcode, body, 20015)
assert field(body, 2, 0) == 0
reserved_player_id = field(body, 1)
assert reserved_player_id > 0
opcode, body = request(sock, 20016, vf(1, reserved_player_id) + vf(2, 1) + sf(3, "Auth" + account[-8:]) + vf(90, 6), 6)
assert_response(opcode, body, 20017)
sock.close()
print("PASS valid reconnect voucher logs in once and rotates credentials")

sock = connect()
opcode, body = request(sock, 20008, sf(1, account) + sf(2, initial_voucher) + vf(3, 3) + vf(90, 7), 7)
assert_response(opcode, body, 20009, CLIENT_VISIBLE_CREDENTIAL_ERROR, "账号或密码错误")
sock.close()
print("PASS consumed reconnect voucher cannot be replayed")

sock = connect()
opcode, body = request(sock, 20008, sf(1, account) + sf(2, password) + vf(3, 3) + vf(90, 15), 15)
assert_response(opcode, body, 20009)
assert field(body, 2, 0) > 0
sock.close()
print("PASS cached Voucher mode accepts only the account's exact password as fallback")

sock = connect()
opcode, body = request(sock, 20008, sf(1, account) + sf(2, password) + vf(90, 8), 8)
assert_response(opcode, body, 20009)
opcode, body = request(sock, 20014, vf(1, field(body, 2)) + vf(2, 1) + vf(90, 9), 9)
assert_response(opcode, body, 20015)
player_id = field(body, 1)
assert player_id > 0 and field(body, 2) == 1
opcode, body = request(sock, 20018, vf(1, player_id) + vf(90, 10), 10)
assert_response(opcode, body, 20019)
sock.close()
print("PASS role deletion succeeds without deleting the account")

sock = connect()
opcode, body = request(sock, 20008, sf(1, account) + sf(2, "wrong-password") + vf(90, 11), 11)
assert_response(opcode, body, 20009, CLIENT_VISIBLE_CREDENTIAL_ERROR, "账号或密码错误")
opcode, body = request(sock, 20008, sf(1, account) + sf(2, password) + vf(90, 12), 12)
assert_response(opcode, body, 20009)
opcode, body = request(sock, 20014, vf(1, field(body, 2)) + vf(2, 1) + vf(90, 13), 13)
assert_response(opcode, body, 20015)
assert field(body, 2, 0) == 0
assert field(body, 1) == player_id
sock.close()
print("PASS deleted role leaves the original password and reserved identity intact")
print("PASS: login, reconnect voucher, and role deletion regression")
