# -*- coding: utf-8 -*-
import sys, io
sys.stdout = io.TextIOWrapper(open(sys.argv[2], 'wb'), encoding='utf-8', errors='replace')
import dnfile
pe = dnfile.dnPE(sys.argv[1])
md = pe.net.mdtables
c = md.Constant.rows[0]
p = c.Parent
print('parent type', type(p))
print('attrs', [a for a in dir(p) if not a.startswith('_')])
for a in ('table', 'row_index', 'row', 'value', 'tag'):
    try:
        print(' %s = %r' % (a, getattr(p, a)))
    except Exception as e:
        print(' %s ERR %r' % (a, e))
print('value obj', type(c.Value), [a for a in dir(c.Value) if not a.startswith('_')])
try:
    print(' value.value', repr(c.Value.value))
except Exception as e:
    print(' value.value ERR', e)
try:
    print(' value.size', c.Value.size)
except Exception as e:
    print(' value.size ERR', e)
