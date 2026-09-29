# -*- coding: utf-8 -*-
"""从数据表 bundle 提取全部配置 JSON 到 datatable 目录。"""
import sys, os
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
import UnityPy

SRC = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                   'client-test', '梦幻奇遇记_Data', 'StreamingAssets', 'yoo', '_dec', 'aa0',
                   '87c13c7e72546d4e4e0960392e451498.bundle')
OUT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), 'datatable')
os.makedirs(OUT, exist_ok=True)

env = UnityPy.load(SRC)
names = []
for obj in env.objects:
    if obj.type.name == 'TextAsset':
        d = obj.read()
        safe = d.m_Name.replace('/', '_').replace('\\', '_')
        raw = d.m_Script
        if isinstance(raw, str):
            raw = raw.encode('utf-8')
        with open(os.path.join(OUT, safe), 'wb') as f:
            f.write(raw)
        names.append((d.m_Name, len(d.m_Script)))
print(len(names), 'tables extracted')
for n, l in sorted(names):
    print(n, l)
