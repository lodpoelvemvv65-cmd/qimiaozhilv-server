# -*- coding: utf-8 -*-
"""Find managed client TypeDefs by case-insensitive name fragments."""

from __future__ import annotations

import sys
from pathlib import Path

import dnfile


sys.stdout.reconfigure(encoding="utf-8", errors="replace")
ROOT = Path(__file__).resolve().parent.parent


def main() -> None:
    if len(sys.argv) < 3:
        raise SystemExit("usage: _find_client_types.py <Hotfix|HotfixView> <fragment> [fragment...]")
    assembly = sys.argv[1]
    show_members = "--members" in sys.argv[2:]
    fragments = tuple(value.casefold() for value in sys.argv[2:] if value != "--members")
    data_root = next(path for path in (ROOT / "client-test").iterdir() if path.name.endswith("_Data"))
    dll = data_root / "StreamingAssets" / "yoo" / "_extracted" / f"{assembly}.dll"
    metadata = dnfile.dnPE(str(dll)).net.mdtables
    for rid, row in enumerate(metadata.TypeDef.rows, 1):
        namespace = str(row.TypeNamespace or "")
        name = str(row.TypeName or "")
        full_name = f"{namespace}.{name}".strip(".")
        if any(fragment in full_name.casefold() for fragment in fragments):
            print(f"{rid}\t{full_name}")
            if show_members:
                for field in row.FieldList:
                    print(f"  field\t{field.row.Name}")
                for method in row.MethodList:
                    print(f"  method\t{method.row.Name}")


if __name__ == "__main__":
    main()
