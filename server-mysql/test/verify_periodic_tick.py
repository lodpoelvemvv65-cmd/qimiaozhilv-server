"""本地真实 TCP：周期效果（持续治疗/持续伤害）的生效节拍核对。

从 server-mysql 运行 python test/verify_periodic_tick.py。

覆盖：
  310501 护士「持续治疗」—— 技能 desc 是「给3-5个友方单体添加1%【持续治疗】，
  持续14秒」，客户端表里 modifier 31050111 continueTime=14s、thinkInterval 为空、
  事件 8 节点也没有任何节拍字段。所以节拍只能来自线上抓包实测：参考数据/抓包归档/
  online-skill-effects-20260906.pcapng 中同一次施法的持续治疗在 +4.00s/+8.00s/
  +12.00s 各跳一次，+16.00s 的第四跳没有出现，即 4.00 秒一跳、首跳延后一个间隔、
  跳数 = floor(14/4) = 3。

判据：
  1. 施加帧 20080 Add 的 Time=14000ms，且落在施法者自己身上。
  2. 之后恰好 3 次对施法者的回血 20078（正值），间隔各约 4.00 秒。
  3. 第 3 跳之后再等 4 秒（越过 14 秒持续期）不应出现第 4 跳。

只使用本次新注册的临时账号，结束时删除账号与角色。
"""

import json
import secrets
import sys
import threading
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from verify_skill_option_fidelity import (  # noqa: E402
    BATTLE_SCENE, HOST, Client, field, fields, login, mysql, provision, sf, vf,
)

SERVER = Path(__file__).resolve().parents[1]
HOT_SKILL = 310501
HOT_MODIFIER = 31050111
BASIC_SKILL = 300001
# 客户端 JobId 1..8 是具体皮肤，相邻两个共用职业族 (jobId+1)/2（见 trans.go jobTypeOf）：
# 1/2=军官、3/4=运动员、5/6=护士、7/8=超人。护士要用 5 或 6；用 3 会落到运动员职业，
# 20229 会以「不能放置该技能」拒绝 310501。
JOB = 6
DURATION_MS = 14000
TICK_SECONDS = 4.0
TOLERANCE_SECONDS = 0.7
# 目标血量压到很低，回血不会被上限截断（上限由岗位和等级决定，这里不去猜）。
START_HP = 100


def signed(value):
    """protobuf int 是 varint；负数按 64 位补码编码成 10 字节。"""
    return value - (1 << 64) if value >= (1 << 63) else value


def player_hp_frames(client, mark, player_id):
    """(时间, 血量变化) —— 施法者自己的 20078。"""
    return [(when, signed(field(raw, 2)))
            for when, op, raw in client.frames[mark:]
            if op == 20078 and field(raw, 1) == player_id]


def state_frames(client, mark, player_id, change):
    return [(when, raw) for when, op, raw in client.frames[mark:]
            if op == 20080 and field(raw, 1) == HOT_MODIFIER
            and field(raw, 2) == player_id and field(raw, 6) == change]


def run_case(recorder=None):
    account = "dot%s" % secrets.token_hex(5)
    role = "PT" + account[-6:]
    quoted = "'%s'" % account
    client = None
    observations = []
    try:
        provision(account, role, recorder)
        time.sleep(0.3)
        player_id = int(mysql("SELECT p.id FROM players p JOIN accounts a ON p.account_id=a.id "
                              "WHERE a.account=%s" % quoted)[0][0])
        mysql("UPDATE players SET level=1,job_id=%d,skin_id=%d,current_hp=%d,current_mp=-1,"
              "phy_add=0,wim_add=0 WHERE id=%d;"
              "DELETE FROM player_skills WHERE player_id=%d;"
              "INSERT INTO player_skills(player_id,skill_id,level,sort_order) VALUES"
              "(%d,%d,1,0),(%d,%d,1,1)"
              % (JOB, JOB, START_HP, player_id, player_id,
                 player_id, BASIC_SKILL, player_id, HOT_SKILL))
        client = login(account, recorder)
        assert client.player_id == player_id, (client.player_id, player_id)
        client.call(20229, 20230, vf(1, 0) + vf(2, HOT_SKILL))
        time.sleep(0.5)
        client.call(20031, 20032, vf(1, BATTLE_SCENE))
        client.send(20030)
        time.sleep(1.0)
        mark = len(client.frames)
        client.call(20048, 20049, vf(1, 0))
        client.wait(lambda: any(op == 20050 for _, op, _ in client.frames[mark:]))
        battle = next(raw for _, op, raw in client.frames[mark:] if op == 20050)
        monsters = [field(sub, 1) for tag, wire, sub in fields(battle)
                    if tag == 1 and wire == 2]
        assert monsters, "20050 里没有怪物"
        client.send(20030)
        client.call(20073, 20074, vf(1, monsters[0]))

        mark = len(client.frames)
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            client.call(20233, 20234, vf(1, 0))
            if any(op == 20075 and field(raw, 1) == player_id and field(raw, 2) == HOT_SKILL
                   for _, op, raw in client.frames[mark:]):
                break
            time.sleep(0.4)
        else:
            raise AssertionError("310501 始终没有释放")

        # 施加帧是节拍锚点：节拍从落地的这一刻起算，不是从施法那一刻起算。
        deadline = time.monotonic() + 4
        while time.monotonic() < deadline:
            added = state_frames(client, mark, player_id, 1)
            if added:
                break
            time.sleep(0.05)
        else:
            raise AssertionError("31050111 没有落到施法者身上（20080 Add 缺失）")
        anchor, add_frame = added[0]
        assert field(add_frame, 7) == DURATION_MS, \
            "31050111 持续时间 %dms，期望 %dms" % (field(add_frame, 7), DURATION_MS)

        # 观察 3 跳 + 一个间隔：14 秒持续期走完，第 4 跳不该出现。
        observe_until = anchor + TICK_SECONDS * 4 + TOLERANCE_SECONDS + 1.5
        while time.monotonic() < observe_until:
            time.sleep(0.2)

        frames = [(when, change) for when, change in player_hp_frames(client, mark, player_id)
                  if when >= anchor]
        heals = [(when - anchor, change) for when, change in frames if change > 0]
        observations.extend({"offset_s": round(offset, 3), "change": change}
                            for offset, change in heals)
        if len(heals) != 3:
            raise AssertionError(
                "持续治疗跳了 %d 次，期望 floor(14/4)=3 次；实际 (相对施加帧秒数, 回血量)=%s；"
                "施法者全部 20078=%s"
                % (len(heals), observations,
                   [(round(when - anchor, 3), change) for when, change in frames]))

        offsets = [offset for offset, _ in heals]
        for index, offset in enumerate(offsets, start=1):
            expected = TICK_SECONDS * index
            if abs(offset - expected) > TOLERANCE_SECONDS:
                raise AssertionError(
                    "第 %d 跳在 +%.3fs，期望 +%.2fs（容差 ±%.1fs）" %
                    (index, offset, expected, TOLERANCE_SECONDS))
        print("PASS 持续治疗 31050111: duration=%dms ticks=%s"
              % (DURATION_MS, [round(offset, 3) for offset in offsets]), flush=True)
        return {"player_id": player_id, "monsters": monsters, "duration_ms": DURATION_MS,
                "ticks": observations}
    finally:
        if client is not None:
            client.close()
        time.sleep(0.3)
        import re
        assert re.fullmatch(r"dot[0-9a-f]+", account), account
        mysql("DELETE p FROM players p JOIN accounts a ON p.account_id=a.id "
              "WHERE a.account IN (%s);DELETE FROM accounts WHERE account IN (%s)"
              % (quoted, quoted))


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    if HOST not in ("127.0.0.1", "localhost", "::1"):
        raise RuntimeError("这个协议实测只跑本地服务端")
    capture_dir = SERVER.parent / "参考数据/抓包归档"
    capture_dir.mkdir(parents=True, exist_ok=True)
    capture = capture_dir / ("periodic-tick-local-%s.jsonl" % time.strftime("%Y%m%d-%H%M%S"))
    handle = capture.open("w", encoding="utf-8")
    lock = threading.Lock()
    watched = {20075, 20078, 20080, 20169, 20170}

    def recorder(label, opcode, body):
        if opcode not in watched:
            return
        with lock:
            handle.write(json.dumps({"time": time.time(), "client": label, "opcode": opcode,
                                     "body_hex": body.hex()}, ensure_ascii=False) + "\n")
            handle.flush()

    summary = {"host": HOST, "capture": str(capture)}
    try:
        summary["config_revision"] = int(mysql("SELECT MAX(revision) FROM game_config_revisions")[0][0])
        print("MySQL revision=%d" % summary["config_revision"], flush=True)
        summary["case"] = run_case(recorder)
        summary["passed"] = True
        print("PASS periodic tick live TCP: 3 ticks at the captured 4s cadence; "
              "temporary account removed", flush=True)
    finally:
        handle.close()
        output = SERVER / "logs/runtime" / ("periodic-tick-%s.json"
                                            % time.strftime("%Y%m%d-%H%M%S"))
        output.write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
        print("Summary:", output, flush=True)
        print("Frame archive:", capture, flush=True)


if __name__ == "__main__":
    main()
