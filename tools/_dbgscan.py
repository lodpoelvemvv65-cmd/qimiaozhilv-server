# -*- coding: utf-8 -*-
"""调试 _scanall 的扫描基础能力。"""
import sys, io, os
sys.stdout = io.TextIOWrapper(open(sys.argv[1], 'wb'), encoding='utf-8', errors='replace')
import dnfile
from dncil.cil.body import CilMethodBody
from dncil.cil.body.reader import CilMethodBodyReaderBytes
from dncil.clr.token import StringToken, InvalidToken, Token

dll = sys.argv[2]
pe = dnfile.dnPE(dll)
md = pe.net.mdtables
print('TypeDef rows:', len(md.TypeDef.rows))
print('MethodDef rows:', len(md.MethodDef.rows))
sys.stdout.flush()
us = None
try:
    print('metadata.streams keys:', list(pe.net.metadata.streams.keys()))
except Exception as e:
    print('streams err', repr(e))
try:
    print('user_strings:', type(pe.net.user_strings))
    us = pe.net.user_strings
    print('user_strings size', len(us))
except Exception as e:
    print('user_strings err', repr(e))
sys.stdout.flush()
m0 = None
for td in md.TypeDef.rows:
    if td.MethodList:
        m0 = td.MethodList[0]
        break
print('m0 type', type(m0))
for attr in ('Rva', 'Name', 'row_index'):
    try:
        print('  m0.%s = %r' % (attr, getattr(m0, attr)))
    except Exception as e:
        print('  m0.%s ERR %r' % (attr, e))
try:
    print('  m0.row =', m0.row, 'row.Rva=', m0.row.Rva)
except Exception as e:
    print('  m0.row ERR', repr(e))
sys.stdout.flush()
n_bodies = 0
n_insn = 0
n_str = 0
samples = []
for td in md.TypeDef.rows:
    if td.MethodList is None:
        continue
    for m in td.MethodList:
        try:
            rva = m.Rva
        except Exception:
            continue
        if rva in (None, 0):
            continue
        try:
            body = CilMethodBody(CilMethodBodyReaderBytes(pe.get_data(rva, 0x4000)))
            n_bodies += 1
        except Exception:
            continue
        for i in body.instructions:
            n_insn += 1
            if isinstance(i.operand, StringToken):
                n_str += 1
                if len(samples) < 5:
                    try:
                        off = i.operand.value & 0x00FFFFFF
                        v = pe.net.user_strings.get(off)
                        samples.append((hex(off), repr(v)))
                    except Exception as e:
                        samples.append(('err', repr(e)))
print('bodies=%d instructions=%d ldstr=%d' % (n_bodies, n_insn, n_str))
sys.stdout.flush()
for s in samples:
    print('  sample', s)
sys.stdout.flush()
