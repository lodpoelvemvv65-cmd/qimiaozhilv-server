# -*- coding: utf-8 -*-
"""Real TCP regression: a field monster attack emits damage and HP updates."""

import socket
import struct
import os
import time


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


def frame(opcode, body):
    return struct.pack("<HH", len(body) + 2, opcode) + body


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
        field, wire = tag >> 3, tag & 7
        if wire == 0:
            value = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                value |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((field, wire, value))
        elif wire == 2:
            length = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                length |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((field, wire, body[offset:offset + length]))
            offset += length
        elif wire == 5:
            result.append((field, wire, struct.unpack("<f", body[offset:offset + 4])[0]))
            offset += 4
        else:
            raise AssertionError("unsupported wire type %d" % wire)
    return result


def value(body, field, wire=0):
    return next((item for number, item_wire, item in fields(body)
                 if number == field and item_wire == wire), None)


def signed32(value):
    """Decode protobuf int32 values (negative values arrive as 10-byte varints)."""
    if value is None:
        return None
    value &= 0xFFFFFFFF
    return value - 0x100000000 if value & 0x80000000 else value


def rpc_of(body):
    return value(body, 90)


class Client:
    def __init__(self):
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.rpc = 0
        self.inbox = []

    def close(self):
        self.sock.close()

    def recv(self, timeout=5):
        self.sock.settimeout(timeout)
        header = self.sock.recv(4)
        if len(header) != 4:
            raise ConnectionError("server closed")
        length, opcode = struct.unpack("<HH", header)
        body = b""
        while len(body) < length - 2:
            chunk = self.sock.recv(length - 2 - len(body))
            if not chunk:
                raise ConnectionError("server closed")
            body += chunk
        return opcode, body

    def drain(self, timeout=0.6):
        self.sock.settimeout(timeout)
        while True:
            try:
                self.inbox.append(self.recv(timeout))
            except (socket.timeout, ConnectionError):
                return

    def call(self, opcode, body=b""):
        self.rpc += 1
        rpc = self.rpc
        self.sock.sendall(frame(opcode, body + vf(90, rpc)))
        while True:
            response_opcode, response = self.recv()
            if rpc_of(response) == rpc:
                self.drain()
                return response_opcode, response
            self.inbox.append((response_opcode, response))

    def collect_for(self, seconds):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            try:
                self.inbox.append(self.recv(max(0.05, deadline - time.monotonic())))
            except (socket.timeout, ConnectionError):
                return


HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))

client = Client()
account = "monsterdamage%d" % time.time_ns()
try:
    opcode, body = client.call(20010, sf(1, account) + sf(2, "123456"))
    assert opcode == 20011 and not value(body, 91), "register failed"
    key, gate = value(body, 2), value(body, 3)
    print("LoginGate:", client.call(20014, vf(1, key) + vf(2, gate))[0])
    create_opcode, create_body = client.call(20016, vf(2, 1) + sf(3, "d" + account[-6:]))
    print("CreateRole:", create_opcode, fields(create_body))
    enter_opcode, enter_body = client.call(20027)
    player_id = value(enter_body, 1)
    print("EnterGame:", enter_opcode, fields(enter_body))
    # A newly-created role owns the profession basic skill after EnterGame,
    # but its nine persisted shortcut slots are intentionally empty. Install
    # the basic attack through the native drop-skill RPC before casting.
    slot_opcode, slot_body = client.call(20229, vf(1, 0) + vf(2, 100001))
    assert slot_opcode == 20230 and not value(slot_body, 92, 2), "failed to initialize skill slot"
    print("SkillSlot:", slot_opcode, fields(slot_body))
    print("EnterTrial:", client.call(20031, vf(1, 1000901))[0])
    client.collect_for(1.5)
    trial_inits = [body for opcode, body in client.inbox if opcode == 20091]
    assert trial_inits, "trial map initialization not received"
    battle_opcode, battle_response = client.call(20092)
    print("StartBattle:", battle_opcode, fields(battle_response))
    client.collect_for(1.0)
    monsters = []
    for opcode, body in client.inbox:
        if opcode != 20050:
            continue
        for field, wire, raw in fields(body):
            if field == 1 and wire == 2:
                monsters.append(value(raw, 1))
    monsters = {unit_id for unit_id in monsters if unit_id}
    if not monsters:
        print("battle inbox opcodes:", [opcode for opcode, _ in client.inbox])
    assert monsters, "battle did not contain monster units"
    # The native opening cadence is five seconds. Waiting here also lets the
    # trial scene settle before the first player action, so the server can
    # schedule and emit the monster response wave deterministically.
    time.sleep(5.2)
    skill_opcode, skill_response = client.call(20233, vf(1, 0))
    print("UseSkill:", skill_opcode, fields(skill_response))
    client.collect_for(3.5)
    # The current server uses the native 20075 PlaySkill envelope for both
    # player and monster presentations (the separate 20076 schema is kept for
    # older clients but is not emitted on this path).
    monster_plays = [body for opcode, body in client.inbox
                     if opcode == 20075 and value(body, 1) in monsters]
    monster_effects = [body for opcode, body in client.inbox
                       if opcode == 20077 and value(body, 1) in monsters and value(body, 4) == player_id]
    monster_damage = [body for opcode, body in client.inbox
                      if opcode == 20078 and value(body, 1) == player_id and signed32(value(body, 2)) < 0]
    monster_hp = [body for opcode, body in client.inbox
                  if opcode == 20169 and value(body, 1) == player_id and value(body, 2) == 1001]
    if not monster_plays:
        print("attack inbox opcodes:", [opcode for opcode, _ in client.inbox])
    assert monster_plays, "monster PlaySkill (20075) missing"
    assert monster_effects, "monster skill effect (20077) missing"
    assert monster_damage, "monster damage (20078) missing"
    assert monster_hp, "monster HP update (20169) missing"
    print("PASS monster TCP damage: units=%s play=%d effects=%d damage=%d hp=%d" %
          (sorted(monsters), len(monster_plays), len(monster_effects),
           len(monster_damage), len(monster_hp)))
finally:
    client.close()
