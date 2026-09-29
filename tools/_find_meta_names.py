# -*- coding: utf-8 -*-
"""在指定的客户端程序集里搜 字段名 / 属性名 / 方法名 / 类型名 是否含片段。"""
from __future__ import annotations

import sys
from pathlib import Path

import dnfile

sys.stdout.reconfigure(encoding="utf-8", errors="replace")
ROOT = Path(__file__).resolve().parent.parent

FRAGS = [f.casefold() for f in (
    "flagtype", "thinkertype", "overlaytype", "peroverlay", "statev", "statek",
    "valuek", "valuev", "skilleventdic", "modifiereventdic", "skilllogic",
    "skilldic", "modifierdic",
)]

DEFAULT_TARGETS = [
    r"梦幻奇遇记_Data\Managed\Unity.Model.dll",
    r"梦幻奇遇记_Data\Managed\Unity.ModelView.dll",
    r"梦幻奇遇记_Data\Managed\Cal.Core.dll",
    r"梦幻奇遇记_Data\Managed\Assembly-CSharp.dll",
    r"梦幻奇遇记_Data\StreamingAssets\yoo\_extracted\Hotfix.dll",
    r"梦幻奇遇记_Data\StreamingAssets\yoo\_extracted\HotfixView.dll",
]


def hit(name: str) -> str | None:
    low = name.casefold()
    for f in FRAGS:
        if f in low:
            return f
    return None


def scan(path: Path) -> None:
    try:
        pe = dnfile.dnPE(str(path))
    except Exception as exc:  # noqa: BLE001
        print("!! %s: %s" % (path.name, exc))
        return
    if pe.net is None or pe.net.mdtables is None:
        print("!! %s: no metadata" % path.name)
        return
    md = pe.net.mdtables
    print("=" * 90)
    print("## %s" % path.name)

    n = 0
    for rid, td in enumerate(md.TypeDef.rows, 1):
        full = ("%s.%s" % (td.TypeNamespace, td.TypeName)).strip(".")
        if hit(full):
            print("  TYPE   rid=%-6d %s" % (rid, full)); n += 1
        if td.FieldList is not None:
            for fi in td.FieldList:
                if hit(str(fi.row.Name)):
                    print("  FIELD  %-46s .%s" % (full, fi.row.Name)); n += 1
        if td.MethodList is not None:
            for mi in td.MethodList:
                if hit(str(mi.row.Name)):
                    print("  METHOD %-46s .%s" % (full, mi.row.Name)); n += 1
    # 属性表独立于 TypeDef 迭代
    if md.PropertyMap is not None:
        for pm in md.PropertyMap.rows:
            parent_rid = pm.Parent.row_index
            try:
                parent = md.TypeDef.rows[parent_rid - 1]
                pname = ("%s.%s" % (parent.TypeNamespace, parent.TypeName)).strip(".")
            except Exception:  # noqa: BLE001
                pname = "?"
            for pi in pm.PropertyList:
                if hit(str(pi.row.Name)):
                    print("  PROPV  %-46s .%s" % (pname, pi.row.Name)); n += 1
    if n == 0:
        print("  (no hits)")


def main() -> None:
    client = sys.argv[1] if len(sys.argv) > 1 else "client-127.0.0.1"
    base = ROOT / client
    for rel in DEFAULT_TARGETS:
        p = base / rel
        if p.exists():
            scan(p)
        else:
            print("!! missing %s" % rel)


if __name__ == "__main__":
    main()
