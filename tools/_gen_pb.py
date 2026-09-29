# -*- coding: utf-8 -*-
"""用 Python subprocess 调用 protoc 重新生成 protocol.pb.go（规避控制台编码问题）"""
import os
import subprocess, sys
from pathlib import Path
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

root = Path(__file__).resolve().parent.parent
protoc = str(root / 'tools' / 'protoc' / 'bin' / 'protoc.exe')
tool_bin = str(root / 'tools' / 'protoc' / 'bin')
srv = str(root / 'server-mysql')

cmd = [
    protoc,
    '--go_out=.',
    '--go_opt=module=mhqserver',
    '-I', '.',
    'protocol.proto',
]
env = os.environ.copy()
env['PATH'] = tool_bin + os.pathsep + env.get('PATH', '')
r = subprocess.run(cmd, cwd=srv, env=env, capture_output=True)
print('rc=', r.returncode)
print('stdout=', r.stdout.decode('utf-8', 'replace'))
print('stderr=', r.stderr.decode('utf-8', 'replace'))
