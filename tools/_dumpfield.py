# -*- coding: utf-8 -*-
"""导出枚举/类的字段（含常量值）。用法: python _dumpfield.py <dll路径> <TypeDef rid>"""
import sys
import io
import os
outp = sys.argv[3] if len(sys.argv) > 3 else None
if outp:
    sys.stdout = io.TextIOWrapper(open(outp, 'wb'), encoding='utf-8')
else:
    sys.stdout.reconfigure(encoding='utf-8', errors='replace')
import dnfile

dll = sys.argv[1]
rid = int(sys.argv[2])
pe = dnfile.dnPE(dll)
md = pe.net.mdtables
td = md.TypeDef.rows[rid - 1]
print('TypeDef %d: %s.%s' % (rid, td.TypeNamespace, td.TypeName))
for f in (td.FieldList or []):
    try:
        fld = md.Field.rows[f.row.row_index - 1] if hasattr(f, 'row') else f
    except Exception:
        fld = f
    try:
        val = fld.Constant
        cv = val.value if val is not None else None
    except Exception:
        cv = None
    try:
        name = fld.Name
    except Exception:
        name = '?'
    print('   %s = %s' % (name, cv))
