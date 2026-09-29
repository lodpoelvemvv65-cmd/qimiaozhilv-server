"""Real TCP verification for energy pricing, deduction and persistence."""
import os
import socket
import sqlite3
import struct
import time

HOST = os.environ.get("MHQ_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_PORT", "7756"))
DB = os.path.abspath(os.environ.get("MHQ_DB", "data/mhq.db"))
os.chdir(os.path.dirname(os.path.abspath(__file__)))


def varint(value):
    out = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        out.append(byte | (0x80 if value else 0))
        if not value:
            return bytes(out)


def vf(tag, value):
    return varint(tag << 3) + varint(value)


def sf(tag, value):
    raw = value.encode()
    return varint((tag << 3) | 2) + varint(len(raw)) + raw


def frame(opcode, body):
    return struct.pack("<HH", len(body) + 2, opcode) + body


def recv_one(sock):
    header = sock.recv(4)
    if len(header) != 4:
        raise ConnectionError("server closed")
    total, opcode = struct.unpack("<HH", header)
    body = b""
    while len(body) < total - 2:
        chunk = sock.recv(total - 2 - len(body))
        if not chunk:
            raise ConnectionError("server closed")
        body += chunk
    return opcode, body


def fields(body):
    result = []
    index = 0
    while index < len(body):
        token = 0
        shift = 0
        while True:
            byte = body[index]
            index += 1
            token |= (byte & 0x7F) << shift
            shift += 7
            if not byte & 0x80:
                break
        tag, wire = token >> 3, token & 7
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
            result.append((tag, value))
        elif wire == 2:
            length = 0
            shift = 0
            while True:
                byte = body[index]
                index += 1
                length |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((tag, body[index:index + length]))
            index += length
        elif wire == 5:
            index += 4
        else:
            raise ValueError("unsupported protobuf wire type")
    return result


def field(body, tag):
    return next((value for current, value in fields(body) if current == tag), None)


def request(sock, opcode, body, rpc):
    sock.sendall(frame(opcode, body))
    for _ in range(500):
        got_opcode, got_body = recv_one(sock)
        if field(got_body, 90) == rpc:
            return got_opcode, got_body
    raise RuntimeError("RPC response not received")


def login(sock, account):
    _, body = request(sock, 20010, sf(1, account) + sf(2, "123456") + vf(90, 1), 1)
    _, body = request(sock, 20008, sf(1, account) + sf(2, "123456") + vf(90, 2), 2)
    key, gate = field(body, 2), field(body, 3)
    request(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    request(sock, 20016, vf(2, 1) + sf(3, "energy-test") + vf(90, 5), 5)
    request(sock, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    request(sock, 20027, vf(90, 7), 7)


account = "energy" + str(int(time.time()))
sock = socket.create_connection((HOST, PORT), timeout=5)
sock.settimeout(5)
login(sock, account)
sock.close()
time.sleep(0.3)

db = sqlite3.connect(DB)
player_id = db.execute(
    "SELECT p.id FROM players p JOIN accounts a ON a.id=p.account_id WHERE a.account=?",
    (account,),
).fetchone()[0]
db.execute("UPDATE players SET coin=?, energy=? WHERE id=?", (1000001, 9950, player_id))
db.commit()
db.close()

sock = socket.create_connection((HOST, PORT), timeout=5)
sock.settimeout(5)
login(sock, account)
opcode, body = request(sock, 20316, vf(90, 10), 10)
assert opcode == 20317 and field(body, 1) == 1000000 and field(body, 2) == 100, (opcode, fields(body))
opcode, body = request(sock, 20318, vf(90, 11), 11)
assert opcode == 20319 and field(body, 92) is None, (opcode, fields(body))
time.sleep(0.3)
db = sqlite3.connect(DB)
coin, energy = db.execute("SELECT coin, energy FROM players WHERE id=?", (player_id,)).fetchone()
db.close()
assert (coin, energy) == (1, 10000), (coin, energy)
opcode, body = request(sock, 20318, vf(90, 12), 12)
assert opcode == 20319 and field(body, 92), fields(body)
sock.close()
print("energy TCP price/deduction/max/persistence: PASS")
