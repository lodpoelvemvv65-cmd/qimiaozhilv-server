# -*- coding: utf-8 -*-
"""Add the higher-capacity magic balls to the local ShopBase bundle.

The ordinary shop UI enumerates the client's ShopBase TextAsset, so a server
YAML/MySQL change alone cannot create visible slots.  This installer only
touches client-test and keeps a side-by-side rollback copy of the original
bundle.  The public client is intentionally not modified.
"""

import base64
import glob
import json
import os
import re
import shutil
import sys

import UnityPy


sys.stdout.reconfigure(encoding="utf-8", errors="replace")


ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUNDLE_NAME = "87c13c7e72546d4e4e0960392e451498.bundle"
CLIENT_ROOT = glob.glob(os.path.join(ROOT, "client-test", "*_Data"))[0]
YOO_ROOT = os.path.join(CLIENT_ROOT, "StreamingAssets", "yoo")
TARGET = os.path.join(YOO_ROOT, "aa0", BUNDLE_NAME)
# This is the already-installed local datatable bundle with prior local
# patches.  Fall back to the archived decrypted source on a clean checkout.
SOURCE_CANDIDATES = [
    TARGET,
    os.path.join(ROOT, "_work", "datatable-blue-mech", BUNDLE_NAME),
]
SOURCE_CANDIDATES += glob.glob(
    os.path.join(ROOT, "_archive", "client-test-backup-*", "*_Data",
                 "StreamingAssets", "yoo", "_dec", "aa0", BUNDLE_NAME)
)
SOURCE = next((path for path in SOURCE_CANDIDATES if os.path.isfile(path)), None)
OUTPUT_DIR = os.path.join(ROOT, "_work", "shop-magic-balls")

MAGIC_BALL_ROWS = {
    10036: (110809, 1000000),
    10037: (110810, 2000000),
    10038: (110811, 1000000),
    10039: (110812, 2000000),
    10040: (110832, 4000000),
    10041: (110833, 8000000),
    10042: (110834, 4000000),
    10043: (110835, 8000000),
    # Equipment enhancement coupon.  The server may toggle this row and
    # change its price at runtime, but the client still needs a static slot
    # in ShopBase before it can render the item in the ordinary shop.
    110836: (110836, 500),
}

# Keep the local client tooltip in sync with the operational GoodsBase YAML.
# The server still treats MySQL as authoritative; these fields are only the
# static name/description shown by the Unity item detail panel.
MAGIC_BALL_GOODS_OVERRIDES = {
    110330: {
        "Name": "5000万精力魔法球",
        "Description": "战斗结束后自动回满精力，不可叠加，存储50000000mp",
        "Capacity": 50000000,
    },
    110833: {
        "Name": "8亿生命魔法球",
        "Description": "战斗结束后自动回满生命，不可叠加，存储800000000hp",
        "Capacity": 800000000,
    },
    110835: {
        "Name": "8亿精力魔法球",
        "Description": "战斗结束后自动回满精力，不可叠加，存储800000000mp",
        "Capacity": 800000000,
    },
}


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
    import zlib
    return parse_rows(zlib.decompress(decoded).decode("utf-8"))


def write_rows(asset, rows):
    import zlib
    text = "[\r\n" + ",\r\n".join(
        "[%d, %s]" % (key, json.dumps(value, ensure_ascii=False, separators=(",", ":")))
        for key, value in rows
    ) + "\r\n]"
    encoded = base64.b64encode(zlib.compress(text.encode("utf-8"))).decode("ascii")
    data = asset.read()
    data.m_Script = encoded
    data.save()


def main():
    if SOURCE is None:
        raise RuntimeError("no decrypted datatable bundle source found")
    env = UnityPy.load(SOURCE)
    assets = {}
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        name = obj.read().m_Name.strip()
        if name in ("ShopBase", "GoodsBase"):
            assets[name] = obj
    if "ShopBase" not in assets or "GoodsBase" not in assets:
        raise RuntimeError(
            "expected ShopBase and GoodsBase TextAssets, found %s"
            % ", ".join(sorted(assets))
        )

    shop_asset = assets["ShopBase"]
    shop_rows = read_rows(shop_asset)
    by_id = {int(row[0]): row for row in shop_rows}
    shop_changed = False
    for key, (item_id, price) in MAGIC_BALL_ROWS.items():
        expected = [key, {"_id": key, "ItemType": 2, "Page": 0,
                          "ItemId": item_id, "Price": price}]
        if by_id.get(key) != expected:
            by_id[key] = expected
            shop_changed = True

    goods_asset = assets["GoodsBase"]
    goods_rows = read_rows(goods_asset)
    goods_by_id = {int(row[0]): row for row in goods_rows}
    goods_changed = False
    for item_id, overrides in MAGIC_BALL_GOODS_OVERRIDES.items():
        row = goods_by_id.get(item_id)
        if row is None:
            raise RuntimeError("GoodsBase item %d not found" % item_id)
        for field, value in overrides.items():
            if row[1].get(field) != value:
                row[1][field] = value
                goods_changed = True
    if not (shop_changed or goods_changed):
        print("ShopBase magic-ball rows already present")
        return

    if shop_changed:
        write_rows(shop_asset, [by_id[key] for key in sorted(by_id)])
    if goods_changed:
        write_rows(goods_asset, [goods_by_id[key] for key in sorted(goods_by_id)])
    for file_item in env.files.values():
        file_item.mark_changed()
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    env.save(pack="lz4", out_path=OUTPUT_DIR)
    edited = os.path.join(OUTPUT_DIR, BUNDLE_NAME)
    backup = TARGET + ".pre-shop-magic-balls.bak"
    if not os.path.exists(backup):
        shutil.copy2(TARGET, backup)
    shutil.copy2(edited, TARGET)
    print("installed", TARGET)
    print("rollback copy", backup)


if __name__ == "__main__":
    main()
