"""一次性探针：带几率触发的持续伤害（流血/灼烧）在真实 TCP 上落地后怎么跳。

跑法（在 server-mysql 下）：
    python test/_probe_periodic_dot.py <skillId> <level> [layer] [casts]
例：
    python test/_probe_periodic_dot.py 210603 3 10 20   # 运动员 开大脚 Lv3 流血 4%/14s
    python test/_probe_periodic_dot.py 410601 3 10 30   # 超人   热眼射线 Lv3 灼烧 3%/10s

不是回归脚本：这两个技能的持续伤害都挂在「命中后 N% 几率」后面（210603 = 20%、
410601 = 14%），固定次数内不一定触发；要确定性验证请跑 go test（见
skill_periodic_damage_test.go、battle_periodic_tick_test.go）。

前置：把角色放到沙滩第 <layer> 层（MainStory 10006XX），层数进度存在
player_kill_counts[2147400001]（beachHighestLayerKey），这里直接预置成 layer-1，
再逐层发 20031 走到目标层，不用真打过去。第 10 层的怪（MonsterBase 10010，2000 血）
扛得住子弹，流血/灼烧有时间跳完。

判据：子弹是「施法后 +1000ms 的一次伤害」；持续伤害每跳是目标最大生命值的 pct%，
每 4 秒一次、首跳延后一个间隔、跳数 = floor(时长/4)，末次施法之后仍然继续出现。
"""

import json
import re
import secrets
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from verify_skill_option_fidelity import (  # noqa: E402
    HOST, Client, field, fields, login, mysql, provision, vf,
)

if len(sys.argv) < 3:
    raise SystemExit(__doc__)
SKILL = int(sys.argv[1])
LEVEL = int(sys.argv[2])
LAYER = int(sys.argv[3]) if len(sys.argv) > 3 else 10
# 触发几率决定「一次都没触发」的概率：210603 是 20%（9 次 13%、20 次 1.2%），
# 410601 是 14%（20 次 4.9%、30 次 1.1%）。
CASTS = int(sys.argv[4]) if len(sys.argv) > 4 else 20
# 职业族：技能号首位 1=军官、2=运动员、3=护士、4=超人；客户端 skin JobId 取该族第二个
# （jobTypeOf：1/2=军官、3/4=运动员、5/6=护士、7/8=超人），所以用 2、4、6、8。
JOB = (SKILL // 100000) * 2
# 每跳 = 目标最大生命值的百分之几，取自该 modifier 事件 8 伤害节点自己的
# damageCalculate_Target（calculateType 2 = 最大生命值）或技能描述。这里用来核对实测值。
TICK_PERCENT = {(210603, 3): 4.0, (410601, 3): 3.0}
# 各职业的基础攻击技能：军官 100001、运动员 200001、护士 300001、超人 400001。
BASIC = (JOB // 2) * 100000 + 1
BEACH_FIRST = 1000601
BEACH_TOP = 1000600 + LAYER
BEACH_HIGHEST_KEY = 2147400001
# MonsterBase 里沙滩各层怪的最大生命值，用来挑一个扛得住子弹又有时间跳完持续伤害的目标。
MONSTER_HP = {10001: 80, 10002: 160, 10003: 240, 10004: 320, 10005: 400,
              10006: 480, 10007: 560, 10008: 640, 10009: 720, 10010: 2000}
COOLDOWN_SECONDS = 12.0
WATCHED = (20075, 20078, 20080, 20169, 20170)


def signed(value):
    return value - (1 << 64) if value >= (1 << 63) else value


def battle_monsters(client, mark):
    """(unitId, MonsterId) —— 20050 M2C_MainStoryMonsterInfo 的 MonsterUnitInfo 列表。"""
    battle = None
    for _, op, raw in client.frames[mark:]:
        if op == 20050:
            battle = raw
            break
    if battle is None:
        return []
    return [(field(sub, 1), field(sub, 2)) for tag, wire, sub in fields(battle)
            if tag == 1 and wire == 2]


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    if HOST not in ("127.0.0.1", "localhost", "::1"):
        raise RuntimeError("只跑本地服务端")
    account = "dot%s" % secrets.token_hex(5)
    role = "DT" + account[-6:]
    quoted = "'%s'" % account
    percent = TICK_PERCENT.get((SKILL, LEVEL))
    client = None
    try:
        provision(account, role)
        time.sleep(0.3)
        player_id = int(mysql("SELECT p.id FROM players p JOIN accounts a ON p.account_id=a.id "
                              "WHERE a.account=%s" % quoted)[0][0])
        # 血量给足，免得怪把测试角色打死；层数进度预置到 layer-1 层。
        mysql("UPDATE players SET level=1,job_id=%d,skin_id=%d,current_hp=100000,current_mp=-1,"
              "phy_add=0,wim_add=0 WHERE id=%d;"
              "DELETE FROM player_skills WHERE player_id=%d;"
              "INSERT INTO player_skills(player_id,skill_id,level,sort_order) VALUES"
              "(%d,%d,1,0),(%d,%d,%d,1);"
              "REPLACE INTO player_kill_counts(player_id,monster_id,kill_count) VALUES(%d,%d,%d)"
              % (JOB, JOB, player_id, player_id,
                 player_id, BASIC, player_id, SKILL, LEVEL,
                 player_id, BEACH_HIGHEST_KEY, LAYER - 1))
        client = login(account)
        assert client.player_id == player_id
        client.call(20229, 20230, vf(1, 0) + vf(2, SKILL))
        time.sleep(0.5)
        client.call(20031, 20032, vf(1, BEACH_FIRST))
        client.send(20030)
        time.sleep(0.8)
        for target in range(BEACH_FIRST + 1, BEACH_TOP + 1):
            client.call(20031, 20032, vf(1, target))
            time.sleep(0.5)
        print("skill=%d lv%d job=%d walked to map %d" % (SKILL, LEVEL, JOB, BEACH_TOP), flush=True)

        mark = len(client.frames)
        client.call(20048, 20049, vf(1, 0))
        client.wait(lambda: any(op == 20050 for _, op, _ in client.frames[mark:]))
        roster = battle_monsters(client, mark)
        print("roster:", roster, flush=True)
        by_id = {}
        for unit_id, monster_id in roster:
            by_id.setdefault(monster_id, unit_id)
        target_monster = max(by_id, key=lambda mid: MONSTER_HP.get(mid, 0))
        unit = by_id[target_monster]
        client.send(20030)
        client.call(20073, 20074, vf(1, unit))
        print("target unit=%d monsterId=%d maxHp=%s 期望每跳=%s"
              % (unit, target_monster, MONSTER_HP.get(target_monster),
                 "?" if percent is None else percent * MONSTER_HP.get(target_monster, 0) / 100),
              flush=True)

        mark = len(client.frames)
        hits = []

        def collect():
            frames = [(when, signed(field(raw, 2))) for when, op, raw in client.frames[mark:]
                      if op == 20078 and field(raw, 1) == unit]
            for when, damage in frames[len(hits):]:
                hits.append((when, damage))
                print("  t=%+7.3f damage=%+d" % (when - casts[0], damage), flush=True)

        casts = []
        for attempt in range(CASTS):
            before = len(client.frames)
            client.call(20233, 20234, vf(1, 0))
            casts.append(time.monotonic())
            released = [field(raw, 2) for _, op, raw in client.frames[before:]
                        if op == 20075 and field(raw, 1) == player_id]
            time.sleep(1.6)
            collect()
            print("cast %d released=%s" % (attempt + 1, released),
                  "| hits so far=%d" % len(hits), flush=True)
            if not released:
                # 技能冷却中/目标已死，等一个冷却再试，避免把空转发成假证据。
                time.sleep(COOLDOWN_SECONDS)
            else:
                time.sleep(COOLDOWN_SECONDS - 1.6)
        print("waiting 17s after the last cast (no more casts, so any further hit is a tick)",
              flush=True)
        while time.monotonic() < casts[-1] + 17:
            time.sleep(0.3)
            collect()

        last = casts[-1]
        print("monster 20078 (距末次施法秒, 伤害):",
              [(round(when - last, 3), damage) for when, damage in hits], flush=True)
        capture = Path(__file__).resolve().parents[2] / "参考数据/抓包归档" / (
            "probe-periodic-dot-%d-%s.jsonl" % (SKILL, time.strftime("%Y%m%d-%H%M%S")))
        with capture.open("w", encoding="utf-8") as handle:
            for when, op, raw in client.frames[mark:]:
                if op in WATCHED:
                    handle.write(json.dumps({"t": when, "opcode": op, "body_hex": raw.hex()},
                                            ensure_ascii=False) + "\n")
        print("archive:", capture, flush=True)
    finally:
        if client is not None:
            client.close()
        time.sleep(0.3)
        assert re.fullmatch(r"dot[0-9a-f]+", account), account
        mysql("DELETE p FROM players p JOIN accounts a ON p.account_id=a.id "
              "WHERE a.account IN (%s);DELETE FROM accounts WHERE account IN (%s)"
              % (quoted, quoted))


if __name__ == "__main__":
    main()
