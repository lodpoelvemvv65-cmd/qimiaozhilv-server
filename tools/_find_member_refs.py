# -*- coding: utf-8 -*-
"""List external managed member references matching a method/type fragment."""

from __future__ import annotations

import sys
from pathlib import Path

import dnfile


sys.stdout.reconfigure(encoding="utf-8", errors="replace")
ROOT = Path(__file__).resolve().parent.parent


def main() -> None:
    if len(sys.argv) < 3:
        raise SystemExit("usage: _find_member_refs.py <Hotfix|HotfixView> <fragment> [fragment...]")
    assembly = sys.argv[1]
    fragments = tuple(value.casefold() for value in sys.argv[2:])
    data_root = next(path for path in (ROOT / "client-test").iterdir() if path.name.endswith("_Data"))
    dll = data_root / "StreamingAssets" / "yoo" / "_extracted" / f"{assembly}.dll"
    metadata = dnfile.dnPE(str(dll)).net.mdtables
    for row in metadata.MemberRef.rows:
        parent = row.Class
        parent_name = str(parent.row.TypeName) if parent and parent.row else ""
        name = str(row.Name)
        full_name = f"{parent_name}::{name}"
        if any(fragment in full_name.casefold() for fragment in fragments):
            print(full_name)


if __name__ == "__main__":
    main()
