# -*- coding: utf-8 -*-
"""cap39 独立复核 · 第 2 部分：doc40 的技能/状态/落地率/盾/暴击/治疗"""
import io, json, struct, collections, re

BASE = './'
JL = BASE + '_work/pcap_frames/online-control-resist-20260918.jsonl'
OUT = BASE + '_work/cap39_recheck_B.txt'
w = io.open(OUT, 'w', encoding='utf-8')
def p(*a):
    w.write(' '.join(str(x) for x in a) + '\n')

rows = [json.loads(l) for l in io.open(JL, encoding='utf-8') if l.strip()]
BASE_STREAM = '101.43.6.155:7757>192.168.x.x:61643'
r1 = [r for r in rows if r['stream'] == BASE_STREAM]
p('基准流 %s 帧数 %d' % (BASE_STREAM, len(r1)))

BOSS = 3640454171745994031
PLAYERS = [18937, 22233, 22234, 22235, 22240]

def fnum(v):
    if isinstance(v, str):
        m = re.match(r'<f32 ([0-9a-fA-F]+)>', v)
        if m:
            return struct.unpack('<f', bytes.fromhex(m.group(1)))[0]
    return v

def i64(v):
    if isinstance(v, int) and v >= 2**63:
        return v - 2**64
    return v

# ---------- 1. 20075 技能施放 ----------
p('\n===== 1. 20075 M2C_PlaySkill 单流明细 =====')
casts = [(r['t'], r['fields'].get('1'), r['fields'].get('2')) for r in r1 if r['op'] == 20075]
p('单流 20075 帧数 = %d' % len(casts))
byunit = collections.Counter(c[1] for c in casts)
p('按施法单位：')
for u, n in byunit.most_common():
    p('   %s : %d' % (u, n))
bosscasts = [c for c in casts if c[1] == BOSS]
p('BOSS(%d) 施法次数 = %d' % (BOSS, len(bosscasts)))
sk = collections.Counter(c[2] for c in bosscasts)
tot = sum(sk.values())
for s, n in sk.most_common():
    p('   %-8d %-4d %5.1f%%   doc40 期望见下' % (s, n, 100.0 * n / tot))
p('时间点：%s' % ', '.join('%.2f' % c[0] for c in bosscasts))

# ---------- 2. 20080 状态 ----------
p('\n===== 2. 20080 M2C_BattleChangeState =====')
st = [r for r in r1 if r['op'] == 20080]
p('单流 20080 帧数 = %d（全包 %d）' % (len(st), sum(1 for r in rows if r['op'] == 20080)))
tgt = collections.Counter(r['fields'].get('2') for r in st)
p('按目标单位：%s' % dict(tgt))
p('目标里有 BOSS 吗：%s' % ('有' if BOSS in tgt else '没有'))
icon = collections.Counter((r['fields'].get('3'), r['fields'].get('7')) for r in st)
p('按 (icon, Time)：')
for (ic, tm), n in icon.most_common():
    p('   %-28s Time=%-8s x%d' % (ic, tm, n))
ids = [r['fields'].get('1') for r in st]
p('状态 Id 数 = %d，唯一 Id 数 = %d  ⇒ %s' % (len(ids), len(set(ids)), '单流内不重复' if len(ids) == len(set(ids)) else '有重复!'))
dup = [k for k, v in collections.Counter(ids).items() if v > 1]
if dup:
    p('   重复 Id：%s' % dup[:10])
p('Type(6) 取值：%s' % dict(collections.Counter(r['fields'].get('6') for r in st)))
p('IsBuff(8) 取值：%s' % dict(collections.Counter(r['fields'].get('8') for r in st)))

# ---------- 3. 500041 眩晕落地率 ----------
p('\n===== 3. 500041 眩晕落地率（按存活期）=====')
deaths = {}
for r in r1:
    if r['op'] == 20083:
        deaths[r['fields'].get('1')] = r['t']
p('死亡时刻：%s' % deaths)
vtime = [x for x in bosscasts if x[2] == 500041]
p('500041 施法 %d 次：%s' % (len(vtime), ', '.join('%.2f' % x[0] for x in vtime)))
# 每个玩家的眩晕落地帧
land = collections.defaultdict(list)
for r in st:
    if r['fields'].get('3') == 'bufficon_vertigo':
        land[r['fields'].get('2')].append(r['t'])
tot_att = tot_land = 0
p('%-8s %-10s %-10s %-10s %s' % ('玩家', '存活至', '在场施法', '落地', '落地率'))
for pl in PLAYERS:
    dl = deaths.get(pl, 1e9)
    att = [x for x in vtime if x[0] < dl]
    ld = [t for t in land.get(pl, []) if t < dl]
    tot_att += len(att); tot_land += len(ld)
    p('%-8d %-10.1f %-10d %-10d %.1f%%' % (pl, dl, len(att), len(ld), 100.0 * len(ld) / len(att) if att else 0))
p('合计 %d/%d = %.1f%%' % (tot_land, tot_att, 100.0 * tot_land / tot_att))
# 眩晕覆盖率
p('眩晕覆盖率：')
for pl in PLAYERS:
    dl = deaths.get(pl, 1e9)
    seg = [t for t in land.get(pl, []) if t < dl]
    p('   %-8d 存活 %6.1fs 眩晕 %5.1fs = %.1f%%' % (pl, dl, 12.0 * len(seg), 100.0 * 12.0 * len(seg) / dl))

# ---------- 4. 20078 伤害/治疗/暴击 ----------
p('\n===== 4. 20078 M2C_BattleSkillRet 统计 =====')
all78 = [r for r in rows if r['op'] == 20078]
dmg = [r for r in all78 if i64(r['fields'].get('2')) < 0]
heal = [r for r in all78 if i64(r['fields'].get('2')) > 0]
zero = [r for r in all78 if i64(r['fields'].get('2')) == 0]
p('全包 20078 帧 %d  伤害(负) %d  治疗(正) %d  零 %d' % (len(all78), len(dmg), len(heal), len(zero)))
p('doc40 写：伤害 1555 / 治疗 1240（= 单流 311 / 248 ×5）→ %s' % ('吻合' if (len(dmg) == 1555 and len(heal) == 1240) else '不吻合'))
def crit(r):
    return r['fields'].get('3') in (1, True, '1')
dc = sum(1 for r in dmg if crit(r))
hc = sum(1 for r in heal if crit(r))
p('伤害暴击 %d/%d = %.1f%%   （doc40 写 705/1555 = 45.3%%）' % (dc, len(dmg), 100.0 * dc / max(1, len(dmg))))
p('治疗暴击 %d/%d = %.1f%%   （doc40 写 1030/1240 = 83.1%%）' % (hc, len(heal), 100.0 * hc / max(1, len(heal))))
p('field3 出现次数 %d / %d（缺 field3 = 非暴击）' % (sum(1 for r in all78 if '3' in r['fields']), len(all78)))
# 伤害=1
p('伤害 == -1 的帧数 = %d（全包）' % sum(1 for r in dmg if i64(r['fields'].get('2')) == -1))
p('治疗值 Top：%s' % collections.Counter(i64(r['fields'].get('2')) for r in heal).most_common(8))
p('伤害值 Top：%s' % collections.Counter(i64(r['fields'].get('2')) for r in dmg).most_common(8))
# 反伤候选
c = collections.Counter(i64(r['fields'].get('2')) for r in dmg)
p('同时出现在 BOSS 与玩家身上的伤害值：')
plset = set(PLAYERS)
bossvals = collections.Counter(i64(r['fields'].get('2')) for r in dmg if r['fields'].get('1') == BOSS)
for v, n in c.most_common(40):
    if v in bossvals and any(r['fields'].get('1') in plset for r in dmg if i64(r['fields'].get('2')) == v):
        p('   %-14d 总%d次 BOSS%d次' % (v, n, bossvals[v]))

# ---------- 5. 属性面板 ----------
p('\n===== 5. 属性面板 NumericType 清单 =====')
attr = collections.defaultdict(lambda: collections.defaultdict(set))
for r in rows:
    if r['op'] == 20170:
        u = r['fields'].get('1')
        for e in (r['fields'].get('2') or []):
            m = re.match(r'<([0-9A-Fa-f]+)B\s+([0-9a-fA-F]+)>', e)
            if not m:
                continue
            b = bytes.fromhex(m.group(2)); i = 0
            d = {}
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
            if 1 in d:
                attr[u][d[1]].add(round(d.get(2) or 0, 4))
for u in sorted(attr, key=lambda x: (x != BOSS, x)):
    ks = sorted(attr[u])
    p('  单位 %s (%s) 共 %d 项: %s' % (u, 'BOSS' if u == BOSS else '玩家', len(ks), ks))
p('单体 20169 推的 NumericType：')
single = collections.defaultdict(collections.Counter)
for r in rows:
    if r['op'] == 20169:
        single[r['fields'].get('1')][r['fields'].get('2')] += 1
for u in sorted(single, key=lambda x: (x != BOSS, x)):
    p('  %s : %s' % (u, dict(single[u].most_common())))

w.close()
print('OK', OUT)
