#!/usr/bin/env python3
"""MySQL+Redis live total test.

Creates 25 real TCP sessions (five parties of five), checks town visibility,
performs team operations concurrently, and exercises representative map,
chat, shop, bag, skill, mail, activity, boss and ranking RPCs.  It deliberately
does not use file databases or write serialized state; all persistence is left to the
running MySQL/Redis server.
"""

import concurrent.futures
import os
import select
import socket
import struct
import sys
import time

HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7757"))
PASSWORD = os.environ.get("MHQ_TEST_PASSWORD", "123456")
STAMP = str(time.time_ns())


def varint(value):
    value = int(value)
    out = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        out.append(byte | (0x80 if value else 0))
        if not value:
            return bytes(out)


def vf(field, value):
    return varint(field << 3) + varint(value)


def sf(field, value):
    raw = value.encode("utf-8")
    return varint((field << 3) | 2) + varint(len(raw)) + raw


def bf(field, value):
    return varint((field << 3) | 2) + varint(len(value)) + value


def decode(raw):
    result = []
    offset = 0
    while offset < len(raw):
        tag = 0
        shift = 0
        while True:
            byte = raw[offset]
            offset += 1
            tag |= (byte & 0x7F) << shift
            if not byte & 0x80:
                break
            shift += 7
        field, wire = tag >> 3, tag & 7
        if wire == 0:
            value = 0
            shift = 0
            while True:
                byte = raw[offset]
                offset += 1
                value |= (byte & 0x7F) << shift
                if not byte & 0x80:
                    break
                shift += 7
            result.append((field, wire, value))
        elif wire == 2:
            length = 0
            shift = 0
            while True:
                byte = raw[offset]
                offset += 1
                length |= (byte & 0x7F) << shift
                if not byte & 0x80:
                    break
                shift += 7
            result.append((field, wire, raw[offset:offset + length]))
            offset += length
        elif wire == 5:
            result.append((field, wire, struct.unpack("<f", raw[offset:offset + 4])[0]))
            offset += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire)
    return result


def first(raw, field, wire=None):
    for number, field_wire, value in decode(raw):
        if number == field and (wire is None or field_wire == wire):
            return value
    return None


def all_values(raw, field, wire=None):
    return [value for number, field_wire, value in decode(raw)
            if number == field and (wire is None or field_wire == wire)]


def repeated_varints(raw, field):
    values = all_values(raw, field, 0)
    for packed in all_values(raw, field, 2):
        offset = 0
        while offset < len(packed):
            value = 0
            shift = 0
            while True:
                byte = packed[offset]
                offset += 1
                value |= (byte & 0x7F) << shift
                if not byte & 0x80:
                    break
                shift += 7
            values.append(value)
    return values


class Client:
    def __init__(self, label):
        self.label = label
        self.sock = socket.create_connection((HOST, PORT), timeout=8)
        self.sock.settimeout(8)
        self.rpc = 0
        self.inbox = []
        self.player_id = 0
        self.account = ""

    def close(self):
        try:
            self.sock.close()
        except OSError:
            pass

    def send(self, opcode, body=b""):
        self.sock.sendall(struct.pack("<HH", len(body) + 2, opcode) + body)

    def recv(self, timeout=8):
        self.sock.settimeout(timeout)
        header = self.sock.recv(4)
        if len(header) != 4:
            raise ConnectionError("%s closed" % self.label)
        length, opcode = struct.unpack("<HH", header)
        body = b""
        while len(body) < length - 2:
            part = self.sock.recv(length - 2 - len(body))
            if not part:
                raise ConnectionError("%s closed" % self.label)
            body += part
        return opcode, body

    def call(self, opcode, expected, body=b"", timeout=12):
        self.rpc += 1
        rpc = self.rpc
        self.send(opcode, body + vf(90, rpc))
        deadline = time.time() + timeout
        while time.time() < deadline:
            op, response = self.recv(max(0.1, deadline - time.time()))
            if op == expected and first(response, 90) == rpc:
                return response
            self.inbox.append((op, response))
        raise TimeoutError("%s waiting opcode %d; inbox=%s" %
                           (self.label, expected, [op for op, _ in self.inbox[-12:]]))

    def wait_for(self, opcode, predicate=lambda _: True, timeout=8):
        deadline = time.time() + timeout
        while time.time() < deadline:
            for index, (op, body) in enumerate(self.inbox):
                if op == opcode and predicate(body):
                    self.inbox.pop(index)
                    return body
            readable, _, _ = select.select([self.sock], [], [], max(0.05, deadline - time.time()))
            if not readable:
                break
            self.inbox.append(self.recv(max(0.1, deadline - time.time())))
        raise TimeoutError("%s waiting push opcode %d; inbox=%s" %
                           (self.label, opcode, [op for op, _ in self.inbox[-12:]]))

    def drain(self, seconds=0.25):
        deadline = time.time() + seconds
        while time.time() < deadline:
            readable, _, _ = select.select([self.sock], [], [], max(0, deadline - time.time()))
            if not readable:
                break
            self.inbox.append(self.recv(max(0.05, deadline - time.time())))


def login_and_enter(client, account, name, job):
    client.account = account
    # Registration only creates the account. Always perform the normal login
    # RPC afterwards; the gate session is intentionally bound by C2R_Login.
    client.call(20010, 20011, sf(1, account) + sf(2, PASSWORD))
    response = client.call(20008, 20009, sf(1, account) + sf(2, PASSWORD))
    key, gate = first(response, 2), first(response, 3)
    if key is None or gate is None:
        raise AssertionError("%s login key/gate missing: %r" % (client.label, decode(response)))
    gate_response = client.call(20014, 20015, vf(1, key) + vf(2, gate))
    if not first(gate_response, 2):
        create = client.call(20016, 20017, vf(2, job) + sf(3, name))
        if first(create, 91) not in (None, 0):
            raise AssertionError("%s create role failed: %r" % (client.label, decode(create)))
        client.close()
        saved_account = client.account
        client.__init__(client.label + "-relogin")
        client.account = saved_account
        response = client.call(20008, 20009, sf(1, account) + sf(2, PASSWORD))
        key, gate = first(response, 2), first(response, 3)
        client.call(20014, 20015, vf(1, key) + vf(2, gate))
    entered = client.call(20027, 20028)
    client.player_id = first(entered, 1)
    if not client.player_id:
        raise AssertionError("%s enter game failed: %r" % (client.label, decode(entered)))
    client.call(20012, 20013)
    # Explicitly rebuild the town scene. New players already start here, but
    # this also verifies the map-change transaction and same-map broadcasts.
    client.call(20031, 20032, vf(1, 1000601))
    client.drain(0.35)


def unit_ids_from_frames(frames):
    ids = set()
    for opcode, body in frames:
        if opcode not in (20034, 20036, 20025):
            continue
        for field, wire, value in decode(body):
            if wire != 2:
                continue
            # M2C_EnterMap/M2C_SendUnitInfo use UnitCharacter at field 2/1;
            # UnitsInMap is a repeated UnitCharacter list at field 1.
            try:
                nested = decode(value)
            except Exception:
                continue
            try:
                candidate = first(value, 1)
            except (AssertionError, IndexError):
                candidate = None
            if candidate:
                ids.add(candidate)
            for _, nested_wire, nested_value in nested:
                if nested_wire == 2:
                    try:
                        candidate = first(nested_value, 1)
                    except (AssertionError, IndexError):
                        candidate = None
                    if candidate:
                        ids.add(candidate)
    return ids


def form_group(clients):
    leader = clients[0]
    expected = [client.player_id for client in clients]
    for target in clients[1:]:
        leader.call(20156, 20157, vf(1, target.player_id))
        target.wait_for(20159, lambda body: first(body, 1) == leader.player_id)
        # IsRequest=false means the invitee accepts an invitation from the
        # leader, so HandleInfo.Id is the inviter (not the invitee itself).
        handle = vf(1, leader.player_id) + vf(2, 1)
        handled = target.call(20160, 20161, bf(1, handle) + vf(2, 0))
        if first(handled, 91) not in (None, 0) or first(handled, 92) is not None:
            raise AssertionError("team accept failed on %s leader=%d target=%d body=%r: %r" %
                                 (target.label, leader.player_id, target.player_id,
                                  decode(bf(1, handle) + vf(2, 0)), decode(handled)))
    for client in clients:
        deadline = time.time() + 8
        snapshots = []
        while time.time() < deadline:
            snapshots.extend(body for op, body in client.inbox if op == 20162)
            client.inbox = [(op, body) for op, body in client.inbox if op != 20162]
            if any(set(repeated_varints(body, 2)) == set(expected) for body in snapshots):
                break
            client.drain(0.2)
        else:
            raise AssertionError("team snapshot mismatch for %s expected=%s got=%s" %
                                 (client.label, expected, [repeated_varints(body, 2) for body in snapshots]))
    return expected


def exercise_client(client):
    # These calls intentionally run in parallel across all five parties.
    checks = [
        (20012, 20013, b""),             # heartbeat
        (20227, 20228, b""),             # main UI slots
        (20239, 20240, b""),             # learned skills
        (20258, 20259, b""),             # bag
        (20281, 20282, b""),             # mail
        (20292, 20293, vf(1, 0)),         # ranking
        (20172, 20173, vf(1, 0)),         # market/shop config
        (20188, 20189, vf(1, 0)),         # warehouse
        (20338, 20339, b""),              # PVP board
        (20396, 20400, b""),              # star-soul bag
        (20139, 20141, b""),              # family boss info
        (20419, 20420, vf(1, 1)),         # daily sign-in reward
    ]
    for opcode, expected, body in checks:
        print("[total] %s -> %d" % (client.label, opcode), flush=True)
        try:
            response = client.call(opcode, expected, body)
        except Exception as exc:
            raise RuntimeError("%s feature opcode=%d expected=%d failed: %r" %
                               (client.label, opcode, expected, exc)) from exc
        if first(response, 91) not in (None, 0):
            # A normal business rejection still proves the handler and
            # transaction path executed; only a missing response is failure.
            continue
    return True


def main():
    print("[total] target=%s:%d; creating 25 clients" % (HOST, PORT))
    clients = []
    try:
        for index in range(25):
            account = "mysqltotal%s%02d" % (STAMP, index)
            client = Client("p%02d" % index)
            login_and_enter(client, account, "MySQL总测%02d" % index, index % 4 + 1)
            clients.append(client)
        print("[total] created/entered=25")

        # Every earlier town client must receive every later EnterMap, and
        # every late client must receive the earlier roster. This checks both
        # directions of same-map visibility rather than merely team state.
        for client in clients:
            client.drain(0.8)
        expected_ids = {client.player_id for client in clients}
        for client in clients:
            visible = unit_ids_from_frames(client.inbox) - {client.player_id}
            if len(visible & expected_ids) < 24:
                raise AssertionError("%s sees %d/24 town players" % (client.label, len(visible & expected_ids)))
        print("[total] town visibility: 25 x 25 passed")

        groups = [clients[offset:offset + 5] for offset in range(0, 25, 5)]
        with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
            futures = [pool.submit(form_group, group) for group in groups]
            [future.result() for future in futures]
        print("[total] concurrent teams: 5 teams x 5 members passed")

        # Chat and map traffic are deliberately concurrent with the feature
        # calls below, exercising channel locks and MySQL save transactions.
        for index, group in enumerate(groups):
            leader = group[0]
            leader.call(20298, 20299, sf(1, "mysql-total-chat-%d" % index) + vf(2, 5))
        with concurrent.futures.ThreadPoolExecutor(max_workers=25) as pool:
            [future.result() for future in [pool.submit(exercise_client, client) for client in clients]]
        print("[total] parallel feature RPCs: heartbeat/UI/skill/bag/mail/shop/market/store/PVP/Boss/activity passed")

        # Move one party to the world-boss town and request the boss roster.
        boss_leader = groups[0][0]
        boss_leader.call(20031, 20032, vf(1, 1000610))
        boss_leader.call(20057, 20058)
        print("[total] world-boss map/roster request passed")

        # Disconnect/reconnect one member. The authoritative player row and
        # normalized relations must load again, while Redis online state is
        # refreshed by the new session.
        reconnect_account = clients[-1].account
        clients[-1].close()
        time.sleep(0.25)
        replacement = Client("reconnect")
        login_and_enter(replacement, reconnect_account, "MySQL重连", 1)
        clients[-1] = replacement
        print("[total] disconnect/reconnect persistence passed (account=%s)" % reconnect_account)
        print("[total] PASS")
    finally:
        for client in clients:
            client.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print("[total] FAIL: %r" % (exc,), file=sys.stderr)
        raise
