# -*- coding: utf-8 -*-
"""按关键字在指定 DLL 里搜索类名。用法: python _findtype2.py <dll全路径> <关键字...>"""
import sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
import dnfile

dll = sys.argv[1]
kws = sys.argv[2:]
pe = dnfile.dnPE(dll)
md = pe.net.mdtables
for i, td in enumerate(md.TypeDef.rows, 1):
    full = (str(td.TypeNamespace) + '.' + str(td.TypeName)).strip('.')
    if any(k.lower() in full.lower() for k in kws):
        nmeth = len(td.MethodList) if td.MethodList is not None else 0
        print('rid=%d  %s   (methods=%d)' % (i, full, nmeth))
