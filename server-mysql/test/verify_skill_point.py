# -*- coding: utf-8 -*-
"""技能点逐级消耗的真实 TCP 回归（20243/20244 学习升级 + 20245 免费洗点）。

玩家反馈：整条技能加满才扣 1 点技能点。线上规则是每升一级（含首次学习）都扣
1 点技能点，再额外扣当前等级行的技能书；技能点用完后即使技能书管够也不能升级。
本脚本用 GM HTTP 造出「技能点 + 技能书」的干净角色，然后全程走客户端协议
（20243 学习/升级 → 20244）核对：

  1. 首次学习扣 1 点技能点 + `SkillLearn[11010100]` 的秘籍；
  2. 升级再扣 1 点技能点 + `SkillLearn[11010101]` 的秘籍（修复点：以前不扣点）；
  3. 技能点为 0 时继续升级 → Message="技能点不足"，等级/技能点/背包全不变，
     且 Error 必须保持 0（客户端 Session.Call 会把 Error!=0 抛成断线）；
  4. 每次扣点都要推 20025 的 UnitCharacter.SkillPoint，界面才能刷新；
  5. 免费洗点按等级返还（2 级返还 2 点），可以重新学回来；
  6. 重登后技能点与技能等级落库一致；
  7. 前置技能只有 1 级时，同层的两个分支（110201 强化攻击、110202 天马飞瀑，
     前置都是 110101 MaxLevel 7）都被拒绝且不扣点不扣书；把前置学满到 7 级后
     两个分支立刻都可学。

Run via: MHQ_TEST_GM_SKILLS_TCP=1 go test ./cmd/gm-api -run TestSkillPointLiveTCP -v
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

SKILL = 110101      # 军官一阶技能「爆炸汽油弹」，MaxLevel 7，前置 100001（普攻 1 级）
SKILL_NAME = "爆炸汽油弹"
SKILL_MAX = 7
NEXT = 110201       # 军官二阶技能「强化攻击」，MaxLevel 6，前置 110101
NEXT_NAME = "强化攻击"
SIBLING = 110202    # 军官二阶同层分支「天马飞瀑」，MaxLevel 6，前置同样是 110101
SIBLING_NAME = "天马飞瀑"
BOOK = 20217        # 军官技能秘籍


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


def text(raw, tag, default=""):
    value = field(raw, tag, b"")
    return value.decode("utf-8") if isinstance(value, bytes) else default


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
            self.rpc(20016, vf(1, player_id) + vf(2, 1) + sf(3, "技能点" + account[-8:]))
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

    def learned_level(self, response, skill_id):
        for tag, entry in fields(response):
            if tag == 1 and field(entry, 1) == skill_id:
                return field(entry, 2)
        return None

    def pushed_skill_points(self, player_id):
        """20025 的 UnitCharacter.SkillPoint（tag 10），只取本角色的推送。"""
        seen = []
        for op, body in self.pushes:
            if op != 20025:
                continue
            unit = field(body, 1, b"")
            if field(unit, 1) == player_id:
                seen.append(field(unit, 10))
        return seen


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
    assert account.startswith("skillpoint") and HOST in ("127.0.0.1", "localhost"), "local isolated tests only"
    password = "Test" + uuid.uuid4().hex[:12]
    game = Game()
    player_id = game.login(account, password, create=True)
    path = f"/players/{player_id}"
    try:
        assert int(api(path)["skillPoint"]) == 0, "新角色技能点应为 0"
        assert api(path + "/skills-adjust", {"skillPoint": "2"})["status"] == "completed"
        assert api(path + "/items/grant", {"items": [{"itemId": BOOK, "count": 4}]})["status"] == "completed"
        assert int(api(path)["skillPoint"]) == 2
        before = game.bag()
        assert before[BOOK] == 4, f"秘籍未到账: {before[BOOK]}"

        learned = game.rpc(20243, vf(1, SKILL))
        assert game.learned_level(learned, SKILL) == 1, "首次学习未到 1 级"
        after_learn = game.bag()
        learn_books = before[BOOK] - after_learn[BOOK]
        assert learn_books >= 1, f"首次学习没有扣秘籍: {before[BOOK]} -> {after_learn[BOOK]}"
        assert int(api(path)["skillPoint"]) == 1, "首次学习应当扣 1 点技能点"
        assert 1 in game.pushed_skill_points(player_id), "扣点后未推 20025 的 SkillPoint"
        print(f"PASS 首次学习 0->1：扣 1 技能点 + {learn_books} 本秘籍，并推送 UnitCharacter")

        upgraded = game.rpc(20243, vf(1, SKILL))
        assert game.learned_level(upgraded, SKILL) == 2, "升级未到 2 级"
        after_upgrade = game.bag()
        upgrade_books = after_learn[BOOK] - after_upgrade[BOOK]
        assert upgrade_books >= 1, f"升级没有扣秘籍: {after_learn[BOOK]} -> {after_upgrade[BOOK]}"
        assert int(api(path)["skillPoint"]) == 0, "升级必须再扣 1 点技能点"
        assert 0 in game.pushed_skill_points(player_id), "升级扣点后未推 20025 的 SkillPoint"
        print(f"PASS 升级 1->2：再扣 1 技能点 + {upgrade_books} 本秘籍")

        blocked = game.rpc(20243, vf(1, SKILL), allow_message=True)
        assert text(blocked, 92) == "技能点不足", f"Message={text(blocked, 92)!r}"
        assert game.learned_level(blocked, SKILL) == 2, "被拒绝的升级改了等级"
        assert game.bag() == after_upgrade, "被拒绝的升级扣了秘籍"
        assert int(api(path)["skillPoint"]) == 0
        print("PASS 技能点为 0 时：技能书管够也拒绝（Message=技能点不足，Error=0，状态不变）")

        game.rpc(20245)
        assert int(api(path)["skillPoint"]) == 2, "免费洗点应按等级返还 2 点技能点"
        relearned = game.rpc(20243, vf(1, SKILL))
        assert game.learned_level(relearned, SKILL) == 1, "洗点后未能重新学习"
        assert int(api(path)["skillPoint"]) == 1
        print("PASS 免费洗点按等级返还技能点，并可用返还的点重新学习")

        game.sock.close()
        game = Game()
        assert game.login(account, password) == player_id
        assert int(api(path)["skillPoint"]) == 1
        reloaded = game.rpc(20239)
        assert game.learned_level(reloaded, SKILL) == 1, "重登后技能等级不符"
        print("PASS 重登后技能点/技能等级落库一致")

        # 前置技能必须学满（SkillConfig.level-0 的 PreSkillId 解锁链），
        # 只学过一级就能学下一个是错的。110201 强化攻击和 110202 天马飞瀑
        # 是同一层的两个分支，前置都是 110101，所以两个都必须被拦住。
        assert api(path + "/skills-adjust", {"skillPoint": "10"})["status"] == "completed"
        assert api(path + "/items/grant", {"items": [{"itemId": BOOK, "count": 12}]})["status"] == "completed"
        points_before = int(api(path)["skillPoint"])
        books_before = game.bag()[BOOK]
        for child, child_name in ((NEXT, NEXT_NAME), (SIBLING, SIBLING_NAME)):
            locked = game.rpc(20243, vf(1, child), allow_message=True)
            assert text(locked, 92) == "请先把%s学满" % SKILL_NAME, f"{child_name} Message={text(locked, 92)!r}"
            assert not game.learned_level(locked, child), f"被拒绝的学习解锁了{child_name}"
            assert int(api(path)["skillPoint"]) == points_before, "被拒绝的学习扣了技能点"
            assert game.bag()[BOOK] == books_before, "被拒绝的学习扣了秘籍"
        print("PASS 前置技能只有 1 级时：同层两个分支（强化攻击/天马飞瀑）都被拒绝且状态不变")

        while game.learned_level(game.rpc(20239), SKILL) < SKILL_MAX:
            upgraded = game.rpc(20243, vf(1, SKILL))
            assert upgraded is not None
        assert game.learned_level(game.rpc(20239), SKILL) == SKILL_MAX
        for child, child_name in ((NEXT, NEXT_NAME), (SIBLING, SIBLING_NAME)):
            unlocked = game.rpc(20243, vf(1, child))
            assert game.learned_level(unlocked, child) == 1, f"前置学满后仍学不了{child_name}"
        assert int(api(path)["skillPoint"]) == points_before - (SKILL_MAX - 1) - 2
        print(f"PASS 前置技能学满 {SKILL_MAX} 级后：同层两个分支都可学，按等级扣点")
    finally:
        game.sock.close()
        cleanup = Game()
        cleanup.login(account, password, enter=False)
        cleanup.rpc(20018, vf(1, player_id))
        cleanup.sock.close()
        print("PASS 临时测试角色已通过原始删角协议删除")


if __name__ == "__main__":
    main()
