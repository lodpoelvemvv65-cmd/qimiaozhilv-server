# -*- coding: utf-8 -*-
"""正确导出 DLL 中枚举的成员与常量值（走 Constant 表）。用法: python _enum2.py <dll> <out.txt> [过滤]"""
import sys, io
sys.stdout = io.TextIOWrapper(open(sys.argv[2], 'wb'), encoding='utf-8', errors='replace')
import dnfile

dll = sys.argv[1]
flt = sys.argv[3] if len(sys.argv) > 3 else ''
pe = dnfile.dnPE(dll)
md = pe.net.mdtables

# Constant 表: Parent(coded index) -> field rid, Value
fld_const = {}
for c in md.Constant.rows:
    try:
        p = c.Parent
        if p.table.name == 'Field':
            fld_const[p.row_index] = c.Value
    except Exception:
        pass

def field_rid(f):
    try:
        return f.row.row_index
    except Exception:
        return None

for i, td in enumerate(md.TypeDef.rows, 1):
    full = (str(td.TypeNamespace) + '.' + str(td.TypeName)).strip('.')
    if flt and flt.lower() not in full.lower():
        continue
    entries = []
    for f in (td.FieldList or []):
        rid = field_rid(f)
        try:
            row = f.row
            nm = str(row.Name)
        except Exception:
            continue
        entries.append((nm, fld_const.get(rid)))
    members = [(n, v) for n, v in entries if v is not None]
    if members and len(members) >= 1 and len(members) == len(entries):
        print('enum %s' % full)
        for n, v in entries:
            print('    %-45s = %s' % (n, v))
