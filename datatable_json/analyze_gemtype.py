import json

with open('MaterialBase.json', 'r', encoding='utf-8') as f:
    content = f.read()
    data = json.loads(content)

# 收集所有GemKey对应的信息
gem_info = {}
for item in data:
    if isinstance(item, list) and len(item) == 2:
        item_id, item_data = item
        if 'GemKey' in item_data:
            gem_key = item_data['GemKey']
            if gem_key not in gem_info:
                gem_info[gem_key] = {
                    'key': gem_key,
                    'type': item_data.get('GemType', 0),
                    'name': item_data.get('Name', '').split('·')[0]
                }

# 按GemType分组
type_groups = {}
for gem_key, info in gem_info.items():
    gem_type = info['type']
    if gem_type not in type_groups:
        type_groups[gem_type] = []
    type_groups[gem_type].append(info)

# 输出结果
for gem_type in sorted(type_groups.keys()):
    gems = type_groups[gem_type]
    print(f"\nGemType={gem_type} ({len(gems)}种):")
    for gem in sorted(gems, key=lambda x: x['key']):
        print(f"  GemKey={gem['key']:2d}  {gem['name']}")
