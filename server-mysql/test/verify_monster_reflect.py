# -*- coding: utf-8 -*-
"""Real TCP regression: a monster's 反伤 (reflect) buff actually reflects the
player's damage back at the player.

为什么单独写一个：
  * `verify_monster_damage.py` 用的试炼小怪（技能组 10001）没有反伤技能；
  * `verify_monster_effects.py` 验证的是怪物 DOT，而 DOT 目前**被状态命中判定挡死**
    （见 `文档/32-怪物技能实现核对-2026-09-16.md` §八）。
    反伤属于 `buffType=0`（有益），**不走**那条判定，所以必须单独验一次，
    不能因为 DOT 上不去就默认反伤也上不去。

打谁：**家族 5 阶 BOSS**（怪物 50005，技能组 10030 = `[500032, 500021, 500023,
500012, 500010, 500010, 500041]`）。其中 **500032「打我试试」= 给自身 100% 反伤 11 秒**
（`modifier 50003228`，`stateK 101`，`buffType 0`，事件 6 挂 `SkillOption_反伤 param=100`）。
500032 占 1/7 的槽位 ≈ 每次施法 14.3%，BOSS 每 ~6 秒一波，等待窗口给足即可。

判据（全部来自真实 TCP 帧）：
  1. 观测到 `20075` 里 BOSS 施放了 **500032**；
  2. 随后玩家主动出手（`20233` 主界面技能槽 0 = 普攻），产生落在**怪物**身上的 `20078`；
  3. **同一批里**出现一条落在**玩家**身上、且**伤害量与我的输出严格相等**的 `20078`
     （500032 是 100% 反伤，所以两者数值必须相同；实测 `dt=0.000s`）。

两个约束都必须有，都踩过坑：

  * 只看「没有前导施法的孤立掉血帧」→ 会被 BOSS 自己那一波攻击干扰而随机失败；
  * 只看「时间接近」→ 会把 BOSS 同一秒的暴击伤害当成反伤帧
    （实测同一秒出现 `[怪打我 8 / 我打怪 1 / 怪反我 1]`，按时间配对的会选中那条 8）；
  * 出手 → 弹道飞行 → 落地约 2 秒，所以出手窗口必须 > 2s（`ATTACK_LANDING_WINDOW`），
    卡在 2.0 秒会因为十几毫秒的抖动把落地帧漏在窗外（实测落地在 +2.016s）。

运行（从 server-mysql 目录，真实服务端已启动）：
    python .\\test\\verify_monster_reflect.py
可用 `MHQ_REFLECT_WAIT_SECONDS` 调整等待 BOSS 抽到 500032 的上限（默认 200 秒）。
注意服务端 `connIdleTimeout = 30s`，长等待期间必须照原生客户端那样每 2 秒发一次
`20333 C2G_Ping`，否则会被断开。
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

FAMILY_BOSS_ID = 5
REFLECT_SKILL_ID = 500032
WAIT_SECONDS = int(os.environ.get("MHQ_REFLECT_WAIT_SECONDS", "200"))
# 家族 BOSS 物攻 240 万，1 级角色会被一击打死；用加点夹具把血顶起来。
# 抗性走 Spi/Sta（CharacterGrowth 行 261），与这里注入的 Phy 无关。
TEST_PHY_ADD = int(os.environ.get("MHQ_MONSTER_EFFECT_PHY_ADD", "500000"))
# 反伤帧与"我打中 BOSS"那一帧是同批下发的，允许的到达时间差。
REFLECT_PAIR_WINDOW = 0.5
# 我出手 → 弹道飞行 → 伤害落地，线上弹道约 2 秒。窗口必须大于它，
# 2.0 秒会差十几毫秒把落地帧漏在窗外（实测踩过：落地在 +2.016s）。
ATTACK_LANDING_WINDOW = 3.5


class Client:
    """带持久缓冲的读取器。

    这里刻意**不**用「先 settimeout 再 read_exactly(N)」的写法：那样在超时时
    可能已经吃掉了帧头的一部分字节，字节流就错位了，后续读到的全是垃圾
    （实测症状：观察窗口跑到一半客户端报 WinError 10053 断线，服务端日志显示
    `broadcast OffLine`，战斗被 `battle_ended_or_replaced` 取消）。
    改成把收到的字节全部留在 `self.buf` 里，超时只可能发生在**一帧都还没开始**时，
    因此永远不会丢掉半帧。
    """

    def __init__(self):
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.rpc = 0
        self.inbox = []
        self.buf = b""
        # 单一时间线：**每一个**收到的帧都带到达时刻记在这里。
        # 不要只分析 collect_timed 的返回值 —— `call()` 在等 RPC 响应时会把途中的帧
        # 收进 inbox，漏掉它们会让"成对帧"判据随机失败（实测踩过）。
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
        # 帧头：超时只允许发生在还没有任何本帧字节的时候。
        while len(self.buf) < 4:
            self._fill(timeout)
            timeout = 5.0      # 已经收到本帧的开头，后半个帧必须等满
        length, opcode = struct.unpack("<HH", self.buf[:4])
        # 线上的 len 字段**包含**那 2 字节 opcode，所以整帧 = 4 + (length - 2) = length + 2 字节。
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


def monster_units(timeline):
    """家族 BOSS 走 `20144 M2C_SendFamilyBossInfo{UnitId=2}`，不是 20050。"""
    units = []
    for _, opcode, body in timeline:
        if opcode == 20144:
            units.append(value(body, 2))
    return {unit for unit in units if unit}


def main():
    client = Client()
    account = "monsterreflect%d" % time.time_ns()
    try:
        opcode, body = client.call(20010, sf(1, account) + sf(2, "123456"))
        assert opcode == 20011 and not value(body, 91), "register failed"
        key, gate = value(body, 2), value(body, 3)
        assert client.call(20014, vf(1, key) + vf(2, gate))[0] == 20015, "login failed"
        create_opcode, create_body = client.call(20016, vf(2, 1) + sf(3, "r" + account[-6:]))
        assert create_opcode == 20017 and not value(create_body, 91), "create role failed"
        player_id = int(mysql(
            "SELECT id FROM players WHERE account_id = "
            "(SELECT id FROM accounts WHERE account = '%s')" % account)[0][0])
        mysql("UPDATE players SET phy_add = %d, current_hp = -1 WHERE id = %d"
              % (TEST_PHY_ADD, player_id))

        enter_opcode, enter_body = client.call(20027)
        assert enter_opcode == 20028 and value(enter_body, 1) == player_id, "enter game failed"
        print("EnterGame: player=%d phy_add=%d" % (player_id, TEST_PHY_ADD))

        slot_opcode, slot_body = client.call(20229, vf(1, 0) + vf(2, 100001))
        assert slot_opcode == 20230 and not value(slot_body, 92, 2), "failed to install basic attack"

        family_name = "rf" + str(time.time_ns())[-10:]
        family_opcode, family_body = client.call(20123, sf(1, family_name))
        assert family_opcode == 20124 and not value(family_body, 92, 2), \
            "create family failed: %r" % (value(family_body, 92, 2),)

        boss_opcode, boss_body = client.call(20142, vf(1, FAMILY_BOSS_ID))
        assert boss_opcode == 20143 and not value(boss_body, 92, 2), \
            "start family boss rejected: %r" % (value(boss_body, 92, 2),)
        client.collect_timed(1.5)
        monsters = monster_units(client.timeline)
        assert monsters, "battle did not contain the boss unit"
        boss_unit = sorted(monsters)[0]
        print("Boss unit: %d" % boss_unit)

        # Phase A：等 BOSS 抽到 500032「打我试试」。
        print("Waiting up to %ds for the boss to cast %d ..." % (WAIT_SECONDS, REFLECT_SKILL_ID))
        deadline = time.monotonic() + WAIT_SECONDS
        cast_seen = None
        while time.monotonic() < deadline and cast_seen is None:
            # 服务端 `tcp_srv.go:18 connIdleTimeout = 30s`：30 秒没有任何请求就断开。
            # 原生客户端每 2 秒发一次 `20333 C2G_Ping`，这里照做，否则长等待一定被踢
            # （实测症状：等待中途客户端掉线，服务端日志 `broadcast OffLine`）。
            client.call(20333)
            client.collect_timed(2.0)
            for at, opcode, body in client.timeline:
                if opcode == 20075 and value(body, 2) == REFLECT_SKILL_ID:
                    cast_seen = at
                    break
        skills = sorted({value(b, 2) for _, o, b in client.timeline
                         if o == 20075 and value(b, 2)})
        print("Boss casts seen: %s" % skills)
        assert cast_seen is not None, (
            "BOSS 在 %ds 内没有抽到 %d（1/7 槽位）。可用 MHQ_REFLECT_WAIT_SECONDS 加长重跑。"
            % (WAIT_SECONDS, REFLECT_SKILL_ID))

        # BOSS 施放后 +2s 命中、反伤状态随即装上并持续 11 秒。
        # 等 3.5 秒再出手，避开它自己那一波的伤害帧。
        print("Boss cast %d. Waiting for the buff to land, then attacking ..." % REFLECT_SKILL_ID)
        client.collect_timed(3.5)

        # Phase B：玩家主动出手，随后找反伤帧。
        #
        # 反伤的发出位置是 `battle_effects.go applyDamageInternal` 内部：它属于
        # **本次伤害请求**的同一批事件，所以「我打中 BOSS」与「BOSS 反回我」两帧
        # 是**同一时刻一起下发**的（实测服务端日志：两条 `[combat.damage]` 时间戳完全相同，
        # 第一条 `reflected=false`、第二条 `reflected=true`）。
        # 因此判据用「成对出现」而不是"孤立帧"——后者会被 BOSS 自己的攻击波干扰。
        attack_times = []
        window_start = time.monotonic()
        for attempt in range(2):
            attack_times.append(time.monotonic())
            atk_opcode, _ = client.call(20233, vf(1, 0))
            client.collect_timed(3.0)
            if attempt == 0:
                time.sleep(2.0)   # 普攻 CD 5 秒

        # 只分析本次出手之后的帧（timeline 里前面的都还在，所以要卡时间下界）。
        after = [(at, o, b) for at, o, b in client.timeline if at >= window_start]
        damage_on_player = [(at, signed32(value(b, 2)))
                            for at, o, b in after
                            if o == 20078 and value(b, 1) == player_id
                            and signed32(value(b, 2)) is not None and signed32(value(b, 2)) < 0]
        damage_on_boss = [(at, signed32(value(b, 2)))
                          for at, o, b in after
                          if o == 20078 and value(b, 1) == boss_unit
                          and signed32(value(b, 2)) is not None and signed32(value(b, 2)) < 0]

        print("player -> boss damage frames = %d" % len(damage_on_boss))
        print("boss   -> player damage frames = %d" % len(damage_on_player))
        assert damage_on_boss, "玩家的出手没有对 BOSS 造成伤害，无法判断反伤"

        # 反伤签名：在我出手后 2 秒内，出现「我打中 BOSS」与「BOSS 打回我」成对的两帧，
        # 且**两者的伤害量相等**（500032 的反伤是 100%，`treatCalculate`/`param=100`）。
        #
        # 为什么必须比伤害量：BOSS 自己那一波攻击可能恰好落在我出手的同一瞬间
        # （实测日志同一秒三条：[怪打我 8 / 我打怪 1 / 怪反我 1]）。只用"时间接近"
        # 会把那条 8 当成反伤帧，测试就通过得没有说服力。
        reflected = []
        for boss_at, boss_delta in damage_on_boss:
            if not any(atk <= boss_at <= atk + ATTACK_LANDING_WINDOW for atk in attack_times):
                continue          # 不是我这几次出手造成的，跳过
            partner = [(at, d) for at, d in damage_on_player
                       if abs(at - boss_at) <= REFLECT_PAIR_WINDOW and -d == -boss_delta]
            if partner:
                reflected.append((boss_at, boss_delta, partner[0]))
        print("reflected pairs (my hit + an equal-sized hit back, same batch) = %d"
              % len(reflected))
        for boss_at, boss_delta, (player_at, player_delta) in reflected:
            print("    I hit boss for %d  ->  boss reflected %d back to me (dt=%.3fs)"
                  % (-boss_delta, -player_delta, abs(player_at - boss_at)))

        assert reflected, (
            "在 BOSS 持有反伤的窗口内主动出手，没有观测到「我打中 BOSS」与"
            "「BOSS 立刻反回等量伤害」成对出现的两帧 —— 反伤未生效。")
        print("PASS monster reflect over TCP: boss_cast=%d my_hits=%d reflected_pairs=%d"
              % (REFLECT_SKILL_ID, len(damage_on_boss), len(reflected)))
    finally:
        client.close()


if __name__ == "__main__":
    main()
