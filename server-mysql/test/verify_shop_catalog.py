# -*- coding: utf-8 -*-
"""Smoke-test the server-driven Market/Shop/MultiShop catalog wire fields."""

from __future__ import annotations

import os
import select
import socket
import struct
import time


HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))


def varint(value: int) -> bytes:
    out = bytearray()
    value = int(value)
    while True:
        b = value & 0x7F
        value >>= 7
        out.append(b | 0x80 if value else b)
        if not value:
            return bytes(out)


def vf(field: int, value: int) -> bytes:
    return varint((field << 3) | 0) + varint(value)


def sf(field: int, value: str) -> bytes:
    raw = value.encode("utf-8")
    return varint((field << 3) | 2) + varint(len(raw)) + raw


def pack(opcode: int, body: bytes) -> bytes:
    return struct.pack("<HH", len(body) + 2, opcode) + body


def decode(body: bytes):
    out = []
    i = 0
    while i < len(body):
        tag = 0
        shift = 0
        while True:
            b = body[i]
            i += 1
            tag |= (b & 0x7F) << shift
            if not b & 0x80:
                break
            shift += 7
        field, wire = tag >> 3, tag & 7
        if wire == 0:
            value = 0
            shift = 0
            while True:
                b = body[i]
                i += 1
                value |= (b & 0x7F) << shift
                if not b & 0x80:
                    break
                shift += 7
            out.append((field, wire, value))
        elif wire == 2:
            size = 0
            shift = 0
            while True:
                b = body[i]
                i += 1
                size |= (b & 0x7F) << shift
                if not b & 0x80:
                    break
                shift += 7
            out.append((field, wire, body[i : i + size]))
            i += size
        elif wire == 5:
            out.append((field, wire, struct.unpack_from("<I", body, i)[0]))
            i += 4
        else:
            raise ValueError(f"unsupported wire type {wire}")
    return out


def all_fld(body: bytes, field: int) -> list[int]:
    values: list[int] = []
    for number, wire, value in decode(body):
        if number != field:
            continue
        if wire == 0:
            values.append(value)
        elif wire == 2:
            values.extend(unpack_varints(value))
    return values


def unpack_varints(raw: bytes) -> list[int]:
    values: list[int] = []
    i = 0
    while i < len(raw):
        value = 0
        shift = 0
        while True:
            b = raw[i]
            i += 1
            value |= (b & 0x7F) << shift
            if not b & 0x80:
                break
            shift += 7
        values.append(value)
    return values


def field(body: bytes, number: int):
    for f, _, value in decode(body):
        if f == number:
            return value
    return None


class Client:
    def __init__(self):
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.sock.settimeout(5)
        self.rpc = 0

    def close(self):
        self.sock.close()

    def recv_exact(self, size: int) -> bytes:
        data = bytearray()
        while len(data) < size:
            chunk = self.sock.recv(size - len(data))
            if not chunk:
                raise ConnectionError("server closed")
            data.extend(chunk)
        return bytes(data)

    def request(self, opcode: int, body: bytes):
        self.rpc += 1
        rpc = self.rpc
        self.sock.sendall(pack(opcode, body + vf(90, rpc)))
        pushes = []
        while True:
            total, op = struct.unpack("<HH", self.recv_exact(4))
            payload = self.recv_exact(total - 2)
            if field(payload, 90) == rpc:
                return op, payload, pushes
            pushes.append((op, payload))


def main() -> None:
    client = Client()
    account = f"shopcatalog{int(time.time() * 1000)}"
    try:
        op, body, _ = client.request(20010, sf(1, account) + sf(2, "123456"))
        assert op == 20011, (op, body)
        key, gate = field(body, 2), field(body, 3)
        assert key and gate
        op, body, _ = client.request(20014, vf(1, key) + vf(2, gate))
        player = field(body, 1)
        assert player
        op, body, _ = client.request(
            20016, vf(1, player) + vf(2, 1) + sf(3, "商店测试" + account[-6:])
        )
        assert op == 20017 and not field(body, 91), body
        op, body, pushes = client.request(20027, b"")
        assert op == 20028, (op, body)
        pushes += collect(client.sock, {20423}, 3.0)
        active = next((payload for op, payload in pushes if op == 20423), None)
        assert active is not None, "missing M2C_SendActiveInfo"
        compact = all_fld(active, 6)
        assert compact and len(compact) % 7 == 0, compact[:20]
        store_types = {compact[i] for i in range(0, len(compact), 7)}
        assert {1, 2, 3}.issubset(store_types), store_types
        assert all(compact[i + 5] >= 0 for i in range(0, len(compact), 7))
        # Enabled: false rows must be absent from the authoritative directory;
        # the patched client uses this to hide them from the ordinary shop.
        shop_item_ids = {
            compact[i + 4] for i in range(0, len(compact), 7) if compact[i] == 2
        }
        assert 110305 not in shop_item_ids, "disabled ShopBase item 110305 leaked"
        # 110836 is an operator-controlled row and may be either listed or
        # unlisted while this smoke test runs.  Its presence is intentionally
        # not hard-coded here; the catalog/handler unit tests cover the
        # Enabled=false filtering and raw-slot preservation semantics.

        op, market, _ = client.request(20172, vf(1, 0))
        assert op == 20173, op
        ids = all_fld(market, 2)
        prices = all_fld(market, 6)
        assert ids and len(prices) >= len(ids) * 2, (len(ids), len(prices))
        print(f"PASS shop catalog stores={sorted(store_types)} activeItems={len(compact)//7} marketItems={len(ids)}")
    finally:
        client.close()


def collect(sock: socket.socket, required: set[int], timeout: float):
    out = []
    end = time.time() + timeout
    while time.time() < end and not required.issubset({op for op, _ in out}):
        left = end - time.time()
        ready, _, _ = select.select([sock], [], [], left)
        if not ready:
            break
        header = recv_exact(sock, 4)
        total, op = struct.unpack("<HH", header)
        payload = recv_exact(sock, total - 2)
        out.append((op, payload))
    return out


def recv_exact(sock: socket.socket, size: int) -> bytes:
    data = bytearray()
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise ConnectionError("server closed")
        data.extend(chunk)
    return bytes(data)


if __name__ == "__main__":
    main()
