# -*- coding: utf-8 -*-
"""在客户端程序集里定位目标选择器相关类型（SelectMultiTarget / SelectSingleTarget /
含 flag 语义的枚举），打印字段、属性、枚举成员。用于判定 SelectMultiTarget.flags 的语义。

用法（工作目录随意，脚本自己解析路径）：
    python tools/_dump_target_selector.py [client-test|client-127.0.0.1]
"""
from __future__ import annotations

import sys
from pathlib import Path

import dnfile

sys.stdout.reconfigure(encoding="utf-8", errors="replace")
ROOT = Path(__file__).resolve().parent.parent

TARGETS = [
    r"梦幻奇遇记_Data\Managed\Unity.Model.dll",
    r"梦幻奇遇记_Data\Managed\Unity.ModelView.dll",
    r"梦幻奇遇记_Data\Managed\Cal.Core.dll",
    r"梦幻奇遇记_Data\Managed\Assembly-CSharp.dll",
    r"梦幻奇遇记_Data\StreamingAssets\yoo\_extracted\Hotfix.dll",
    r"梦幻奇遇记_Data\StreamingAssets\yoo\_extracted\HotfixView.dll",
]

# 类型名片段：选择器本身 + 可能与 flags 对应的枚举
NAME_FRAGS = ["selectmultitarget", "selectsingletarget", "selecttarget",
              "targetflag", "targettype", "selectflag", "targetfilter"]

TYPES = ["I4", "U4", "I2", "U2", "I1", "U1", "BOOLEAN", "STRING", "CLASS",
         "VALUETYPE", "GENERICINST", "SZARRAY", "OBJECT"]


def type_name(pe, td):
    """把字段签名里的 TypeDefOrRef 解析成可读名字。"""
    try:
        sig = td.Signature.value
        return str(sig)
    except Exception:  # noqa: BLE001
        return "?"


def scan(path: Path) -> None:
    try:
        pe = dnfile.dnPE(str(path))
    except Exception as exc:  # noqa: BLE001
        print("!! %s: %s" % (path.name, exc))
        return
    if pe.net is None or pe.net.mdtables is None:
        return
    md = pe.net.mdtables
    rows = md.TypeDef.rows
    hit_types = []
    for rid, td in enumerate(rows, 1):
        full = ("%s.%s" % (td.TypeNamespace, td.TypeName)).strip(".")
        low = full.casefold()
        if any(f in low for f in NAME_FRAGS):
            hit_types.append((rid, td, full))
    if not hit_types:
        return
    print("=" * 92)
    print("## %s  (%d 个命中类型)" % (path.name, len(hit_types)))
    for rid, td, full in hit_types:
        # 枚举判定：父类 System.Enum 且第一个字段 value__
        print("")
        print("  TYPE rid=%-6d %s" % (rid, full))
        if td.Extends is not None:
            try:
                print("       extends = %s" % td.Extends.row.TypeName)
            except Exception:  # noqa: BLE001
                pass
        if td.FieldList is not None:
            for fi in td.FieldList:
                fr = fi.row
                try:
                    sig = str(fr.Signature)
                except Exception:  # noqa: BLE001
                    sig = "?"
                print("       FIELD  %-28s %s" % (fr.Name, sig))
        # 枚举成员值：读 Constant 表
        if td.FieldList is not None:
            consts = {}
            if md.Constant is not None:
                for crow in md.Constant.rows:
                    try:
                        parent = crow.Parent
                        # Parent 指向 Field；row_index 是 1 基
                        consts[parent.row_index] = crow.Value
                    except Exception:  # noqa: BLE001
                        pass
            first_rid = td.FieldList[0].row_index if len(td.FieldList) else 0
            members = []
            for i, fi in enumerate(td.FieldList):
                nm = str(fi.row.Name)
                if nm == "value__":
                    continue
                ridx = first_rid + i
                if ridx in consts:
                    members.append("%s=%s" % (nm, consts[ridx]))
            if members:
                print("       ENUM   %s" % ", ".join(members))


def main() -> None:
    client = sys.argv[1] if len(sys.argv) > 1 else "client-test"
    base = ROOT / client
    for rel in TARGETS:
        p = base / rel
        if p.exists():
            scan(p)


if __name__ == "__main__":
    main()
