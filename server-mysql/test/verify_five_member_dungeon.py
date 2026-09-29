# -*- coding: utf-8 -*-
"""Five real TCP clients verify team enrollment and space-travel quota charging."""

import os
import select
import socket
import struct
import sys
import time


sys.stdout.reconfigure(encoding="utf-8", errors="replace")
HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
TEAM_SIZE = 5


def varint(value):
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


def bf(field, raw):
    return varint((field << 3) | 2) + varint(len(raw)) + raw


def decode(raw):
    result = []
    offset = 0
    while offset < len(raw):
        key = 0
        shift = 0
        while True:
            byte = raw[offset]
            offset += 1
            key |= (byte & 0x7F) << shift
            if not byte & 0x80:
                break
            shift += 7
        field, wire = key >> 3, key & 7
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
            value = raw[offset:offset + length]
            offset += length
        elif wire == 5:
            value = raw[offset:offset + 4]
            offset += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire)
        result.append((field, wire, value))
    return result


def value(raw, field, wire=0):
    for number, field_wire, item in decode(raw):
        if number == field and field_wire == wire:
            return item
    return None


def float_value(raw, field):
    encoded = value(raw, field, 5)
    return 0.0 if encoded is None else struct.unpack("<f", encoded)[0]


def values(raw, field, wire=None):
    return [item for number, field_wire, item in decode(raw)
            if number == field and (wire is None or field_wire == wire)]


def repeated_varints(raw, field):
    result = values(raw, field, 0)
    for packed in values(raw, field, 2):
        offset = 0
        while offset < len(packed):
            item = 0
            shift = 0
            while True:
                byte = packed[offset]
                offset += 1
                item |= (byte & 0x7F) << shift
                if not byte & 0x80:
                    break
                shift += 7
            result.append(item)
    return result


class Client:
    def __init__(self, label):
        self.label = label
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.sock.settimeout(5)
        self.rpc = 0
        self.inbox = []
        self.player_id = 0

    def close(self):
        self.sock.close()

    def send(self, opcode, body=b""):
        self.sock.sendall(struct.pack("<HH", len(body) + 2, opcode) + body)

    def _exact(self, length):
        result = b""
        while len(result) < length:
            part = self.sock.recv(length - len(result))
            if not part:
                raise ConnectionError("server closed %s" % self.label)
            result += part
        return result

    def recv(self, timeout=5):
        self.sock.settimeout(timeout)
        length, opcode = struct.unpack("<HH", self._exact(4))
        return opcode, self._exact(length - 2)

    def call(self, opcode, expected, body=b""):
        self.rpc += 1
        rpc = self.rpc
        self.send(opcode, body + vf(90, rpc))
        while True:
            response_opcode, response = self.recv()
            if response_opcode == expected and value(response, 90) == rpc:
                return response
            self.inbox.append((response_opcode, response))

    def collect_until(self, predicate, timeout=5):
        deadline = time.time() + timeout
        while not predicate(self.inbox) and time.time() < deadline:
            remaining = deadline - time.time()
            readable, _, _ = select.select([self.sock], [], [], max(0, remaining))
            if not readable:
                break
            self.inbox.append(self.recv(max(0.05, remaining)))
        if not predicate(self.inbox):
            raise AssertionError("%s timed out; opcodes=%s" %
                                 (self.label, [opcode for opcode, _ in self.inbox]))

    def take(self, opcode):
        matches = [body for item_opcode, body in self.inbox if item_opcode == opcode]
        self.inbox = [(item_opcode, body) for item_opcode, body in self.inbox
                      if item_opcode != opcode]
        return matches


def register_and_enter(client, suffix):
    unique = "%d%s" % (time.time_ns(), suffix)
    account = "dq" + unique
    response = client.call(20010, 20011, sf(1, account) + sf(2, "CorrectPass123"))
    assert value(response, 91) in (None, 0), decode(response)
    key, gate = value(response, 2), value(response, 3)
    assert key and gate is not None, "register key/gate missing"
    client.call(20014, 20015, vf(1, key) + vf(2, gate))
    created = client.call(20016, 20017, vf(2, 1) + sf(3, "dq" + unique[-8:]))
    assert value(created, 91) in (None, 0), decode(created)
    entered = client.call(20027, 20028)
    client.player_id = value(entered, 1)
    assert client.player_id, "enter game player id missing"
    client.send(20030)


def has_team_size(frames, size):
    return any(opcode == 20162 and len(repeated_varints(body, 2)) == size
               for opcode, body in frames)


clients = [Client("member-%d" % (index + 1)) for index in range(TEAM_SIZE)]
try:
    for index, client in enumerate(clients):
        register_and_enter(client, str(index + 1))

    leader = clients[0]
    for member in clients[1:]:
        leader.call(20156, 20157, vf(1, member.player_id))
        member.collect_until(lambda frames: any(opcode == 20159 for opcode, _ in frames))
        invite = member.take(20159)[-1]
        assert value(invite, 1) == leader.player_id
        handle = bf(1, vf(1, leader.player_id) + vf(2, 1))
        accepted = member.call(20160, 20161, handle)
        assert value(accepted, 92, 2) is None, decode(accepted)

    expected_ids = [client.player_id for client in clients]
    for client in clients:
        client.collect_until(lambda frames: has_team_size(frames, TEAM_SIZE))
        snapshots = [body for opcode, body in client.inbox
                     if opcode == 20162 and len(repeated_varints(body, 2)) == TEAM_SIZE]
        assert snapshots, "%s missing five-member snapshot" % client.label
        assert value(snapshots[-1], 1) == leader.player_id
        assert repeated_varints(snapshots[-1], 2) == expected_ids

    for client in clients:
        client.inbox.clear()
    denied = clients[1].call(20031, 20032, vf(1, 1003901))
    denied_message = value(denied, 92, 2)
    assert denied_message and denied_message.decode("utf-8") == "只有队长可以进入副本", decode(denied)
    assert not any(opcode == 20033 for opcode, _ in clients[1].inbox), \
        "regular member changed map despite dungeon-entry rejection"

    time.sleep(1.1)
    entered = leader.call(20031, 20032, vf(1, 1003901))
    assert value(entered, 92, 2) is None, decode(entered)
    spawn_positions = {}
    for client in clients:
        client.collect_until(lambda frames: any(
            opcode == 20033 and value(body, 4) == 1003901 for opcode, body in frames))
        changes = [body for opcode, body in client.inbox
                   if opcode == 20033 and value(body, 4) == 1003901]
        assert changes, "%s missing dungeon ChangeMap" % client.label
        spawn_positions[client.player_id] = (float_value(changes[-1], 2), float_value(changes[-1], 3))
        client.inbox.clear()

    leader_position = spawn_positions[leader.player_id]
    assert all(position == leader_position for position in spawn_positions.values()), \
        "dungeon party positions differ: %r" % spawn_positions

    started = leader.call(20391, 20392, vf(1, 1) + vf(2, 0))
    assert value(started, 92, 2) is None, decode(started)
    for client in clients:
        client.collect_until(lambda frames: any(opcode == 20050 for opcode, _ in frames))
        battle_index = next(index for index, (opcode, _) in enumerate(client.inbox)
                            if opcode == 20050)
        batches = [body for opcode, body in client.inbox[:battle_index] if opcode == 20170]
        by_unit = {}
        for body in batches:
            unit_id = value(body, 1)
            if unit_id not in expected_ids:
                continue
            assert value(body, 93) == client.player_id, \
                "%s batch actor mismatch for %d" % (client.label, unit_id)
            attributes = values(body, 2, 2)
            keys = {value(attribute, 1) for attribute in attributes}
            assert len(attributes) == 39 and len(keys) == 39, \
                "%s player %d incomplete numeric batch: entries=%d keys=%d" % \
                (client.label, unit_id, len(attributes), len(keys))
            assert {1001, 1002, 1003, 1004}.issubset(keys), \
                "%s player %d batch missing HP/MP fields" % (client.label, unit_id)
            by_unit[unit_id] = by_unit.get(unit_id, 0) + 1
        assert by_unit == {unit_id: 1 for unit_id in expected_ids}, \
            "%s pre-battle numeric batches=%r" % (client.label, by_unit)
        client.take(20050)

    for client in clients:
        client.take(20262)
        client.send(20045, vf(90, 900 + int(client.player_id % 50)))
        client.collect_until(lambda frames: any(opcode == 20262 for opcode, _ in frames))
        tip = client.take(20262)[-1]
        message = value(tip, 1, 2).decode("utf-8")
        assert "时空旅行战斗次数：49" in message, "%s tip=%r" % (client.label, message)

    print("PASS five-member TCP dungeon: players=%r shared-position=%r leader-only entry, "
          "batched attributes, all entered battle and quotas are 49" %
          (expected_ids, spawn_positions))
finally:
    for client in clients:
        try:
            client.send(20171)
        except OSError:
            pass
        client.close()
