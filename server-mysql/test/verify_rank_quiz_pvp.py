# -*- coding: utf-8 -*-
"""Network regression for ranking, quiz, and personal PVP handlers.

答题部分覆盖一整轮：20304 开始 → 20308 逐题作答（重复作答拒绝）→ 20306 取下一题，
答满配置题数（默认 10）后 20306 必须回非空 Message 且不再下发 ConfigId（原版客户端
的唯一结束路径），此时误点选项也不能回业务错误码，随后可用 20304 重新开始一轮。
"""

import os
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


def recv_one(sock):
    header = b""
    while len(header) < 4:
        chunk = sock.recv(4 - len(header))
        if not chunk:
            raise ConnectionError("connection closed while reading header")
        header += chunk
    total, opcode = struct.unpack("<HH", header)
    body = b""
    while len(body) < total - 2:
        chunk = sock.recv(total - 2 - len(body))
        if not chunk:
            raise ConnectionError("connection closed while reading body")
        body += chunk
    return opcode, body


def decode_fields(body):
    fields = []
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
            fields.append((field, "var", value))
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
            fields.append((field, "bytes", body[offset:offset + length]))
            offset += length
        elif wire_type == 5:
            fields.append((field, "float", struct.unpack("<f", body[offset:offset + 4])[0]))
            offset += 4
        elif wire_type == 1:
            fields.append((field, "fixed64", body[offset:offset + 8]))
            offset += 8
        else:
            raise ValueError("unsupported protobuf wire type %d" % wire_type)
    return fields


def field_value(body, tag, default=None):
    for field, _, value in decode_fields(body):
        if field == tag:
            return value
    return default


def repeated_messages(body, tag):
    return [value for field, kind, value in decode_fields(body)
            if field == tag and kind == "bytes"]


def rpc_of(body):
    return field_value(body, 90)


def send_request(sock, opcode, body, rpc_id):
    sock.sendall(pack(opcode, body))
    pushes = []
    for _ in range(500):
        response_opcode, response_body = recv_one(sock)
        if rpc_of(response_body) == rpc_id:
            return response_opcode, response_body, pushes
        pushes.append((response_opcode, response_body))
    raise TimeoutError("RPC %d response not found" % rpc_id)


def login(sock, account, role_name):
    send_request(sock, 20010, sf(1, account) + sf(2, "123456") + vf(90, 1), 1)
    key = None
    gate = None
    for _ in range(3):
        _, body, _ = send_request(
            sock, 20008, sf(1, account) + sf(2, "123456") + vf(90, 2), 2
        )
        key = field_value(body, 2)
        gate = field_value(body, 3)
        if key is not None and gate is not None:
            break
        time.sleep(1)
    if key is None or gate is None:
        raise RuntimeError("gate login did not return key and gate id")
    send_request(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    create_opcode, create_body, _ = send_request(
        sock, 20016, vf(2, 1) + sf(3, role_name) + vf(90, 5), 5
    )
    if create_opcode != 20017 or field_value(create_body, 91, 0) != 0:
        raise AssertionError("create role failed: opcode=%s fields=%s" %
                             (create_opcode, decode_fields(create_body)))

    # LoginGate consumes its one-time Redis key. Creating a role does not
    # mint another key, so log in again before entering the game.
    _, body, _ = send_request(
        sock, 20008, sf(1, account) + sf(2, "123456") + vf(90, 6), 6
    )
    key = field_value(body, 2)
    gate = field_value(body, 3)
    if key is None or gate is None:
        raise RuntimeError("second login did not return key and gate id")
    send_request(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 7), 7)
    opcode, body, _ = send_request(sock, 20027, vf(90, 8), 8)
    check(opcode == 20028 and field_value(body, 91, 0) == 0, "login enters the game")


passed = 0


def check(condition, message):
    global passed
    if not condition:
        raise AssertionError("FAIL: " + message)
    passed += 1
    print("  PASS", message)


host = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
port = int(os.environ.get("MHQ_TEST_PORT", "7756"))
account = "rankquizpvp%d" % int(time.time())
name = "RQP" + account[-8:]
sock = socket.create_connection((host, port), timeout=5)
sock.settimeout(8)

try:
    login(sock, account, name)

    opcode, body, _ = send_request(sock, 20292, vf(1, 0) + vf(90, 11), 11)
    rankings = repeated_messages(body, 1)
    check(opcode == 20293 and field_value(body, 91, 0) == 0, "ranking response opcode and status")
    check(len(rankings) > 0, "ranking contains players")
    rank_fields = decode_fields(rankings[0])
    check(any(field == 1 and kind == "bytes" for field, kind, _ in rank_fields), "ranking contains a name")
    check(any(field == 2 and kind == "bytes" for field, kind, _ in rank_fields), "ranking contains a job")
    check(any(field == 3 and kind == "float" for field, kind, _ in rank_fields), "ranking contains a numeric value")

    opcode, body, _ = send_request(sock, 20292, vf(1, 99) + vf(90, 12), 12)
    check(opcode == 20293 and field_value(body, 91, 0) != 0, "invalid ranking type is rejected")

    opcode, body, _ = send_request(sock, 20304, vf(90, 21), 21)
    first_question = field_value(body, 1, 0)
    check(opcode == 20305 and field_value(body, 91, 0) == 0, "quiz starts")
    check(first_question > 0, "quiz start returns a question id")

    opcode, body, _ = send_request(sock, 20306, vf(90, 22), 22)
    check(opcode == 20307 and field_value(body, 91, 0) != 0, "next question before answering is rejected")

    opcode, body, _ = send_request(sock, 20308, vf(1, 1) + vf(90, 23), 23)
    check(opcode == 20309 and field_value(body, 91, 0) == 0, "quiz answer is accepted")
    check(field_value(body, 1, 0) >= 1, "quiz answer returns elapsed time")

    opcode, body, _ = send_request(sock, 20308, vf(1, 1) + vf(90, 24), 24)
    check(opcode == 20309 and field_value(body, 91, 0) != 0, "duplicate quiz answer is rejected")

    opcode, body, _ = send_request(sock, 20306, vf(90, 25), 25)
    check(opcode == 20307 and field_value(body, 91, 0) == 0, "next quiz question succeeds after answering")
    check(field_value(body, 1, 0) > 0, "next quiz response returns a question id")

    rpc_id = 26
    for answer_index in range(2, 11):
        opcode, body, _ = send_request(
            sock, 20308, vf(1, 1) + vf(90, rpc_id), rpc_id
        )
        check(opcode == 20309 and field_value(body, 91, 0) == 0,
              "quiz answer %d is accepted" % answer_index)
        rpc_id += 1
        if answer_index < 10:
            opcode, body, _ = send_request(sock, 20306, vf(90, rpc_id), rpc_id)
            check(opcode == 20307 and field_value(body, 91, 0) == 0,
                  "next question after answer %d succeeds" % answer_index)
            check(field_value(body, 1, 0) > 0,
                  "next question after answer %d has a config id" % answer_index)
            rpc_id += 1

    # 答满配置题数后必须能结束：原版客户端（ET.QuestAnswerUI::<OnClickAnswer>d__7）
    # 只在 M2C_NextAnswerQuest.Message 非空时停止请求下一题，所以这里既不能回 Error，
    # 也不能再下发 ConfigId（否则界面永远出新题，玩家结束不了答题）。
    opcode, body, _ = send_request(sock, 20306, vf(90, rpc_id), rpc_id)
    end_message = field_value(body, 92, b"")
    check(opcode == 20307 and field_value(body, 91, 0) == 0,
          "next request after ten answers ends the round without an error")
    check(end_message, "finished round returns a non-empty message to end the client UI")
    check(field_value(body, 1, 0) in (None, 0),
          "finished round must not send a new question id")
    rpc_id += 1

    opcode, body, _ = send_request(
        sock, 20308, vf(1, 1) + vf(90, rpc_id), rpc_id
    )
    check(opcode == 20309 and field_value(body, 91, 0) == 0,
          "answer after a finished round does not raise a business error")
    check(field_value(body, 92, b"") == end_message,
          "answer after a finished round repeats the end message")
    rpc_id += 1

    opcode, body, _ = send_request(sock, 20304, vf(90, rpc_id), rpc_id)
    check(opcode == 20305 and field_value(body, 91, 0) == 0,
          "quiz can be restarted after a finished round")
    check(field_value(body, 1, 0) > 0, "restarted round returns a question id")
    rpc_id += 1

    opcode, body, _ = send_request(
        sock, 20308, vf(1, 1) + vf(90, rpc_id), rpc_id
    )
    check(opcode == 20309 and field_value(body, 91, 0) == 0,
          "first answer in the new quiz round is accepted")
    rpc_id += 1

    quiz_board_started = time.perf_counter()
    opcode, body, _ = send_request(sock, 20310, vf(90, rpc_id), rpc_id)
    quiz_board_elapsed = time.perf_counter() - quiz_board_started
    quiz_scores = repeated_messages(body, 1)
    check(opcode == 20311 and field_value(body, 91, 0) == 0, "quiz score board response")
    check(quiz_board_elapsed < 1.0,
          "quiz score board responds within 1 second (%.1f ms)" % (quiz_board_elapsed * 1000))
    check(len(quiz_scores) > 0, "quiz score board contains players")
    check(any(field_value(entry, 3, b"").decode("utf-8", "replace") == name for entry in quiz_scores),
          "quiz score board contains the current player")

    opcode, body, _ = send_request(sock, 20326, vf(90, 31), 31)
    check(opcode == 20327 and field_value(body, 91, 0) == 0, "personal PVP matching request")

    opcode, body, _ = send_request(sock, 20338, vf(90, 32), 32)
    pvp_ranks = repeated_messages(body, 5)
    check(opcode == 20339 and field_value(body, 91, 0) == 0, "PVP board response opcode and status")
    check(field_value(body, 3, 0) == 1, "PVP board preserves matching state")
    check(field_value(body, 4, 0) >= 1, "PVP board reports at least one matching player")
    check(len(pvp_ranks) > 0, "PVP board contains ranked players")

    print("PASS: %d ranking/quiz/PVP network checks" % passed)
finally:
    sock.close()
