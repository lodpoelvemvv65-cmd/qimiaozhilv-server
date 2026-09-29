"""Real TCP regression: a trial leader starts one shared fight for both members."""
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


def bf(field, value):
    return varint((field << 3) | 2) + varint(len(value)) + value


def pack(opcode, body):
    return struct.pack("<HH", len(body) + 2, opcode) + body


def decode(body):
    fields = []
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
        field, wire = token >> 3, token & 7
        if wire == 0:
            value, shift = 0, 0
            while True:
                byte = body[index]
                index += 1
                value |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            fields.append((field, wire, value))
        elif wire == 2:
            length, shift = 0, 0
            while True:
                byte = body[index]
                index += 1
                length |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            fields.append((field, wire, body[index:index + length]))
            index += length
        elif wire == 5:
            fields.append((field, wire, body[index:index + 4]))
            index += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire)
    return fields


def first(body, number, wire=None):
    for field, field_wire, value in decode(body):
        if field == number and (wire is None or field_wire == wire):
            return value
    return None


def all_fields(body, number, wire=None):
    return [value for field, field_wire, value in decode(body)
            if field == number and (wire is None or field_wire == wire)]


def packed_varints(raw):
    values = []
    index = 0
    while index < len(raw):
        value, shift = 0, 0
        while True:
            byte = raw[index]
            index += 1
            value |= (byte & 0x7F) << shift
            shift += 7
            if not byte & 0x80:
                break
        values.append(value)
    return values


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

    def recv(self):
        header = self.sock.recv(4)
        if len(header) != 4:
            raise ConnectionError("closed %s" % self.label)
        length, opcode = struct.unpack("<HH", header)
        body = b""
        while len(body) < length - 2:
            chunk = self.sock.recv(length - 2 - len(body))
            if not chunk:
                raise ConnectionError("closed %s" % self.label)
            body += chunk
        return opcode, body

    def call(self, opcode, expected, body=b""):
        self.rpc += 1
        rpc = self.rpc
        self.sock.sendall(pack(opcode, body + vf(90, rpc)))
        while True:
            response_opcode, response = self.recv()
            if response_opcode == expected and first(response, 90) == rpc:
                return response
            self.inbox.append((response_opcode, response))

    def collect(self, opcode, timeout=3):
        deadline = time.time() + timeout
        while time.time() < deadline:
            found = [body for current, body in self.inbox if current == opcode]
            if found:
                self.inbox = [(current, body) for current, body in self.inbox
                              if current != opcode]
                return found
            self.sock.settimeout(max(0.05, deadline - time.time()))
            try:
                current, body = self.recv()
            except socket.timeout:
                break
            self.inbox.append((current, body))
        return []


def login(client, suffix, job):
    account = "trial_party_%s_%d" % (suffix, time.time_ns())
    registered = client.call(20010, 20011, sf(1, account) + sf(2, "123456"))
    key, gate = first(registered, 2), first(registered, 3)
    client.call(20014, 20015, vf(1, key) + vf(2, gate))
    client.call(20016, 20017, vf(2, job) + sf(3, account[-20:]))
    client.call(20014, 20015, vf(1, key) + vf(2, gate))
    entered = client.call(20027, 20028)
    client.player_id = first(entered, 1)
    client.sock.sendall(pack(20030, b""))


def presentation(body):
    result = []
    for raw in all_fields(body, 1, 2):
        result.append((first(raw, 1), first(raw, 2)))
    return result


first_client, second_client = Client("leader"), Client("member")
try:
    login(first_client, "leader", 1)
    login(second_client, "member", 1)
    first_client.call(20031, 20032, vf(1, 1000901))
    second_client.call(20031, 20032, vf(1, 1000901))
    first_client.inbox.clear()
    second_client.inbox.clear()

    first_client.call(20156, 20157, vf(1, second_client.player_id))
    assert second_client.collect(20159), "member did not receive team invite"
    handle = vf(1, first_client.player_id) + vf(2, 1)
    second_client.call(20160, 20161, bf(1, handle) + vf(2, 0))
    assert first_client.collect(20162) and second_client.collect(20162), "team snapshot missing"
    first_client.inbox.clear()
    second_client.inbox.clear()

    response = first_client.call(20092, 20093)
    unit_ids = []
    for raw in all_fields(response, 2, 2):
        unit_ids.extend(packed_varints(raw))
    if not unit_ids:
        unit_ids = all_fields(response, 2, 0)
    leader_present = first_client.collect(20050)
    member_present = second_client.collect(20050)
    assert len(leader_present) == 1, "leader did not receive exactly one trial presentation"
    assert len(member_present) == 1, "member did not enter the trial battle presentation"
    leader_units = presentation(leader_present[0])
    member_units = presentation(member_present[0])
    assert leader_units == member_units, "%s != %s" % (leader_units, member_units)
    assert [unit_id for unit_id, _ in leader_units] == unit_ids, \
        "response UnitIdList differs from combat presentation"
    print("PASS trial party TCP: players=%d/%d shared units=%s both clients received 20050" %
          (first_client.player_id, second_client.player_id, leader_units))
finally:
    first_client.close()
    second_client.close()
