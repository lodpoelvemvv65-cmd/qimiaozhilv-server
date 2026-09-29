# -*- coding: utf-8 -*-
"""在客户端 DLL 的 TypeDef 表里按关键字搜索类名，输出 rid。用法: python findtype.py <DLL名> <关键字...>"""
import sys, os
from pathlib import Path
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
import dnfile

ROOT = Path(__file__).resolve().parent.parent  # 项目根
dllname = sys.argv[1]
kws = sys.argv[2:]
base = next((q for q in ROOT.joinpath('client-127.0.0.1').iterdir() if q.name.endswith('_Data')))
dll = base.joinpath('StreamingAssets', 'yoo', '_extracted', '%s.dll' % dllname)
print('DLL:', dll)
pe = dnfile.dnPE(str(dll))
md = pe.net.mdtables
for i, td in enumerate(md.TypeDef.rows, 1):
    full = (str(td.TypeNamespace) + '.' + str(td.TypeName)).strip('.')
    if any(k.lower() in full.lower() for k in kws):
        nmeth = len(td.MethodList) if td.MethodList is not None else 0
        print('rid=%d  %s   (methods=%d)' % (i, full, nmeth))
