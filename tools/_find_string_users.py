# -*- coding: utf-8 -*-
"""按 UI 字面量/类型名定位客户端方法（用于协议字段与界面行为对照）。

用法：
  python tools/_find_string_users.py <Hotfix|HotfixView> str <片段> [片段...]
  python tools/_find_string_users.py <Hotfix|HotfixView> type <片段> [片段...]
  python tools/_find_string_users.py <Hotfix|HotfixView> use <类型名> [类型名...]
  python tools/_find_string_users.py <Hotfix|HotfixView> dump <类型名> [方法名前缀...]

`str` 找 ldstr 里含片段的类型::方法；`type` 按类型名找 TypeDef rid；
`use` 找方法体里引用指定类型（newobj/call/callvirt/ldtoken）的调用点；
`dump` 打印该类型（含嵌套状态机）的 IL，后续参数可限定方法名前缀。

默认读 `client-test/` 的 DLL（本地测试客户端）；加 `--client public` 读
`client-127.0.0.1/`（原版）。
"""

from __future__ import annotations

import sys
from pathlib import Path

import dnfile
from dncil.cil.body import CilMethodBody
from dncil.cil.body.reader import CilMethodBodyReaderBytes
from dncil.cil.error import MethodBodyFormatError
from dncil.clr.token import InvalidToken, StringToken, Token

sys.stdout.reconfigure(encoding="utf-8", errors="replace")
ROOT = Path(__file__).resolve().parent.parent


def client_dll(assembly: str, public: bool) -> Path:
    name = "client-127.0.0.1" if public else "client-test"
    data_root = next(p for p in (ROOT / name).iterdir() if p.name.endswith("_Data"))
    return data_root / "StreamingAssets" / "yoo" / "_extracted" / f"{assembly}.dll"


class Module:
    def __init__(self, path: Path) -> None:
        self.pe = dnfile.dnPE(str(path))
        self.md = self.pe.net.mdtables
        self.us = self.pe.net.user_strings
        self.owner: dict[int, tuple[int, str]] = {}
        for rid, td in enumerate(self.md.TypeDef.rows, 1):
            if td.MethodList is None:
                continue
            name = f"{td.TypeNamespace}.{td.TypeName}".strip(".")
            for mi in td.MethodList:
                self.owner[mi.row_index] = (rid, name)
        self.by_name: dict[str, list[int]] = {}
        for rid, td in enumerate(self.md.TypeDef.rows, 1):
            self.by_name.setdefault(str(td.TypeName), []).append(rid)

    def body(self, method_row):
        rva = getattr(method_row, "Rva", 0)
        if not rva:
            return None
        try:
            return CilMethodBody(CilMethodBodyReaderBytes(self.pe.get_data(rva, 0x8000)))
        except MethodBodyFormatError:
            return None

    def string_at(self, token) -> str:
        try:
            value = self.us.get(token.rid).value
        except Exception:
            return ""
        if isinstance(value, bytes):
            try:
                value = value.decode("utf-16-le", "replace")
            except Exception:
                pass
        return str(value)

    def type_of_token(self, token) -> str:
        """把 TypeDef/TypeRef/TypeSpec token 还原成类型名。"""
        try:
            table = token.table
            rid = token.rid
        except Exception:
            return ""
        try:
            if table == 0x02:
                row = self.md.TypeDef.rows[rid - 1]
                return f"{row.TypeNamespace}.{row.TypeName}".strip(".")
            if table == 0x01:
                row = self.md.TypeRef.rows[rid - 1]
                return f"{row.TypeNamespace}.{row.TypeName}".strip(".")
            if table == 0x1B:
                return f"TypeSpec#{rid}"
        except Exception:
            pass
        return ""

    def resolve_operand(self, operand) -> str:
        if isinstance(operand, StringToken):
            return f'"{self.string_at(operand)}"'
        if isinstance(operand, InvalidToken):
            return f"tok=0x{operand.value:08x}"
        if isinstance(operand, Token):
            name = self.type_of_token(operand)
            if name:
                return name
            try:
                if operand.table == 0x0A:
                    row = self.md.MemberRef.rows[operand.rid - 1]
                    parent = self.md.TypeRef.rows[row.Class.row_index - 1]
                    return f"{parent.TypeName}::{row.Name}"
                if operand.table == 0x06:
                    row = self.md.MethodDef.rows[operand.rid - 1]
                    return f"{self.owner.get(operand.rid, (0, '?'))[1]}::{row.Name}"
                if operand.table == 0x04:
                    return f"Field::{self.md.Field.rows[operand.rid - 1].Name}"
            except Exception:
                pass
            return f"t{operand.table}#{operand.rid}"
        return str(operand)


def each_method(module: Module):
    for rid, row in enumerate(module.md.MethodDef.rows, 1):
        body = module.body(row)
        if body is None:
            continue
        yield rid, row, body


def find_strings(module: Module, fragments: tuple[str, ...]) -> None:
    for rid, row, body in each_method(module):
        for ins in body.instructions:
            if ins.opcode.name != "ldstr":
                continue
            text = module.string_at(ins.operand)
            if any(fragment in text for fragment in fragments):
                where = module.owner.get(rid, (0, "?"))
                print(f"{where[0]}\t{where[1]}::{row.Name}\t{text[:80]}")


def find_types(module: Module, fragments: tuple[str, ...]) -> None:
    for rid, td in enumerate(module.md.TypeDef.rows, 1):
        name = f"{td.TypeNamespace}.{td.TypeName}".strip(".")
        if any(fragment.casefold() in name.casefold() for fragment in fragments):
            print(f"{rid}\t{name}")


def find_uses(module: Module, targets: tuple[str, ...]) -> None:
    wanted = {name.casefold() for name in targets}
    for rid, row, body in each_method(module):
        for ins in body.instructions:
            name = module.resolve_operand(ins.operand) if ins.operand is not None else ""
            if name.casefold() in wanted:
                where = module.owner.get(rid, (0, "?"))
                print(f"{where[0]}\t{where[1]}::{row.Name}\tIL_{ins.offset:04x} {ins.opcode.name} {name}")
                break


def dump_type(module: Module, names: tuple[str, ...], prefixes: tuple[str, ...]) -> None:
    for name in names:
        for rid in module.by_name.get(name, []):
            td = module.md.TypeDef.rows[rid - 1]
            print(f"==== TypeDef {rid}: {td.TypeNamespace}.{td.TypeName}")
            for mi in td.MethodList:
                row = mi.row
                if prefixes and not str(row.Name).startswith(prefixes):
                    continue
                body = module.body(row)
                print(f"  ==== method {row.Name}")
                if body is None:
                    print("      <no body>")
                    continue
                for ins in body.instructions:
                    head = f"      IL_{ins.offset:04x}: {ins.opcode.name}"
                    if ins.operand is not None:
                        head += " " + module.resolve_operand(ins.operand)
                    print(head)
            for nc in module.md.NestedClass.rows:
                try:
                    if nc.EnclosingClass.row_index == rid:
                        nested = module.md.TypeDef.rows[nc.NestedClass.row_index - 1]
                        nested_name = str(nested.TypeName)
                        if prefixes and not any(nested_name.startswith(p) for p in prefixes):
                            continue
                        print(f"  ---- nested {nested_name} (rid {nc.NestedClass.row_index})")
                except Exception:
                    pass


def main() -> None:
    args = [value for value in sys.argv[1:] if value != "--client"]
    public = "--client" in sys.argv and "public" in sys.argv
    if len(args) < 3:
        raise SystemExit(__doc__)
    assembly, mode, rest = args[0], args[1], tuple(args[2:])
    module = Module(client_dll(assembly, public))
    if mode == "str":
        find_strings(module, rest)
    elif mode == "type":
        find_types(module, rest)
    elif mode == "use":
        find_uses(module, rest)
    elif mode == "dump":
        dump_type(module, rest[:1], rest[1:])
    else:
        raise SystemExit(__doc__)


if __name__ == "__main__":
    main()
