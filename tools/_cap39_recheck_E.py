# -*- coding: utf-8 -*-
"""cap39 独立复核 · 第 5 部分：DoT/HOT 节拍、0.5s 拍、盾、减益来源、HP 与清零对齐"""
import io, json, struct, collections, re

BASE = './'
JL = BASE + '_work/pcap_frames/online-control-resist-20260918.jsonl'
OUT = BASE + '_work/cap39_recheck_E.txt'
w = io.open(OUT, 'w', encoding='utf-8')
def p(*a):
    w.write(' '.join(str(x) for x in a) + '\n')

rows = [json.loads(l) for l in io.open(JL, encoding='utf-8') if l.strip()]
BS = '101.43.6.155:7757>192.168.x.x:61643'
r1 = [r for r in rows if r['stream'] == BS]
BOSS = 3640454171745994031
PLAYERS = [18937, 22233, 22234, 22235, 22240]

def i64(v):
    return v - 2**64 if isinstance(v, int) and v >= 2**63 else v

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

# ---------- 1. DoT / HOT 节拍 ----------
p('===== 1. DoT / HOT 节拍（doc43 §1 复核）=====')
st = [r for r in r1 if r['op'] == 20080]
apply_t = {}
for r in st:
    apply_t[(r['fields'].get('2'), r['fields'].get('3'))] = apply_t.get((r['fields'].get('2'), r['fields'].get('3')), []) + [r['t']]
for icon in ['bufficon_poisoning', 'bufficon_bleed', 'bufficon_light', 'bufficon_constrea']:
    p('\n-- %s 施加时刻：' % icon)
    for u in PLAYERS:
        ts = apply_t.get((u, icon), [])
        p('   %-8d %s' % (u, ['%.2f' % x for x in ts]))
    # 结合伤害帧读间隔
    dmg = [r for r in r1 if r['op'] == 20078 and i64(r['fields'].get('2')) < 0]
    heal = [r for r in r1 if r['op'] == 20078 and i64(r['fields'].get('2')) > 0]
    src = dmg if icon != 'bufficon_constrea' else heal
    p('   同目标后续帧间隔：')
    for u in PLAYERS:
        for a in apply_t.get((u, icon), []):
            seq = [r for r in src if r['fields'].get('1') == u and a - 0.01 <= r['t'] <= a + 30]
            if len(seq) >= 2:
                ivs = [round(seq[i+1]['t'] - seq[i]['t'], 2) for i in range(len(seq)-1)]
                p('      施加@%.2f -> %s  间隔=%s' % (a, ['%.2f' % x['t'] for x in seq], ivs))

# ---------- 2. 0.5s 拍 ----------
p('\n===== 2. 伤害/治疗帧时间戳的 0.5s 相位（doc39 坑#9 / doc43 §8）=====')
for op, name in ((20078, 'BattleSkillRet'), (20075, 'PlaySkill')):
    frac = collections.Counter()
    for r in r1:
        if r['op'] == op:
            frac[round((r['t'] % 0.5), 2)] += 1
    p('  op %d %s  t%%0.5 分布（前 12）：%s' % (op, name, dict(sorted(frac.items(), key=lambda kv: -kv[1])[:12])))

# ---------- 3. 盾 ----------
p('\n===== 3. 护盾帧完整字段（doc40 §6）=====')
sh = [r for r in r1 if r['op'] == 20080 and r['fields'].get('3') == 'bufficon_shield']
p('帧数 %d，字段键 = %s' % (len(sh), sorted({k for r in sh for k in r['fields']}, key=int)))
for r in sh[:6]:
    p('   t=%.2f %s' % (r['t'], json.dumps(r['fields'], ensure_ascii=False)))
p('  盾帧 Time 分布：%s' % dict(collections.Counter(r['fields'].get('7') for r in sh)))

# ---------- 4. 减益来源：22233 技能序列 ----------
p('\n===== 4. 22233（运动员）的 20075 施法序列 =====')
for r in r1:
    if r['op'] == 20075 and r['fields'].get('1') == 22233:
        p('   t=%.2f skill=%s' % (r['t'], r['fields'].get('2')))
p('\n===== 4b. 全流 20075 时间序（各玩家，只列 100~145s）=====')
for r in sorted([x for x in r1 if x['op'] == 20075], key=lambda x: x['t']):
    if 100 <= r['t'] <= 145:
        p('   t=%.2f %s skill=%s' % (r['t'], r['fields'].get('1'), r['fields'].get('2')))

# ---------- 5. HP 与清零对齐 ----------
p('\n===== 5. BOSS 血量相位（doc43 §2④ 清零触发）=====')
hp = []
for r in r1:
    if r['op'] == 20170 and r['fields'].get('1') == BOSS:
        for d in dec_list(r['fields'].get('2') or []):
            if d.get(1) == 1001:
                v = d.get(2) or 0
                if not hp or hp[-1][1] != v:
                    hp.append((r['t'], v))
for t, v in hp:
    p('   t=%-8.2f hp=%-14g %.2f%%' % (t, v, 100.0 * v / 2e9))

# ---------- 6. Time=500 状态三连 ----------
p('\n===== 6. Time=500 状态三连的完整字段 =====')
for r in r1:
    if r['op'] == 20080 and (r['fields'].get('7') or 0) == 500:
        p('   t=%-8.2f %s' % (r['t'], json.dumps(r['fields'], ensure_ascii=False)))

w.close()
print('OK', OUT)
