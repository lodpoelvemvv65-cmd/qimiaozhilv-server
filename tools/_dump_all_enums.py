# -*- coding: utf-8 -*-
"""导出程序集里所有枚举及其成员（名字 + 常量值）。

用法：
  python tools/_dump_all_enums.py [client-test|client-127.0.0.1] [名字片段过滤]

默认扫 `client-127.0.0.1`（远程原版），Hotfix.dll + HotfixView.dll。
枚举取值从 #Constant 表解出（`Constant.Parent` 指向字段行，`Constant.Value` 是原始字节，
按 `Constant.Type` 解释成有/无符号整数）。
"""
from __future__ import annotations

import sys
from pathlib import Path

import dnfile

sys.stdout.reconfigure(encoding="utf-8", errors="replace")
ROOT = Path(__file__).resolve().parent.parent

# ElementType → (字节数, 是否有符号)
NUMERIC = {
    0x04: (1, True), 0x05: (1, False),     # I1 / U1
    0x06: (2, True), 0x07: (2, False),     # I2 / U2
    0x08: (4, True), 0x09: (4, False),     # I4 / U4
    0x0A: (8, True), 0x0B: (8, False),     # I8 / U8
}


def decode_constant(row) -> object:
    try:
        value = row.Value
        raw = getattr(value, "value", value)
        if isinstance(raw, int):
            return raw
        if not isinstance(raw, (bytes, bytearray)):
            return raw
        etype = getattr(row, "Type", None)
        key = getattr(etype, "value", etype)
        if key in NUMERIC:
            size, signed = NUMERIC[key]
            return int.from_bytes(bytes(raw[:size]), "little", signed=signed)
        return raw
    except Exception as exc:  # noqa: BLE001
        return "?%s" % exc


def dump(path: Path, filt: str) -> None:
    try:
        pe = dnfile.dnPE(str(path))
    except Exception as exc:  # noqa: BLE001
        print("!! %s: %s" % (path.name, exc))
        return
    if pe.net is None or pe.net.mdtables is None:
        print("!! %s: no metadata" % path.name)
        return
    md = pe.net.mdtables

    # Field row_index -> 常量值
    field_index = {}
    for idx, frow in enumerate(md.Field.rows, 1):
        field_index[id(frow)] = idx
    const_by_field = {}
    for row in md.Constant.rows:
        try:
            target = row.Parent.row
            const_by_field[field_index.get(id(target))] = decode_constant(row)
        except Exception:  # noqa: BLE001
            continue

    print("=" * 88)
    print("## %s   (Constant=%d)" % (path.name, len(md.Constant.rows)))
    count = 0
    for rid, td in enumerate(md.TypeDef.rows, 1):
        full = ("%s.%s" % (td.TypeNamespace, td.TypeName)).strip(".")
        if filt and filt.casefold() not in full.casefold():
            continue
        try:
            base = "%s.%s" % (td.Extends.row.TypeNamespace, td.Extends.row.TypeName)
        except Exception:  # noqa: BLE001
            base = ""
        if not base.endswith(".Enum"):
            continue
        count += 1
        print("-" * 84)
        print("rid=%-6d %-42s : %s" % (rid, full, base))
        if td.FieldList is None:
            continue
        for fi in td.FieldList:
            name = str(fi.row.Name)
            if name == "value__":
                continue
            print("      %-34s = %s" % (name, const_by_field.get(field_index.get(id(fi.row)), "?")))
    print()
    print("枚举个数 = %d" % count)


def main() -> None:
    client = sys.argv[1] if len(sys.argv) > 1 else "client-127.0.0.1"
    filt = sys.argv[2] if len(sys.argv) > 2 else ""
    base = ROOT / client
    for rel in [
        r"梦幻奇遇记_Data\StreamingAssets\yoo\_extracted\Hotfix.dll",
        r"梦幻奇遇记_Data\StreamingAssets\yoo\_extracted\HotfixView.dll",
    ]:
        p = base / rel
        if p.exists():
            dump(p, filt)
        else:
            print("!! missing %s" % rel)


if __name__ == "__main__":
    main()
