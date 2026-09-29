# -*- coding: utf-8 -*-
"""Real TCP regression for daily sign-in and SQLite persistence."""

import json
import socket
import sqlite3
import struct
import time
from datetime import datetime, timezone
from pathlib import Path


HOST, PORT = "127.0.0.1", 7756
PASSWORD = "123456"
ROOT = Path(__file__).resolve().parent.parent
DB_PATH = ROOT / "server" / "data" / "mhq.db"
DAILY_PATH = ROOT / "datatable_json" / "SignInRewardConfig.json"


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
        key = shift = 0
        while True:
            byte = raw[offset]
            offset += 1
            key |= (byte & 0x7F) << shift
            if not byte & 0x80:
                break
            shift += 7
        field, wire = key >> 3, key & 7
        if wire == 0:
            item = shift = 0
            while True:
                byte = raw[offset]
                offset += 1
                item |= (byte & 0x7F) << shift
                if not byte & 0x80:
                    break
                shift += 7
        elif wire == 2:
            length = shift = 0
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
        fields.append((field, wire, item))
    return fields


def value(raw, field, wire=0):
    for number, field_wire, item in decode(raw):
        if number == field and field_wire == wire:
            return item
    return None


class Client:
    def __init__(self):
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.sock.settimeout(5)
        self.rpc = 0
        self.inbox = []

    def close(self):
        self.sock.close()

    def send(self, opcode, body=b""):
        self.sock.sendall(struct.pack("<HH", len(body) + 2, opcode) + body)

    def recv(self):
        header = self._exact(4)
        length, opcode = struct.unpack("<HH", header)
        return opcode, self._exact(length - 2)

    def _exact(self, length):
        result = b""
        while len(result) < length:
            chunk = self.sock.recv(length - len(result))
            if not chunk:
                raise ConnectionError("server closed")
            result += chunk
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


def current_online_row():
    pairs = json.loads(DAILY_PATH.read_text(encoding="utf-8"))
    rows = [pair[1] for pair in pairs]
    now = datetime.now(timezone.utc)
    exact = [row for row in rows if row["Year"] == now.year and
             row["Month"] == now.month and row["Day"] == now.day]
    if exact:
        return exact[0]
    templates = [row for row in rows if row["Month"] == now.month and row["Day"] == now.day]
    if templates:
        return max(templates, key=lambda row: row["Year"])
    nearest = [row for row in rows if row["Month"] == now.month]
    return min(nearest, key=lambda row: (abs(row["Day"] - now.day), -row["Year"]))


def create_account(account):
    client = Client()
    try:
        response = client.call(20010, 20011, sf(1, account) + sf(2, PASSWORD))
        key, gate = value(response, 2), value(response, 3)
        client.call(20014, 20015, vf(1, key) + vf(2, gate))
        client.call(20016, 20017, vf(2, 1) + sf(3, account[-20:]))
        entered = client.call(20027, 20028)
        return value(entered, 1)
    finally:
        client.close()


def login(account, player_id):
    client = Client()
    response = client.call(20008, 20009, sf(1, account) + sf(2, PASSWORD))
    key, gate = value(response, 2), value(response, 3)
    client.call(20014, 20015, vf(1, key) + vf(2, gate))
    entered = client.call(20027, 20028)
    assert value(entered, 1) == player_id
    client.send(20030)
    while not any(opcode == 20423 for opcode, _ in client.inbox):
        client.inbox.append(client.recv())
    return client


row = current_online_row()
config_id = row["_id"]
account = "signin_%d" % time.time_ns()
player_id = create_account(account)
time.sleep(0.3)
client = login(account, player_id)
try:
    initial = [body for opcode, body in client.inbox if opcode == 20423][-1]
    assert value(initial, 1) == config_id, (value(initial, 1), config_id)
    assert value(initial, 2) in (None, 0)

    rejected = client.call(20419, 20420, vf(1, config_id + 1))
    assert value(rejected, 92, 2), "wrong config id was accepted"

    accepted = client.call(20419, 20420, vf(1, config_id))
    assert value(accepted, 91) in (None, 0) and value(accepted, 92, 2) in (None, b"")
    active_frames = [body for opcode, body in client.inbox if opcode == 20423]
    assert active_frames and value(active_frames[-1], 1) == config_id
    assert value(active_frames[-1], 2) == 1 and value(active_frames[-1], 4) == 1

    repeated = client.call(20419, 20420, vf(1, config_id))
    assert value(repeated, 92, 2), "duplicate daily sign-in was accepted"
finally:
    client.close()

time.sleep(0.4)
with sqlite3.connect(DB_PATH, timeout=10) as db:
    raw = db.execute("SELECT signin_json FROM players WHERE id=?", (player_id,)).fetchone()[0]
state = json.loads(raw)
today = datetime.now(timezone.utc).strftime("%Y%m%d")
assert state["day"] == today and state["mc"] == 1, state

reward_text = ", ".join("%s x%s" % (item["_Id"], item["Count"]) for item in row["RewardsArr"])
print("PASS sign-in TCP: player=%d config=%d reward=%s persisted=%s" %
      (player_id, config_id, reward_text, state["day"]))
