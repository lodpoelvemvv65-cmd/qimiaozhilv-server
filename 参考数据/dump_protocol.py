# -*- coding: utf-8 -*-
"""
从 Hotfix.dll 提取完整协议定义：
- 429 个消息类的 opcode
- 每个消息类的字段（属性名 + 类型 + ProtoMember tag）
- 请求↔响应映射（ResponseTypeAttribute）
- 消息类实现的接口（IMessage/IRequest/IResponse/IActorMessage）
输出：协议清单（控制台 + protocol_dump.txt）
"""
import sys, io
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

import dnfile

DLL = r'<原版客户端>\梦幻奇遇记_Data\StreamingAssets\yoo\_extracted\Hotfix.dll'
pe = dnfile.dnPE(DLL)
md = pe.net.mdtables

# ---------- 工具 ----------

def resolve_mr_parent(c):
    """MemberRefParent / TypeDefOrRef 解析为类型名"""
    if c.table is None:
        return '?'
    rid = c.row_index
    row = c.table.rows[rid - 1]
    if c.table.name == 'TypeRef':
        return f'{row.TypeNamespace}.{row.TypeName}'.strip('.')
    if c.table.name == 'TypeDef':
        return f'{row.TypeNamespace}.{row.TypeName}'.strip('.')
    if c.table.name == 'ModuleRef':
        return f'<ModuleRef {row.Name}>'
    if c.table.name == 'MethodDef':
        return f'<MethodDef {row.Name}>'
    if c.table.name == 'TypeSpec':
        return f'<TypeSpec {rid}>'
    return f'{c.table.name}[{rid}]'

def ca_ctor(ca):
    """CustomAttribute 的构造函数全名"""
    t = ca.Type
    if t.table is None:
        return '?'
    if t.table.name == 'MemberRef':
        mr = t.row
        return f'{resolve_mr_parent(mr.Class)}::{mr.Name}'
    return f'{t.table.name}::{t.row.Name}'

def ca_value(ca):
    v = ca.Value.value_bytes
    return v() if callable(v) else v

def resolve_tdor_encoded(enc):
    """TypeDefOrRef coded index (2-bit tag): 返回类型名"""
    tag = enc & 0x3
    rid = enc >> 2
    if tag == 0:
        td = md.TypeDef.rows[rid - 1]
        return f'{td.TypeNamespace}.{td.TypeName}'.strip('.')
    elif tag == 1:
        tr = md.TypeRef.rows[rid - 1]
        return f'{tr.TypeNamespace}.{tr.TypeName}'.strip('.')
    elif tag == 2:
        return f'<TypeSpec {rid}>'
    return f'<tdor {enc}>'

ELEM = {
    0x02: 'bool', 0x03: 'char', 0x04: 'sbyte', 0x05: 'byte',
    0x06: 'short', 0x07: 'ushort', 0x08: 'int', 0x09: 'uint',
    0x0a: 'long', 0x0b: 'ulong', 0x0c: 'float', 0x0d: 'double',
    0x0e: 'string', 0x0f: 'IntPtr', 0x1c: 'object',
}

class SigReader:
    def __init__(self, b):
        self.b = b
        self.i = 0
    def u8(self):
        v = self.b[self.i]; self.i += 1; return v
    def compressed(self):
        # ECMA compressed integer (max 4 bytes)
        v = self.b[self.i]; self.i += 1
        if v & 0x80 == 0:
            return v
        if v & 0xC0 == 0x80:
            return ((v & 0x3F) << 8) | self.u8()
        if v & 0xE0 == 0xC0:
            return ((v & 0x1F) << 24) | (self.u8() << 16) | (self.u8() << 8) | self.u8()
        return v

def parse_type(r):
    """解析单个类型，返回 (类型名字符串, 消耗后reader)"""
    e = r.u8()
    if e in ELEM:
        return ELEM[e]
    if e == 0x1d:  # SZARRAY
        elem, = parse_type(r),  # 递归
        inner = parse_type(r)
        return inner + '[]'
    if e == 0x14:  # ARRAY (完整数组)
        inner = parse_type(r)
        rank = r.compressed()
        _ = r.compressed()  # numsizes
        for _ in range(r.compressed()):
            pass
        _ = r.compressed()  # numlobounds
        for _ in range(r.compressed()):
            pass
        return inner + '[' + ',' * (rank - 1) + ']'
    if e in (0x12, 0x11, 0x50):  # class / valuetype / type
        enc = r.compressed()
        return resolve_tdor_encoded(enc)
    if e == 0x15:  # GENERICINST
        inner = parse_type(r)
        nargs = r.compressed()
        args = []
        for _ in range(nargs):
            args.append(parse_type(r))
        return f'{inner}<{",".join(args)}>'
    if e == 0x13:  # VAR
        n = r.compressed()
        return f'!{n}'
    if e == 0x1e:  # MVAR
        n = r.compressed()
        return f'!!{n}'
    if e == 0x10:  # BYREF
        return parse_type(r) + '&'
    if e == 0x0e:
        return 'string'
    return f'<elem 0x{e:02x}>'

def parse_property_type(sig_blob):
    """解析 PropertySig -> 属性类型字符串"""
    r = SigReader(sig_blob)
    hdr = r.u8()  # 0x28 instance / 0x08 static
    r.compressed()  # param count (properties 通常 0)
    try:
        return parse_type(r)
    except Exception as e:
        return f'<parse_err {e}>'

# ---------- 1. 消息类 opcode ----------
msg_opcode = {}   # TypeDef rid -> opcode
for ca in md.CustomAttribute.rows:
    if ca_ctor(ca) == 'ET.MessageAttribute::.ctor':
        v = ca_value(ca)
        opcode = int.from_bytes(v[2:4], 'little')
        p = ca.Parent
        if p.table is not None and p.table.name == 'TypeDef':
            msg_opcode[p.row_index] = opcode

# ---------- 2. 字段 ProtoMember tag ----------
prop_tag = {}     # Property rid -> ProtoMember tag
for ca in md.CustomAttribute.rows:
    if ca_ctor(ca) == 'ProtoBuf.ProtoMemberAttribute::.ctor':
        v = ca_value(ca)
        tag = int.from_bytes(v[2:6], 'little')
        p = ca.Parent
        if p.table is not None and p.table.name == 'Property':
            prop_tag[p.row_index] = tag

# ---------- 3. ResponseType 请求->响应 ----------
resp_map = {}     # 请求类 rid -> 响应类型名
for ca in md.CustomAttribute.rows:
    if ca_ctor(ca) == 'ET.ResponseTypeAttribute::.ctor':
        v = ca_value(ca)
        p = ca.Parent
        if p.table is not None and p.table.name == 'TypeDef':
            # blob: prolog(2) + 压缩长度(1) + UTF8 类型名字符串 + NumNamed(2)
            try:
                r = SigReader(v)
                _ = r.u8(); _ = r.u8()  # prolog 0x0001
                slen = r.compressed()
                s = v[r.i:r.i+slen].decode('utf-8', 'replace')
                resp_map[p.row_index] = s
            except Exception as e:
                resp_map[p.row_index] = f'<parse_err {e}>'

# ---------- 4. TypeDef rid -> Property 列表 ----------
td_props = {}     # TypeDef rid -> [Property rid]
for pm in md.PropertyMap.rows:
    parent = pm.Parent
    if parent.table is not None and parent.table.name == 'TypeDef':
        td_props[parent.row_index] = [it.row_index for it in pm.PropertyList]

# ---------- 5. 接口实现 ----------
def td_interfaces(td_rid):
    """返回该 TypeDef 实现的接口类型名列表（从 InterfaceImpl 表）"""
    out = []
    for ii in md.InterfaceImpl.rows:
        if ii.Class.table is not None and ii.Class.table.name == 'TypeDef' and ii.Class.row_index == td_rid:
            if ii.Interface.table is not None:
                out.append(resolve_mr_parent(ii.Interface))
    return out

# ---------- 组装输出 ----------
def tname(rid):
    td = md.TypeDef.rows[rid - 1]
    return f'{td.TypeNamespace}.{td.TypeName}'.strip('.')

lines = []
lines.append('#' * 80)
lines.append('# 梦幻奇遇记 网络协议完整定义')
lines.append('# 来源: Hotfix.dll')
lines.append('#' * 80)

by_opcode = sorted(msg_opcode.items(), key=lambda x: x[1])
for rid, opcode in by_opcode:
    td = md.TypeDef.rows[rid - 1]
    clsname = tname(rid)
    ifaces = td_interfaces(rid)
    iface_str = ','.join(i.split('.')[-1] for i in ifaces if 'ET.' in i)
    resp = resp_map.get(rid, '')
    resp_str = f'  -> {resp}' if resp else ''
    lines.append(f'\n## [{opcode}] {clsname}  ({iface_str}){resp_str}')

    # 字段
    props = td_props.get(rid, [])
    # 按 ProtoMember tag 排序
    field_entries = []
    for prid in props:
        p = md.Property.rows[prid - 1]
        name = str(p.Name)
        sig = p.Type.value_bytes
        sig = sig() if callable(sig) else sig
        typ = parse_property_type(sig)
        tag = prop_tag.get(prid, '?')
        field_entries.append((tag, name, typ))
    field_entries.sort(key=lambda x: (x[0] if isinstance(x[0], int) else 10**9,))
    for tag, name, typ in field_entries:
        lines.append(f'    [{tag}] {name}: {typ}')

out = '\n'.join(lines)

with open(r'<原版客户端>\梦幻奇遇记_Data\StreamingAssets\yoo\_extracted\protocol_dump.txt', 'w', encoding='utf-8') as f:
    f.write(out)

print(out[:6000])
print('\n\n... 已保存到 protocol_dump.txt')
print(f'总消息类: {len(msg_opcode)}')
