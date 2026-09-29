# -*- coding: utf-8 -*-
"""Two real TCP sessions: visibility, team, layer-10 boss and shared damage."""

import select
import socket
import struct
import time


HOST, PORT = "127.0.0.1", 7756


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
    fields = []
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
            fields.append((field, wire, value))
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
            fields.append((field, wire, raw[offset:offset + length]))
            offset += length
        elif wire == 5:
            fields.append((field, wire, struct.unpack("<f", raw[offset:offset + 4])[0]))
            offset += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire)
    return fields


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


class Client:
    def __init__(self, label):
        self.label = label
        self.sock = socket.create_connection((HOST, PORT), timeout=3)
        self.sock.settimeout(3)
        self.rpc = 0
        self.inbox = []
        self.player_id = 0

    def close(self):
        self.sock.close()

    def send(self, opcode, body=b""):
        self.sock.sendall(struct.pack("<HH", len(body) + 2, opcode) + body)

    def recv(self, timeout=3):
        self.sock.settimeout(timeout)
        header = self._exact(4)
        length, opcode = struct.unpack("<HH", header)
        return opcode, self._exact(length - 2)

    def _exact(self, length):
        result = b""
        while len(result) < length:
            part = self.sock.recv(length - len(result))
            if not part:
                raise ConnectionError("server closed %s" % self.label)
            result += part
        return result

    def call(self, opcode, expected, body=b""):
        self.rpc += 1
        rpc = self.rpc
        self.send(opcode, body + vf(90, rpc))
        while True:
            response_opcode, response = self.recv()
            if response_opcode == expected and value(response, 90) == rpc:
                return response
            self.inbox.append((response_opcode, response))

    def collect_until(self, predicate, timeout=3):
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


def register_and_enter(client, suffix, job):
    account = "multi_%s_%d" % (suffix, time.time_ns())
    response = client.call(20010, 20011, sf(1, account) + sf(2, "123456"))
    key, gate = value(response, 2), value(response, 3)
    assert key and gate is not None, "register key/gate missing"
    client.call(20014, 20015, vf(1, key) + vf(2, gate))
    client.call(20016, 20017, vf(2, job) + sf(3, account[-20:]))
    entered = client.call(20027, 20028)
    client.player_id = value(entered, 1)
    assert client.player_id, "enter game player id missing"
    client.send(20030)
    client.collect_until(lambda frames: any(opcode == 20162 for opcode, _ in frames))
    client.take(20162)


def team_snapshot(client, expected_leader, expected_members):
    client.collect_until(lambda frames: any(opcode == 20162 for opcode, _ in frames))
    snapshots = client.take(20162)
    body = snapshots[-1]
    assert value(body, 1) == expected_leader
    assert repeated_varints(body, 2) == expected_members


def boss_units(client):
    client.collect_until(lambda frames: any(opcode == 20050 for opcode, _ in frames))
    body = client.take(20050)[-1]
    return [(value(item, 1), value(item, 2)) for item in values(body, 1, 2)]


first, second = Client("first"), Client("second")
try:
    register_and_enter(first, "a", 1)
    register_and_enter(second, "b", 3)

    # Both login directions must create the other player's scene unit.
    first.collect_until(lambda frames: any(opcode == 20034 for opcode, _ in frames))
    first_entries = first.take(20034)
    assert any(value(body, 2, 2) and value(value(body, 2, 2), 1) == second.player_id
               for body in first_entries), "earlier player did not receive later player EnterMap"

    # Invite and accept. The target must receive 20159 and both receive one
    # authoritative 20162 snapshot with the same member order.
    first.call(20156, 20157, vf(1, second.player_id))
    second.collect_until(lambda frames: any(opcode == 20159 for opcode, _ in frames))
    invite = second.take(20159)[-1]
    assert value(invite, 1) == first.player_id
    assert value(invite, 2) == 30000, "invite timeout must be client milliseconds"
    handle = bf(1, vf(1, first.player_id) + vf(2, 1))
    second.call(20160, 20161, handle)  # IsRequest=false: accept an invitation.
    expected_members = [first.player_id, second.player_id]
    team_snapshot(first, first.player_id, expected_members)
    team_snapshot(second, first.player_id, expected_members)

    # Chat is locally echoed by the client. The sender must not receive a
    # second 20300 from the server, while the other online player does.
    first.call(20298, 20299, sf(1, "multiplayer-chat") + vf(2, 5))
    second.collect_until(lambda frames: any(opcode == 20300 for opcode, _ in frames))
    assert not first.take(20300), "chat sender received a duplicate server echo"
    assert second.take(20300), "other player did not receive chat broadcast"

    # Move the complete team to the online beach layer-10 boss map. The
    # second move also exercises late same-map visibility after map rebuild.
    time.sleep(1.1)  # map-change debounce protects login's initial scene restore
    first.call(20031, 20032, vf(1, 1000610))
    second.collect_until(lambda frames: any(opcode == 20033 and value(body, 4) == 1000610
                                             for opcode, body in frames))
    second_map_frames = second.take(20033)
    assert any(value(body, 4) == 1000610 for body in second_map_frames), \
        "leader map change did not carry teammate: %r" % [decode(body) for body in second_map_frames]
    time.sleep(0.2)

    first.call(20048, 20049, vf(1, 1010))
    first_boss = boss_units(first)
    second_boss = boss_units(second)
    assert first_boss and first_boss == second_boss, \
        "boss presentation mismatch: %r != %r" % (first_boss, second_boss)
    boss_unit_id = first_boss[0][0]

    second.call(20233, 20234, vf(1, 0))
    for client in (first, second):
        client.collect_until(lambda frames: (
            any(opcode == 20075 for opcode, _ in frames) and
            any(opcode == 20078 for opcode, _ in frames) and
            any(opcode == 20169 and value(body, 1) == boss_unit_id and value(body, 2) == 1001
                for opcode, body in frames)))
        plays = client.take(20075)
        damages = client.take(20078)
        numerics = client.take(20169)
        assert any(value(body, 1) == second.player_id and value(body, 4) == boss_unit_id
                   for body in plays), "%s missing teammate skill" % client.label
        assert any(value(body, 1) == boss_unit_id for body in damages), \
            "%s missing boss damage" % client.label
        hp_values = [value(body, 3, 5) for body in numerics
                     if value(body, 1) == boss_unit_id and value(body, 2) == 1001]
        assert hp_values, "%s missing absolute boss HP" % client.label

    # Actor-message quit has no RPC response. Both clients must still receive
    # BattleEnd so IsFight and movement state are restored.
    first.send(20171)
    second.send(20171)
    for client in (first, second):
        client.collect_until(lambda frames: any(opcode == 20055 for opcode, _ in frames))

    print("PASS multiplayer TCP: players=%d/%d team=%r boss=%r shared skill/damage/HP and exit" %
          (first.player_id, second.player_id, expected_members, first_boss))
finally:
    first.close()
    second.close()
