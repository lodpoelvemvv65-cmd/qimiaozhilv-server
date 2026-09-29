# -*- coding: utf-8 -*-
"""Three real TCP clients verify the native manual-equipment dungeon flow."""

import os
import select
import socket
import struct
import time


HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
TEAM_SIZE = 3


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
            item = 0
            shift = 0
            while True:
                byte = raw[offset]
                offset += 1
                item |= (byte & 0x7F) << shift
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
            item = raw[offset:offset + length]
            offset += length
        elif wire == 5:
            item = raw[offset:offset + 4]
            offset += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire)
        result.append((field, wire, item))
    return result


def value(raw, field, wire=0):
    for number, field_wire, item in decode(raw):
        if number == field and field_wire == wire:
            return item
    return None


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


def float_value(raw, field):
    encoded = value(raw, field, 5)
    return None if encoded is None else struct.unpack("<f", encoded)[0]


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

    def messages(self, opcode):
        return [body for item_opcode, body in self.inbox if item_opcode == opcode]


def register_and_enter(client, suffix):
    unique = "%d%s" % (time.time_ns(), suffix)
    account = "me" + unique
    response = client.call(20010, 20011, sf(1, account) + sf(2, "CorrectPass123"))
    assert value(response, 91) in (None, 0), decode(response)
    key, gate = value(response, 2), value(response, 3)
    assert key and gate is not None, "register key/gate missing"
    client.call(20014, 20015, vf(1, key) + vf(2, gate))
    role_name = "M%d%s" % (time.time_ns() % 1000000000, suffix)
    client.call(20016, 20017, vf(2, 1) + sf(3, role_name))
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
        invite = member.messages(20159)[-1]
        assert value(invite, 1) == leader.player_id
        accepted = member.call(20160, 20161, bf(1, vf(1, leader.player_id) + vf(2, 1)))
        assert value(accepted, 92, 2) is None, decode(accepted)

    expected_ids = [client.player_id for client in clients]
    for client in clients:
        client.collect_until(lambda frames: has_team_size(frames, TEAM_SIZE))
        snapshots = [body for opcode, body in client.inbox
                     if opcode == 20162 and len(repeated_varints(body, 2)) == TEAM_SIZE]
        assert value(snapshots[-1], 1) == leader.player_id
        assert repeated_varints(snapshots[-1], 2) == expected_ids

    leader.call(20031, 20032, vf(1, 1000401))
    for client in clients:
        client.collect_until(lambda frames: any(
            opcode == 20033 and value(body, 4) in (10004, 1000401)
            for opcode, body in frames))
        client.inbox = []

    entered = leader.call(20031, 20032, vf(1, 1003301))
    assert value(entered, 92, 2) is None, decode(entered)
    field_ids = []
    for client in clients:
        client.collect_until(lambda frames: any(opcode == 20033 for opcode, _ in frames) and
                             any(opcode == 20320 for opcode, _ in frames))
        changes = client.messages(20033)
        fields = client.messages(20320)
        assert value(changes[-1], 4) == 1003301, "%s wrong manual map" % client.label
        assert len(fields) == 1, "%s field count=%d" % (client.label, len(fields))
        field = fields[0]
        assert value(field, 4) == 1001 and value(field, 5) == 2, decode(field)
        assert abs((float_value(field, 2) or 0.0) - 0.0) < 0.001
        assert abs(float_value(field, 3) - (-1.2)) < 0.001
        field_ids.append(value(field, 1))

    started = leader.call(20391, 20392, vf(1, field_ids[0]) + vf(2, 2))
    assert value(started, 92, 2) is None, decode(started)
    for client in clients:
        client.collect_until(lambda frames: any(opcode == 20099 for opcode, _ in frames))
        presentation = client.messages(20099)[-1]
        config_ids = [value(unit, 2) for unit in values(presentation, 2, 2)]
        assert config_ids == [1101, 1102, 1103], \
            "%s manual formation=%r" % (client.label, config_ids)

    print("PASS manual equipment TCP: players=%r field=1001 formation=[1101,1102,1103]" %
          expected_ids)
finally:
    for client in clients:
        try:
            client.send(20171)
        except OSError:
            pass
        client.close()
