# -*- coding: utf-8 -*-
"""尝试解码 datatable 表：base64 -> 各种压缩解压 -> 输出可读文本。"""
import sys, os, base64, zlib, gzip, lzma, bz2, json
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

def try_decode(raw: bytes):
    # 1) base64?
    try:
        b = base64.b64decode(raw)
    except Exception:
        return None, None
    results = {}
    # zlib
    try:
        results['zlib'] = zlib.decompress(b)
    except Exception:
        pass
    # gzip
    try:
        results['gzip'] = gzip.decompress(b)
    except Exception:
        pass
    # lzma
    try:
        results['lzma'] = lzma.decompress(b)
    except Exception:
        pass
    # bz2
    try:
        results['bz2'] = bz2.decompress(b)
    except Exception:
        pass
    # raw json (b 可能本身就是 JSON)
    try:
        json.loads(b)
        results['json'] = b
    except Exception:
        pass
    return b, results

path = sys.argv[1]
raw = open(path, 'rb').read()
print('=== file:', os.path.basename(path), 'size', len(raw))

# 文件本身是 base64 文本?
raw_txt = raw.decode('utf-8', errors='ignore').strip()
b, results = try_decode(raw_txt.encode())
if b is None:
    # 也许整个文件就是压缩后的二进制
    for name, fn in [('zlib', zlib.decompress), ('gzip', gzip.decompress), ('lzma', lzma.decompress), ('bz2', bz2.decompress)]:
        try:
            results[name] = fn(raw)
        except Exception:
            pass

if not results:
    print('无法解码，直接打印前 200 字节')
    print(repr(raw[:200]))
else:
    for name, data in results.items():
        print('--- via', name, 'len', len(data))
        txt = data.decode('utf-8', errors='replace')
        print(txt[:1500])
        print('...')
