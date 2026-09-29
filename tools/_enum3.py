# -*- coding: utf-8 -*-
"""正确导出枚举成员与常量值。用法: python _enum3.py <dll> <out.txt> [过滤]"""
import sys, io, struct
sys.stdout = io.TextIOWrapper(open(sys.argv[2], 'wb'), encoding='utf-8', errors='replace')
import dnfile

SIZES = {2: 1, 3: 2, 4: 1, 5: 1, 6: 2, 7: 2, 8: 4, 9: 4, 10: 8, 11: 8, 12: 4, 13: 8}

pe = dnfile.dnPE(sys.argv[1])
md = pe.net.mdtables
flt = sys.argv[3] if len(sys.argv) > 3 else ''

fld_const = {}
for c in md.Constant.rows:
    try:
        p = c.Parent
        tname = p.table.name
        idx = p.row_index
        if tname != 'Field':
            continue
        raw = c.Value.value if hasattr(c.Value, 'value') else bytes(c.Value)
        sz = SIZES.get(c.Type, len(raw))
        if len(raw) < sz:
            raw = raw + b'\x00' * (sz - len(raw))
        if c.Type in (2, 4, 5):
            v = struct.unpack('<B', raw[:1])[0]
        elif c.Type in (3, 6, 7):
            v = struct.unpack('<H', raw[:2])[0]
        elif c.Type in (8, 9, 12):
            v = struct.unpack('<i', raw[:4])[0]
        else:
            v = struct.unpack('<q', raw[:8])[0]
        fld_const[idx] = v
    except Exception:
        pass

out = []
print('fld_const size = %d' % len(fld_const))
cnt_enums = 0
for i, td in enumerate(md.TypeDef.rows, 1):
    full = (str(td.TypeNamespace) + '.' + str(td.TypeName)).strip('.')
    if flt and flt.lower() not in full.lower():
        continue
    entries = []
    dbg_errs = []
    for f in (td.FieldList or []):
        try:
            rid = f.row_index
            nm = str(f.row.Name)
        except Exception as e:
            try:
                rid = f.row.row_index
                nm = str(f.row.Name)
            except Exception as e2:
                dbg_errs.append(repr(e2))
                continue
        entries.append((nm, fld_const.get(rid)))
    if 'ValueCalculateType' in full or 'SkillOptionType' in full:
        print('DEBUG %s nfields=%d entries=%r errs=%r' % (full, len(td.FieldList or []), entries[:6], dbg_errs[:3]))
    if not entries:
        continue
    is_enum = entries[0][0] == 'value__'
    members = [e for e in entries if e[1] is not None]
    if is_enum and members:
        cnt_enums += 1
        out.append('enum %s' % full)
        for n, v in entries:
            out.append('    %-45s = %s' % (n, v if v is not None else '(backing field)'))
print('\n'.join(out) if out else '(no enum matched)')
print('cnt_enums=%d' % cnt_enums)
