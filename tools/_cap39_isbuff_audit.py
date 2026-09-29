# -*- coding: utf-8 -*-
"""复核 20080 的 IsBuff：把 SkillLogicConfig 里每个带 bufficon 的 modifier
按服务端 skill_logic.go:710-711 的同一套谓词算一遍，和抓包实测逐图标对照。"""
import io, json, collections

BASE = './'
CFG = BASE + '_test_runs/skill-description-audit-20260915/SkillLogicConfig.json'
JL = BASE + '_work/pcap_frames/online-control-resist-20260918.jsonl'
OUT = BASE + '_work/isbuff_recheck.txt'
w = io.open(OUT, 'w', encoding='utf-8')
def p(*a):
    w.write(' '.join(str(x) for x in a) + '\n')

cfg = json.load(io.open(CFG, encoding='utf-8'))
skills = dict(cfg['skillDic'])

def resolve(valueStruct, level=1):
    """照 SkillOptionParam.Resolve：skillSourcetype=2 用扁平 value；=1 按等级查 values。"""
    if not isinstance(valueStruct, dict):
        return 0.0
    if valueStruct.get('skillSourcetype') == 2:
        return float(valueStruct.get('value') or 0)
    best = None
    for row in valueStruct.get('values') or []:
        if len(row) >= 2 and row[0] <= level:
            best = row[1]
    return float(best if best is not None else (valueStruct.get('value') or 0))

rows = []
for sid, skill in skills.items():
    for entry in (skill.get('modifierDic') or []):
        mod = entry[1] if isinstance(entry, list) and len(entry) > 1 else {}
        icon = mod.get('iconId')
        if not icon or not str(icon).startswith('bufficon'):
            continue
        buffType = int(mod.get('buffType') or 0)
        valueKey = int(mod.get('valueK') or 0)
        value = resolve(mod.get('valueV') or {})
        isBuff = (buffType == 0) and (valueKey != 0) and (value >= 0)
        isDebuff = (buffType == 1) or (valueKey != 0 and value < 0)
        rows.append(dict(skill=sid, mod=mod.get('_id', {}).get('Value') if isinstance(mod.get('_id'), dict) else mod.get('_id'),
                         icon=icon, buffType=buffType, valueK=valueKey, valueV=value,
                         stateK=mod.get('stateK'), attribute=mod.get('attribute'),
                         isBuff=isBuff, isDebuff=isDebuff))

# 抓包实测
cap = collections.defaultdict(set)
for line in io.open(JL, encoding='utf-8'):
    if not line.strip():
        continue
    r = json.loads(line)
    if r['stream'] != '101.43.6.155:7757>192.168.x.x:61643' or r['op'] != 20080:
        continue
    cap[r['fields'].get('3')].add(bool(r['fields'].get('8')) if '8' in r['fields'] else False)

p('%-22s %-9s %-8s %-9s %-8s %-7s | %-9s %-6s | %s' % (
    'icon', 'skill', 'buffType', 'valueK', 'valueV', 'stateK', '公式IsBuff', '抓包', '判定'))
icons = sorted(set(list(cap.keys()) + [x['icon'] for x in rows]))
bad = []
for icon in icons:
    caps = cap.get(icon)
    mine = sorted({x['isBuff'] for x in rows if x['icon'] == icon})
    sample = next((x for x in rows if x['icon'] == icon), None)
    verdict = ''
    if caps is None:
        verdict = '（本包无此图标）'
    elif len(mine) == 1:
        verdict = 'OK' if (list(caps) == [mine[0]]) else '**不一致**'
        if verdict != 'OK':
            bad.append((icon, mine[0], sorted(caps)))
    else:
        verdict = '公式给出多种结果 %s，抓包 %s' % (mine, sorted(caps))
    p('%-22s %-9s %-8s %-9s %-8s %-7s | %-9s %-6s | %s' % (
        icon,
        sample['skill'] if sample else '-',
        sample['buffType'] if sample else '-',
        sample['valueK'] if sample else '-',
        sample['valueV'] if sample else '-',
        sample['stateK'] if sample else '-',
        mine, sorted(caps) if caps else '-', verdict))

p('\n== 结论 ==')
if bad:
    for icon, a, b in bad:
        p('  ❌ %s：公式=%s，抓包=%s' % (icon, a, b))
else:
    p('  ✅ 抓包里出现过的每个 bufficon，公式算出的 IsBuff 都与实测一致')

p('\n== 全部带 bufficon 的 modifier（含本包未出现的）==')
for x in sorted(rows, key=lambda r: (r['icon'], r['skill'])):
    p('  %-22s skill=%-8s mod=%-12s buffType=%-2s valueK=%-3s valueV=%-8s stateK=%-3s attr=%-3s IsBuff=%-5s IsDebuff=%s'
      % (x['icon'], x['skill'], x['mod'], x['buffType'], x['valueK'], x['valueV'],
         x['stateK'], x['attribute'], x['isBuff'], x['isDebuff']))

w.close()
print('OK', OUT)
