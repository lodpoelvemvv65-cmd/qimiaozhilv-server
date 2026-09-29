# -*- coding: utf-8 -*-
"""Five real TCP clients: team sync, shared combat, skill, HP and settlement."""

import select
import socket
import sqlite3
import struct
import time
from pathlib import Path


HOST, PORT = "127.0.0.1", 7756
PASSWORD = "123456"
DB_PATH = Path(__file__).resolve().parent / "data" / "mhq.db"


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
            item = struct.unpack("<f", raw[offset:offset + 4])[0]
            offset += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire)
        fields.append((field, wire, item))
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
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.sock.settimeout(5)
        self.rpc = 0
        self.inbox = []
        self.player_id = 0

    def close(self):
        try:
            self.sock.close()
        except OSError:
            pass

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
        header = self._exact(4)
        length, opcode = struct.unpack("<HH", header)
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

    def drain_for(self, timeout=0.1):
        deadline = time.time() + timeout
        while time.time() < deadline:
            readable, _, _ = select.select([self.sock], [], [], max(0, deadline - time.time()))
            if not readable:
                break
            self.inbox.append(self.recv(max(0.05, deadline - time.time())))

    def take(self, opcode):
        matches = [body for item_opcode, body in self.inbox if item_opcode == opcode]
        self.inbox = [(item_opcode, body) for item_opcode, body in self.inbox
                      if item_opcode != opcode]
        return matches


def create_account(account, job):
    client = Client("bootstrap-%s" % account)
    try:
        response = client.call(20010, 20011, sf(1, account) + sf(2, PASSWORD))
        key, gate = value(response, 2), value(response, 3)
        assert key and gate is not None, "register key/gate missing"
        client.call(20014, 20015, vf(1, key) + vf(2, gate))
        client.call(20016, 20017, vf(2, job) + sf(3, account[-20:]))
        entered = client.call(20027, 20028)
        player_id = value(entered, 1)
        assert player_id, "created player id missing"
        return player_id
    finally:
        client.close()


def elevate_players(player_ids):
    # Closed bootstrap sessions have saved first. Raising only these unique
    # test ids keeps the production database otherwise untouched.
    time.sleep(0.4)
    placeholders = ",".join("?" for _ in player_ids)
    with sqlite3.connect(DB_PATH, timeout=10) as db:
        db.execute("UPDATE players SET level=4000, exp=0, energy=2000 "
                   "WHERE id IN (%s)" % placeholders, player_ids)
        db.commit()


def login_and_enter(account, label):
    client = Client(label)
    response = client.call(20008, 20009, sf(1, account) + sf(2, PASSWORD))
    key, gate = value(response, 2), value(response, 3)
    assert key and gate is not None, "%s login key/gate missing" % label
    client.call(20014, 20015, vf(1, key) + vf(2, gate))
    entered = client.call(20027, 20028)
    client.player_id = value(entered, 1)
    assert client.player_id, "%s player id missing" % label
    client.send(20030)
    client.collect_until(lambda frames: any(opcode == 20162 for opcode, _ in frames))
    client.take(20162)
    return client


def team_snapshot(client, leader_id, member_ids):
    client.collect_until(lambda frames: any(opcode == 20162 for opcode, _ in frames))
    snapshot = client.take(20162)[-1]
    assert value(snapshot, 1) == leader_id, "%s leader mismatch" % client.label
    assert repeated_varints(snapshot, 2) == member_ids, "%s team mismatch: %r" % (
        client.label, repeated_varints(snapshot, 2))


def battle_units(client):
    client.collect_until(lambda frames: any(opcode == 20050 for opcode, _ in frames))
    body = client.take(20050)[-1]
    return [(value(item, 1), value(item, 2)) for item in values(body, 1, 2)]


stamp = str(time.time_ns())
accounts = ["team5_%s_%d" % (stamp, index) for index in range(5)]
player_ids = [create_account(account, index % 4 + 1)
              for index, account in enumerate(accounts)]
elevate_players(player_ids)

clients = []
try:
    clients = [login_and_enter(account, "member-%d" % (index + 1))
               for index, account in enumerate(accounts)]
    assert [client.player_id for client in clients] == player_ids
    leader = clients[0]

    # Invite and accept four members. After every join, every current member
    # must receive the same full snapshot, not only the most recent client.
    for index, target in enumerate(clients[1:], start=1):
        leader.call(20156, 20157, vf(1, target.player_id))
        target.collect_until(lambda frames: any(opcode == 20159 for opcode, _ in frames))
        invite = target.take(20159)[-1]
        assert value(invite, 1) == leader.player_id
        handle = bf(1, vf(1, leader.player_id) + vf(2, 1))
        target.call(20160, 20161, handle)
        current_ids = player_ids[:index + 1]
        for member in clients[:index + 1]:
            team_snapshot(member, leader.player_id, current_ids)

    # Leader moves the full party, then starts the online layer-1 roster.
    time.sleep(1.1)
    leader.call(20031, 20032, vf(1, 1000601))
    for member in clients[1:]:
        member.collect_until(lambda frames: any(
            opcode == 20033 and value(body, 4) == 1000601 for opcode, body in frames))
        member.take(20033)
    time.sleep(0.2)
    leader.call(20048, 20049, vf(1, 1001))
    rosters = [battle_units(member) for member in clients]
    assert rosters[0] and all(roster == rosters[0] for roster in rosters), rosters
    assert len(rosters[0]) == 2 and all(monster_id == 10001 for _, monster_id in rosters[0])

    # The fifth member casts first. Every client must see the same caster,
    # target, damage event and absolute monster HP update.
    fifth = clients[4]
    fifth.call(20233, 20234, vf(1, 0))
    monster_ids = {unit_id for unit_id, _ in rosters[0]}
    for member in clients:
        member.collect_until(lambda frames: (
            any(opcode == 20075 for opcode, _ in frames) and
            any(opcode == 20078 for opcode, _ in frames) and
            any(opcode == 20169 and value(body, 1) in monster_ids and value(body, 2) == 1001
                for opcode, body in frames)))
        plays = member.take(20075)
        damages = member.take(20078)
        numerics = member.take(20169)
        assert any(value(body, 1) == fifth.player_id and value(body, 4) in monster_ids
                   for body in plays), "%s missing fifth member skill" % member.label
        assert any(value(body, 1) in monster_ids and value(body, 2) is not None
                   for body in damages), "%s missing monster damage" % member.label
        assert any(value(body, 1) in monster_ids and value(body, 2) == 1001
                   for body in numerics), "%s missing absolute monster HP" % member.label

    # Continue with different party members until the shared encounter emits
    # one victory to every endpoint.
    for caster in clients:
        for member in clients:
            member.drain_for()
        if all(any(opcode == 20054 for opcode, _ in member.inbox) for member in clients):
            break
        caster.call(20233, 20234, vf(1, 0))
        time.sleep(1.05)
    for member in clients:
        member.collect_until(lambda frames: any(opcode == 20054 for opcode, _ in frames))
        victories = member.take(20054)
        assert len(victories) == 1, "%s victory count=%d" % (member.label, len(victories))
        assert value(victories[0], 1) == 1, "%s wrong battle type" % member.label

    # A disconnected member is removed from the remaining four snapshots.
    fifth_id = fifth.player_id
    for member in clients[:4]:
        member.take(20162)  # battle preparation sent a pre-disconnect snapshot
    fifth.close()
    clients = clients[:4]
    remaining_ids = player_ids[:4]
    for member in clients:
        team_snapshot(member, leader.player_id, remaining_ids)

    print("PASS five-player TCP: team=%r roster=%r fifth=%d skill/damage/HP/victory/disconnect" %
          (player_ids, rosters[0], fifth_id))
finally:
    for client in clients:
        client.close()
