# -*- coding: utf-8 -*-
"""精确解析 YooAsset 1.4.17 PackageManifest（字段顺序来自 DeserializeManifestOperation 反汇编）。
用法: python parse_manifest2.py <manifest.bytes> [bundle名过滤]"""
import sys, struct
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

class R:
    def __init__(self, data):
        self.d = data; self.p = 0
    def u32(self):
        v = struct.unpack_from('<I', self.d, self.p)[0]; self.p += 4; return v
    def i32(self):
        v = struct.unpack_from('<i', self.d, self.p)[0]; self.p += 4; return v
    def i64(self):
        v = struct.unpack_from('<q', self.d, self.p)[0]; self.p += 8; return v
    def u8(self):
        v = self.d[self.p]; self.p += 1; return v
    def boolean(self):
        v = self.d[self.p]; self.p += 1; return v != 0
    def utf8(self):
        n = self.u16(); v = self.d[self.p:self.p+n].decode('utf-8', errors='replace'); self.p += n; return v
    def u16(self):
        v = struct.unpack_from('<H', self.d, self.p)[0]; self.p += 2; return v
    def utf8arr(self):
        # 反汇编：ReadUTF8Array 内部 call ReadUInt16 读 count（不是 i32）
        n = self.u16(); return [self.utf8() for _ in range(n)]
    def i32arr(self):
        # 反汇编：ReadInt32Array 内部 call ReadUInt16 读 count
        n = self.u16(); return [self.i32() for _ in range(n)]

path = sys.argv[1]
data = open(path, 'rb').read()
r = R(data)

magic = r.u32()
assert magic == 5853007, 'bad magic %x' % magic
version = r.utf8()
enableAddressable = r.boolean()
locationToLower = r.boolean()
includeAssetGUID = r.boolean()
outputNameStyle = r.i32()
packageName = r.utf8()
packageVersion = r.utf8()
print('magic ok version=%s pkg=%s ver=%s addr=%s lower=%s guid=%s style=%s' % (
    version, packageName, packageVersion, enableAddressable, locationToLower, includeAssetGUID, outputNameStyle))

nAsset = r.i32()
assets = []
for i in range(nAsset):
    addr = r.utf8()
    apath = r.utf8()
    guid = r.utf8()
    tags = r.utf8arr()
    bundleID = r.i32()
    deps = r.i32arr()
    assets.append((addr, apath, bundleID, tags, deps))
print('assets:', nAsset)

nBundle = r.i32()
bundles = []
for i in range(nBundle):
    name = r.utf8()
    fileHash = r.utf8()
    fileCRC = r.utf8()
    fileSize = r.i64()
    isRaw = r.boolean()
    loadMethod = r.u8()
    tags = r.utf8arr()
    refs = r.i32arr()
    bundles.append((name, fileHash, fileCRC, fileSize, isRaw, loadMethod, tags, refs))
print('bundles:', nBundle)
print('consumed bytes:', r.p, 'of', len(data))

want = sys.argv[2:] if len(sys.argv) > 2 else None
if want is not None:
    for i, (addr, apath, bundleID, tags, deps) in enumerate(assets):
        bundle_name = bundles[bundleID][0]
        searchable = "\n".join((addr, apath, bundle_name)).lower()
        if any(item.lower() in searchable for item in want):
            print('asset[%d] addr=%s path=%s bundle[%d]=%s tags=%s deps=%s' % (
                i, addr, apath, bundleID, bundle_name, tags, deps))
for i, b in enumerate(bundles):
    name, fileHash, fileCRC, fileSize, isRaw, loadMethod, tags, refs = b
    if want is None or any(w.lower() in name.lower() for w in want):
        print('bundle[%d] %s hash=%s crc=%s size=%d isRaw=%s loadMethod=%s tags=%s refs=%s' % (
            i, name, fileHash, fileCRC, fileSize, isRaw, loadMethod, tags, refs))
