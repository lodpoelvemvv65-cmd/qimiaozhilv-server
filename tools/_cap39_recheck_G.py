# -*- coding: utf-8 -*-
"""cap39 复核 · 第 7 部分：把抓包里的 (icon, Time) 与 SkillLogicConfig 逐一对照"""
import io, json, collections

BASE = './'
CFG = BASE + '_test_runs/skill-description-audit-20260915/SkillLogicConfig.json'
JL = BASE + '_work/pcap_frames/online-control-resist-20260918.jsonl'
OUT = BASE + '_work/cap39_recheck_G.txt'
w = io.open(OUT, 'w', encoding='utf-8')
def p(*a):
    w.write(' '.join(str(x) for x in a) + '\n')

d = json.load(io.open(CFG, encoding='utf-8'))
sd = d['skillDic']

# 遍历整棵配置树，收集 {icon: {(continueTime, thinkInterval, 技能ID)}}
found = collections.defaultdict(set)

def walk(o, sid=None):
    if isinstance(o, dict):
        for k, v in o.items():
            if k in ('ID', 'SkillId', 'skillId') and isinstance(v, (int, str)):
                sid = v
            walk(v, sid)
    elif isinstance(o, list):
        for v in o:
            walk(v, sid)
    else:
        return

def walk2(o, sid=None, ctime=None):
    if isinstance(o, dict):
        if 'ContinueTime' in o or 'continueTime' in o:
            ctime = o.get('ContinueTime', o.get('continueTime'))
        if 'ID' in o and isinstance(o['ID'], (int, str)):
            sid = o['ID']
        icon = None
        for k, v in o.items():
            if isinstance(v, str) and v.startswith('bufficon'):
                icon = v
            if k in ('EffectID', 'stateKey'):
                pass
        if icon:
            found[icon].add((ctime if isinstance(ctime, (int, float, str, type(None))) else str(ctime), str(sid)))
        for v in o.values():
            walk2(v, sid, ctime)
    elif isinstance(o, list):
        for v in o:
            walk2(v, sid, ctime)

walk2(sd)

p('===== 抓包出现的 (icon, Time) =====')
rows = [json.loads(l) for l in io.open(JL, encoding='utf-8') if l.strip()]
BS = '101.43.6.155:7757>192.168.x.x:61643'
cap = collections.Counter()
for r in rows:
    if r['stream'] == BS and r['op'] == 20080:
        cap[(r['fields'].get('3'), r['fields'].get('7'), r['fields'].get('8'))] += 1
p('%-24s %-8s %-8s %-5s | 配置里的 continueTime / 来源ID' % ('icon', 'Time', 'IsBuff', 'x'))
for (ic, tm, ib), n in sorted(cap.items()):
    cfg = sorted({c for c, s in found.get(ic, set()) if isinstance(c, (int, float))})
    p('%-24s %-8s %-8s %-5d | %s' % (ic, tm, ib, n, cfg))
    if tm is not None:
        hit = [c for c in cfg if abs(c * 1000 - tm) < 1]
        p('%-24s   → 与 Time=%s 匹配的配置 continueTime：%s' % ('', tm, hit if hit else '❌ 没匹配上'))

p('\n===== 配置里每个 icon 的全部 (continueTime, ID) =====')
for ic in sorted(found):
    p('%-24s %s' % (ic, sorted(found[ic])[:14]))

p('\n===== 配置里 continueTime = 0.5 的条目 =====')
def walk3(o, sid=None):
    if isinstance(o, dict):
        if 'ID' in o and isinstance(o['ID'], (int, str)):
            sid = o['ID']
        ct = o.get('ContinueTime', o.get('continueTime'))
        if isinstance(ct, (int, float)) and abs(float(ct) - 0.5) < 1e-9:
            p('   ID=%-12s %s' % (sid, json.dumps({k: v for k, v in o.items() if k in ('ID', 'Icon', 'IconId', 'StateIcon', 'ContinueTime', 'continueTime', 'Desc', 'Describe')}, ensure_ascii=False)))
        for v in o.values():
            walk3(v, sid)
    elif isinstance(o, list):
        for v in o:
            walk3(v, sid)
walk3(sd)

w.close()
print('OK', OUT)
