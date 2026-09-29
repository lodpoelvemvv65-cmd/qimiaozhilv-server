# -*- coding: utf-8 -*-
"""cap39 独立复核 · 第 4 部分：叠层/减益对齐 + 20077 + 20024"""
import io, json, struct, collections, re

BASE = './'
JL = BASE + '_work/pcap_frames/online-control-resist-20260918.jsonl'
OUT = BASE + '_work/cap39_recheck_D.txt'
w = io.open(OUT, 'w', encoding='utf-8')
def p(*a):
    w.write(' '.join(str(x) for x in a) + '\n')

rows = [json.loads(l) for l in io.open(JL, encoding='utf-8') if l.strip()]
BS = '101.43.6.155:7757>192.168.x.x:61643'
r1 = [r for r in rows if r['stream'] == BS]
BOSS = 3640454171745994031
BASE_ATK = 3672000.0
BASE_DEF = 3208532.0

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

# ---------- 物攻/物防 变化点 ----------
def series_of(key):
    out = []
    for r in r1:
        if r['op'] == 20170 and r['fields'].get('1') == BOSS:
            for d in dec_list(r['fields'].get('2') or []):
                if d.get(1) == key:
                    v = d.get(2) or 0
                    if not out or out[-1][1] != v:
                        out.append((r['t'], v))
    return out

atk = series_of(1009)
dfn = series_of(1011)
boss_casts = [(r['t'], r['fields'].get('2')) for r in r1 if r['op'] == 20075 and r['fields'].get('1') == BOSS]
all_casts = [(r['t'], r['fields'].get('1'), r['fields'].get('2')) for r in r1 if r['op'] == 20075]
ev77 = [(r['t'], r['fields'].get('1'), r['fields'].get('4'), r['fields'].get('9'), r['fields'].get('6'), r['fields'].get('8')) for r in r1 if r['op'] == 20077]

p('===== 物攻 1009 全部变化点 + 减益系数 + 推断层数 =====')
p('%-9s %-13s %-9s %-8s %s' % ('t', 'value', 'ratio', 'n(=1+0.33n)', '同刻事件'))
for t, v in atk:
    ratio = v / BASE_ATK
    # 解 n 与 debuff：ratio = 1 + 0.33n - d
    n_est = round((ratio - 1) / 0.33, 4)
    near_cast = [x for x in all_casts if abs(x[0] - t) < 0.06]
    ev = '施法=%s' % ','.join('%s/%s' % (x[1], x[2]) for x in near_cast) if near_cast else ''
    near77 = [x for x in ev77 if abs(x[0] - t) < 0.06]
    if near77:
        ev += ' 20077=%s' % ','.join('%s->%s(eff%s)' % (x[1], x[2], x[3]) for x in near77[:4])
    p('%-9.2f %-13g %-9.4f %-8.2f %s' % (t, v, ratio, n_est, ev))

p('\n===== 物防 1011 全部变化点（比值）=====')
for t, v in dfn:
    ratio = v / BASE_DEF
    near_cast = [x for x in all_casts if abs(x[0] - t) < 0.06]
    p('%-9.2f %-13g %-9.4f %s' % (t, v, ratio, ','.join('%s/%s' % (x[1], x[2]) for x in near_cast)))

# ---------- 4.71 网格检验 ----------
p('\n===== 4.71s 网格检验（doc43 §2②）=====')
p('BOSS 施法相邻间隔直方图：')
iv = collections.Counter(round(boss_casts[i+1][0] - boss_casts[i][0], 2) for i in range(len(boss_casts)-1))
for k, v in sorted(iv.items()):
    p('   %-8s x%d   = %.2f × 4.71' % (k, v, k / 4.71))
p('物攻变化点相邻间隔直方图：')
iv2 = collections.Counter(round(atk[i+1][0] - atk[i][0], 2) for i in range(len(atk)-1))
for k, v in sorted(iv2.items()):
    p('   %-8s x%d' % (k, v))

p('\n===== 「变化点是否都有 BOSS 施法」=====')
nocast = [(t, v) for t, v in atk if not any(abs(x[0] - t) < 0.06 for x in boss_casts)]
p('物攻在变化但 %d 个点没有同刻 BOSS 施法：' % len(nocast))
for t, v in nocast:
    c = [x for x in all_casts if abs(x[0] - t) < 0.06]
    e = [x for x in ev77 if abs(x[0] - t) < 0.06]
    p('   t=%.2f v=%g 同刻施法=%s 同刻20077=%s' % (t, v, [(x[1], x[2]) for x in c], [(x[1], x[3]) for x in e][:3]))

# ---------- 20077 分析 ----------
p('\n===== 20077 M2C_PlaySkillEffect（单流 %d 帧）=====' % len(ev77))
p('施法者分布：%s' % dict(collections.Counter(x[1] for x in ev77)))
p('EffectId(9) 分布：%s' % dict(collections.Counter(x[3] for x in ev77).most_common(40)))
p('Time(6) 分布：%s' % dict(collections.Counter(x[4] for x in ev77).most_common(20)))
p('EffectPos(8) 分布：%s' % dict(collections.Counter(x[5] for x in ev77).most_common(20)))
p('(施法者,EffectId) 组合：')
for k, v in collections.Counter((x[1], x[3]) for x in ev77).most_common(60):
    p('   %-22s eff=%-6s x%d' % (k[0], k[1], v))
p('\n20077 = 每个 (Unit, EffectId) 首帧时刻：')
first = {}
for t, u, tg, e, tm, pos in ev77:
    first.setdefault((u, e), (t, tg, tm))
for (u, e), (t, tg, tm) in sorted(first.items(), key=lambda x: x[1][0]):
    p('   t=%-8.2f %-22s eff=%-6s target=%-22s Time=%s' % (t, u, e, tg, tm))

# ---------- 20024 移动 ----------
p('\n===== 20024 M2C_PathfindingResult（单流 %d 帧）=====' % len([r for r in r1 if r['op'] == 20024]))
pf = [r for r in r1 if r['op'] == 20024]
p('单位分布：%s' % dict(collections.Counter(r['fields'].get('1') for r in pf)))
p('首 20 帧：')
for r in pf[:20]:
    f = r['fields']
    def fl(k):
        v = f.get(str(k))
        if isinstance(v, str):
            m = re.match(r'<f32 ([0-9a-fA-F]+)>', v)
            if m:
                return round(struct.unpack('<f', bytes.fromhex(m.group(1)))[0], 1)
        return v
    p('   t=%-8.2f unit=%-6s x=%s z=%s ?5=%s ?6=%s ?7=%s' % (r['t'], f.get('1'), fl(2), fl(3), fl(5), fl(6), fl(7)))

w.close()
print('OK', OUT)
