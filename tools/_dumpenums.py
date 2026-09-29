# -*- coding: utf-8 -*-
"""导出 DLL 中所有枚举（含常量值）。用法: python _dumpenums.py <dll路径> <out.txt> [命名空间过滤]"""
import sys, io, os
sys.stdout = io.TextIOWrapper(open(sys.argv[2], 'wb'), encoding='utf-8', errors='replace')
import dnfile

dll = sys.argv[1]
flt = sys.argv[3] if len(sys.argv) > 3 else ''
pe = dnfile.dnPE(dll)
md = pe.net.mdtables

def const_of(fld):
    try:
        c = fld.Constant
        if c is None:
            return None
        return c.value
    except Exception:
        return None

for i, td in enumerate(md.TypeDef.rows, 1):
    ns = str(td.TypeNamespace)
    nm = str(td.TypeName)
    full = (ns + '.' + nm).strip('.')
    if flt and flt.lower() not in full.lower():
        continue
    fields = td.FieldList or []
    entries = []
    for f in fields:
        try:
            fld = f.row
        except Exception:
            fld = f
        try:
            entries.append((str(fld.Name), const_of(fld)))
        except Exception as e:
            entries.append(('?', None))
    # 只看像枚举的：所有字段都有常量值
    if entries and all(v is not None for _, v in entries):
        print('enum %s' % full)
        for n, v in entries:
            print('    %-40s = %s' % (n, v))
