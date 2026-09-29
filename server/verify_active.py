# -*- coding: utf-8 -*-
"""Real TCP regression for all ActivePerDayConfig battle methods."""

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


class Client:
    def __init__(self):
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
                raise ConnectionError("server closed")
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
            readable, _, _ = select.select([self.sock], [], [], max(0, deadline - time.time()))
            if not readable:
                break
            self.inbox.append(self.recv(max(0.05, deadline - time.time())))
        if not predicate(self.inbox):
            raise AssertionError("timed out; opcodes=%r" % [opcode for opcode, _ in self.inbox])

    def take(self, opcode):
        matches = [body for item_opcode, body in self.inbox if item_opcode == opcode]
        self.inbox = [(item_opcode, body) for item_opcode, body in self.inbox
                      if item_opcode != opcode]
        return matches


def create_account(account, level):
    bootstrap = Client()
    try:
        response = bootstrap.call(20010, 20011, sf(1, account) + sf(2, PASSWORD))
        key, gate = value(response, 2), value(response, 3)
        bootstrap.call(20014, 20015, vf(1, key) + vf(2, gate))
        bootstrap.call(20016, 20017, vf(2, 1) + sf(3, account[-20:]))
        entered = bootstrap.call(20027, 20028)
        player_id = value(entered, 1)
        assert player_id
    finally:
        bootstrap.close()
    time.sleep(0.4)
    with sqlite3.connect(DB_PATH, timeout=10) as db:
        db.execute("UPDATE players SET level=?, exp=0, energy=2000 WHERE id=?", (level, player_id))
        db.commit()
    return player_id


def login(account, player_id):
    client = Client()
    response = client.call(20008, 20009, sf(1, account) + sf(2, PASSWORD))
    key, gate = value(response, 2), value(response, 3)
    client.call(20014, 20015, vf(1, key) + vf(2, gate))
    entered = client.call(20027, 20028)
    client.player_id = value(entered, 1)
    assert client.player_id == player_id
    client.send(20030)
    return client


def assert_success(response, label):
    assert value(response, 91) in (None, 0), "%s returned error" % label
    assert value(response, 92, 2) in (None, b""), "%s returned message=%r" % (
        label, value(response, 92, 2))


def take_roster(client):
    client.collect_until(lambda frames: any(opcode == 20050 for opcode, _ in frames))
    body = client.take(20050)[-1]
    assert value(body, 2) == 2  # ET.UnitType.Monster
    return [(value(item, 1), value(item, 2)) for item in values(body, 1, 2)]


def quit_battle(client, battle_type):
    client.send(20171)
    client.collect_until(lambda frames: any(opcode == 20055 for opcode, _ in frames))
    defeat = client.take(20055)[-1]
    assert value(defeat, 1) == battle_type
    client.collect_until(lambda frames: any(opcode == 20033 for opcode, _ in frames))
    client.take(20033)


account = "active_%d" % time.time_ns()
player_id = create_account(account, 6000)
client = login(account, player_id)
try:
    # Monday-Friday each expose one four-item star-soul group. Weekends expose
    # every group, so the first item remains valid.
    weekday = time.localtime().tm_wday  # Monday=0
    star_item = 10001 + weekday * 4 if weekday < 5 else 10001

    response = client.call(20409, 20410, vf(2, star_item))
    assert_success(response, "star soul")
    roster = take_roster(client)
    assert len(roster) == 10 and roster[0][1] == 71001
    assert all(monster_id == 70001 for _, monster_id in roster[1:])
    quit_battle(client, 23)

    response = client.call(20409, 20410, vf(2, 10022))
    assert_success(response, "world boss")
    roster = take_roster(client)
    assert len(roster) == 3 and all(monster_id == 81001 for _, monster_id in roster)
    quit_battle(client, 24)

    response = client.call(20409, 20410, vf(2, 10023))
    assert_success(response, "journey of death")
    roster = take_roster(client)
    assert roster and roster[0][1] == 90001
    quit_battle(client, 25)

    response = client.call(20409, 20410, vf(2, 10021))
    assert_success(response, "idle battle")
    roster = take_roster(client)
    assert roster
    end_response = client.call(20089, 20090)
    assert_success(end_response, "end idle battle")
    client.collect_until(lambda frames: any(opcode == 20033 for opcode, _ in frames))
    client.take(20033)

    # The next difficulty is gated by the online ActivePerDayConfig.Params.
    response = client.call(20409, 20410, vf(2, 10024))
    assert value(response, 92, 2), "level-gated death-road entry unexpectedly succeeded"
finally:
    client.close()

time.sleep(0.4)
with sqlite3.connect(DB_PATH, timeout=10) as db:
    energy = db.execute("SELECT energy FROM players WHERE id=?", (player_id,)).fetchone()[0]
assert energy == 1987, "activity energy=%d, want 2000-10-3" % energy

print("PASS activity TCP: player=%d star=%d world=3x81001 death=90001 idle energy=%d" %
      (player_id, star_item, energy))

# Reproduce the original cross-battle failure over the real protocol: a low-level
# player dies in star-soul combat, then immediately tries to enter world boss.
defeated_account = "active_dead_%d" % time.time_ns()
defeated_player_id = create_account(defeated_account, 1)
defeated = login(defeated_account, defeated_player_id)
try:
    response = defeated.call(20409, 20410, vf(2, star_item))
    assert_success(response, "low-level star soul")
    take_roster(defeated)
    defeated.inbox.clear()

    response = defeated.call(20233, 20234, vf(1, 0))
    assert_success(response, "low-level basic attack")
    assert any(opcode == 20055 for opcode, _ in defeated.inbox), \
        "low-level player was not defeated by the online star-soul roster"
    # Proto3 omits a scalar field whose value is zero, so an absent field 3 is
    # the wire representation of authoritative HP=0 here.
    assert any(opcode == 20169 and value(body, 2) == 1001 and (value(body, 3, 5) or 0) == 0
               for opcode, body in defeated.inbox), \
        "defeat did not push authoritative HP=0 before settlement"
    defeated.inbox.clear()

    response = defeated.call(20409, 20410, vf(2, 10022))
    assert value(response, 92, 2) == "生命值不足，请先恢复".encode("utf-8"), \
        "defeated world-boss entry message=%r" % value(response, 92, 2)
    forbidden = [opcode for opcode, _ in defeated.inbox if opcode in (20033, 20047, 20050)]
    assert not forbidden, "defeated world-boss entry changed map or initialized battle: %r" % forbidden
finally:
    defeated.close()

print("PASS defeated-entry TCP: player=%d hp=0 world-boss rejected before map/battle init" %
      defeated_player_id)
