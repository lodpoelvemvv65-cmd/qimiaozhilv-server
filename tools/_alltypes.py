# -*- coding: utf-8 -*-
"""导出 DLL 的全部 TypeDef 名称。用法: python _alltypes.py <dll路径> <out.txt>"""
import sys, io, os
sys.stdout = io.TextIOWrapper(open(sys.argv[2], 'wb'), encoding='utf-8', errors='replace')
import dnfile
pe = dnfile.dnPE(sys.argv[1])
md = pe.net.mdtables
print('DLL: %s' % os.path.basename(sys.argv[1]))
for i, td in enumerate(md.TypeDef.rows, 1):
    full = (str(td.TypeNamespace) + '.' + str(td.TypeName)).strip('.')
    try:
        rid = td.row.row_index
    except Exception:
        rid = i
    n = len(td.MethodList) if td.MethodList is not None else 0
    print('%5d  %-70s m=%d' % (rid, full, n))
