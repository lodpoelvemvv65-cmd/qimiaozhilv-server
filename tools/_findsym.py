# -*- coding: utf-8 -*-
"""在整个 DLL 的所有方法 IL 里搜索操作数包含指定子串的指令。
用法: python _findsym.py <dll路径> <子串> [上下文行数]
输出: 文件::方法 以及命中的指令及其前后若干条指令。"""
import sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
import dnfile
from dncil.cil.body import CilMethodBody
from dncil.cil.body.reader import CilMethodBodyReaderBytes
from dncil.cil.error import MethodBodyFormatError
from dncil.clr.token import StringToken, InvalidToken, Token

dll = sys.argv[1]
needle = sys.argv[2]
ctx = int(sys.argv[3]) if len(sys.argv) > 3 else 3

pe = dnfile.dnPE(dll)
md = pe.net.mdtables

md2cls = {}
for td in md.TypeDef.rows:
    if td.MethodList is None:
        continue
    for mi in td.MethodList:
        try:
            md2cls[mi.row.row_index] = (str(td.TypeNamespace) + '.' + str(td.TypeName)).strip('.')
        except Exception:
            pass

def clsname(td):
    return (str(td.TypeNamespace) + '.' + str(td.TypeName)).strip('.')

def read_compressed(buf, pos):
    x = buf[pos]; pos += 1
    if x & 0x80:
        x = ((x & 0x7F) << 8) | buf[pos]; pos += 1
        if x & 0x4000:
            x = ((x & 0x3FFF) << 16) | (buf[pos] << 8) | buf[pos + 1]; pos += 2
    return x, pos

def parse_type(buf, pos):
    prim = {0x01:'void',0x02:'bool',0x03:'char',0x04:'i1',0x05:'u1',0x06:'i2',0x07:'u2',
            0x08:'i4',0x09:'u4',0x0A:'i8',0x0B:'u8',0x0C:'r4',0x0D:'r8',0x0E:'string',
            0x16:'typedbyref',0x18:'native int',0x19:'native uint',0x1C:'object'}
    b = buf[pos]
    if b in prim: return prim[b], pos + 1
    if b == 0x0F:
        t, pos = parse_type(buf, pos + 1); return t + '*', pos
    if b == 0x10:
        t, pos = parse_type(buf, pos + 1); return t + '&', pos
    if b in (0x11, 0x12):
        idx, pos = read_compressed(buf, pos + 1)
        return typedef_or_ref(idx >> 2, idx & 3), pos
    if b == 0x13:
        v, pos = read_compressed(buf, pos + 1); return '!%d' % v, pos
    if b == 0x1E:
        v, pos = read_compressed(buf, pos + 1); return '!!%d' % v, pos
    if b == 0x15:
        v, pos = read_compressed(buf, pos + 1)
        base = typedef_or_ref(v >> 2, v & 3)
        n, pos = read_compressed(buf, pos)
        args = []
        for _ in range(n):
            t, pos = parse_type(buf, pos); args.append(t)
        return base + '<' + ','.join(args) + '>', pos
    if b == 0x1D:
        t, pos = parse_type(buf, pos + 1); return t + '[]', pos
    if b == 0x14:
        t, pos = parse_type(buf, pos + 1)
        rank, pos = read_compressed(buf, pos); return t + '[rank%d]' % rank, pos
    return 't0x%02x' % b, pos + 1

def typedef_or_ref(idx, tag):
    if tag == 0: return clsname(md.TypeDef.rows[idx - 1])
    if tag == 1:
        r = md.TypeRef.rows[idx - 1]; return (str(r.TypeNamespace) + '.' + str(r.TypeName)).strip('.')
    return 'spec#%d' % idx

def spec_args(ms):
    sig = ms.Instantiation
    if sig is None: return []
    blob = bytes(sig.value) if hasattr(sig, 'value') else bytes(sig)
    pos = 1 if len(blob) >= 2 and blob[0] == 0x0A else 0
    n, pos = read_compressed(blob, pos)
    out = []
    for _ in range(n):
        if pos >= len(blob): out.append('...'); break
        t, pos = parse_type(blob, pos); out.append(t)
    return out

def member_ref_name(mr):
    try:
        c = mr.Class
        if c is None or c.table is None: return 'MR::%s' % mr.Name
        tn = c.table.name
        r = c.table.rows[c.row_index - 1]
        if tn == 'TypeDef': return '%s::%s' % (clsname(r), mr.Name)
        if tn == 'TypeRef': return '%s::%s' % ((str(r.TypeNamespace) + '.' + str(r.TypeName)).strip('.'), mr.Name)
        if tn == 'TypeSpec': return 'TS::%s' % mr.Name
        return '%s::%s' % (tn, mr.Name)
    except Exception:
        return 'MR::%s' % mr.Name

def resolve(t):
    rid = t.rid
    try:
        if t.table == 0x2B:
            ms = md.MethodSpec.rows[rid - 1]
            mt = ms.Method
            args = spec_args(ms)
            if mt.table.name == 'MemberRef':
                base = member_ref_name(md.MemberRef.rows[mt.row_index - 1])
            else:
                base = '%s::%s' % (md2cls.get(mt.row_index, '?'), md.MethodDef.rows[mt.row_index - 1].Name)
            return '%s<%s>' % (base, ','.join(args))
        elif t.table == 0x0A: return member_ref_name(md.MemberRef.rows[rid - 1])
        elif t.table == 0x06: return '%s::%s' % (md2cls.get(rid, '?'), md.MethodDef.rows[rid - 1].Name)
        elif t.table == 0x01:
            r = md.TypeRef.rows[rid - 1]; return (str(r.TypeNamespace) + '.' + str(r.TypeName)).strip('.')
        elif t.table == 0x02: return clsname(md.TypeDef.rows[rid - 1])
        elif t.table == 0x04: return 'Field::%s' % md.Field.rows[rid - 1].Name
        return 't%d#%d' % (t.table, rid)
    except Exception as e:
        return 't%d#%d<%r>' % (t.table, rid, e)

def fmt(insn):
    op = insn.opcode.name
    o = insn.operand
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

def rowidx(td, fallback=None):
    try:
        return td.row.row_index
    except Exception:
        return fallback if fallback is not None else 0


hits = 0
for td in md.TypeDef.rows:
    cname = clsname(td)
    if td.MethodList is None: continue
    for m in td.MethodList:
        try:
            meth = md.MethodDef.rows[m.row_index - 1] if hasattr(m, 'row_index') else m
            rva = getattr(meth, 'Rva', None)
            if rva in (None, 0): continue
            body = CilMethodBody(CilMethodBodyReaderBytes(pe.get_data(rva, 0x20000)))
        except Exception:
            continue
        lines = [fmt(i) for i in body.instructions]
        for idx, ln in enumerate(lines):
            if needle.lower() in ln.lower():
                hits += 1
                print('--- %s::%s  (TypeDef rid=%d)   IL_%04x' % (cname, meth.Name, rowidx(td), body.instructions[idx].offset))
                lo = max(0, idx - ctx); hi = min(len(lines), idx + ctx + 1)
                for j in range(lo, hi):
                    print('    %sIL_%04x: %s' % ('>>' if j == idx else '  ', body.instructions[j].offset, lines[j]))
print('TOTAL HITS: %d' % hits)
