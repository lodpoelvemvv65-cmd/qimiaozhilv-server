# -*- coding: utf-8 -*-
"""在客户端 DLL 的元数据里按片段搜索字段名 / 类型名 / 方法名 / 用户字符串字面量。

用法：
  python tools/_search_meta.py <Hotfix|HotfixView> field  <片段...>
  python tools/_search_meta.py <Hotfix|HotfixView> type   <片段...>
  python tools/_search_meta.py <Hotfix|HotfixView> method <片段...>
  python tools/_search_meta.py <Hotfix|HotfixView> ustr   <片段...>      # 只看 #US 字符串表
  python tools/_search_meta.py <Hotfix|HotfixView> use    <片段...>      # 找 ldstr 引用该字符串的方法

默认读 `client-test/`（本地测试客户端，Hotfix/HotfixView 打过本地补丁）；
加 `--client public` 读 `client-127.0.0.1/`（原版，反汇编应优先看这个）。
"""

from __future__ import annotations

import sys
from pathlib import Path

import dnfile
from dncil.cil.body import CilMethodBody
from dncil.cil.body.reader import CilMethodBodyReaderBytes
from dncil.cil.error import MethodBodyFormatError
from dncil.clr.token import StringToken

sys.stdout.reconfigure(encoding="utf-8", errors="replace")
ROOT = Path(__file__).resolve().parent.parent


def dll_path(assembly: str, public: bool) -> Path:
    name = "client-127.0.0.1" if public else "client-test"
    data_root = next(p for p in (ROOT / name).iterdir() if p.name.endswith("_Data"))
    return data_root / "StreamingAssets" / "yoo" / "_extracted" / f"{assembly}.dll"


def main() -> None:
    argv = sys.argv[1:]
    public = "public" in argv
    argv = [a for a in argv if a != "public"]
    if len(argv) < 3:
        raise SystemExit(__doc__)
    assembly, mode, fragments = argv[0], argv[1], tuple(argv[2:])
    path = dll_path(assembly, public)
    print("# %s  (%s)" % (path.name, "远程原版" if public else "本地测试客户端"))
    pe = dnfile.dnPE(str(path))
    md = pe.net.mdtables
    frags = tuple(f.casefold() for f in fragments)

    if mode in ("field", "type", "method"):
        if mode == "type":
            for rid, td in enumerate(md.TypeDef.rows, 1):
                name = "%s.%s" % (td.TypeNamespace, td.TypeName)
                if any(f in name.casefold() for f in frags):
                    print("rid=%-6d %s" % (rid, name.strip(".")))
            return
        if mode == "method":
            for rid, row in enumerate(md.MethodDef.rows, 1):
                name = str(row.Name)
                if any(f in name.casefold() for f in frags):
                    print("MethodDef %-6d %s" % (rid, name))
            return
        # field：按 TypeDef 的 FieldList 归属
        for rid, td in enumerate(md.TypeDef.rows, 1):
            tname = "%s.%s" % (td.TypeNamespace, td.TypeName)
            if td.FieldList is None:
                continue
            for fi in td.FieldList:
                row = fi.row
                fname = str(row.Name)
                if not any(f in fname.casefold() for f in frags):
                    continue
                sig = ""
                try:
                    sig = str(row.Signature.value) if hasattr(row.Signature, "value") else ""
                except Exception:
                    pass
                print("type rid=%-6d %-52s field %-24s flag=%s size=%s"
                      % (rid, tname.strip("."), fname, row.Flags, row.Offset))
        return

    if mode == "ustr":
        # #US 堆不直接可迭代，改从所有方法体的 ldstr 里收集（与 use 同一遍扫描，顺带去重）
        seen = {}
        owner = {}
        for rid, td in enumerate(md.TypeDef.rows, 1):
            if td.MethodList is None:
                continue
            for mi in td.MethodList:
                owner[mi.row_index] = (rid, "%s.%s" % (td.TypeNamespace, td.TypeName))
        us = pe.net.user_strings
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
                except Exception:
                    continue
                if any(f in text.casefold() for f in frags):
                    where = owner.get(rid, (0, "?"))
                    seen.setdefault(text, "%s::%s" % (where[1].strip("."), row.Name))
        for text in sorted(seen):
            print("%-90s <- %s" % (repr(text)[:90], seen[text]))
        return

    if mode == "use":
        us = pe.net.user_strings
        owner = {}
        for rid, td in enumerate(md.TypeDef.rows, 1):
            if td.MethodList is None:
                continue
            for mi in td.MethodList:
                owner[mi.row_index] = (rid, "%s.%s" % (td.TypeNamespace, td.TypeName))
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
                except Exception:
                    continue
                if any(f in text.casefold() for f in frags):
                    where = owner.get(rid, (0, "?"))
                    print("%-6d %s::%s\tIL_%04x\t%r"
                          % (where[0], where[1].strip("."), row.Name, ins.offset, text[:70]))
        return

    raise SystemExit(__doc__)


if __name__ == "__main__":
    main()
