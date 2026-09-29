# -*- coding: utf-8 -*-
"""在客户端所有托管 DLL 里搜索 IL 操作数 / 用户字符串中包含指定关键字的指令。
用法: python _scanall.py <out.txt> <关键字...>
"""
import sys, os, io, glob
sys.stdout = io.TextIOWrapper(open(sys.argv[1], 'wb'), encoding='utf-8', errors='replace')
needles = [n.lower() for n in sys.argv[2:]]

import dnfile
from dncil.cil.body import CilMethodBody
from dncil.cil.body.reader import CilMethodBodyReaderBytes
from dncil.clr.token import StringToken, InvalidToken, Token

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CLI = os.path.join(ROOT, 'client-127.0.0.1', '梦幻奇遇记_Data')
dlls = sorted(glob.glob(os.path.join(CLI, 'Managed', '*.dll')))
dlls += sorted(glob.glob(os.path.join(CLI, 'StreamingAssets', 'yoo', '_extracted', '*.dll')))

for dll in dlls:
    try:
        pe = dnfile.dnPE(dll)
    except Exception as e:
        print('SKIP %s (%r)' % (os.path.basename(dll), e))
        continue
    md = pe.net.mdtables
    base = os.path.basename(dll)
    found_in_dll = []
    md2cls = {}
    for td in md.TypeDef.rows:
        if td.MethodList is None:
            continue
        for mi in td.MethodList:
            try:
                md2cls[mi.row.row_index] = (str(td.TypeNamespace) + '.' + str(td.TypeName)).strip('.')
            except Exception:
                pass

    def clsname(t):
        return (str(t.TypeNamespace) + '.' + str(t.TypeName)).strip('.')

    def read_c(buf, pos):
        x = buf[pos]; pos += 1
        if x & 0x80:
            x = ((x & 0x7F) << 8) | buf[pos]; pos += 1
            if x & 0x4000:
                x = ((x & 0x3FFF) << 16) | (buf[pos] << 8) | buf[pos + 1]; pos += 2
        return x, pos

    def parse_t(buf, pos):
        prim = {0x01:'void',0x02:'bool',0x03:'char',0x04:'i1',0x05:'u1',0x06:'i2',0x07:'u2',
                0x08:'i4',0x09:'u4',0x0A:'i8',0x0B:'u8',0x0C:'r4',0x0D:'r8',0x0E:'string',
                0x16:'byref',0x18:'nint',0x19:'nuint',0x1C:'object'}
        b = buf[pos]
        if b in prim: return prim[b], pos + 1
        if b == 0x0F:
            t, p = parse_t(buf, pos + 1); return t + '*', p
        if b == 0x10:
            t, p = parse_t(buf, pos + 1); return t + '&', p
        if b in (0x11, 0x12):
            idx, p = read_c(buf, pos + 1)
            return tor(idx >> 2, idx & 3), p
        if b == 0x13:
            v, p = read_c(buf, pos + 1); return '!%d' % v, p
        if b == 0x1E:
            v, p = read_c(buf, pos + 1); return '!!%d' % v, p
        if b == 0x15:
            v, p = read_c(buf, pos + 1)
            bse = tor(v >> 2, v & 3)
            n, p = read_c(buf, p)
            a = []
            for _ in range(n):
                t, p = parse_t(buf, p); a.append(t)
            return bse + '<' + ','.join(a) + '>', p
        if b == 0x1D:
            t, p = parse_t(buf, pos + 1); return t + '[]', p
        if b == 0x14:
            t, p = parse_t(buf, pos + 1)
            rk, p = read_c(buf, p); return t + '[rank%d]' % rk, p
        return 't0x%02x' % b, pos + 1

    def tor(idx, tag):
        try:
            if tag == 0: return clsname(md.TypeDef.rows[idx - 1])
            if tag == 1:
                r = md.TypeRef.rows[idx - 1]; return (str(r.TypeNamespace) + '.' + str(r.TypeName)).strip('.')
        except Exception:
            pass
        return 'spec#%d' % idx

    def spec_args(ms):
        sig = ms.Instantiation
        if sig is None: return []
        blob = bytes(sig.value) if hasattr(sig, 'value') else bytes(sig)
        pos = 1 if len(blob) >= 2 and blob[0] == 0x0A else 0
        n, pos = read_c(blob, pos)
        o = []
        for _ in range(n):
            if pos >= len(blob): o.append('...'); break
            t, pos = parse_t(blob, pos); o.append(t)
        return o

    def mref(mr):
        try:
            c = mr.Class
            if c is None or c.table is None: return 'MR::%s' % mr.Name
            tn = c.table.name
            r = c.table.rows[c.row_index - 1]
            if tn == 'TypeDef': return '%s::%s' % (clsname(r), mr.Name)
            if tn == 'TypeRef': return '%s::%s' % ((str(r.TypeNamespace) + '.' + str(r.TypeName)).strip('.'), mr.Name)
            return '%s::%s' % (tn, mr.Name)
        except Exception:
            return 'MR::%s' % mr.Name

    def resolve(t):
        rid = t.rid
        try:
            if t.table == 0x2B:
                ms = md.MethodSpec.rows[rid - 1]
                mt = ms.Method
                a = spec_args(ms)
                bse = mref(md.MemberRef.rows[mt.row_index - 1]) if mt.table.name == 'MemberRef' else '%s::%s' % (md2cls.get(mt.row_index, '?'), md.MethodDef.rows[mt.row_index - 1].Name)
                return '%s<%s>' % (bse, ','.join(a))
            if t.table == 0x0A: return mref(md.MemberRef.rows[rid - 1])
            if t.table == 0x06: return '%s::%s' % (md2cls.get(rid, '?'), md.MethodDef.rows[rid - 1].Name)
            if t.table == 0x01:
                r = md.TypeRef.rows[rid - 1]; return (str(r.TypeNamespace) + '.' + str(r.TypeName)).strip('.')
            if t.table == 0x02: return clsname(md.TypeDef.rows[rid - 1])
            if t.table == 0x04: return 'Field::%s' % md.Field.rows[rid - 1].Name
            return 't%d#%d' % (t.table, rid)
        except Exception:
            return 't%d#%d' % (t.table, rid)

    def fmt(i):
        op = i.opcode.name
        o = i.operand
        if o is None: return op
        if isinstance(o, StringToken):
            try:
                off = o.value & 0x00FFFFFF
                v = pe.net.user_strings.get(off)
                if v is not None: return '%s "%s"' % (op, v)
            except Exception: pass
            return '%s "?"' % op
        if isinstance(o, InvalidToken): return '%s tok=0x%08x' % (op, o.value)
        if isinstance(o, Token): return '%s %s' % (op, resolve(o))
        return '%s %s' % (op, str(o))

    for td in md.TypeDef.rows:
        cname = clsname(td)
        if td.MethodList is None: continue
        for m in td.MethodList:
            try:
                fld = m.row if hasattr(m, 'row') else m
                if getattr(fld, 'Rva', None) in (None, 0):
                    continue
                body = CilMethodBody(CilMethodBodyReaderBytes(pe.get_data(fld.Rva, 0x20000)))
                mname = str(fld.Name)
            except Exception:
                continue
            for i in body.instructions:
                s = fmt(i)
                for nd in needles:
                    if nd in s.lower():
                        found_in_dll.append((cname, mname, i.offset, s))
                        break
    print('### %s : %d hits' % (base, len(found_in_dll)))
    for c, mn, off, s in found_in_dll[:400]:
        print('    %s::%s  IL_%04x  %s' % (c, mn, off, s))
print('ALL DONE')
