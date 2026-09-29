# -*- coding: utf-8 -*-
"""批量解压 datatable：base64 -> zlib -> JSON，输出到 datatable_json/ 目录。"""
import sys, os, base64, zlib, json, re
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

SRC = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), 'datatable')
OUT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), 'datatable_json')
os.makedirs(OUT, exist_ok=True)

def parse_lenient(data):
    if isinstance(data, bytes):
        s = data.decode('utf-8')
    else:
        s = data
    try:
        return json.loads(s)
    except Exception:
        pass
    # 移除所有尾随逗号：逗号后紧跟着 } 或 ]（允许中间空白）
    t = re.sub(r',(\s*[}\]])', r'\1', s)
    return json.loads(t)

def decode(path):
    raw = open(path, 'rb').read()
    txt = raw.decode('utf-8', errors='ignore').strip()
    try:
        b = base64.b64decode(txt)
    except Exception:
        return None, None
    try:
        data = zlib.decompress(b)
    except Exception:
        try:
            data = zlib.decompress(b, -zlib.MAX_WBITS)  # raw deflate
        except Exception:
            return b, None
    return data, parse_lenient(data)

for name in sorted(os.listdir(SRC)):
    p = os.path.join(SRC, name)
    if not os.path.isfile(p):
        continue
    data, obj = decode(p)
    if obj is None:
        print('%-24s FAIL (bytes=%s, first=%r)' % (name, len(data) if data else 0, (data or b'')[:80]))
        continue
    with open(os.path.join(OUT, name + '.json'), 'w', encoding='utf-8') as f:
        json.dump(obj, f, ensure_ascii=False, indent=1)
    # 统计条目数
    if isinstance(obj, list):
        n = len(obj)
        keys = list(obj[0].keys()) if obj and isinstance(obj[0], dict) else []
    elif isinstance(obj, dict):
        n = len(obj)
        keys = list(obj.keys())[:8]
    else:
        n = '?'; keys = []
    print('%-24s OK  rows=%s  sample_keys=%s' % (name, n, keys))
