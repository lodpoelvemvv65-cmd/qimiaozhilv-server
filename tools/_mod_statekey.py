# -*- coding: utf-8 -*-
"""Read-only: print stateKey / iconId / effectID for selected modifiers.
Usage: python _mod_statekey.py <SkillLogicConfig.json> <modId> [modId...]"""
import json
import sys
import io

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

path = sys.argv[1]
wanted = [int(v) for v in sys.argv[2:]]
raw = json.load(open(path, encoding="utf-8"))


def walk(node, out):
    """Collect every dict that looks like a modifier entry."""
    if isinstance(node, dict):
        if "stateKey" in node or "iconId" in node or "buffType" in node:
            out.append(node)
        for value in node.values():
            walk(value, out)
    elif isinstance(node, list):
        for value in node:
            walk(value, out)


found = {}
for skill_id, skill in (raw.get("skillDic") or {}).items() if isinstance(raw.get("skillDic"), dict) else []:
    pass

# skillDic is a list of [key, value] pairs in this dump.
skills = raw.get("skillDic")
entries = []
if isinstance(skills, list):
    for entry in skills:
        if isinstance(entry, list) and len(entry) == 2:
            entries.append((entry[0], entry[1]))
        elif isinstance(entry, dict) and "key" in entry:
            entries.append((entry["key"], entry["value"]))
elif isinstance(skills, dict):
    entries = list(skills.items())

print("skills parsed:", len(entries))
for key, skill in entries[:1]:
    print("sample skill key=%r type=%s" % (key, type(skill).__name__))
    if isinstance(skill, dict):
        print("  keys:", sorted(skill.keys()))
        md = skill.get("modifierDic")
        print("  modifierDic type=%s" % type(md).__name__)
        print("  modifierDic head:", json.dumps(md, ensure_ascii=False)[:600])

for key, skill in entries:
    modifiers = skill.get("modifierDic") if isinstance(skill, dict) else None
    pairs = []
    if isinstance(modifiers, list):
        for entry in modifiers:
            # Shape: [[{"Value": <id>}, {modifier...}], ...]
            if isinstance(entry, list) and len(entry) == 2:
                head, body = entry
                if isinstance(head, dict):
                    pairs.append((head.get("Value", head.get("value")), body))
                else:
                    pairs.append((head, body))
            elif isinstance(entry, dict) and "key" in entry:
                pairs.append((entry["key"], entry["value"]))
    elif isinstance(modifiers, dict):
        pairs = list(modifiers.items())
    for mod_key, modifier in pairs:
        if not isinstance(modifier, dict):
            continue
        try:
            mod_id = int(mod_key)
        except (TypeError, ValueError):
            continue
        found[mod_id] = (key, modifier)

for mod_id in wanted:
    if mod_id not in found:
        print("%d: <missing>" % mod_id)
        continue
    owner, modifier = found[mod_id]
    continue_time = modifier.get("continueTime") or {}
    value_v = modifier.get("valueV") or {}
    print("%d (skill %s): stateK=%r stateV=%r iconId=%r effectId=%r buffType=%r attribute=%r"
          % (mod_id, owner, modifier.get("stateK"), modifier.get("stateV"),
             modifier.get("iconId"), modifier.get("effectId"), modifier.get("buffType"),
             modifier.get("attribute")))
    print("     continueTime: src=%r value=%r values=%r"
          % (continue_time.get("skillSourcetype"), continue_time.get("value"), continue_time.get("values")))
    print("     valueV: src=%r value=%r values=%r"
          % (value_v.get("skillSourcetype"), value_v.get("value"), value_v.get("values")))
