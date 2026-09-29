# -*- coding: utf-8 -*-
"""dump 指定类的属性名 + Property 上的 ProtoMember tag。遍历 PropertyMap 表。"""
import os
import sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
import dnfile
DLL = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'client-test', '梦幻奇遇记_Data', 'StreamingAssets', 'yoo', '_extracted', 'Hotfix.dll')
pe = dnfile.dnPE(DLL)
md = pe.net.mdtables

ca_map = {}
for c in md.CustomAttribute.rows:
    try:
        p = c.Parent
        if p.table is not None:
            ca_map.setdefault((p.table.name, p.row_index), []).append(c)
    except Exception:
        pass

def blob_i32(blob):
    try:
        b = bytes(blob.value)
    except Exception:
        return None
    if len(b) < 6 or b[0] != 0x01 or b[1] != 0x00:
        return None
    return int.from_bytes(b[2:6], 'little')

# td rid -> 属性列表(rid)
td_props = {}
for pm in md.PropertyMap.rows:
    try:
        td_rid = pm.Parent.row_index
    except Exception:
        continue
    props = []
    for pl in pm.PropertyList:
        props.append(pl.row_index)
    td_props[td_rid] = props

def clsname(c):
    return (str(c.TypeNamespace) + '.' + str(c.TypeName)).strip('.')

wants = set(sys.argv[1:] or ['M2C_GetMainUISetting'])
for i, td in enumerate(md.TypeDef.rows, 1):
    name = str(td.TypeName)
    if name not in wants:
        continue
    print('==== TypeDef %d %s.%s ====' % (i, str(td.TypeNamespace), name))
    for pr_rid in td_props.get(i, []):
        pr = md.Property.rows[pr_rid - 1]
        tag = None
        for c in ca_map.get(('Property', pr_rid), []):
            t = blob_i32(c.Value)
            if t is not None:
                tag = t
        print('  prop %d: %s  ProtoTag=%s' % (pr_rid, pr.Name, tag))
    # 也打印字段上的 tag（非 backing field，如 List 字段）
    for f in td.FieldList:
        fr = f.row
        is_backing = str(fr.Name).startswith('<')
        tag = None
        for c in ca_map.get(('Field', f.row_index), []):
            t = blob_i32(c.Value)
            if t is not None:
                tag = t
        if tag is not None:
            print('  field %d: %s  ProtoTag=%s' % (f.row_index, fr.Name, tag))
