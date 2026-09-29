# -*- coding: utf-8 -*-
import sys
import dnfile
from dncil.cil.body.reader import CilMethodBodyReaderBytes
from dncil.cil.body import CilMethodBody
from dncil.clr.token import StringToken, Token
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

DLL = r'<原版客户端>\梦幻奇遇记_Data\Managed\Unity.Model.dll'
pe = dnfile.dnPE(DLL)
md = pe.net.mdtables
us = pe.net.user_strings

def resolve(token):
    t = token.table; rid = token.rid
    try:
        if t == 0x0A:
            r = md.MemberRef.rows[rid-1]
            c = r.Class
            if c.table is not None:
                cr = c.table.rows[c.row_index-1]
                if c.table.name in ('TypeRef','TypeDef'):
                    return f'{cr.TypeNamespace}.{cr.TypeName}::{r.Name}'.strip('.')
            return f'MemberRef::{r.Name}'
        elif t == 0x06: r = md.MethodDef.rows[rid-1]; return f'MethodDef::{r.Name}'
        elif t == 0x01: r = md.TypeRef.rows[rid-1]; return f'{r.TypeNamespace}.{r.TypeName}'.strip('.')
        elif t == 0x02: r = md.TypeDef.rows[rid-1]; return f'{r.TypeNamespace}.{r.TypeName}'.strip('.')
        elif t == 0x04: r = md.Field.rows[rid-1]; return f'Field::{r.Name}'
        elif t == 0x2B: return f'MethodSpec::rid={rid}'
        else: return f'table={t} rid={rid}'
    except Exception as e:
        return f'table={t} rid={rid} err={e}'

def get_str(token):
    try:
        u = us.get(token.rid)
        if u is not None: return repr(u.value)
    except Exception: pass
    return None

def disasm(m):
    rva = m.Rva
    if rva == 0: return []
    try:
        body_bytes = pe.get_data(rva, 0x8000)
        body = CilMethodBody(CilMethodBodyReaderBytes(body_bytes))
    except Exception as e:
        return [f'  !! parse error: {e}']
    lines = []
    for insn in body.instructions:
        op = insn.opcode.name
        s = f'    {insn.offset:04X}: {op}'
        operand = insn.operand
        if insn.is_ldstr():
            s += ' ' + (get_str(operand) or repr(operand))
        elif operand is not None:
            if isinstance(operand, StringToken):
                s += ' ' + (get_str(operand) or repr(operand))
            elif isinstance(operand, Token):
                s += ' ' + resolve(operand)
            else:
                s += ' ' + repr(operand)
        lines.append(s)
    return lines

for td in md.TypeDef.rows:
    name = str(td.TypeName)
    if name == 'MessageSerializeHelper':
        ns = str(td.TypeNamespace)
        print(f'========== {ns}.{name} ==========')
        for mi in td.MethodList:
            m = mi.row
            print(f'\n  --- {m.Name} (RVA 0x{m.Rva:X}) ---')
            for line in disasm(m):
                print(line)
