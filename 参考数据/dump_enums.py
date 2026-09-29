# -*- coding: utf-8 -*-
import sys, struct
import dnfile
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

DLL = r'<原版客户端>\梦幻奇遇记_Data\StreamingAssets\yoo\_extracted\Hotfix.dll'
pe = dnfile.dnPE(DLL)
md = pe.net.mdtables

# Constant 表: Parent -> Field row_index, Value -> bytes
const_by_field = {}
for c in md.Constant.rows:
    p = c.Parent
    if p.table is not None and p.table.name == 'Field':
        v = c.Value
        v = v.value if hasattr(v, 'value') else v
        const_by_field[p.row_index] = v

def to_val(b):
    if b is None:
        return None
    if len(b) == 1: return struct.unpack('b', b)[0]
    if len(b) == 2: return struct.unpack('<h', b)[0]
    if len(b) == 4: return struct.unpack('<i', b)[0]
    if len(b) == 8: return struct.unpack('<q', b)[0]
    return b.hex()

enums = []
for td in md.TypeDef.rows:
    fields = list(td.FieldList)
    if not fields:
        continue
    non_value = [f for f in fields if str(f.row.Name) != 'value__']
    if not non_value:
        continue
    if all(f.row.Flags.fdLiteral for f in non_value):
        ns = str(td.TypeNamespace)
        name = str(td.TypeName)
        members = []
        for f in fields:
            fn = str(f.row.Name)
            if fn == 'value__':
                continue
            members.append((fn, to_val(const_by_field.get(f.row_index))))
        enums.append((ns, name, members))

print(f'共找到 {len(enums)} 个枚举\n')

TARGET = ('LoginType','ItemType','CampType','ChatType','JobType','SexType','MailState',
          'MainUIType','MarketType','FamilyPosition','TaskState','ChangeType','StoneType')
for ns, name, members in enums:
    if name in TARGET or ns == 'ET' or ns == '':
        print(f'enum {ns}.{name} {{')
        for fn, val in members:
            print(f'    {fn} = {val}')
        print('}')
        print()
