# -*- coding: utf-8 -*-
"""cap39 独立复核 · 第 1 部分：帧/流/C2S/未挖掘 opcode
不复用 _capture39_*.py 的任何逻辑，独立从 jsonl 重算。"""
import io, json, struct, collections, re

BASE = './'
JL = BASE + '_work/pcap_frames/online-control-resist-20260918.jsonl'
PROTO = BASE + '参考数据/protocol_dump.txt'
OUT = BASE + '_work/cap39_recheck_A.txt'
w = io.open(OUT, 'w', encoding='utf-8')
def p(*a):
    w.write(' '.join(str(x) for x in a) + '\n')

rows = [json.loads(l) for l in io.open(JL, encoding='utf-8') if l.strip()]
p('== 总帧 %d ==' % len(rows))

# 协议名表
names = {}
for l in io.open(PROTO, encoding='utf-8', errors='replace'):
    m = re.match(r'## \[(\d+)\] (\S+)', l.strip())
    if m:
        names[int(m.group(1))] = m.group(2)
def nm(op):
    return names.get(op, '???')

S2C = [r for r in rows if r['stream'].startswith('101.43.6.155')]
C2S = [r for r in rows if not r['stream'].startswith('101.43.6.155')]
p('S2C %d  C2S %d' % (len(S2C), len(C2S)))

p('\n== 各流帧数 / t 跨度 ==')
by = collections.defaultdict(list)
for r in rows:
    by[r['stream']].append(r['t'])
for s in sorted(by):
    ts = by[s]
    p('  %-42s n=%-6d t0=%.3f t1=%.3f' % (s, len(ts), min(ts), max(ts)))

p('\n== 双向 opcode 计数 ==')
c_s = collections.Counter(r['op'] for r in S2C)
c_c = collections.Counter(r['op'] for r in C2S)
for op in sorted(set(list(c_s) + list(c_c))):
    p('  %-6d %-32s S2C=%-6d C2S=%-6d' % (op, nm(op), c_s.get(op, 0), c_c.get(op, 0)))

p('\n== C2S 业务消息（非 Ping）==')
p('  Ping(20333) = %d' % c_c.get(20333, 0))
for op, n in c_c.most_common():
    if op != 20333:
        p('  %-6d %-32s %d' % (op, nm(op), n))
p('  合计(含 Ping) = %d' % len(C2S))

p('\n== 出现 0 次的战斗相关 opcode（线上未使用）==')
for op in range(20070, 20095):
    if c_s.get(op, 0) + c_c.get(op, 0) == 0:
        p('  %-6d %s' % (op, nm(op)))

p('\n== doc43 §7 列举的 C2S 与实际对照 ==')
for op, n in c_c.most_common():
    if op != 20333:
        p('  实际 %-6d %-30s %d 帧' % (op, nm(op), n))
p('  doc43 写的是 C2M_GetCharacter 20036 —— 协议表里 20036 = %s' % nm(20036))

# 20083 死亡
p('\n== 20083 M2C_UnitDead 全部时刻（每流）==')
dead = collections.defaultdict(list)
for r in rows:
    if r['op'] == 20083:
        dead[r['stream']].append((r['t'], r['fields'].get('1')))
for s in sorted(dead):
    p('  %-42s %s' % (s, dead[s]))

p('\n== 20055 M2C_BattleDefeat ==')
for r in [x for x in rows if x['op'] == 20055]:
    p('  t=%.2f %s f=%s' % (r['t'], r['stream'], r['fields']))

w.close()
print('OK', OUT)
