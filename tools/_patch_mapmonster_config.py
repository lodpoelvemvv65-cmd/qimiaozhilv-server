# -*- coding: utf-8 -*-
"""扩展客户端 MapMonsterConfig：为每个需要的怪物 PrefabId 补/更新一行，使字段怪
能显示与战斗怪一致的线上模型、中文名、真实等级。

背景（子代理反汇编确认）：
- 客户端 M2C_CreateMapMonster(20320) 按 ConfigId 查 MapMonsterConfigCategory
  取 PrefabId/Level/Desc → Sys_Prefab 建模型 + HUD 显示 (Level, Desc)。
- 原版表只有 17 行；战斗怪（MonsterBase.PrefabId）用到 201-284/4071-4121 等。
- 本脚本：ConfigId = 10000 + PrefabId 补行；Desc = MonsterBase 中文 NickName
  （字段怪 HUD 显示中文名而非 Monster01），Level = 该怪真实等级。
- 幂等：已存在的行也会【更新】Desc/Level（首次补丁是英文名/Level=100）。

用法：python tools/_patch_mapmonster_config.py
"""
import sys, os, shutil, base64, zlib, json, re
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
import UnityPy

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASE = os.path.join(ROOT, 'client-test', '梦幻奇遇记_Data', 'StreamingAssets', 'yoo')
SRC_BUNDLE = os.path.join(BASE, '_dec', 'aa0', '87c13c7e72546d4e4e0960392e451498.bundle')
BUNDLE_NAME = '87c13c7e72546d4e4e0960392e451498.bundle'
DT = os.path.join(ROOT, 'datatable_json')

def parse_lenient(s):
    try:
        return json.loads(s)
    except Exception:
        return json.loads(re.sub(r',(\s*[}\]])', r'\1', s))

def load_table(name):
    raw = open(os.path.join(DT, name + '.json'), 'rb').read().decode('utf-8', 'replace')
    return parse_lenient(raw)

def main():
    # PrefabId → (中文名, Level)：MonsterBase 中该 PrefabId 的最小 _id 怪
    prefab_info = {}
    for _k, row in load_table('MonsterBase'):
        pid = row.get('PrefabId')
        mid = row.get('_id')
        if not pid or not mid:
            continue
        cur = prefab_info.get(pid)
        if cur is None or mid < cur[0]:
            prefab_info[pid] = (mid, row.get('NickName') or '怪物', row.get('Level') or 1)
    # BossBase 的 MonsterId 也在 MonsterBase 里（10255-10279 → PrefabId 4071-4103）✅

    env = UnityPy.load(SRC_BUNDLE)
    target = None
    for obj in env.objects:
        if obj.type.name == 'TextAsset':
            d = obj.read()
            if d.m_Name == 'MapMonsterConfig':
                target = obj
                break
    if target is None:
        raise SystemExit('MapMonsterConfig TextAsset not found in %s' % SRC_BUNDLE)
    raw = target.read().m_Script
    if isinstance(raw, str):
        raw = raw.encode('utf-8')
    txt = zlib.decompress(base64.b64decode(raw.decode('utf-8').strip())).decode('utf-8', errors='replace')
    rows = parse_lenient(txt)
    existing = {r[0]: r[1] for r in rows}
    added = updated = 0
    for pid, (mid, nick, level) in sorted(prefab_info.items()):
        cfg = 10000 + pid
        if cfg in existing:
            row = existing[cfg]
            # 更新中文名/真实等级（旧补丁是英文名/Level=100）
            if row.get('Desc') != nick or row.get('Level') != level:
                row['Desc'] = nick
                row['Level'] = level
                row['Name'] = 'M%03d' % pid
                updated += 1
            continue
        rows.append([cfg, {
            "_id": cfg, "Name": 'M%03d' % pid, "Desc": nick,
            "PrefabId": pid, "Level": level, "Dropasubset": 0, "X": 0, "Y": -1.2,
        }])
        added += 1
    if added == 0 and updated == 0:
        print('all rows already up-to-date, nothing to change')
        return
    new_txt = '[\r\n' + ',\r\n'.join(
        '[%d, %s]' % (k, json.dumps(v, ensure_ascii=False, separators=(',', ':')))
        for k, v in rows) + '\r\n]'
    b64 = base64.b64encode(zlib.compress(new_txt.encode('utf-8'))).decode('ascii')
    d = target.read()
    d.m_Script = b64
    d.save()
    for fitem in env.files.values():
        fitem.mark_changed()
    env.save(pack='lz4', out_path=os.path.join(ROOT, 'server'))
    out = os.path.join(ROOT, 'server', BUNDLE_NAME)
    print('bundle edited ->', out, os.path.getsize(out), 'rows:', len(rows), 'added:', added, 'updated:', updated)
    for sub in ('aa0', '_dec\\aa0'):
        dst = os.path.join(BASE, sub, BUNDLE_NAME)
        if not os.path.exists(dst + '.bak2'):
            shutil.copy2(dst, dst + '.bak2')
        shutil.copy2(out, dst)
        print('replaced', dst)
    print('manifest loadmethod: run tools/_patch_datatable.py if not already plaintext')

if __name__ == '__main__':
    main()
