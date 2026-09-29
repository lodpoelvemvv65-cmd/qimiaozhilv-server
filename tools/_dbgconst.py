# -*- coding: utf-8 -*-
"""调试 Constant 表结构。"""
import sys, io
sys.stdout = io.TextIOWrapper(open(sys.argv[2], 'wb'), encoding='utf-8', errors='replace')
import dnfile
pe = dnfile.dnPE(sys.argv[1])
md = pe.net.mdtables
print('Constant table:', md.Constant)
print('rows:', len(md.Constant.rows))
for c in md.Constant.rows[:5]:
    print('--- row', repr(c)[:400])
    for a in ('Type', 'Parent', 'Value'):
        try:
            print('   %s = %r' % (a, getattr(c, a)))
        except Exception as e:
            print('   %s ERR %r' % (a, e))
