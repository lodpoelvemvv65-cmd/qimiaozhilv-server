# -*- coding: utf-8 -*-
import sys, re
import dnfile
from dncil.cil.body.reader import CilMethodBodyReaderBytes
from dncil.cil.body import CilMethodBody
from dncil.clr.token import StringToken, Token
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

DLLS = {
    'Unity.Model.dll': r'<原版客户端>\梦幻奇遇记_Data\Managed\Unity.Model.dll',
    'Unity.ModelView.dll': r'<原版客户端>\梦幻奇遇记_Data\Managed\Unity.ModelView.dll',
}

def build_resolver(md, us):
    TABLES = {0x06:'MethodDef',0x0A:'MemberRef',0x2B:'MethodSpec',
              0x01:'TypeRef',0x02:'TypeDef',0x1B:'TypeSpec',
              0x04:'Field',0x0B:'CustomAttribute',0x11:'StandAloneSig'}
    def resolve(token):
        t = token.table; rid = token.rid; tn = TABLES.get(t, hex(t))
        try:
            if t == 0x06:
                r = md.MethodDef.rows[rid-1]; return f'{tn}::{str(r.Name)}'
            elif t == 0x0A:
                r = md.MemberRef.rows[rid-1]
                c = r.Class
                if c.table is not None and c.table.name in ('TypeRef','TypeDef'):
                    cr = c.table.rows[c.row_index-1]
                    return f'{cr.TypeNamespace}.{cr.TypeName}::{str(r.Name)}'.strip('.')
                return f'{tn}::{str(r.Name)}'
            elif t == 0x01:
                r = md.TypeRef.rows[rid-1]; return f'{tn}::{str(r.TypeNamespace)}.{str(r.TypeName)}'
            elif t == 0x02:
                r = md.TypeDef.rows[rid-1]; return f'{tn}::{str(r.TypeName)}'
            elif t == 0x04:
                r = md.Field.rows[rid-1]; return f'{tn}::{str(r.Name)}'
            else:
                return f'{tn}::rid={rid}'
        except Exception:
            return f'{tn}::rid={rid}'
    def get_str(tok):
        try:
            u = us.get(tok.rid)
            if u is not None: return repr(u.value)
        except Exception: pass
        return None
    return resolve, get_str

def disasm(pe, m, resolve, get_str):
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
        o = insn.operand
        if insn.is_ldstr():
            s += ' ' + (get_str(o) or repr(o))
        elif o is not None:
            if isinstance(o, StringToken):
                s += ' ' + (get_str(o) or repr(o))
            elif isinstance(o, Token):
                s += ' ' + resolve(o)
            else:
                s += ' ' + repr(o)
        lines.append(s)
    return lines

def dump_class(pe, md, us, clsname, title):
    resolve, get_str = build_resolver(md, us)
    for td in md.TypeDef.rows:
        if str(td.TypeName) == clsname:
            ns = str(td.TypeNamespace)
            print(f'\n========== {title}: {ns}.{clsname} ==========')
            for mi in td.MethodList:
                m = mi.row
                print(f'\n  --- {str(m.Name)} (RVA 0x{m.Rva:X}) ---')
                for l in disasm(pe, m, resolve, get_str):
                    print(l)

def scan_strings(pe, title):
    # 读 #US 堆原始字节，按 UTF-16LE 扫描可打印字符串
    print(f'\n========== {title}: USER STRING SCAN ==========')
    try:
        us = pe.net.user_strings
        data = us.get_bytes() if hasattr(us, 'get_bytes') else None
        if data is None:
            # 尝试从文件读
            print('  (cannot read raw heap)')
            return
    except Exception as e:
        print(f'  (heap read err: {e})')
        return
    # UTF-16LE 扫描
    found = []
    i = 0
    n = len(data)
    while i < n - 3:
        # 尝试读一个长度前缀（7bit 压缩）后面跟着 UTF16 字符
        # 简单方式：找连续可打印 UTF16 序列
        pass
    # 直接扫描：每个偶偏移尝试解码
    text = data.decode('utf-16-le', errors='ignore')
    pat = re.compile(r'[\x20-\x7e]{4,}')
    seen = set()
    for mm in pat.finditer(text):
        s = mm.group()
        if s not in seen:
            seen.add(s)
            if re.search(r'(http|\.com|\.cn|\.io|\.top|\.xyz|\.net|://|\d+\.\d+\.\d+\.\d+|:[0-9]{2,5}|key|Key|KEY|aes|Aes|AES|secret|Secret|token|Token|pass|Pass|account|Account|login|Login|gate|Gate|realm|Realm|server|Server|host|Host|port|Port)', s):
                found.append(s)
    for s in found:
        print(f'  {s}')

# 反汇编关键类
for dll, path in DLLS.items():
    pe = dnfile.dnPE(path); md = pe.net.mdtables; us = pe.net.user_strings
    print('#' * 70)
    print(f'# {dll}')
    print('#' * 70)
    for cls in ['Encryption', 'CryptoHelper', 'GameKeyComponent', 'GameKeyComponentAwakeSystem',
                'RemoteServices', 'GameDecryptionServices']:
        dump_class(pe, md, us, cls, cls)
    scan_strings(pe, dll)
