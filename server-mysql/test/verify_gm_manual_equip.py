# -*- coding: utf-8 -*-
"""GM 手工装备（player.equip_manual）：星级/随机属性/洗练词缀/强化/宝石全链路实测。

Run via: MHQ_TEST_GM_MANUAL_TCP=1 go test ./cmd/gm-api -run TestGMManualEquipLiveTCP -v
The Go harness supplies an ephemeral local test session; no GM password needed.

覆盖：
  1. 候选池接口的中文名与装备规则（部位、宝石槽数、允许的宝石）与真实配置一致；
  2. 成功路径：品质/星级/强化/随机属性/词缀/宝石一次设成，客户端可见包与落库都一致；
  3. **重登不变样**——词缀的三种稳定态（空 / 正好槽位数 / 1 条六维）重登后都原样，
     随机属性与宝石槽同样不变。这是「严格按服务端规则」的验收标准；
  4. 非法输入回传**具体中文原因**（写出上限数字），而不是裸状态码；
  5. 被拒的提交不留下部分写入。
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
            self.rpc(20016, vf(1, player_id) + vf(2, 1) + sf(3, "手工" + account[-8:]))
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


def persisted(player_id, item):
    """GM API 读的是 MySQL 落库结果（服务端的登录修复也会落库），按 location+slot 归并。

    GM API 把所有 int64 列都序列化成字符串（server_id 等），所以这里统一 int() 归一化，
    位置也按数值排序（字符串排序会把 10 排到 2 前面）。
    """
    def rows(resource, column):
        items = api(f"/players/{player_id}/{resource}")["items"]
        selected = [row for row in items if same_slot(row, item)]
        selected.sort(key=lambda row: int(row["position"]))
        return [int(row[column]) for row in selected]
    return {
        "randomAttrs": rows("random-attributes", "attribute_id"),
        "addAttrs": rows("affixes", "affix_id"),
        "gems": rows("gems", "gem_item_id"),
    }


def bag_item(player_id, item_id):
    for entry in api(f"/players/{player_id}/inventory")["items"]:
        if int(entry["item_id"]) == item_id:
            return entry
    raise AssertionError(f"granted item {item_id} is not in the bag")


def relogin(account, password, player_id):
    """完整重登：走到 20027 EnterGame，服务端的登录修复（ensureEquipmentVariations）就在这一步跑。

    返回 (新连接, 背包全量)。必须返回新连接：新 LoginGate 成功后服务端会向旧会话推
    G2C_ForceOffLine(20328) 并把它排除出在线路由，旧 socket 之后只能心跳。
    """
    session = Game()
    assert session.login(account, password) == player_id
    bag = session.bag_snapshot()
    time.sleep(0.5)
    return session, bag


def main():
    account = os.environ["GM_TEST_ACCOUNT"]
    assert account.startswith("gmmanual") and HOST in ("127.0.0.1", "localhost"), "local isolated tests only"
    password = "Test" + uuid.uuid4().hex[:12]
    game = Game()
    player_id = game.login(account, password, create=True)
    path = f"/players/{player_id}"
    try:
        options = api("/equip-manual/options")
        limits = options["limits"]
        assert limits["minStar"] == 1 and limits["maxStar"] == 20 and limits["maxStrength"] == 20, limits
        assert limits["minQuality"] == 1 and limits["maxQuality"] == 6, limits
        assert limits["bonusCountByQuality"] == {"1": 1, "2": 1, "3": 2, "4": 2, "5": 3, "6": 3}, limits
        assert options["attributeNames"]["3"] == "力量" and options["attributeNames"]["1"] == "最大生命"
        assert all(name and name != "无" for name in options["gemTypeNames"].values()), options["gemTypeNames"]
        assert all(gem["name"] and gem["gemKeyName"] and gem["gemTypeName"] for gem in options["gems"])
        assert all(affix["label"] and all(attr["keyName"] for attr in affix["attrs"]) for affix in options["affixes"])
        print(f"PASS options: limits, {len(options['gems'])} gems, {len(options['affixes'])} affixes, 全中文名")

        catalog = api("/items")["items"]
        catalog_by_id = {item["id"]: item for item in catalog}

        gem_item_id = None
        for candidate in GEM_ITEM_CANDIDATES:
            rule = equip_rule(api(f"/equip-manual/options?itemId={candidate}"), candidate)
            if not rule or rule["maxHole"] < 2 or candidate not in catalog_by_id:
                continue
            if len(gem_rows_by_key(options, rule["allowedGemIds"])) < rule["maxHole"]:
                continue
            gem_item_id = candidate
            break
        assert gem_item_id, "no gem-capable equipment candidate"

        # 再找一件没有宝石槽的装备（EquipBase.MaxHole 为 0），用来验证「没有宝石槽」这条
        # 规则先于宝石内容生效。候选只能一个个问接口：equipRules 依赖 EquipBase 的
        # Type/MaxHole，服务端没给批量入口。
        gemless_item_id = None
        for entry in catalog:
            if not entry["equipSlotName"] or entry["id"] == gem_item_id:
                continue
            candidate_rule = equip_rule(api(f"/equip-manual/options?itemId={entry['id']}"), entry["id"])
            if candidate_rule and candidate_rule["maxHole"] == 0:
                gemless_item_id = entry["id"]
                break
        assert gemless_item_id, "no equipment without gem slots"

        rule = equip_rule(api(f"/equip-manual/options?itemId={gem_item_id}"), gem_item_id)
        max_hole = rule["maxHole"]
        allowed_ids = rule["allowedGemIds"]
        assert rule["typeName"] == catalog_by_id[gem_item_id]["equipSlotName"], (rule, catalog_by_id[gem_item_id])
        gems_by_key = gem_rows_by_key(options, allowed_ids)
        gem_ids = [gems_by_key[key][0] for key in sorted(gems_by_key)[:max_hole]]
        disallowed = next(gem["id"] for gem in options["gems"] if gem["id"] not in set(allowed_ids))
        print(f"PASS equip rules: {gem_item_id} {rule['typeName']} {rule['maxHole']} 孔 / "
              f"{len(gems_by_key)} 种允许属性；{gemless_item_id} {catalog_by_id[gemless_item_id]['equipSlotName']} 无宝石槽")

        api(path + "/items/grant", {"items": [{"itemId": gem_item_id, "count": 1}, {"itemId": gemless_item_id, "count": 1}]})
        target = bag_item(player_id, gem_item_id)
        gemless = bag_item(player_id, gemless_item_id)
        # 新发放的装备 server_id 是 0（player_items.server_id 默认 0，只有 cmd/grant-items
        # 会发号），所以整条用例都靠 location + slotIndex 定位。
        assert str(target["server_id"]) == "0" and str(gemless["server_id"]) == "0"
        # 装备自身的品质可能不在 1..6（配置缺失时为 0），这里统一显式设成 5 档，
        # 让随机属性档位、词缀池、词缀槽位数都有确定来源。
        quality = int(target["quality"])
        quality = quality if 1 <= quality <= 6 else 5

        pool = options["affixPools"]["byQuality"][str(quality)]
        bonus = limits["bonusCountByQuality"][str(quality)]
        assert len(pool) > bonus, (len(pool), bonus)
        random_pool = [attr["id"] for attr in options["randomAttrsByQuality"][str(quality)]]
        assert len(random_pool) >= 8, len(random_pool)
        six_dimension = options["affixPools"]["sixDimension"][0]
        base = {**loc(target), "quality": str(quality)}

        # ---- 成功路径 + 三种词缀稳定态的重登不变性 ----
        cases = [
            ("规格A 空词缀", [], 8, 15, random_pool[:8]),
            ("规格B 正好槽位", list(pool[:bonus]), 6, 12, random_pool[:6]),
            ("规格C 一条六维", [six_dimension], 3, 9, random_pool[:3]),
        ]
        for label, affixes, star, strength, random_attrs in cases:
            payload = {**base, "star": str(star), "strength": str(strength),
                       "randomAttrs": random_attrs, "addAttrs": affixes, "gems": gem_ids}
            pushes_before = len(game.pushes)
            result = api(path + "/equip-manual", payload)
            assert result["status"] == "completed", result
            after = result["data"]["after"]
            assert after["quality"] == quality and after["star"] == star and after["strength"] == strength, after
            assert after["randomAttrs"] == random_attrs and after["addAttrs"] == affixes, after
            assert after["gems"] == gem_ids and len(after["gems"]) == max_hole, after

            # 客户端可见包（20259 全量背包）：星级、品质、强化、词条、词缀、宝石槽。
            # 顺带确认服务端向在线会话推了 20260 全量背包快照。
            wire = game.bag_snapshot()[gem_item_id]
            assert any(op == 20260 for op, _ in game.pushes[pushes_before:]), "在线角色没有收到背包快照推送"
            assert wire["star"] == star and wire["quality"] == quality and wire["strength"] == strength, wire
            assert wire["randomAttrs"] == random_attrs and wire["addAttrs"] == affixes, wire
            assert wire["gems"][:max_hole] == gem_ids, wire

            # 落库
            stored = persisted(player_id, target)
            assert stored["randomAttrs"] == random_attrs, stored
            assert stored["addAttrs"] == affixes, stored
            assert stored["gems"] == gem_ids, stored
            print(f"PASS {label}: 星级{star} 强化{strength} 随机属性{len(random_attrs)}条 "
                  f"词缀{len(affixes)}条 宝石{len(gem_ids)}颗（在线推送 + 落库）")

            # **重登**：登录修复会裁剪并补齐词缀，这三种状态必须一个字段都不变
            game.sock.close()
            game, reloaded = relogin(account, password, player_id)
            reloaded = reloaded[gem_item_id]
            assert reloaded["star"] == star and reloaded["quality"] == quality and reloaded["strength"] == strength, reloaded
            assert reloaded["randomAttrs"] == random_attrs, reloaded
            assert reloaded["addAttrs"] == affixes, reloaded
            assert reloaded["gems"][:max_hole] == gem_ids, reloaded
            stored = persisted(player_id, target)
            assert stored["randomAttrs"] == random_attrs, stored
            assert stored["addAttrs"] == affixes, stored
            assert stored["gems"] == gem_ids, stored
            print(f"PASS {label}: 重登后 6 个字段一个都没变（客户端包 + MySQL）")

        # ---- 降星：只改星级+随机属性，词缀保持不动（与 equip_adjust 的解耦点）----
        after = api(path + "/equip-manual", {**base, "star": "1", "randomAttrs": random_pool[:1]})["data"]["after"]
        assert after["star"] == 1 and after["randomAttrs"] == random_pool[:1], after
        assert after["addAttrs"] == [six_dimension], after
        game.sock.close()
        game, _ = relogin(account, password, player_id)
        stored = persisted(player_id, target)
        assert stored["addAttrs"] == [six_dimension] and stored["randomAttrs"] == random_pool[:1], stored
        print("PASS 降到星级 1：词缀原样保留，随机属性跟着星级变成 1 条且重登不变")

        # ---- 错误文案：必须是具体中文原因，写出上限数字 ----
        rej = lambda payload, fragment: rejected(path + "/equip-manual", {**base, **payload}, fragment)
        rej({"star": "25"}, "星级不能超过 20")
        rej({"star": "-1"}, "星级不能小于 1")
        # 星级 0 会被 equip.go repairBagItem 在下次推送/登录补回 EquipBase.Star，必须直接拒绝
        rej({"star": "0"}, "星级不能小于 1")
        rej({"strength": "25"}, "强化等级不能超过 20")
        rej({"strength": "21"}, "强化等级不能超过 20")
        rej({"quality": "7"}, "装备品质不能超过 6")
        rej({"quality": "0"}, "装备品质不能小于 1")
        rej({"star": "8", "randomAttrs": random_pool[:3]}, "星级 8 需要 8 条随机属性，当前提交了 3 条")
        rej({"star": "8", "randomAttrs": [random_pool[0]] * 8}, "随机属性重复")
        other_quality = 1 if quality != 1 else 2
        other_id = options["randomAttrsByQuality"][str(other_quality)][0]["id"]
        rej({"star": "1", "randomAttrs": [other_id]}, f"不属于 {quality} 档属性池")
        rej({"addAttrs": [six_dimension, pool[0]]}, "六维词缀只能 1 条，当前提交了 2 条")
        rej({"addAttrs": list(pool[:bonus + 1])}, f"品质 {quality} 的洗练词缀只能为空或正好 {bonus} 条")
        rej({"addAttrs": [six_dimension, six_dimension]}, "洗练词缀重复")
        rej({"gems": gem_ids[:max_hole - 1]}, f"该装备有 {max_hole} 个宝石槽，必须正好提交 {max_hole} 项")
        rej({"gems": [gem_ids[0]] * max_hole}, "该装备已镶嵌相同属性宝石，请更换其他属性！")
        rej({"gems": [110305] * max_hole}, "不是宝石")
        rej({"gems": [disallowed] * max_hole}, f"该部位不能镶嵌 {disallowed} 这种宝石！")
        print("PASS 16 类非法输入全部回传具体中文原因（含上限数字与具体 id），而不是裸状态码")

        # 不能镶宝石的部位：gemMaxHole=0 必须先于宝石内容报错
        rejected(path + "/equip-manual", {**loc(gemless), "gems": [gem_ids[0]]},
                 "该装备没有宝石槽")
        # serverId 必须为正实例号：新装备的 0 要明确报错，不能退化成「随便挑一件」
        rejected(path + "/equip-manual", {"serverId": "0", "star": "1"}, "装备实例不能小于 1")
        # 星级变了却不带随机属性：不能留下「星级 N 但只有 M 条」的中间态
        rejected(path + "/equip-manual", {**base, "star": "5"},
                 "星级 5 需要 5 条随机属性，装备当前有 1 条")

        # 被拒之后状态一个字段都不能动（此刻是上一条降星用例留下的 星级1 + 1条随机属性
        # + 1 条六维词缀 + 满槽宝石）
        stored = persisted(player_id, target)
        assert stored["addAttrs"] == [six_dimension] and stored["randomAttrs"] == random_pool[:1], stored
        wire = game.bag_snapshot()[gem_item_id]
        assert wire["addAttrs"] == [six_dimension] and wire["randomAttrs"] == random_pool[:1], wire
        assert wire["star"] == 1 and wire["gems"][:max_hole] == gem_ids, wire
        print("PASS 被拒的提交没有留下任何部分写入")

        # ---- 老的「调整装备」面板（player.equip_adjust）同样要能用 location + slotIndex 定位 ----
        # 它原来只发 serverId，而新发放装备的 server_id 是 0，于是这类装备一件都改不动
        # （服务端会回「装备实例不能小于 1」）。这里用刚发放、server_id 仍为 0 的帽子验证。
        result = api(path + "/equip-adjust", {**loc(gemless), "strength": "7"})
        assert result["status"] == "completed", result
        assert result["data"]["after"]["strength"] == 7, result["data"]
        rejected(path + "/equip-adjust", {"serverId": "0", "strength": "7"}, "装备实例不能小于 1")
        assert game.bag_snapshot()[gemless_item_id]["strength"] == 7, "调整装备没有推到在线会话的背包里"
        print("PASS equip-adjust: server_id 为 0 的新装备用 location + slotIndex 能把强化改成 7")

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
