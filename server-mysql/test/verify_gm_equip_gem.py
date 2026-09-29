# -*- coding: utf-8 -*-
"""GM 镶嵌宝石（player.equip_gem）：把宝石从「手工装备」里独立出来之后的独立入口实测。

Run via: MHQ_TEST_GM_GEM_TCP=1 go test ./cmd/gm-api -run TestGMEquipGemLiveTCP -v
The Go harness supplies an ephemeral local test session; no GM password needed.

覆盖：
  1. 语义是「设置宝石槽最终状态」：镶嵌满槽、只换一个孔、整件卸下（全 0）都走同一个入口；
  2. **只碰宝石**——星级 / 品质 / 强化 / 随机属性 / 洗练词缀在一个动作前后逐字段不变，
     这正是它和 player.equip_manual 分开的理由（手工那条会强制「星级 N 需要 N 条随机属性」）；
  3. 一件「星级 5 但 0 条随机属性」的新装备（玩法侧新发放装备的正常状态）可以正常镶嵌，
     不会被手工那套联动规则连带拒绝；
  4. 重登后宝石槽原样（客户端可见包 + MySQL 两边都核）；
  5. 非法输入回传**具体中文原因**，被拒的提交宝石槽一个字节都不动。
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

# 一件要能镶满宝石的装备（MaxHole>0，且允许的属性够每孔一个不同 GemKey）。
GEM_ITEM_CANDIDATES = [120227, 120909, 120381, 120401, 120644, 130001, 120904]


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
            self.rpc(20016, vf(1, player_id) + vf(2, 1) + sf(3, "宝石" + account[-8:]))
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


def equip_rule(options, item_id):
    return (options.get("equipRules") or {}).get(str(item_id))


def gem_rows_by_key(options, allowed_ids):
    """allowedGemIds -> {GemKey: [itemId...]}，用来给每个孔挑一个不同属性的宝石。"""
    allowed = set(allowed_ids)
    by_key = {}
    for gem in options["gems"]:
        if gem["id"] in allowed:
            by_key.setdefault(gem["gemKey"], []).append(gem["id"])
    return by_key


def same_slot(left, right):
    return str(left["location"]) == str(right["location"]) and str(left["slot_index"]) == str(right["slot_index"])


def loc(item):
    """装备定位键。player_items 的主键是 (player_id, location, slot_index)，
    而 server_id 对新生成的装备恒为 0（schema 默认 0，只有 cmd/grant-items 会发号），
    所以 GM 面板必须用 location + slotIndex 定位，不能用 serverId。
    """
    return {"location": str(item["location"]), "slotIndex": str(item["slot_index"])}


def persisted_gems(player_id, item):
    """GM API 读的是 MySQL 落库结果，按 location+slot 取宝石子表（position 升序）。"""
    rows = [row for row in api(f"/players/{player_id}/gems")["items"] if same_slot(row, item)]
    rows.sort(key=lambda row: int(row["position"]))
    return [int(row["gem_item_id"]) for row in rows]


def bag_item(player_id, item_id):
    for entry in api(f"/players/{player_id}/inventory")["items"]:
        if int(entry["item_id"]) == item_id:
            return entry
    raise AssertionError(f"granted item {item_id} is not in the bag")


def relogin(account, password, player_id):
    """完整重登：服务端的登录修复（ensureEquipmentVariations）就在 20027 这一步跑。"""
    session = Game()
    assert session.login(account, password) == player_id
    bag = session.bag_snapshot()
    time.sleep(0.5)
    return session, bag


def other_fields(snapshot):
    """取出「宝石以外的所有字段」，用来证明镶嵌宝石不会顺手动别的东西。"""
    return {key: value for key, value in snapshot.items() if key != "gems"}


def main():
    account = os.environ["GM_TEST_ACCOUNT"]
    assert account.startswith("gmgem") and HOST in ("127.0.0.1", "localhost"), "local isolated tests only"
    password = "Test" + uuid.uuid4().hex[:12]
    game = Game()
    player_id = game.login(account, password, create=True)
    path = f"/players/{player_id}"
    try:
        options = api("/equip-manual/options")
        catalog = api("/items")["items"]

        gem_item_id = None
        for candidate in GEM_ITEM_CANDIDATES:
            rule = equip_rule(api(f"/equip-manual/options?itemId={candidate}"), candidate)
            if not rule or rule["maxHole"] < 2:
                continue
            if len(gem_rows_by_key(options, rule["allowedGemIds"])) < rule["maxHole"]:
                continue
            gem_item_id = candidate
            break
        assert gem_item_id, "no gem-capable equipment candidate"

        rule = equip_rule(api(f"/equip-manual/options?itemId={gem_item_id}"), gem_item_id)
        max_hole = rule["maxHole"]
        allowed_ids = rule["allowedGemIds"]
        gems_by_key = gem_rows_by_key(options, allowed_ids)
        gem_ids = [gems_by_key[key][0] for key in sorted(gems_by_key)[:max_hole]]
        disallowed = next(gem["id"] for gem in options["gems"] if gem["id"] not in set(allowed_ids))
        # 同一个 GemKey 的宝石重复填满所有孔，用来验证「同一属性不能镶嵌两颗」。
        same_key_ids = [gems_by_key[min(gems_by_key)][0]] * max_hole
        print(f"PASS equip rules: {gem_item_id} {rule['typeName']} {max_hole} 孔 / "
              f"{len(gems_by_key)} 种允许属性；{len(options['gems'])} 颗宝石全带中文名")

        # 找一件没有宝石槽的装备（EquipBase.MaxHole 为 0，原版表里是称号类）。
        gemless_item_id = None
        for entry in catalog:
            if not entry["equipSlotName"] or entry["id"] == gem_item_id:
                continue
            candidate_rule = equip_rule(api(f"/equip-manual/options?itemId={entry['id']}"), entry["id"])
            if candidate_rule and candidate_rule["maxHole"] == 0:
                gemless_item_id = entry["id"]
                break
        assert gemless_item_id, "no equipment without gem slots"

        api(path + "/items/grant", {"items": [{"itemId": gem_item_id, "count": 1}, {"itemId": gemless_item_id, "count": 1}]})
        target = bag_item(player_id, gem_item_id)
        gemless = bag_item(player_id, gemless_item_id)
        # 新发放的装备 server_id 是 0，整条用例都靠 location + slotIndex 定位。
        assert str(target["server_id"]) == "0" and str(gemless["server_id"]) == "0"
        base = loc(target)

        # ---- 1. 满槽镶嵌：只改宝石，其它字段一个都不动 ----
        pushes_before = len(game.pushes)
        result = api(path + "/equip-gem", {**base, "gems": gem_ids})
        assert result["status"] == "completed", result
        before, after = result["data"]["before"], result["data"]["after"]
        assert after["gems"] == gem_ids and len(after["gems"]) == max_hole, after
        # before/after 里除 gems 之外的字段必须逐一相等：这是「宝石独立」的核心断言。
        assert other_fields(before) == other_fields(after), (before, after)
        wire = game.bag_snapshot()[gem_item_id]
        assert any(op == 20260 for op, _ in game.pushes[pushes_before:]), "在线角色没有收到背包快照推送"
        assert wire["gems"][:max_hole] == gem_ids, wire
        assert persisted_gems(player_id, target) == gem_ids
        print(f"PASS 满槽镶嵌：{len(gem_ids)} 颗宝石写进客户端可见包 + MySQL，"
              f"星级{after['star']}/品质{after['quality']}/强化{after['strength']}/"
              f"随机属性{len(after['randomAttrs'])}条/词缀{len(after['addAttrs'])}条全部原样")

        # ---- 2. 重登不变样 ----
        game.sock.close()
        game, reloaded = relogin(account, password, player_id)
        reloaded = reloaded[gem_item_id]
        assert reloaded["gems"][:max_hole] == gem_ids, reloaded
        assert persisted_gems(player_id, target) == gem_ids
        print("PASS 重登后宝石槽原样（客户端包 + MySQL 两边一致）")

        # ---- 3. 只换一个孔：其余孔保持不动，长度仍是定长 max_hole ----
        swapped = [0 if index == 0 else gem_ids[index] for index in range(max_hole)]
        swapped[0] = gem_ids[max_hole - 1]
        swapped[max_hole - 1] = 0
        result = api(path + "/equip-gem", {**base, "gems": swapped})["data"]["after"]
        assert result["gems"] == swapped and len(result["gems"]) == max_hole, result
        assert game.bag_snapshot()[gem_item_id]["gems"][:max_hole] == swapped
        # player_item_gems 是**定长**落库：saveItemsTx 对 GemList 逐位 INSERT，
        # 空槽也写一行 gem_item_id=0（读取时同样按 position 逐位 append），
        # 所以库里的槽数与 GemList 长度永远一致，不会被压成「只剩非空孔」。
        assert persisted_gems(player_id, target) == swapped, persisted_gems(player_id, target)
        print(f"PASS 只换/清一个孔：槽位仍是定长 {max_hole} 项（客户端包 + 落库都含空槽 0）")

        # ---- 4. 整件卸下：全填 0 ----
        result = api(path + "/equip-gem", {**base, "gems": [0] * max_hole})["data"]["after"]
        assert result["gems"] == [0] * max_hole, result
        assert game.bag_snapshot()[gem_item_id]["gems"][:max_hole] == [0] * max_hole
        assert persisted_gems(player_id, target) == [0] * max_hole, persisted_gems(player_id, target)
        # 卸下不返还物品：背包里不该多出宝石（GM 是直接设状态，不走玩法扣料）。
        assert not any(int(entry["item_id"]) in set(gem_ids) for entry in api(f"/players/{player_id}/inventory")["items"])
        print("PASS 整件卸下：宝石槽全 0、宝石子表清空，且不返还背包物品")

        # ---- 5. 星级 5 但 0 条随机属性的新装备照样能镶 ----
        # 玩法侧新发放的装备天生就是 EquipBase.Star 星级 + 0 条随机属性（repairBagItem
        # 只补 Star，不补 RandomAttrs）。手工装备那条 action 会因为这个状态拒绝除星级外的
        # 提交，宝石这条必须不受影响——这就是两者分开的意义。
        stored_star = int(target["star"])
        # 对照组：同一件装备走手工装备那条改星级，会被联动规则挡下来
        # （星级 N 就必须正好 N 条随机属性，当前 0 条）。
        rejected(path + "/equip-manual", {**base, "star": "5"},
                 "星级 5 需要 5 条随机属性，装备当前有 0 条")
        # 宝石这条完全不涉及星级，同样的装备随时可镶。
        independent = api(path + "/equip-gem", {**base, "gems": gem_ids})["data"]["after"]
        assert independent["star"] == stored_star, independent
        assert independent["randomAttrs"] == [], independent
        assert independent["gems"] == gem_ids, independent
        print(f"PASS 星级 {independent['star']} + 0 条随机属性的新装备能直接镶嵌："
              f"手工装备那条改星级被联动规则拒绝，宝石这条不受影响")

        # ---- 6. 非法输入必须回传具体中文原因 ----
        rej = lambda payload, fragment: rejected(path + "/equip-gem", {**base, **payload}, fragment)
        rej({"gems": gem_ids[:max_hole - 1]}, f"该装备有 {max_hole} 个宝石槽，必须正好提交 {max_hole} 项")
        rej({"gems": gem_ids + [0]}, f"该装备有 {max_hole} 个宝石槽，必须正好提交 {max_hole} 项")
        rej({"gems": [110305] * max_hole}, "不是宝石")
        rej({"gems": [disallowed] * max_hole}, f"该部位不能镶嵌 {disallowed} 这种宝石！")
        rej({"gems": same_key_ids}, "该装备已镶嵌相同属性宝石，请更换其他属性！")
        rej({}, "请至少选择宝石孔")
        print("PASS 6 类非法宝石输入全部回传具体中文原因（含槽位数与具体 id）")

        # 没有宝石槽的装备：部位规则先于宝石内容生效
        rejected(path + "/equip-gem", {**loc(gemless), "gems": [gem_ids[0]]}, "该装备没有宝石槽")
        # serverId 必须为正实例号，不能用新装备的 0 去模糊定位
        rejected(path + "/equip-gem", {"serverId": "0", "gems": gem_ids}, "装备实例不能小于 1")

        # 被拒之后宝石槽一个字节都不能动（此刻是上一条成功用例留下的满槽状态）
        assert persisted_gems(player_id, target) == gem_ids
        assert game.bag_snapshot()[gem_item_id]["gems"][:max_hole] == gem_ids

        # ---- 7. 锁定装备：整单拒绝，解锁后恢复可镶嵌 ----
        api(path + "/equip-adjust", {**base, "isLocked": True})
        rejected(path + "/equip-gem", {**base, "gems": [0] * max_hole}, "装备已锁定，无法镶嵌")
        assert game.bag_snapshot()[gem_item_id]["gems"][:max_hole] == gem_ids, "被拒后宝石槽变了"
        api(path + "/equip-adjust", {**base, "isLocked": False})
        result = api(path + "/equip-gem", {**base, "gems": gem_ids})["data"]["after"]
        assert result["gems"] == gem_ids, result
        print("PASS 锁定装备镶嵌被拒且宝石槽不变，解锁后可以正常镶嵌")

        # ---- 8. 与手工装备的宝石规则一致：两个入口镶出来的结果必须一样 ----
        # 手工装备那边同一件装备、同一组宝石，after.gems 必须与 equip-gem 完全相同。
        expected = [gem_ids[0]] + [0] * (max_hole - 1)
        manual = api(path + "/equip-manual", {**base, "gems": expected})["data"]["after"]
        assert manual["gems"] == expected, manual
        assert persisted_gems(player_id, target) == expected, persisted_gems(player_id, target)
        print("PASS equip-manual 与 equip-gem 对同一件装备给出同一套宝石规则结果")

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
