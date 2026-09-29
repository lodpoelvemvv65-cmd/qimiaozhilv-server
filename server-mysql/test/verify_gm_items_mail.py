# -*- coding: utf-8 -*-
"""GM HTTP -> Redis -> game TCP grants/mail regression using its own new role.

Run via: MHQ_TEST_GM_ITEMS_TCP=1 go test ./cmd/gm-api -run TestGMItemsMailLiveTCP -v
The Go harness supplies an ephemeral local test session; no GM password needed.
"""
import json
import os
import socket
import struct
import sys
import urllib.error
import urllib.request
import uuid
from collections import Counter

sys.stdout.reconfigure(encoding="utf-8")
HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))


def varint(value):
    result = bytearray()
    while value > 127:
        result.append((value & 127) | 128)
        value >>= 7
    result.append(value)
    return bytes(result)


def vf(tag, value):
    return varint(tag << 3) + varint(value)


def sf(tag, text):
    raw = text.encode("utf-8")
    return varint((tag << 3) | 2) + varint(len(raw)) + raw


def fields(raw):
    pos = 0

    def read_int():
        nonlocal pos
        value = shift = 0
        while True:
            byte = raw[pos]
            pos += 1
            value |= (byte & 127) << shift
            if not byte & 128:
                return value
            shift += 7

    while pos < len(raw):
        key = read_int()
        tag, wire = key >> 3, key & 7
        if wire == 0:
            value = read_int()
        elif wire == 2:
            size = read_int()
            value = raw[pos:pos + size]
            pos += size
        elif wire in (1, 5):
            size = 8 if wire == 1 else 4
            value = raw[pos:pos + size]
            pos += size
        else:
            raise AssertionError(f"unsupported protobuf wire {wire}")
        yield tag, value


def field(raw, tag, default=0):
    return next((value for key, value in fields(raw) if key == tag), default)


class Game:
    def __init__(self):
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.sock.settimeout(5)
        self.rpc_id = 0
        self.pushes = []

    def read(self, size):
        data = b""
        while len(data) < size:
            more = self.sock.recv(size - len(data))
            if not more:
                raise ConnectionError("game disconnected")
            data += more
        return data

    def rpc(self, opcode, payload=b"", allow_message=False):
        self.rpc_id += 1
        raw = payload + vf(90, self.rpc_id)
        self.sock.sendall(struct.pack("<HH", len(raw) + 2, opcode) + raw)
        for _ in range(1000):
            total, op = struct.unpack("<HH", self.read(4))
            body = self.read(total - 2)
            if op == opcode + 1 and field(body, 90) == self.rpc_id:
                assert field(body, 91) == 0, (op, list(fields(body)))
                if not allow_message:
                    assert not field(body, 92, b""), (op, field(body, 92))
                return body
            self.pushes.append((op, body))
        raise TimeoutError("RPC response missing")

    def login(self, account, password, create=False, enter=True):
        if create:
            self.rpc(20010, sf(1, account) + sf(2, password))
        login = self.rpc(20008, sf(1, account) + sf(2, password))
        gate = self.rpc(20014, vf(1, field(login, 2)) + vf(2, field(login, 3, 1)))
        player_id = field(gate, 1)
        if create:
            self.rpc(20016, vf(1, player_id) + vf(2, 1) + sf(3, "GM附件" + account[-8:]))
        if enter:
            self.rpc(20027)
        return player_id

    def bag(self):
        raw = self.rpc(20258)
        counts = Counter()
        for tag, entry in fields(raw):
            if tag == 1:
                item = field(entry, 2, b"")
                counts[field(item, 1)] += field(item, 4)
        return counts


def api(path, payload=None, key=None, expected=200):
    headers = {"Cookie": f"mhq_gm_session={os.environ['GM_TEST_SESSION']}; mhq_gm_csrf={os.environ['GM_TEST_CSRF']}", "X-CSRF-Token": os.environ["GM_TEST_CSRF"]}
    body = None
    if payload is not None:
        headers.update({"Content-Type": "application/json", "Idempotency-Key": key or uuid.uuid4().hex})
        body = json.dumps({"payload": payload}, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(os.environ["GM_TEST_API_BASE"] + "/api/v1" + path, data=body, headers=headers)
    try:
        response = urllib.request.urlopen(request, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        envelope = json.load(response)
        assert response.status == expected, (response.status, expected, envelope)
        return envelope.get("data") if expected == 200 else envelope


def main():
    account = os.environ["GM_TEST_ACCOUNT"]
    assert account.startswith("gmitems") and HOST in ("127.0.0.1", "localhost"), "local isolated tests only"
    password = "Test" + uuid.uuid4().hex[:12]
    game = Game()
    player_id = game.login(account, password, create=True)
    path = f"/players/{player_id}"
    try:
        catalog = api("/items")
        item_map = {item["id"]: item for item in catalog["items"]}
        assert item_map[120227]["equipSlotName"] == "武器" and item_map[120227]["jobName"] == "军官"
        assert item_map[120367]["equipSlotName"] == "称号"
        print(f"PASS Chinese catalog: {catalog['total']} items; weapon/profession/title labels")

        grants = [{"itemId": 110305, "count": 2}, {"itemId": 120227, "count": 1}, {"itemId": 120367, "count": 1}]
        before = game.bag()
        grant_key = uuid.uuid4().hex
        assert api(path + "/items/grant", {"items": grants}, grant_key)["status"] == "completed"
        after = game.bag()
        for item in grants:
            assert after[item["itemId"]] == before[item["itemId"]] + item["count"]
        api(path + "/items/grant", {"items": grants}, grant_key)
        assert game.bag() == after
        print("PASS direct multi-item grant, online bag push and idempotent retry")

        attachments = [{"itemId": 110306, "count": 3}, {"itemId": 120243, "count": 1}, {"itemId": 110203, "count": 123}]
        payload = {"title": "GM中文附件实测", "content": "药水、衣服和金币附件", "items": attachments}
        for invalid in [{"itemId": 2147483647, "count": 1}, {"itemId": 110305, "count": 0}, {"itemId": 110305, "count": 1.5}, {"itemId": 110305, "count": 1000000001}]:
            api(path + "/mail", {**payload, "items": [invalid]}, expected=422)
        print("PASS unknown ID, zero/fractional/oversized attachment rejected before enqueue")
        before_mail_bag = game.bag()
        before_coin = int(api(path)["coin"])
        mail_key = uuid.uuid4().hex
        result = api(path + "/mail", payload, mail_key)
        assert result["status"] == "completed"
        mail_id = int(result["data"]["mailId"])
        api(path + "/mail", payload, mail_key)
        mails = [raw for tag, raw in fields(game.rpc(20281, vf(1, 1))) if tag == 1]
        selected = [raw for raw in mails if field(raw, 8) == mail_id]
        assert len(selected) == 1
        received = [{"itemId": field(raw, 1), "count": field(raw, 4)} for tag, raw in fields(selected[0]) if tag == 6]
        assert received == attachments and field(selected[0], 2).decode("utf-8") == payload["title"]
        assert game.bag() == before_mail_bag, "mail attachments must wait for claiming"
        assert any(op == 20282 for op, _ in game.pushes), "online mail snapshot not pushed"
        print("PASS Chinese mail, exact multi-item wire attachments, online mail push and send idempotency")

        # 上面那次断言只看「有没有推 20282」，而客户端随后自己又请求了一次
        # 20281 Page:1 并把结果当成了邮件列表 —— 所以它抓不到「推错了页」。
        # ss.mails 是插入序、第 1 页 = 最旧的 mailPageSize 封，新邮件 append 在
        # 末尾：角色累计到第 11 封时恒推 Page:1 就推不到刚发的邮件（邮件其实已
        # 落库，玩家翻到最后一页才能发现）。这里只认服务端主动推的那一帧。
        for index in range(1, 12):
            mark = len(game.pushes)
            result = api(path + "/mail", {"title": f"分页实测{index}", "content": "分页实测", "items": []})
            assert result["status"] == "completed"
            new_id = int(result["data"]["mailId"])
            game.rpc(20258)  # 读一次 socket，把服务端主动推送收进 game.pushes
            visible = [field(raw, 8) for op, body in game.pushes[mark:] if op == 20282
                       for tag, raw in fields(body) if tag == 1]
            assert new_id in visible, f"第 {index} 封 GM 邮件不在服务端推送的邮件页里：{visible}"
        print("PASS GM mail push lands on the page that holds the newest mail (11 mails, past the paging boundary)")

        game.rpc(20283, vf(1, mail_id) + vf(2, 1))
        claimed = game.bag()
        assert claimed[110306] == before_mail_bag[110306] + 3
        assert claimed[120243] == before_mail_bag[120243] + 1
        assert int(api(path)["coin"]) == before_coin + 123
        repeat = game.rpc(20283, vf(1, mail_id) + vf(2, 1), allow_message=True)
        assert field(repeat, 92, b"").decode("utf-8") == "邮件已领取"
        assert game.bag() == claimed
        assert api(path + "/mail", {"title": "纯文字实测", "content": "无附件", "items": []})["status"] == "completed"
        print("PASS claim delivers goods/equipment/currency once; plain text mail remains supported")

        game.sock.close()
        game = Game()
        assert game.login(account, password) == player_id
        assert game.bag() == claimed
        print("PASS grants and claimed attachments persist across reconnect")
    finally:
        game.sock.close()
        cleanup = Game()
        cleanup.login(account, password, enter=False)
        cleanup.rpc(20018, vf(1, player_id))
        cleanup.sock.close()
        print("PASS temporary test role removed through native delete-role protocol")


if __name__ == "__main__":
    main()
