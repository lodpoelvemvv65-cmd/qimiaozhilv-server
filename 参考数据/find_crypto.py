# -*- coding: utf-8 -*-
import sys, re
import dnfile
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

pat = re.compile(r'(encrypt|decrypt|crypt|secret|remote|server|gate|address|host|connect|login|account|key|token|voucher|passwd|password)', re.I)

for dll in ['Unity.Model.dll', 'Unity.ModelView.dll']:
    p = r'<原版客户端>\梦幻奇遇记_Data\Managed' + '\\' + dll
    pe = dnfile.dnPE(p); md = pe.net.mdtables
    print(f'===== {dll} =====')
    for td in md.TypeDef.rows:
        nm = str(td.TypeName)
        if pat.search(nm):
            print(f'  {td.TypeNamespace}.{nm}')
