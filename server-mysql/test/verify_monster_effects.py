# -*- coding: utf-8 -*-
"""Real TCP regression: monster periodic effects (燃烧/中毒/流血) land on the
player and keep ticking on the 4-second cadence, independent of the monster's
own cast cadence.

为什么单独写一个：`verify_monster_damage.py` 用的是试炼小怪（技能组 10001，
只有 500001 一个纯直伤技能），所以它只能断言"有伤害"，看不到任何周期效果。

**为什么打家族 BOSS 而不是主线怪**：主线地图 `20031` 有逐层推进的硬门禁
（实测 `reject non-beach main-story transition ... 无法进入该层`），
从海滩走到"※狂暴野猪王"那层不现实；而**家族 5 阶 BOSS（怪物 50005）的技能组
10030 有 4/7 的槽是 DOT**（500010 中毒 26 秒 / 500021 流血 11 秒 /
500023 燃烧 14 秒），是唯一可由单账号直接开打的 DOT 怪物。
新角色开家族没有任何等级/道具门槛（`onCrateFamily` 只校验名字）。

判据（全部来自真实 TCP 帧，不读服务端日志）：
  1. 怪物有施法（20075）与落在玩家身上的伤害（20078 负数 HP 变化）；
  2. **周期跳伤的签名 = 同一个伤害值以约 4 秒的间隔反复出现**
     （运营配置 `Gameplay.combat.effect_tick_interval_ms`）。
     不要用"到达前若干秒内没有怪物施法"来判定：怪物每 ~6 秒一波、DOT 每 4 秒
     一跳，两者必然周期性重叠，那条启发式会把挨着施法的跳漏掉，只剩 8 秒的假
     间隔（实测踩过）。按伤害值分组则很干净 —— 直伤每次数值不同（暴击与非暴击、
     技能不同），而同一层 DOT 的每跳数值恒等。

三个客户端侧的坑也已修掉（都踩过）：
  * `settimeout` + 分段读：超时可能吃掉半个帧头导致字节流错位、后续读到的全是
    垃圾 → 改成持久缓冲，超时只可能发生在"一帧都还没开始"时；
  * 只分析 `collect_timed` 的返回值会周期性地丢帧（心跳 `call()` 期间到达的帧
    进了 inbox）→ 统一从 `client.timeline` 取；
  * 服务端 `tcp_srv.go:18 connIdleTimeout = 30s`：观察窗口超过 30 秒必须照原生
    客户端每 2 秒发一次 `20333 C2G_Ping`，否则一定被断开。

运行（从 server-mysql 目录，服务端已启动）：
    python .\\test\\verify_monster_effects.py
可用 `MHQ_MONSTER_EFFECT_SECONDS` 调整观察窗口（默认 75 秒），
`MHQ_TEST_HOST` / `MHQ_TEST_PORT` 指向别的服务端。
"""

import os
import pathlib
import re
import shutil
import socket
import struct
import subprocess
import time


def mysql(sql):
    """Run one statement on the local development MySQL, reusing the server's own
    fallback DSN so no password is copied into this script."""
    dsn = os.environ.get("MHQ_TEST_MYSQL_DSN") or os.environ.get("MHQ_MYSQL_DSN")
    if not dsn:
        source = (pathlib.Path(__file__).resolve().parents[1]
                  / "internal/mysqlschema/schema.go").read_text(encoding="utf-8")
        match = re.search(r'fallbackDSN\s*=\s*"([^"]+)"', source)
        if not match:
            raise RuntimeError("Set MHQ_TEST_MYSQL_DSN for the local database")
        dsn = match[1]
    match = re.fullmatch(r"([^:]+):?(.*?)@tcp\(([^:]+):(\d+)\)/([^?]+)(?:\?.*)?", dsn)
    if not match:
        raise RuntimeError("Expected a TCP MySQL DSN")
    user, password, host, port, database = match.groups()
    if host not in ("127.0.0.1", "localhost", "::1"):
        raise RuntimeError("This regression only touches a local MySQL")
    executable = os.environ.get("MHQ_TEST_MYSQL_EXE") or shutil.which("mysql")
    if not executable:
        raise RuntimeError("mysql client not found")
    environment = os.environ.copy()
    environment["MYSQL_PWD"] = password
    result = subprocess.run(
        [executable, "--batch", "--raw", "--skip-column-names",
         "--default-character-set=utf8mb4", "-h", host, "-P", port,
         "-u", user, database, "-e", sql],
        env=environment, capture_output=True, timeout=15,
    )
    if result.returncode:
        raise RuntimeError(result.stderr.decode("utf-8", "replace"))
    return [line.split("\t") for line in result.stdout.decode("utf-8").splitlines() if line]


def varint(value):
    out = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        out.append(byte | (0x80 if value else 0))
        if not value:
            return bytes(out)


def vf(field, value):
    return varint(field << 3) + varint(value)


def sf(field, value):
    raw = value.encode("utf-8")
    return varint((field << 3) | 2) + varint(len(raw)) + raw


def frame(opcode, body):
    return struct.pack("<HH", len(body) + 2, opcode) + body


def fields(body):
    result = []
    offset = 0
    while offset < len(body):
        tag = 0
        shift = 0
        while True:
            byte = body[offset]
            offset += 1
            tag |= (byte & 0x7F) << shift
            shift += 7
            if not byte & 0x80:
                break
        field, wire = tag >> 3, tag & 7
        if wire == 0:
            val = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                val |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((field, wire, val))
        elif wire == 2:
            length = 0
            shift = 0
            while True:
                byte = body[offset]
                offset += 1
                length |= (byte & 0x7F) << shift
                shift += 7
                if not byte & 0x80:
                    break
            result.append((field, wire, body[offset:offset + length]))
            offset += length
        elif wire == 5:
            result.append((field, wire, struct.unpack("<f", body[offset:offset + 4])[0]))
            offset += 4
        else:
            raise AssertionError("unsupported wire type %d" % wire)
    return result


def value(body, field, wire=0):
    return next((item for number, item_wire, item in fields(body)
                 if number == field and item_wire == wire), None)


def signed32(raw):
    """Decode a protobuf int32 (negatives arrive as 10-byte varints)."""
    if raw is None:
        return None
    raw &= 0xFFFFFFFF
    return raw - 0x100000000 if raw & 0x80000000 else raw


def rpc_of(body):
    return value(body, 90)


HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))

FAMILY_BOSS_ID = 5              # 5 阶 BOSS → 怪物 50005 → 技能组 10030（4/7 槽是 DOT）
OBSERVE_SECONDS = int(os.environ.get("MHQ_MONSTER_EFFECT_SECONDS", "75"))
TICK_INTERVAL = 4.0             # Gameplay.combat.effect_tick_interval_ms 默认 4000
# 家族 BOSS 物攻 240 万，而新角色只有 13000 血，会被一击打死、
# 根本活不到 DOT 的第一跳（+4s）。
#
# 不能直接改 `players.current_hp`：`loadData` 会把超过上限的血夹回 maxHP
# （`playerdata.go:323`：`if s.hp >= 0 && s.hp > s.playerMaxHp()`）。
# 所以改成抬高 **maxHP 本身**：maxHP = `characterGrowthValue(ss, 1)`，
# 而 `CharacterGrowth` 行 11 = `{"Phy": 1000}`（HP = 1000 × 体质），
# `playerPhy() = naturalPrimaryStat() + phy_add + 装备/星魂/转生加成`。
# 给 `players.phy_add` 注入一个大值即可把血量顶到足以撑过整个观察窗口。
# 这是测试夹具，不是绕过校验：服务端本来就允许通过加点提升体质。
TEST_PHY_ADD = int(os.environ.get("MHQ_MONSTER_EFFECT_PHY_ADD", "500000"))


class Client:
    """带持久缓冲的读取器 + 单一时间线。

    两个都是踩过坑才加的：
      * 直接 `settimeout` + `read_exactly(N)`：超时时可能已经吃掉帧头的一部分
        字节，字节流就错位了（症状：窗口跑到一半报 WinError 10053 断线，
        服务端日志 `broadcast OffLine`、战斗被 `battle_ended_or_replaced` 取消）。
        改成把收到的字节全部留在 `self.buf`，超时只可能发生在"一帧都还没开始"时。
      * 线上的 len 字段**包含**那 2 字节 opcode，整帧 = length + 2 字节。
      * 只分析 `collect_timed` 的返回值会漏帧 —— `call()` 等 RPC 响应时会把途中的帧
        收进 inbox，所以统一记到 `self.timeline`（带到达时刻）。
    """

    def __init__(self):
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.rpc = 0
        self.inbox = []
        self.buf = b""
        self.timeline = []

    def close(self):
        self.sock.close()

    def _fill(self, timeout):
        self.sock.settimeout(timeout)
        chunk = self.sock.recv(65536)
        if not chunk:
            raise ConnectionError("server closed")
        self.buf += chunk

    def recv(self, timeout=5):
        while len(self.buf) < 4:
            self._fill(timeout)
            timeout = 5.0      # 本帧已经开始，后半个帧必须等满
        length, opcode = struct.unpack("<HH", self.buf[:4])
        total = length + 2
        while len(self.buf) < total:
            self._fill(5.0)
        body = self.buf[4:total]
        self.buf = self.buf[total:]
        self.timeline.append((time.monotonic(), opcode, body))
        return opcode, body

    def drain(self, timeout=0.6):
        while True:
            try:
                self.inbox.append(self.recv(timeout))
            except (socket.timeout, ConnectionError):
                return

    def call(self, opcode, body=b""):
        self.rpc += 1
        rpc = self.rpc
        self.sock.sendall(frame(opcode, body + vf(90, rpc)))
        while True:
            response_opcode, response = self.recv()
            if rpc_of(response) == rpc:
                self.drain()
                return response_opcode, response
            self.inbox.append((response_opcode, response))

    def collect_timed(self, seconds):
        captured = []
        deadline = time.monotonic() + seconds
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return captured
            try:
                opcode, body = self.recv(max(0.05, remaining))
            except socket.timeout:
                return captured
            except ConnectionError:
                return captured
            captured.append((time.monotonic(), opcode, body))


def monster_units(inbox):
    """Collect monster unit ids.

    试炼/主线战斗用 `20050`（单位列表）下发怪物；**家族 BOSS 走的是
    `20144 M2C_SendFamilyBossInfo{BossId=1, UnitId=2, Hp=3}`**，没有 20050，
    所以这里两种都要认。
    """
    units = []
    for opcode, body in inbox:
        if opcode == 20050:
            for field, wire, raw in fields(body):
                if field == 1 and wire == 2:
                    units.append(value(raw, 1))
        elif opcode == 20144:
            units.append(value(body, 2))
    return {unit for unit in units if unit}


def main():
    client = Client()
    account = "monstereffect%d" % time.time_ns()
    try:
        opcode, body = client.call(20010, sf(1, account) + sf(2, "123456"))
        assert opcode == 20011 and not value(body, 91), "register failed"
        key, gate = value(body, 2), value(body, 3)
        assert client.call(20014, vf(1, key) + vf(2, gate))[0] == 20015, "login failed"
        create_opcode, create_body = client.call(20016, vf(2, 1) + sf(3, "e" + account[-6:]))
        assert create_opcode == 20017 and not value(create_body, 91), "create role failed"
        player_id = int(mysql(
            "SELECT id FROM players WHERE account_id = "
            "(SELECT id FROM accounts WHERE account = '%s')" % account)[0][0])

        # 撑血：`onEnterGame` 会重新读 players 行（`store.FirstPlayer`），
        # 所以在 20027 之前注入 `phy_add` 就能生效，不必重连。
        mysql("UPDATE players SET phy_add = %d, current_hp = -1 WHERE id = %d"
              % (TEST_PHY_ADD, player_id))
        rows = mysql("SELECT phy_add, current_hp FROM players WHERE id = %d" % player_id)
        assert rows and int(rows[0][0]) == TEST_PHY_ADD and int(rows[0][1]) == -1, \
            "phy_add injection failed"

        enter_opcode, enter_body = client.call(20027)
        assert enter_opcode == 20028, "enter game failed"
        assert value(enter_body, 1) == player_id, "entered as a different player"
        print("EnterGame: player=%d phy_add=%d" % (player_id, TEST_PHY_ADD))

        # 新角色快捷栏为空，先用原生 drop-skill RPC 装上普攻。
        slot_opcode, slot_body = client.call(20229, vf(1, 0) + vf(2, 100001))
        assert slot_opcode == 20230 and not value(slot_body, 92, 2), "failed to install basic attack"

        # 家族 5 阶 BOSS。onCrateFamily 只校验名字，新角色可直接开。
        family_name = "mf" + str(time.time_ns())[-10:]
        family_opcode, family_body = client.call(20123, sf(1, family_name))
        assert family_opcode == 20124 and not value(family_body, 92, 2), \
            "create family failed: %r" % (value(family_body, 92, 2),)
        print("CreateFamily: %s" % family_name)

        boss_opcode, boss_body = client.call(20142, vf(1, FAMILY_BOSS_ID))
        message = value(boss_body, 92, 2)
        assert boss_opcode == 20143 and not message, \
            "start family boss %d rejected: %r" % (FAMILY_BOSS_ID, message)
        print("StartFamilyBoss: boss=%d" % FAMILY_BOSS_ID)
        client.collect_timed(1.5)

        monsters = monster_units(client.inbox)
        assert monsters, "battle did not contain monster units"
        print("Battle monsters: %s" % sorted(monsters))

        print("Observing %ds of monster casts ..." % OBSERVE_SECONDS)
        # 服务端 `tcp_srv.go:18 connIdleTimeout = 30s`：30 秒没有任何请求就断开。
        # 原生客户端每 2 秒发一次 `20333 C2G_Ping`，这里照做。
        window_start = time.monotonic()
        observe_deadline = window_start + OBSERVE_SECONDS
        while time.monotonic() < observe_deadline:
            client.call(20333)
            client.collect_timed(2.0)
        # 从 timeline 取**全部**帧：心跳 call() 期间到达的帧会进 inbox 而不是
        # collect_timed 的返回值，只分析后者会周期性丢帧（实测表现为 DOT 跳伤
        # 出现 7.91s 的假间隔）。
        frames = [(at, opcode, body) for at, opcode, body in client.timeline if at >= window_start]

        casts = []          # (t, skillId, source)
        cast_visuals = []   # (t, source)  20077
        player_hits = []    # (t, delta)   20078 落在玩家身上
        player_states = []  # (t, iconId, type, time, target)  20080 落在玩家身上
        for at, opcode, body in frames:
            if opcode == 20075:
                casts.append((at, value(body, 2), value(body, 1)))
            elif opcode == 20077:
                cast_visuals.append((at, value(body, 1)))
            elif opcode == 20080:
                icon = value(body, 3, 2)
                icon = icon.decode("utf-8", "replace") if isinstance(icon, bytes) else ""
                if value(body, 2) == player_id:
                    player_states.append((at, icon, value(body, 6), value(body, 7)))
            elif opcode == 20078 and value(body, 1) == player_id:
                delta = signed32(value(body, 2))
                if delta is not None and delta < 0:
                    player_hits.append((at, delta))

        print("monster casts (20075) = %d, skills = %s" % (
            len(casts), sorted({skill for _, skill, _ in casts if skill})))
        print("monster cast visuals (20077) = %d" % len(cast_visuals))
        print("player damage frames (20078) = %d" % len(player_hits))
        print("player state frames (20080) = %d" % len(player_states))
        for at, icon, change, duration in player_states:
            print("    %.1fs icon=%-22s change=%s time=%s"
                  % (at - window_start, icon or "(空)", change, duration))
        assert casts, "monster never cast (20075) during the window"
        assert player_hits, "no damage frame landed on the player"

        # 周期跳伤的签名：**同一个伤害值以约 4 秒的间隔反复出现**。
        #
        # 不要用"到达前若干秒内没有怪物施法"来判定 —— 怪物每 ~6 秒一波、DOT 每
        # 4 秒一跳，两者必然周期性重叠，那条启发式会把挨着施法的跳伤漏掉，只剩
        # 8 秒的假间隔（实测踩过）。按伤害值分组则很干净：直伤每次数值不同
        # （暴击/非暴击、技能不同），而同一层 DOT 的每跳数值恒等。
        by_delta = {}
        for at, delta in player_hits:
            by_delta.setdefault(-delta, []).append(at)

        ticking = []
        for amount, times in by_delta.items():
            if len(times) < 2:
                continue
            gaps = [round(times[i] - times[i - 1], 2) for i in range(1, len(times))]
            near = [gap for gap in gaps if abs(gap - TICK_INTERVAL) <= 0.75]
            if near:
                ticking.append((amount, len(times), gaps))
        ticking.sort(key=lambda item: -item[1])

        print("repeating damage values on the ~%.1fs cadence = %d" % (TICK_INTERVAL, len(ticking)))
        for amount, count, gaps in ticking:
            print("    %d x %d damage, gaps=%s" % (count, amount, gaps))

        assert ticking, (
            "在 %ds 内没有观测到任何以 %.1fs 节拍重复的伤害值 —— 怪物 DOT 没有生效。"
            "（技能组 10030 有 4/7 的槽是 DOT；如果一直没抽到可加长 "
            "MHQ_MONSTER_EFFECT_SECONDS。若状态命中判定回归，燃烧/中毒/流血会被"
            "玩家抗性挡死，这里会以『没有任何重复伤害值』失败。）"
            % (OBSERVE_SECONDS, TICK_INTERVAL))

        total_ticks = sum(count for _, count, _ in ticking)
        print("PASS monster periodic effects over TCP: casts=%d skills=%s ticking=%d total-ticks=%d" % (
            len(casts), sorted({skill for _, skill, _ in casts if skill}), len(ticking), total_ticks))
    finally:
        client.close()


if __name__ == "__main__":
    main()
