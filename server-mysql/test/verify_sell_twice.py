"""回归验证连续卖出：每次卖出都刷新背包并同步当次铜币余额。

运行：在 server-mysql/ 下执行 ``python .\\test\\verify_sell_twice.py``。
脚本连接本地 127.0.0.1:7756，使用临时账号，不修改已有角色数据。
"""
import random
import socket
import struct
import time


def varint(value):
    out = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        out.append(byte | 0x80 if value else byte)
        if not value:
            return bytes(out)


def vf(field, value):
    return varint(field << 3) + varint(value)


def sf(field, value):
    data = value.encode("utf-8")
    return varint((field << 3) | 2) + varint(len(data)) + data


def pack(opcode, body):
    return struct.pack("<HH", len(body) + 2, opcode) + body


def recv_one(sock):
    header = bytearray()
    while len(header) < 4:
        chunk = sock.recv(4 - len(header))
        if not chunk:
            raise ConnectionError("connection closed")
        header.extend(chunk)
    length, opcode = struct.unpack("<HH", header)
    body = bytearray()
    while len(body) < length - 2:
        chunk = sock.recv(length - 2 - len(body))
        if not chunk:
            raise ConnectionError("connection closed")
        body.extend(chunk)
    return opcode, bytes(body)


def fields(body):
    result = []
    index = 0
    while index < len(body):
        tag = 0
        shift = 0
        while True:
            byte = body[index]
            index += 1
            tag |= (byte & 0x7F) << shift
            shift += 7
            if not byte & 0x80:
                break
        field, wire = tag >> 3, tag & 7
        if wire == 0:
            value = 0
            shift = 0
            while True:
                byte = body[index]
                index += 1
                value |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((field, wire, value))
        elif wire == 2:
            size = 0
            shift = 0
            while True:
                byte = body[index]
                index += 1
                size |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((field, wire, body[index:index + size]))
            index += size
        elif wire == 5:
            result.append((field, wire, struct.unpack("<f", body[index:index + 4])[0]))
            index += 4
        else:
            raise ValueError("unsupported protobuf wire type %d" % wire)
    return result


def first_field(body, wanted):
    return next((value for field, _, value in fields(body) if field == wanted), None)


def drain(sock, timeout=0.4):
    old_timeout = sock.gettimeout()
    sock.settimeout(timeout)
    result = []
    try:
        while True:
            result.append(recv_one(sock))
    except (socket.timeout, ConnectionError):
        return result
    finally:
        sock.settimeout(old_timeout)


def rpc(sock, opcode, body, rpc_id):
    sock.sendall(pack(opcode, body))
    pending = []
    while True:
        response_opcode, response_body = recv_one(sock)
        if first_field(response_body, 90) == rpc_id:
            return response_opcode, response_body, pending + drain(sock)
        pending.append((response_opcode, response_body))


def bag_entries(body):
    entries = []
    for field, wire, raw in fields(body):
        if field != 1 or wire != 2:
            continue
        index = first_field(raw, 1)
        net_item = next((value for child, child_wire, value in fields(raw)
                         if child == 2 and child_wire == 2), None)
        if net_item is None:
            continue
        entries.append((index, first_field(net_item, 1), first_field(net_item, 4)))
    return entries


def login(sock, account):
    op, body, _ = rpc(sock, 20010,
                       sf(1, account) + sf(2, "123456") + vf(90, 1), 1)
    if op != 20011:
        raise AssertionError("registration failed: opcode=%d" % op)
    key, gate = first_field(body, 2), first_field(body, 3)
    op, body, _ = rpc(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 2), 2)
    if op != 20015:
        raise AssertionError("login gate failed before role creation")
    has_role = first_field(body, 2)
    if has_role:
        raise AssertionError("temporary account unexpectedly had a role")
    role_name = "卖测" + account[-8:]
    op, body, _ = rpc(sock, 20016,
                      vf(2, 1) + sf(3, role_name) + vf(90, 3), 3)
    if op != 20017 or first_field(body, 91):
        raise AssertionError("role creation failed")
    # LoginGate keys are one-time credentials. Creating the role consumes the
    # registration key, so obtain a fresh key pair through normal login.
    op, body, _ = rpc(sock, 20008,
                      sf(1, account) + sf(2, "123456") + vf(3, 0) + vf(90, 4), 4)
    if op != 20009 or first_field(body, 91):
        raise AssertionError("password login failed after role creation")
    key, gate = first_field(body, 2), first_field(body, 3)
    op, body, _ = rpc(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 5), 5)
    if op != 20015 or not first_field(body, 2):
        raise AssertionError("login gate failed after role creation")
    op, body, pushes = rpc(sock, 20027, vf(90, 6), 6)
    if op != 20028:
        raise AssertionError("enter game failed: opcode=%d" % op)
    # EnterGame acknowledges before its initial scene/currency pushes are
    # emitted. Give those asynchronous writes a short window to arrive.
    time.sleep(0.3)
    pushes.extend(drain(sock, timeout=0.8))
    return pushes


def coin_values(frames):
    values = []
    for opcode, body in frames:
        if opcode == 20169 and first_field(body, 2) == 1028:
            value = next((v for f, w, v in fields(body) if f == 3 and w == 5), None)
            if value is not None:
                values.append(value)
        elif opcode == 20170:
            # EnterGame sends the initial attributes as a field-level list:
            # repeated AttributeMap entries under tag 2.
            for field, wire, raw in fields(body):
                if field != 2 or wire != 2 or first_field(raw, 1) != 1028:
                    continue
                value = next((v for f, w, v in fields(raw) if f == 2 and w == 5), None)
                if value is not None:
                    values.append(value)
    return values


def main():
    # Registration accepts ASCII letters/digits only (no underscore).  Keep
    # the account unique while matching the same validation as the client.
    account = "sell2%d%d" % (int(time.time() * 1000), random.randint(10, 99))
    sock = socket.create_connection(("127.0.0.1", 7756), timeout=5)
    try:
        enter_pushes = login(sock, account)
        initial_coin = coin_values(enter_pushes)[-1]
        op, body, _ = rpc(sock, 20174, vf(1, 0) + vf(2, 0) + vf(3, 2) + vf(90, 6), 6)
        if op != 20175 or first_field(body, 92):
            raise AssertionError("shop purchase failed")
        bought = [entry for entry in bag_entries(body) if entry[1] == 110305]
        if len(bought) != 1 or bought[0][2] != 2:
            raise AssertionError("purchase did not create 110305 x2: %r" % bought)
        slot = bought[0][0]

        responses = []
        pushes = []
        for rpc_id in (7, 8):
            op, response, received = rpc(sock, 20178,
                                          vf(2, slot) + vf(3, 1) + vf(90, rpc_id), rpc_id)
            if op != 20179 or first_field(response, 92):
                raise AssertionError("sale %d failed" % (rpc_id - 6))
            responses.append(bag_entries(response))
            pushes.append(received)

        first_remaining = [entry for entry in responses[0] if entry[1] == 110305]
        second_remaining = [entry for entry in responses[1] if entry[1] == 110305]
        if len(first_remaining) != 1 or first_remaining[0][2] != 1:
            raise AssertionError("first sale snapshot=%r, want 110305 x1" % responses[0])
        if second_remaining:
            raise AssertionError("second sale snapshot still contains item: %r" % responses[1])
        for index, received in enumerate(pushes, 1):
            if sum(opcode == 20260 for opcode, _ in received) != 1:
                raise AssertionError("sale %d did not push exactly one M2C_SendBag" % index)
        values = [coin_values(received)[0] for received in pushes]
        # The setup buys two 110305 items for 1,000 copper before selling them.
        expected = [initial_coin - 1000 + 500, initial_coin - 1000 + 1000]
        if values != expected:
            raise AssertionError("coin sync values=%r, want %r" % (values, expected))
        print("PASS: two sales refreshed snapshots and synced copper %s -> %s" %
              (values[0], values[1]))
    finally:
        sock.close()


if __name__ == "__main__":
    main()
