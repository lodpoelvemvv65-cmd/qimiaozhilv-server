"""Read-only audit for the local client-test patch chain.

The playable client is deliberately not touched.  This command checks the
central ``current/`` copies, the runtime config/datatable bundles, manifest
metadata, and the marker types/methods emitted by the IL patchers.  It is
intended for post-patch diagnosis, not installation or rollback.
"""

from __future__ import annotations

import base64
import hashlib
import json
import re
import struct
from pathlib import Path

import UnityPy
import dnfile
from dncil.cil.body import CilMethodBody
from dncil.cil.body.reader import CilMethodBodyReaderBytes


ROOT = Path(__file__).resolve().parents[3]
CLIENT = next((ROOT / "client-test").glob("*_Data"))
YOO = CLIENT / "StreamingAssets" / "yoo"
AA0 = YOO / "aa0"
AA1 = YOO / "aa1"
BUNDLE = AA0 / "5ff0cd3f4dadb1ed406add45f5bbb636.bundle"
DATATABLE = AA0 / "87c13c7e72546d4e4e0960392e451498.bundle"
CURRENT = ROOT / "server-mysql" / "client-patches" / "client-test" / "current"


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest().upper()


def md5(path: Path) -> str:
    return hashlib.md5(path.read_bytes()).hexdigest().upper()


def bundle_assets(path: Path) -> dict[str, bytes]:
    env = UnityPy.load(str(path))
    out: dict[str, bytes] = {}
    for obj in env.objects:
        if obj.type.name != "TextAsset":
            continue
        data = obj.read()
        name = str(data.m_Name)
        if name not in {"Hotfix.dll", "HotfixView.dll", "ShopBase", "GoodsBase"}:
            continue
        raw = data.m_Script
        out[name] = raw.encode("utf-8", errors="surrogateescape") if isinstance(raw, str) else bytes(raw)
    del env
    return out


def type_markers(path: Path) -> dict[str, tuple[set[str], set[str]]]:
    pe = dnfile.dnPE(str(path))
    result: dict[str, tuple[set[str], set[str]]] = {}
    for td in pe.net.mdtables.TypeDef.rows:
        full = (str(td.TypeNamespace) + "." + str(td.TypeName)).strip(".")
        methods = {str(m.row.Name) for m in td.MethodList if hasattr(m, "row")}
        fields = {str(f.row.Name) for f in td.FieldList if hasattr(f, "row")}
        result[full] = (methods, fields)
    return result


def method_name(pe, operand) -> str:
    """Resolve just the method name needed by structural patch checks."""
    token = getattr(operand, "value", 0)
    table, rid = token >> 24, token & 0x00FFFFFF
    try:
        if table == 0x2B:  # MethodSpec -> MethodDef/MemberRef
            operand = pe.net.mdtables.MethodSpec.rows[rid - 1].Method
            table = {"MemberRef": 0x0A, "MethodDef": 0x06}.get(operand.table.name, 0)
            rid = operand.row_index
        if table == 0x0A:
            return str(pe.net.mdtables.MemberRef.rows[rid - 1].Name)
        if table == 0x06:
            return str(pe.net.mdtables.MethodDef.rows[rid - 1].Name)
    except Exception:
        pass
    return ""


def request_window_reuse_guard(path: Path) -> bool:
    """Return whether CreateRequstLabalEvent removes the old UI before AddComponent."""
    pe = dnfile.dnPE(str(path))
    enclosing = {n.NestedClass.row_index: n.EnclosingClass.row_index
                 for n in pe.net.mdtables.NestedClass.rows}
    parents: dict[int, int] = {}
    for index, td in enumerate(pe.net.mdtables.TypeDef.rows, 1):
        for method in td.MethodList:
            parents[method.row_index] = index
    for index, method in enumerate(pe.net.mdtables.MethodDef.rows, 1):
        if str(method.Name) != "MoveNext" or not method.Rva:
            continue
        type_index = parents.get(index)
        chain: list[str] = []
        while type_index:
            chain.append(str(pe.net.mdtables.TypeDef.rows[type_index - 1].TypeName))
            type_index = enclosing.get(type_index)
        if "CreateRequstLabalEvent" not in chain:
            continue
        body = CilMethodBody(CilMethodBodyReaderBytes(pe.get_data(method.Rva, 0x1000)))
        instructions = body.instructions
        for pos, instruction in enumerate(instructions):
            if instruction.opcode.name != "callvirt" or method_name(pe, instruction.operand) != "AddComponent":
                continue
            # The repair emits: dup; callvirt RemoveComponent; ... AddComponent.
            if any(item.opcode.name == "callvirt" and method_name(pe, item.operand) == "RemoveComponent"
                   for item in instructions[max(0, pos - 4):pos]):
                return True
    return False


def has_method_anywhere(path: Path, wanted: str) -> bool:
    pe = dnfile.dnPE(str(path))
    return any(str(method.row.Name) == wanted
               for td in pe.net.mdtables.TypeDef.rows for method in td.MethodList)


def read_manifest(path: Path) -> dict[str, tuple[str, int, int]]:
    """Return bundle name -> (manifest hash, declared size, LoadMethod)."""

    data = path.read_bytes()
    pos = 0

    def u16() -> int:
        nonlocal pos
        value = struct.unpack_from("<H", data, pos)[0]
        pos += 2
        return value

    def i32() -> int:
        nonlocal pos
        value = struct.unpack_from("<i", data, pos)[0]
        pos += 4
        return value

    def i64() -> int:
        nonlocal pos
        value = struct.unpack_from("<q", data, pos)[0]
        pos += 8
        return value

    def boolean() -> None:
        nonlocal pos
        pos += 1

    def text() -> str:
        nonlocal pos
        size = u16()
        value = data[pos : pos + size].decode("utf-8", errors="replace")
        pos += size
        return value

    def text_array() -> None:
        for _ in range(u16()):
            text()

    def int_array() -> None:
        for _ in range(u16()):
            i32()

    if i32() != 5853007:
        raise RuntimeError(f"bad manifest magic: {path}")
    text()  # version
    boolean()
    boolean()
    boolean()
    i32()  # outputNameStyle
    text()  # package name
    text()  # package version
    assets = []
    for _ in range(i32()):
        text()
        text()
        text()
        text_array()
        assets.append(i32())
        int_array()
    bundles = []
    for _ in range(i32()):
        name = text()
        file_hash = text()
        text()  # file CRC
        size = i64()
        boolean()
        load_method = data[pos]
        pos += 1
        text_array()
        int_array()
        bundles.append((name, file_hash, size, load_method))
    return {name: (file_hash, size, load_method) for name, file_hash, size, load_method in bundles}


def decode_rows(raw: bytes) -> list[list[object]]:
    text = zlib_decompress(base64.b64decode(raw.strip())).decode("utf-8")
    text = re.sub(r",\s*([}\]])", r"\1", text)
    return json.loads(text)


def zlib_decompress(data: bytes) -> bytes:
    import zlib

    return zlib.decompress(data)


def report(label: str, ok: bool, detail: str) -> None:
    print(f"{'OK' if ok else 'WARN'}  {label}: {detail}")


def main() -> None:
    if not BUNDLE.is_file() or not DATATABLE.is_file():
        raise FileNotFoundError("runtime aa0 config/datatable bundle is missing")
    runtime = bundle_assets(BUNDLE)
    central_bundle = CURRENT / BUNDLE.name
    report(
        "central/config bundle",
        central_bundle.is_file() and central_bundle.read_bytes() == BUNDLE.read_bytes(),
        f"runtime={BUNDLE.stat().st_size} bytes central={central_bundle.stat().st_size if central_bundle.is_file() else 'missing'} bytes",
    )
    for name in ("Hotfix.dll", "HotfixView.dll"):
        central = CURRENT / name
        report(
            f"central/{name}",
            central.is_file() and runtime.get(name) == central.read_bytes(),
            f"runtime={len(runtime.get(name, b''))} bytes central={central.stat().st_size if central.is_file() else 'missing'} bytes",
        )

    manifest_path = AA0 / "PackageManifest_aa0_2025-01-05-1027.bytes"
    manifest = read_manifest(manifest_path)
    for filename, key in ((BUNDLE.name, "assets_download_config.bundle"),
                          (DATATABLE.name, "assets_download_datatable.bundle")):
        declared = manifest[key]
        actual = AA0 / filename
        report(
            f"aa0/{filename} manifest",
            declared[0].lower() == md5(actual).lower() and declared[1] == actual.stat().st_size,
            f"declared hash={declared[0]} size={declared[1]} load={declared[2]}; actual md5={md5(actual)} size={actual.stat().st_size} load={declared[2]}",
        )
        report(
            f"aa0/{filename} LoadMethod",
            declared[2] == 0,
            f"LoadMethod={declared[2]} (local plaintext baseline is 0)",
        )

    rows = bundle_assets(DATATABLE)
    for table, wanted in (("ShopBase", {10036, 10037, 10038, 10039, 10040, 10041, 10042, 10043, 110836}),
                          ("GoodsBase", {110833})):
        decoded = decode_rows(rows[table])
        found = {int(row[0]) for row in decoded}
        report(f"datatable/{table} rows", wanted <= found, f"wanted={sorted(wanted)} missing={sorted(wanted - found)}")

    hotfix = type_markers(CURRENT / "Hotfix.dll")
    view = type_markers(CURRENT / "HotfixView.dll")
    checks = [
        ("Hotfix server shop cache", "ET.ServerShopPriceCache" in hotfix and
         {"LoadCatalog", "GetShopPrice", "FormatCoinPrice", "GetPriceText"} <= hotfix["ET.ServerShopPriceCache"][0]),
        ("Hotfix active-info fields", "ET.M2C_SendActiveInfo" in hotfix and
         {"ShopPriceData", "ShopDescriptionData"} <= hotfix["ET.M2C_SendActiveInfo"][1]),
        ("Hotfix title sync marker", "ET.NumHelper" in hotfix and "FillNum" in hotfix["ET.NumHelper"][0]),
        ("Hotfix force-offline dialog", "ET.G2C_ForceOffLineHandler" in hotfix and
         {"Codex_ShowForceOfflineDialog", "Codex_QuitAfterConfirm"} <= hotfix["ET.G2C_ForceOffLineHandler"][0]),
        ("Hotfix trade mode", "ET.TeamHelper" in hotfix and "RequestTrade" in hotfix["ET.TeamHelper"][0]),
        ("Hotfix launcher-independent", "ET.ServerShopPriceCache" in hotfix),
        ("View shop refresh", "ET.ShopUI" in view and
         {"ShowItems", "RefreshFromServer", "FilterServerCatalog"} <= view["ET.ShopUI"][0]),
        ("View character background", "ET.CharacterUI" in view and
         "RefreshCharacterBackground" in view["ET.CharacterUI"][0]),
        ("View skin refresh (expected disabled)", "ET.CharacterUI" in view and
         not ({"RefreshCharacterSkin", "RefreshCharacterSkinFromEvent"} & view["ET.CharacterUI"][0]) and
         not ({"characterDisplayedSkinId"} & view["ET.CharacterUI"][1])),
        ("View launcher direct login", has_method_anywhere(CURRENT / "HotfixView.dll", "CodexLauncherDirectLogin")),
        ("View launcher auto enter", "ET.FUI_EnterGameStartSystem" in view and
         "CodexLauncherAutoEnter" in view["ET.FUI_EnterGameStartSystem"][0]),
        ("View trade request window", request_window_reuse_guard(CURRENT / "HotfixView.dll")),
    ]
    for label, ok in checks:
        report(label, ok, "marker present" if ok else "marker missing")

    print("\nsha256 current:")
    for name in ("Hotfix.dll", "HotfixView.dll"):
        print(f"  {name} {sha256(CURRENT / name)}")
    print(f"  config bundle {sha256(BUNDLE)}")
    print(f"  datatable bundle {sha256(DATATABLE)}")


if __name__ == "__main__":
    main()
