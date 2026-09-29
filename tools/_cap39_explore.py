# -*- coding: utf-8 -*-
"""cap39 复核 · 第一步：摸清各 opcode 的字段形态（只读探索）"""
import io, json, sys, collections

JL = './_work/pcap_frames/online-control-resist-20260918.jsonl'
OUT = './_work/cap39_explore.txt'

rows = [json.loads(l) for l in io.open(JL, encoding='utf-8') if l.strip()]
w = io.open(OUT, 'w', encoding='utf-8')

def p(*a):
    w.write(' '.join(str(x) for x in a) + '\n')

p('总帧 =', len(rows))
streams = collections.Counter(r['stream'] for r in rows)
for s, c in streams.most_common():
    p('  stream', s, c)

ops = collections.Counter(r['op'] for r in rows)
p('\n== 全部 opcode（帧数）==')
for op, c in ops.most_common():
    p('  %-6d %d' % (op, c))

p('\n== 每个 opcode 的字段样本（取第一条）==')
for op, c in ops.most_common():
    r = next(x for x in rows if x['op'] == op)
    f = r['fields']
    keys = sorted(f.keys(), key=lambda k: int(k))
    desc = []
    for k in keys:
        v = f[k]
        s = repr(v)
        if len(s) > 120:
            s = s[:120] + '...'
        desc.append('%s=%s' % (k, s))
    p('--- op %d (n=%d) stream=%s t=%.2f' % (op, c, r['stream'], r['t']))
    p('    ' + ' | '.join(desc))

# 值域统计：每个 opcode 各字段出现次数与取值个数
p('\n== 每个 opcode 字段出现次数 ==')
present = collections.defaultdict(collections.Counter)
for r in rows:
    for k in r['fields']:
        present[r['op']][k] += 1
for op, c in ops.most_common():
    items = sorted(present[op].items(), key=lambda kv: int(kv[0]))
    p('op %-6d %s' % (op, '  '.join('%s:%d' % (k, v) for k, v in items)))

w.close()
print(open(OUT, encoding='utf-8').read()[:100])
print('written', OUT)
