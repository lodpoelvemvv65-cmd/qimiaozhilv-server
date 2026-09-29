# -*- coding: utf-8 -*-
"""从 protocol.pb.go 提取 opcode 注释 + type 声明，生成 opcodes.go 常量表。

注释格式（每个消息类型上方）:
    // opcode 20010  -> R2C_Regist       (请求消息，箭头后是响应名)
    // opcode 20009                      (响应消息，无箭头)
=>  两种情况下，opcode 都属于下一行的 struct 类型。
"""
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SRC = ROOT / 'server-mysql' / 'protocol' / 'protocol.pb.go'
OUT = ROOT / 'server-mysql' / 'protocol' / 'opcodes.go'

pairs = {}
with open(SRC, encoding='utf-8') as f:
    lines = f.readlines()

i = 0
n = len(lines)
while i < n:
    m = re.search(r'// opcode\s+(\d+)(?:\s+->\s+\w+)?', lines[i])
    if m:
        op = int(m.group(1))
        # 下一行应为 type Xxx struct
        j = i + 1
        while j < n and (not lines[j].strip() or lines[j].lstrip().startswith('//')):
            j += 1
        tm = re.match(r'type (\w+) struct', lines[j].strip())
        if tm:
            # 加 Op 前缀避免与同包 struct 类型名冲突（如 type BagMap struct）
            pairs[op] = 'Op' + tm.group(1)
        i = j
    else:
        i += 1

body = ['// Code generated from protocol.pb.go opcode comments. DO NOT EDIT.',
        '',
        'package protocol',
        '',
        '// 全部 Outer 消息 opcode 常量',
        'const (',
        ]
for op in sorted(pairs):
    body.append(f'\t{pairs[op]} = {op}')
body.append(')')
body.append('')

with open(OUT, 'w', encoding='utf-8') as f:
    f.write('\n'.join(body))
print(f'generated {len(pairs)} opcodes -> {OUT}')
