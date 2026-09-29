# -*- coding: utf-8 -*-
"""从 protocol_dump.txt 生成完整 protobuf 协议定义 (proto3)。"""
import re, sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

# ---- 枚举定义（从 Hotfix.dll 提取，已人工核对） ----
ENUMS = {
    'ItemType':       [('NoneItem',0),('EquipItem',1),('GoodsItem',2),('MaterialsItem',3)],
    'CampType':       [('NoneCamp',0),('Pioneer',1),('Guardian',2)],
    'JobType':        [('UnKnown',0),('Officer',1),('Sportsman',2),('Nurse',3),('Superman',4)],
    'SexType':        [('Male',0),('Famale',1)],
    'LoginType':      [('Editor',0),('Tourist',1),('WeChat',2),('Voucher',3)],
    'FamilyPosition': [('FamilyLeader',0),('FamilyDeputyLeader',1),('FamilyMember',2)],
    'MarketType':     [('NoneMarket',0),('VoucherMarket',1),('YuanBaoMarket',2)],
    'TaskState':      [('TaskNoneState',0),('TaskWaiting',1),('TaskRunning',2),('TaskCompleted',3)],
    'MainUIType':     [('NoneSlot',0),('SkillSlot',1),('ItemSlot',2)],
    'MailState':      [('UnReceive',0),('Received',1)],
    'ChatType':       [('NoneChat',0),('Normal',1),('Team',2),('Family',3),('Camp',4),('World',5),('Private',6),('System',7)],
    'ChangeType':     [('None',0),('Add',1),('Reduce',2)],
    'StoneType':      [('Nomal',0),('Rare',1),('Epic',2),('UpgradeRare',3)],
}
ENUM_NAMES = set(ENUMS.keys())

# ---- 基础类型映射 ----
BASIC = {
    'int': 'int32', 'uint': 'uint32', 'long': 'int64', 'ulong': 'uint64',
    'short': 'int32', 'ushort': 'int32', 'byte': 'int32', 'sbyte': 'int32',
    'float': 'float', 'double': 'double', 'bool': 'bool', 'string': 'string',
    'char': 'int32',
}

def map_type(t):
    t = t.strip()
    if t in BASIC:
        return BASIC[t]
    if t.startswith('ET.'):
        name = t[3:]
        return name  # enum 或 message 同名引用
    return t  # ChangeType / StoneType 等裸枚举名

# ---- 解析 protocol_dump.txt ----
txt = open('protocol_dump.txt', encoding='utf-8').read()
lines = txt.splitlines()

messages = []  # (opcode, name, [(tag, fname, ftype)], iface_str, resp)
cur = None
for l in lines:
    m = re.match(r'## \[(\d+)\] ET\.([A-Za-z_][A-Za-z0-9_]*)  \((.*?)\)(?:  -> ET\.([A-Za-z_][A-Za-z0-9_]*))?', l)
    if m:
        if cur:
            messages.append(cur)
        opcode = int(m.group(1)); name = m.group(2); iface = m.group(3); resp = m.group(4)
        cur = [opcode, name, [], iface, resp]
        continue
    m = re.match(r'    \[(\d+)\] ([A-Za-z_][A-Za-z0-9_]*): (.+)$', l)
    if m and cur:
        cur[2].append((int(m.group(1)), m.group(2), m.group(3).strip()))
if cur:
    messages.append(cur)

print(f'解析到 {len(messages)} 条消息')

# ---- 检查字段 tag 合法性 / 保留号 ----
for opcode, name, fields, iface, resp in messages:
    for tag, fname, ftype in fields:
        if tag <= 0 or tag > 536870911 or 19000 <= tag <= 19999:
            print(f'  !! 非法字段号 {name}.{fname} = {tag}')

# ---- 生成 .proto ----
out = []
out.append('// 梦幻奇遇记 网络协议 (自动生成)')
out.append('// 来源: Hotfix.dll, protocol_dump.txt')
out.append('// 共 %d 条消息, %d 个枚举' % (len(messages), len(ENUMS)))
out.append('// wire format: KCP 流内 [u16 opcode][protobuf body] (RpcId 时先 [i64 RpcId])')
out.append('')
out.append('syntax = "proto3";')
out.append('')
out.append('package ET;')
out.append('')

# 枚举
for ename, members in ENUMS.items():
    out.append(f'enum {ename} {{')
    out.append('    // 占位, 保证枚举非空 (proto3 要求第一个值为 0)')
    for fn, val in members:
        out.append(f'    {fn} = {val};')
    out.append('}')
    out.append('')

# 消息
for opcode, name, fields, iface, resp in messages:
    out.append(f'// opcode {opcode}{"  -> " + resp if resp else ""}')
    out.append(f'message {name} {{')
    for tag, fname, ftype in fields:
        mt = map_type(ftype)
        out.append(f'    {mt} {fname} = {tag};')
    out.append('}')
    out.append('')

result = '\n'.join(out)
with open('protocol.proto', 'w', encoding='utf-8') as f:
    f.write(result)
print('已写入 protocol.proto, 共 %d 字节' % len(result))
