# -*- coding: utf-8 -*-
"""在所有客户端 DLL 里搜 flagType / thinkerType / overlayType / stateV / perOverlay：
  A) 作为 TypeDef 字段名出现
  B) 作为 #US 字符串字面量（ldstr）出现，并给出引用它的方法

用法： python tools/_find_flagtype_all.py [client-test|client-127.0.0.1]
输出： stdout（重定向到文件再 Read）
"""
from __future__ import annotations

import sys
from pathlib import Path

import dnfile
from dncil.cil.body import CilMethodBody
from dncil.cil.body.reader import CilMethodBodyReaderBytes
from dncil.cil.error import MethodBodyFormatError

sys.stdout.reconfigure(encoding="utf-8", errors="replace")
ROOT = Path(__file__).resolve().parent.parent

FRAGS = [f.casefold() for f in (
    "flagtype", "thinkertype", "overlaytype", "statev", "peroverlay",
    "valuek", "valuev", "statek", "skilleventdic", "modifiereventdic",
    "skilllogicconfig",
)]


def dlls(client: str):
    data_root = next(p for p in (ROOT / client).iterdir() if p.name.endswith("_Data"))
    base = data_root / "StreamingAssets" / "yoo" / "_extracted"
    yield from sorted(base.glob("*.dll"))


def scan(path: Path, as_str: bool) -> None:
    try:
        pe = dnfile.dnPE(str(path))
    except Exception as exc:  # noqa: BLE001
        print("  !! open failed %s: %s" % (path.name, exc))
        return
    if pe.net is None or pe.net.mdtables is None:
        return
    md = pe.net.mdtables

    if not as_str:
        hits = []
        for rid, td in enumerate(md.TypeDef.rows, 1):
            if td.FieldList is None:
                continue
            for fi in td.FieldList:
                fname = str(fi.row.Name).casefold()
                for f in FRAGS:
                    if f == fname:
                        hits.append("    rid=%-6d %s.%s  field=%s"
                                    % (rid, td.TypeNamespace, td.TypeName, fi.row.Name))
                        break
        if hits:
            print(" [FIELD] %s" % path.name)
            print("\n".join(hits))
        return

    # string scan
    owner = {}
    for rid, td in enumerate(md.TypeDef.rows, 1):
        if td.MethodList is None:
            continue
        for mi in td.MethodList:
            owner[mi.row_index] = "%s::%s" % (
                ("%s.%s" % (td.TypeNamespace, td.TypeName)).strip("."), "?")
    meth_name = {}
    for rid, row in enumerate(md.MethodDef.rows, 1):
        meth_name[rid] = str(row.Name)

    us = pe.net.user_strings
    hits = []
    for rid, row in enumerate(md.MethodDef.rows, 1):
        rva = getattr(row, "Rva", 0)
        if not rva:
            continue
        try:
            body = CilMethodBody(CilMethodBodyReaderBytes(pe.get_data(rva, 0x8000)))
        except MethodBodyFormatError:
            continue
        for ins in body.instructions:
            if ins.opcode.name != "ldstr":
                continue
            try:
                value = us.get(ins.operand.rid).value
                if isinstance(value, bytes):
                    value = value.decode("utf-16-le", "replace")
                text = str(value)
            except Exception:  # noqa: BLE001
                continue
            low = text.casefold()
            for f in FRAGS:
                if f in low:
                    hits.append("    %s::%s  IL_%04x  %r"
                                % (owner.get(rid, "?"), meth_name.get(rid, "?"), ins.offset, text[:60]))
                    break
    if hits:
        print(" [STR] %s" % path.name)
        print("\n".join(hits))


def main() -> None:
    client = sys.argv[1] if len(sys.argv) > 1 else "client-127.0.0.1"
    print("# client = %s" % client)
    for path in dlls(client):
        scan(path, as_str=False)
    print()
    for path in dlls(client):
        scan(path, as_str=True)


if __name__ == "__main__":
    main()
