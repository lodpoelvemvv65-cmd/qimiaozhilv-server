# -*- coding: utf-8 -*-
"""GM 调整词缀（player.equip_affix）：把洗练词缀从「手工装备」里独立出来之后的独立入口实测。

Run via: MHQ_TEST_GM_AFFIX_TCP=1 go test ./cmd/gm-api -run TestGMEquipAffixLiveTCP -v
The Go harness supplies an ephemeral local test session; no GM password needed.

覆盖：
  1. 语义是「设置词缀最终状态」：整组设置 / 换成六维 / 清空都走同一个入口；
  2. **只碰词缀**——星级 / 品质 / 强化 / 随机属性 / 宝石在一个动作前后逐字段不变，
     这正是它和 player.equip_manual 分开的理由（手工那条会强制「星级 N 需要 N 条随机属性」）；
  3. 一件「星级 5 但 0 条随机属性」状态的新装备可以正常换词缀，不被手工那套联动规则拒绝；
  4. **重登不变样**——这是整个面板存在的理由：equip.go 的 ensureEquipmentAffixCount 在每次
     背包推送和登录修复里都会裁剪不在词缀池里的 id、去重、并把条数补齐到
     equipmentBonusCount(品质)，所以「正好槽位数」和「1 条六维」两种状态才算稳定；
  5. 非法输入回传**具体中文原因**，被拒的提交词缀一个字节都不动；
  6. 与手工装备的 addAttrs 规则一致：两个入口对同一组词缀给出同一结果。
"""
import json
import os
import socket
import struct
import sys
import time
import urllib.error
import urllib.request
import uuid

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


def repeated(raw, tag):
    return [value for key, value in fields(raw) if key == tag]


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
            self.rpc(20016, vf(1, player_id) + vf(2, 1) + sf(3, "词缀" + account[-8:]))
        if enter:
            self.rpc(20027)
        return player_id

    def bag_snapshot(self):
        """20258 -> 20259 的角色背包全量，按 ItemId 索引 EquipTransMessage。

        EquipTransMessage 的 tag：randomAttributes=6、Star=7、Quality=8、Level=9、
        addtionalAttributes=11、GemList=12（见 pb_extra.go encodeEquipTrans）。
        """
        raw = self.rpc(20258)
        out = {}
        for tag, entry in fields(raw):
            if tag != 1:
                continue
            net = field(entry, 2, b"")
            equip = field(entry, 3, b"")
            out[field(net, 1)] = {
                "star": field(equip, 7),
                "quality": field(equip, 8),
                "strength": field(equip, 9),
                "randomAttrs": [v for v in repeated(equip, 6) if v],
                "addAttrs": [v for v in repeated(equip, 11) if v],
                "gems": repeated(equip, 12),
            }
        return out


def api(path, payload=None, expected=200):
    headers = {
        "Cookie": f"mhq_gm_session={os.environ['GM_TEST_SESSION']}; mhq_gm_csrf={os.environ['GM_TEST_CSRF']}",
        "X-CSRF-Token": os.environ["GM_TEST_CSRF"],
    }
    body = None
    if payload is not None:
        headers.update({"Content-Type": "application/json", "Idempotency-Key": uuid.uuid4().hex})
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


def rejected(path, payload, fragment):
    """被拒时必须回传具体中文原因，而不是只有一个裸状态码。"""
    envelope = api(path, payload, expected=422)
    message = (envelope.get("error") or {}).get("message") or ""
    assert fragment in message, (fragment, message, envelope)
    return message


def same_slot(left, right):
    return str(left["location"]) == str(right["location"]) and str(left["slot_index"]) == str(right["slot_index"])


def loc(item):
    """装备定位键。player_items 的主键是 (player_id, location, slot_index)，
    而 server_id 对新生成的装备恒为 0（schema 默认 0，只有 cmd/grant-items 会发号），
    所以 GM 面板必须用 location + slotIndex 定位，不能用 serverId。
    """
    return {"location": str(item["location"]), "slotIndex": str(item["slot_index"])}


def persisted_affixes(player_id, item):
    """GM API 读的是 MySQL 落库结果，按 location+slot 取词缀子表（position 升序）。"""
    rows = [row for row in api(f"/players/{player_id}/affixes")["items"] if same_slot(row, item)]
    rows.sort(key=lambda row: int(row["position"]))
    return [int(row["affix_id"]) for row in rows]


def bag_item(player_id, item_id):
    for entry in api(f"/players/{player_id}/inventory")["items"]:
        if int(entry["item_id"]) == item_id:
            return entry
    raise AssertionError(f"granted item {item_id} is not in the bag")


def relogin(account, password, player_id):
    """完整重登：服务端的登录修复（ensureEquipmentAffixCount）就在 20027 这一步跑。"""
    session = Game()
    assert session.login(account, password) == player_id
    bag = session.bag_snapshot()
    time.sleep(0.5)
    return session, bag


def other_fields(snapshot):
    """取出「词缀以外的所有字段」，用来证明调整词缀不会顺手动别的东西。"""
    return {key: value for key, value in snapshot.items() if key != "addAttrs"}


def pick_quality(options):
    """挑一个「槽位 >= 2 且词缀池够填」的品质：槽位只有 1 的话测不出「不足会被补齐」。"""
    limits, pools = options["limits"], options["affixPools"]["byQuality"]
    for quality in sorted((int(q) for q in pools), reverse=True):
        target = limits["bonusCountByQuality"].get(str(quality), 1)
        if target < 2 or len(pools[str(quality)]) < target:
            continue
        return quality, min(target, len(pools[str(quality)]))
    raise AssertionError(f"no quality with >=2 affix slots: {limits['bonusCountByQuality']}")


def main():
    account = os.environ["GM_TEST_ACCOUNT"]
    assert account.startswith("gmaffix") and HOST in ("127.0.0.1", "localhost"), "local isolated tests only"
    password = "Test" + uuid.uuid4().hex[:12]
    game = Game()
    player_id = game.login(account, password, create=True)
    path = f"/players/{player_id}"
    try:
        options = api("/equip-manual/options")
        entry = next(item for item in api("/items")["items"] if item["equipSlotName"])
        item_id = entry["id"]
        quality, target = pick_quality(options)
        pool = [int(aid) for aid in options["affixPools"]["byQuality"][str(quality)]]
        six_dimension = [int(aid) for aid in options["affixPools"]["sixDimension"]]
        affixes = {affix["id"]: affix for affix in options["affixes"]}
        # 同 family 不同档位的词缀：存在于词缀表里，但不在品质 quality 的池内，
        # 用来区分「档位不对」和「根本不在词缀表里」两种错。
        pool_families = {affixes[aid]["family"] for aid in pool}
        wrong_tier = next((affix["id"] for affix in options["affixes"]
                           if affix["id"] not in set(pool) and affix["family"] in pool_families), None)
        assert wrong_tier is not None, "no same-family affix at another tier"
        affix_ids = pool[:target]
        print(f"PASS 候选池：{entry['name']} 品质 {quality} 词缀槽位 {target} 个 / "
              f"池内 {len(pool)} 条（{affixes[affix_ids[0]]['label']}）· "
              f"六维 {len(six_dimension)} 条 · 全带中文名")

        # 先造一件词缀起点干净、且品质已知的装备。
        # 新发放的装备是「EquipBase.Star 星级 + 0 条随机属性」，星级 1 + 1 条随机属性
        # 正好满足「星级 N 需要 N 条随机属性」，所以这条 baseline 一定过得去。
        api(path + "/items/grant", {"items": [{"itemId": item_id, "count": 1}]})
        target_item = bag_item(player_id, item_id)
        # 新发放的装备 server_id 是 0，整条用例都靠 location + slotIndex 定位。
        assert str(target_item["server_id"]) == "0"
        base = loc(target_item)
        tier_pool = options["randomAttrsByQuality"][str(quality)]
        assert tier_pool, f"quality {quality} has no random attribute pool"
        baseline = api(path + "/equip-manual", {**base, "quality": str(quality), "star": "1", "randomAttrs": [tier_pool[0]["id"]]})["data"]["after"]
        assert baseline["quality"] == quality and len(baseline["addAttrs"]) == 0, baseline

        # ---- 1. 整组设置：只改词缀，其它字段一个都不动 ----
        pushes_before = len(game.pushes)
        result = api(path + "/equip-affix", {**base, "addAttrs": affix_ids})
        assert result["status"] == "completed", result
        before, after = result["data"]["before"], result["data"]["after"]
        assert after["addAttrs"] == affix_ids, after
        # before/after 里除 addAttrs 之外的字段必须逐一相等：这是「词缀独立」的核心断言。
        assert other_fields(before) == other_fields(after), (before, after)
        wire = game.bag_snapshot()[item_id]
        assert any(op == 20260 for op, _ in game.pushes[pushes_before:]), "在线角色没有收到背包快照推送"
        assert wire["addAttrs"] == affix_ids, wire
        assert persisted_affixes(player_id, target_item) == affix_ids
        print(f"PASS 整组设置：{target} 条词缀写进客户端可见包 + MySQL，"
              f"星级{after['star']}/品质{after['quality']}/强化{after['strength']}/"
              f"随机属性{len(after['randomAttrs'])}条/宝石{len(after['gems'])}个孔全部原样")

        # ---- 2. 重登不变样（登录修复会裁剪 + 补齐词缀，这是最关键的一条） ----
        game.sock.close()
        game, reloaded = relogin(account, password, player_id)
        reloaded = reloaded[item_id]
        assert reloaded["addAttrs"] == affix_ids, reloaded
        assert persisted_affixes(player_id, target_item) == affix_ids
        print("PASS 重登后词缀原样（正好槽位数是三个稳定态之一，客户端包 + MySQL 两边一致）")

        # ---- 3. 换成 1 条六维：同样稳定，且条数不受槽位数约束 ----
        if six_dimension:
            result = api(path + "/equip-affix", {**base, "addAttrs": [six_dimension[0]]})["data"]["after"]
            assert result["addAttrs"] == [six_dimension[0]], result
            game.sock.close()
            game, reloaded = relogin(account, password, player_id)
            assert reloaded[item_id]["addAttrs"] == [six_dimension[0]], reloaded[item_id]
            assert persisted_affixes(player_id, target_item) == [six_dimension[0]]
            print(f"PASS 六维词缀 {affixes[six_dimension[0]]['label']} 单独 1 条，重登后仍未被补齐/裁掉")
        else:
            print("SKIP 六维词缀：该版本配置里没有六维池")

        # ---- 4. 清空：空列表是合法提交值 ----
        result = api(path + "/equip-affix", {**base, "addAttrs": []})["data"]["after"]
        assert result["addAttrs"] == [], result
        assert game.bag_snapshot()[item_id]["addAttrs"] == []
        assert persisted_affixes(player_id, target_item) == []
        game.sock.close()
        game, reloaded = relogin(account, password, player_id)
        assert reloaded[item_id]["addAttrs"] == [], "清空的词缀重登后被补齐了"
        print("PASS 清空词缀：客户端包、词缀子表都为空，重登后也不会被补齐")

        # ---- 5. 只碰词缀：一件「星级 5 但 0 条随机属性」的装备照样能换 ----
        # 玩法侧新发放的装备天生就是这个状态（repairBagItem 只补 Star，不补 RandomAttrs）。
        # 手工装备那条 action 会因为这个状态拒绝改星级，词缀这条必须不受影响——
        # 这就是两者分开的意义。
        stored_star = int(target_item["star"])
        # 对照组：星级改成 5 但没有 5 条随机属性，手工那条会被联动规则挡下来。
        rejected(path + "/equip-manual", {**base, "star": "5"},
                 "星级 5 需要 5 条随机属性")
        independent = api(path + "/equip-affix", {**base, "addAttrs": affix_ids})["data"]["after"]
        assert independent["star"] == stored_star, independent
        assert independent["addAttrs"] == affix_ids, independent
        print(f"PASS 星级 {independent['star']} 的新装备能直接换词缀：手工装备那条改星级被联动规则拒绝，"
              f"词缀这条不受影响")

        # ---- 6. 非法输入必须回传具体中文原因 ----
        rej = lambda payload, fragment: rejected(path + "/equip-affix", {**base, **payload}, fragment)
        rej({}, "请提交洗练词缀")
        rej({"addAttrs": affix_ids[:1]}, f"只能为空或正好 {target} 条")
        # 多余的必须是**池内另一条**，否则会先撞上「词缀重复」而不是条数校验。
        rej({"addAttrs": affix_ids + pool[target:target + 1]}, f"只能为空或正好 {target} 条")
        rej({"addAttrs": [wrong_tier] + affix_ids[:target - 1]}, f"不在品质 {quality} 的词缀池内")
        rej({"addAttrs": [99999999] + affix_ids[:target - 1]}, "不在词缀表里")
        rej({"addAttrs": [affix_ids[0], affix_ids[0]]}, "洗练词缀重复")
        if six_dimension:
            # 六维混普通词缀：含六维时条数必须正好 1，与槽位数无关。
            rej({"addAttrs": [six_dimension[0], affix_ids[0]]}, "六维词缀只能 1 条")
        print("PASS 非法词缀输入全部回传具体中文原因（含槽位数与具体 id/档位）")

        # serverId 必须为正实例号，不能用新装备的 0 去模糊定位
        rejected(path + "/equip-affix", {"serverId": "0", "addAttrs": affix_ids}, "装备实例不能小于 1")

        # 被拒之后词缀一个字节都不能动（此刻是上一条成功用例留下的满词缀状态）
        assert persisted_affixes(player_id, target_item) == affix_ids
        assert game.bag_snapshot()[item_id]["addAttrs"] == affix_ids

        # ---- 7. 与手工装备的词缀规则一致：两个入口对同一组词缀给出同一套结果 ----
        expected = affix_ids[:target]
        manual = api(path + "/equip-manual", {**base, "addAttrs": expected})["data"]["after"]
        assert manual["addAttrs"] == expected, manual
        assert persisted_affixes(player_id, target_item) == expected
        print("PASS equip-manual 与 equip-affix 对同一件装备给出同一套词缀规则结果")

        game.sock.close()
    finally:
        try:
            game.sock.close()
        except OSError:
            pass
        cleanup = Game()
        cleanup.login(account, password, enter=False)
        cleanup.rpc(20018, vf(1, player_id))
        cleanup.sock.close()
        print("PASS temporary test role removed through native delete-role protocol")


if __name__ == "__main__":
    main()
