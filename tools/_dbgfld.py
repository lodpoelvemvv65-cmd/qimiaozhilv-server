# -*- coding: utf-8 -*-
"""调试单个 TypeDef 的字段与常量。用法: python _dbgfld.py <dll> <rid> <out.txt>"""
import sys, io
sys.stdout = io.TextIOWrapper(open(sys.argv[3], 'wb'), encoding='utf-8', errors='replace')
import dnfile
pe = dnfile.dnPE(sys.argv[1])
md = pe.net.mdtables
rid = int(sys.argv[2])
td = md.TypeDef.rows[rid - 1]
print('TypeDef', rid, td.TypeNamespace, td.TypeName)
print('FieldList repr:', repr(td.FieldList)[:200])
for f in (td.FieldList or []):
    print('--- f type', type(f))
    try:
        row = f.row
        print('  row type', type(row), 'Name=', getattr(row, 'Name', None))
        c = getattr(row, 'Constant', 'NOATTR')
        print('  Constant =', repr(c)[:200], 'type', type(c))
        if c is not None and not isinstance(c, str):
            try:
                print('  c.value =', getattr(c, 'value', 'novalue'))
            except Exception as e:
                print('  c.value err', e)
    except Exception as e:
        print('  err', repr(e))
print('--- Constant table rows:', len(md.Constant.rows) if md.Constant else None)
