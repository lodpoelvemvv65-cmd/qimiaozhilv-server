# -*- coding: utf-8 -*-
"""Install the strengthening coupon in the local client's market data.

The server's MySQL/YAML tables are authoritative, but Unity renders market
slots and item descriptions from its embedded datatable bundle.  This patch is
limited to client-test and keeps rollback copies of every touched bundle.
"""

import base64
import glob
import json
import os
import re
import shutil
import zlib

import UnityPy


ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUNDLE_NAME = "87c13c7e72546d4e4e0960392e451498.bundle"
CLIENT_ROOT = glob.glob(os.path.join(ROOT, "client-test", "*_Data"))[0]
YOO_ROOT = os.path.join(CLIENT_ROOT, "StreamingAssets", "yoo")
TARGET = os.path.join(YOO_ROOT, "aa0", BUNDLE_NAME)
DECODED = os.path.join(YOO_ROOT, "_dec", "aa0", BUNDLE_NAME)
OUTPUT_DIR = os.path.join(ROOT, "_work", "strength-coupon-datatable")

COUPON_ID = 110836
MARKET_ID = 10033
DESCRIPTION = "强化装备到20级（满级），强化背包第一个（左上角）位置的装备"


def parse_rows(text):
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return json.loads(re.sub(r",(\s*[}\]])", r"\1", text))


def read_rows(asset):
    raw = asset.read().m_Script
    if isinstance(raw, str):
        raw = raw.encode("utf-8")
    decoded = base64.b64decode(raw.decode("utf-8").strip())
    return parse_rows(zlib.decompress(decoded).decode("utf-8"))


def write_rows(asset, rows):
    text = "[\r\n" + ",\r\n".join(
        "[%d, %s]" % (key, json.dumps(value, ensure_ascii=False, separators=(",", ":")))
        for key, value in rows
    ) + "\r\n]"
    encoded = base64.b64encode(zlib.compress(text.encode("utf-8"))).decode("ascii")
    data = asset.read()
    data.m_Script = encoded
    data.save()


def backup_once(path):
    backup = path + ".pre-strength-coupon.bak"
    if os.path.isfile(path) and not os.path.exists(backup):
        shutil.copy2(path, backup)


def main():
    if not os.path.isfile(TARGET):
        raise FileNotFoundError(TARGET)
    env = UnityPy.load(TARGET)
    assets = {}
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name in ("GoodsBase", "MarketBase"):
            assets[data.m_Name] = obj
    if set(assets) != {"GoodsBase", "MarketBase"}:
        raise RuntimeError("GoodsBase/MarketBase TextAssets missing")

    goods_rows = read_rows(assets["GoodsBase"])
    goods = {int(row[0]): row for row in goods_rows}
    coupon = goods.get(COUPON_ID)
    if coupon is None:
        raise RuntimeError("GoodsBase item 110836 missing")
    coupon[1]["Description"] = DESCRIPTION

    market_rows = read_rows(assets["MarketBase"])
    market = {int(row[0]): row for row in market_rows}
    market[MARKET_ID] = [MARKET_ID, {
        "_id": MARKET_ID,
        "ItemType": 2,
        "Page": 0,
        "ItemId": COUPON_ID,
        "Price_YuanBao": 999999,
    }]

    write_rows(assets["GoodsBase"], [goods[key] for key in sorted(goods)])
    write_rows(assets["MarketBase"], [market[key] for key in sorted(market)])
    for file_item in env.files.values():
        file_item.mark_changed()
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    env.save(pack="lz4", out_path=OUTPUT_DIR)
    edited = os.path.join(OUTPUT_DIR, BUNDLE_NAME)
    if not os.path.isfile(edited):
        raise RuntimeError("UnityPy did not create the patched bundle")

    backup_once(TARGET)
    shutil.copy2(edited, TARGET)
    if os.path.isfile(DECODED):
        backup_once(DECODED)
        shutil.copy2(edited, DECODED)

    # Re-open the installed bundle and verify both rows survive serialization.
    check = UnityPy.load(TARGET)
    found = {}
    for obj in check.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        if data.m_Name in ("GoodsBase", "MarketBase"):
            found[data.m_Name] = read_rows(obj)
    goods_check = {int(row[0]): row for row in found["GoodsBase"]}
    market_check = {int(row[0]): row for row in found["MarketBase"]}
    if goods_check[COUPON_ID][1].get("Description") != DESCRIPTION:
        raise RuntimeError("installed coupon description mismatch")
    if market_check[MARKET_ID][1].get("ItemId") != COUPON_ID:
        raise RuntimeError("installed coupon market row mismatch")
    print("installed", TARGET)
    print("market row", market_check[MARKET_ID][1])
    print("rollback copy", TARGET + ".pre-strength-coupon.bak")


if __name__ == "__main__":
    main()
