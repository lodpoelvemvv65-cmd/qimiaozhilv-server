# -*- coding: utf-8 -*-
"""cap39 独立复核 · 第 3 部分：BOSS 数值时间序列（含 1018 异常）/ 短时 buff / IsBuff 缺失帧"""
import io, json, struct, collections, re

BASE = './'
JL = BASE + '_work/pcap_frames/online-control-resist-20260918.jsonl'
OUT = BASE + '_work/cap39_recheck_C.txt'
w = io.open(OUT, 'w', encoding='utf-8')
def p(*a):
    w.write(' '.join(str(x) for x in a) + '\n')

rows = [json.loads(l) for l in io.open(JL, encoding='utf-8') if l.strip()]
BS = '101.43.6.155:7757>192.168.x.x:61643'
r1 = [r for r in rows if r['stream'] == BS]
BOSS = 3640454171745994031

def dec_list(entries):
    out = []
    for e in entries:
        m = re.match(r'<([0-9A-Fa-f]+)B\s+([0-9a-fA-F]+)>', e)
        if not m:
            continue
        b = bytes.fromhex(m.group(2)); i = 0; d = {}
        while i < len(b):
            k = b[i]; i += 1; fld = k >> 3
            if (k & 7) == 0:
                v = 0; s = 0
                while i < len(b):
                    x = b[i]; i += 1; v |= (x & 0x7f) << s; s += 7
                    if x < 0x80: break
                d[fld] = v
            elif (k & 7) == 5 and i + 4 <= len(b):
                d[fld] = struct.unpack_from('<f', b, i)[0]; i += 4
            else:
                break
        out.append(d)
    return out

# ---- BOSS 20170 时间序列 ----
p('===== BOSS 20170 时间序列（每个 NumericType 的取值变化）=====')
series = collections.defaultdict(list)
n70 = 0
for r in r1:
    if r['op'] == 20170 and r['fields'].get('1') == BOSS:
        n70 += 1
        for d in dec_list(r['fields'].get('2') or []):
            if 1 in d:
                series[d[1]].append((r['t'], round(d.get(2) or 0, 4)))
p('BOSS 20170 推送帧数 = %d' % n70)
for k in sorted(series):
    vals = []
    for t, v in series[k]:
        if not vals or vals[-1][1] != v:
            vals.append((t, v))
    p('  %-6d 变化 %2d 次：%s' % (k, len(vals), ' '.join('%.2f:%g' % x for x in vals[:14]) + (' ...' if len(vals) > 14 else '')))

p('\n===== BOSS 20169 单推时间序列 =====')
s69 = collections.defaultdict(list)
for r in r1:
    if r['op'] == 20169 and r['fields'].get('1') == BOSS:
        s69[r['fields'].get('2')].append((r['t'], round(struct.unpack('<f', bytes.fromhex(re.match(r'<f32 ([0-9a-fA-F]+)>', r['fields']['3']).group(1)))[0], 4)))
for k in sorted(s69):
    v = s69[k]
    p('  %-6d 共%d次 变化%d：%s' % (k, len(v), len(set(x[1] for x in v)), ' '.join('%.2f:%g' % x for x in v[:12])))

# ---- MonsterBase 50005 原值 ----
p('\n===== MonsterBase 50005 原值 =====')
mb = [r for r in json.load(io.open(BASE + 'datatable_json/MonsterBase.json', encoding='utf-8')) if r[0] == 50005]
p(json.dumps(mb[0][1], ensure_ascii=False, indent=1) if mb else 'NOT FOUND')

# ---- 首帧 17 项与表值比值 ----
p('\n===== 首次推送 vs MonsterBase（doc41 复核）=====')
first = {}
for k in series:
    first[k] = series[k][0][1]
row = mb[0][1]
mapping = [('Hp', 1002), ('PhyAtk', 1009), ('SpiAtk', 1010), ('PhyDef', 1011), ('SpiDef', 1012),
           ('Pcrir', 1013), ('Mcrir', 1014), ('Pcri', 1015), ('Mcri', 1016), ('Dvo', 1017),
           ('Rpcrir', 1018), ('Rpcri', 1019), ('Rmcrir', 1020), ('Rmcri', 1021),
           ('Nphyi', 1022), ('Nmeni', 1023)]
for f, k in mapping:
    b = row.get(f); q = first.get(k)
    p('  %-8s 表=%-14s 首推=%-14s 比值=%s' % (f, b, q, ('%.4f' % (q / b)) if (b and q) else 'n/a'))
p('  表中全部键：%s' % list(row.keys()))

# ---- 短时 buff（Time<=1000）----
p('\n===== 短时状态（Time <= 1000ms）=====')
for r in r1:
    if r['op'] == 20080 and (r['fields'].get('7') or 0) <= 1000:
        f = r['fields']
        p('  t=%7.2f 目标=%-6s icon=%-20s desc=%-12s Time=%-6s IsBuff=%s' % (r['t'], f.get('2'), f.get('3'), f.get('4'), f.get('7'), f.get('8')))

# ---- IsBuff 缺失的帧 ----
p('\n===== 20080 中缺 field8(IsBuff=0) 的 icon 分布 =====')
c = collections.Counter(r['fields'].get('3') for r in r1 if r['op'] == 20080 and '8' not in r['fields'])
for k, v in c.most_common():
    p('  %-24s %d' % (k, v))
p('\n===== 20080 全图标 × IsBuff =====')
c2 = collections.Counter((r['fields'].get('3'), r['fields'].get('8')) for r in r1 if r['op'] == 20080)
for (ic, ib), v in sorted(c2.items()):
    p('  %-24s IsBuff=%-5s %d' % (ic, ib, v))

# ---- ChangeType 枚举 ----
p('\n===== 协议 ChangeType 枚举 =====')
txt = io.open(BASE + '参考数据/protocol_dump.txt', encoding='utf-8', errors='replace').read()
p(txt[:0])
for l in txt.split('\n'):
    if 'ChangeType' in l:
        p('  ' + l.strip())

w.close()
print('OK', OUT)
