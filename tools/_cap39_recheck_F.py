# -*- coding: utf-8 -*-
"""cap39 独立复核 · 第 6 部分：减益来源归因 + 相位 + 技能名"""
import io, json, struct, collections, re

BASE = './'
JL = BASE + '_work/pcap_frames/online-control-resist-20260918.jsonl'
OUT = BASE + '_work/cap39_recheck_F.txt'
w = io.open(OUT, 'w', encoding='utf-8')
def p(*a):
    w.write(' '.join(str(x) for x in a) + '\n')

rows = [json.loads(l) for l in io.open(JL, encoding='utf-8') if l.strip()]
BS = '101.43.6.155:7757>192.168.x.x:61643'
r1 = [r for r in rows if r['stream'] == BS]
BOSS = 3640454171745994031
BA, BD = 3672000.0, 3208532.0

def dec_list(entries):
    out = []
    for e in entries:
        m = re.match(r'<([0-9A-Fa-f]+)B\s+([0-9a-fA-F]+)>', e)
        if not m: continue
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
            else: break
        out.append(d)
    return out

def series_of(key, base):
    out = []
    for r in r1:
        if r['op'] == 20170 and r['fields'].get('1') == BOSS:
            for d in dec_list(r['fields'].get('2') or []):
                if d.get(1) == key:
                    v = d.get(2) or 0
                    if not out or out[-1][1] != round(v, 1):
                        out.append((r['t'], v))
    return [(t, v / base) for t, v in out]

casts = [(r['t'], r['fields'].get('1'), r['fields'].get('2')) for r in r1 if r['op'] == 20075]
ev77 = [(r['t'], r['fields'].get('1'), r['fields'].get('4'), r['fields'].get('9')) for r in r1 if r['op'] == 20077]

# 技能名
sc = dict(json.load(io.open(BASE + 'datatable_json/SkillConfig.json', encoding='utf-8')))
def sname(sid):
    row = sc.get(sid * 100)
    if isinstance(row, dict):
        return row.get('Name') or row.get('Desc') or '?'
    if isinstance(row, list) and row:
        return row[0].get('Name') if isinstance(row[0], dict) else '?'
    return '?'

for key, base, tag in ((1009, BA, '物攻'), (1011, BD, '物防')):
    s = series_of(key, base)
    p('\n===== %s 比值序列的「差值事件」归因（找 t-2.6 ~ t-0.4 内的玩家施法）=====' % tag)
    deltas = collections.Counter()
    for i, (t, ratio) in enumerate(s):
        prev = s[i-1][1] if i else 1.0
        if abs(ratio - prev) < 1e-6:
            continue
        cand = [c for c in casts if c[1] != BOSS and t - 2.7 <= c[0] <= t - 0.35]
        e77 = [x for x in ev77 if x[2] == BOSS and abs(x[0] - t) < 0.1]
        d = round(ratio - prev, 4)
        deltas[(d, tuple(sorted({c[2] for c in cand})), tuple(sorted({x[3] for x in e77})))] += 1
    for (d, sk, ef), n in deltas.most_common(30):
        p('   Δ=%-8.3f x%-3d 候选技能=%s 同刻20077eff=%s' % (d, n, [ (x, sname(x)) for x in sk ], ef))

# 相位
p('\n===== 20078 各单位的 t%%0.5 相位 =====')
ph = collections.defaultdict(collections.Counter)
for r in r1:
    if r['op'] == 20078:
        ph[r['fields'].get('1')][round(r['t'] % 0.5, 2)] += 1
for u in sorted(ph):
    top = ph[u].most_common(4)
    tot = sum(ph[u].values())
    p('   %-22s n=%-4d top=%s  前4合计占比=%.0f%%' % (u, tot, top, 100.0 * sum(x[1] for x in top) / tot))

# 210603 / 210601 / 210401 名字
p('\n===== 相关技能名 =====')
for sid in (210603, 210601, 210401, 110304, 110601, 110604, 310501, 310202, 410304, 400001, 500010, 500012, 500021, 500023, 500032, 500041, 110304):
    p('   %-8d %s' % (sid, sname(sid)))

w.close()
print('OK', OUT)
