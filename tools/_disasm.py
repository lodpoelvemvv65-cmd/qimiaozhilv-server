# -*- coding: utf-8 -*-
"""反汇编指定 TypeDef rid 的所有方法（带 token 解析）。用法: python _disasm.py <rid> [<rid>...]"""
import os
import sys
from pathlib import Path
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
import dnfile
from dncil.cil.body import CilMethodBody
from dncil.cil.body.reader import CilMethodBodyReaderBytes
from dncil.cil.error import MethodBodyFormatError
from dncil.clr.token import StringToken, InvalidToken, Token

DLL = str(next((q for q in Path(os.path.dirname(os.path.abspath(__file__))).parent.joinpath('client-127.0.0.1').iterdir() if q.name.endswith('_Data'))).joinpath('StreamingAssets','yoo','_extracted', '%s.dll' % (sys.argv[1] if len(sys.argv)>1 else 'Hotfix')))
pe = dnfile.dnPE(DLL)
md = pe.net.mdtables

md2cls = {}
for td in md.TypeDef.rows:
    if td.MethodList is None: continue
    for mi in td.MethodList:
        try: md2cls[mi.row.row_index] = (str(td.TypeNamespace)+'.'+str(td.TypeName)).strip('.')
        except Exception: pass
def clsname(td): return (str(td.TypeNamespace)+'.'+str(td.TypeName)).strip('.')

def read_compressed(buf, pos):
    x = buf[pos]; pos += 1
    if x & 0x80:
        x = ((x & 0x7F) << 8) | buf[pos]; pos += 1
        if x & 0x4000:
            x = ((x & 0x3FFF) << 16) | (buf[pos] << 8) | buf[pos+1]; pos += 2
    return x, pos

def parse_type(buf, pos):
    b = buf[pos]
    prim = {0x01:'void',0x02:'bool',0x03:'char',0x04:'i1',0x05:'u1',0x06:'i2',0x07:'u2',
            0x08:'i4',0x09:'u4',0x0A:'i8',0x0B:'u8',0x0C:'r4',0x0D:'r8',0x0E:'string',
            0x16:'typedbyref',0x18:'native int',0x19:'native uint',0x1C:'object'}
    if b in prim: return prim[b], pos+1
    if b == 0x0F:
        t, pos = parse_type(buf, pos+1); return t+'*', pos
    if b == 0x10:
        t, pos = parse_type(buf, pos+1); return t+'&', pos
    if b in (0x11, 0x12):
        idx, pos = read_compressed(buf, pos+1)
        return typedef_or_ref(idx>>2, idx&3), pos
    if b == 0x13:
        v, pos = read_compressed(buf, pos+1); return '!%d'%v, pos
    if b == 0x1E:
        v, pos = read_compressed(buf, pos+1); return '!!%d'%v, pos
    if b == 0x15:
        v, pos = read_compressed(buf, pos+1)
        base = typedef_or_ref(v>>2, v&3)
        n, pos = read_compressed(buf, pos)
        args = []
        for _ in range(n):
            t, pos = parse_type(buf, pos); args.append(t)
        return base+'<'+','.join(args)+'>', pos
    if b == 0x1D:
        t, pos = parse_type(buf, pos+1); return t+'[]', pos
    if b == 0x14:
        t, pos = parse_type(buf, pos+1)
        rank, pos = read_compressed(buf, pos); return t+'[rank%d]'%rank, pos
    return 't0x%02x'%b, pos+1

def typedef_or_ref(idx, tag):
    if tag == 0: return clsname(md.TypeDef.rows[idx-1])
    if tag == 1:
        r = md.TypeRef.rows[idx-1]; return (str(r.TypeNamespace)+'.'+str(r.TypeName)).strip('.')
    return 'spec#%d'%idx

def spec_args(ms):
    """Decode MethodSpec Instantiation blob.
    Observed format in these assemblies: [0x0A prefix][compressed count][type sigs...].
    Each type sig = element-type byte + TypeDefOrRef coded index (compressed).
    """
    sig = ms.Instantiation
    if sig is None: return []
    blob = bytes(sig.value) if hasattr(sig, 'value') else bytes(sig)
    if len(blob) >= 2 and blob[0] == 0x0A:
        pos = 1
    else:
        pos = 0
    n, pos = read_compressed(blob, pos)
    out = []
    for _ in range(n):
        if pos >= len(blob):
            out.append('...'); break
        t, pos = parse_type(blob, pos)
        out.append(t)
    return out

def member_ref_name(mr):
    """Return 'ns.Type::name' for a MemberRef; handle TypeDef/TypeRef/TypeSpec class."""
    try:
        c = mr.Class
        if c is None or c.table is None:
            return 'MR::%s' % mr.Name
        tn = c.table.name
        r = c.table.rows[c.row_index - 1]
        if tn == 'TypeDef':
            return '%s::%s' % (clsname(r), mr.Name)
        if tn == 'TypeRef':
            return '%s::%s' % ((str(r.TypeNamespace)+'.'+str(r.TypeName)).strip('.'), mr.Name)
        if tn == 'TypeSpec':
            return 'TS::%s' % mr.Name
        return '%s::%s' % (tn, mr.Name)
    except Exception:
        return 'MR::%s' % mr.Name

def resolve(t):
    rid = t.rid
    try:
        if t.table == 0x2B:
            ms = md.MethodSpec.rows[rid-1]
            mt = ms.Method
            args = spec_args(ms)
            if mt.table.name == 'MemberRef':
                mr = md.MemberRef.rows[mt.row_index-1]
                base = member_ref_name(mr)
            else:
                mr = md.MethodDef.rows[mt.row_index-1]
                base = '%s::%s' % (md2cls.get(mt.row_index,'?'), mr.Name)
            return '%s<%s>' % (base, ','.join(args))
        elif t.table == 0x0A:
            r = md.MemberRef.rows[rid-1]
            return member_ref_name(r)
        elif t.table == 0x06: return '%s::%s' % (md2cls.get(rid,'?'), md.MethodDef.rows[rid-1].Name)
        elif t.table == 0x01:
            r = md.TypeRef.rows[rid-1]; return (str(r.TypeNamespace)+'.'+str(r.TypeName)).strip('.')
        elif t.table == 0x02: return clsname(md.TypeDef.rows[rid-1])
        elif t.table == 0x04: return 'Field::%s' % md.Field.rows[rid-1].Name
        elif t.table == 0x23:  # FieldRVA -> use Field token? just name
            return 'FieldRVA#%d' % rid
        return 't%d#%d' % (t.table, rid)
    except Exception as e:
        return 't%d#%d<%r>' % (t.table, rid, e)[:60]

def disasm_method(m):
    name = str(m.Name)
    rva = getattr(m, 'Rva', None)
    if rva in (None, 0):
        print('  ==== method %s <no body>' % name)
        return
    print('  ==== method %s' % name)
    try:
        body_bytes = pe.get_data(rva, 0x8000)
        body = CilMethodBody(CilMethodBodyReaderBytes(body_bytes))
    except MethodBodyFormatError:
        print('      <no body>')
        return
    for insn in body.instructions:
        op = insn.opcode.name
        operand = insn.operand
        s = ''
        if operand is None:
            s = op
        elif isinstance(operand, StringToken):
            s = '%s "?"' % op
            try:
                off = operand.value & 0x00FFFFFF
                if hasattr(pe.net, 'user_strings'):
                    v = pe.net.user_strings.get(off)
                    if v is not None:
                        s = '%s "%s"' % (op, v)
            except Exception:
                pass
        elif isinstance(operand, InvalidToken):
            s = '%s tok=0x%08x' % (op, operand.value)
        elif isinstance(operand, Token):
            s = '%s %s' % (op, resolve(operand))
        else:
            s = '%s %s' % (op, str(operand))
        print('      IL_%04x: %s' % (insn.offset, s))

def resolve_method(m):
    if hasattr(m, 'row_index') and not hasattr(m, 'Name'):
        return md.MethodDef.rows[m.row_index - 1]
    return m

def disasm_td(rid, depth):
    td = md.TypeDef.rows[rid - 1]
    print('%s==== TypeDef %d: %s.%s' % ('  '*depth, rid, str(td.TypeNamespace), str(td.TypeName)))
    for m in td.MethodList:
        try:
            disasm_method(resolve_method(m))
        except Exception as e:
            print('      !! err: %r' % e)
    # 递归反汇编嵌套状态机
    for nc in md.NestedClass.rows:
        try:
            if nc.EnclosingClass.row_index == rid:
                disasm_td(nc.NestedClass.row_index, depth + 1)
        except Exception:
            pass

for rid in [int(x) for x in sys.argv[2:]]:
    disasm_td(rid, 0)
