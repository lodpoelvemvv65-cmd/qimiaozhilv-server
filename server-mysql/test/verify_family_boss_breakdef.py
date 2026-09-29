# -*- coding: utf-8 -*-
"""真实 TCP 实测：5 个真实账号打家族 5 阶 BOSS，逐人测「普通攻击能不能破防」。

背景（见 `文档/34-家族BOSS战斗数值与统计口径分析-2026-09-17.md` §9）：

  * 伤害公式（`battle_skill_runtime.go:1203 damageOutcome`，线上公式）是
    **先扣防御、再乘技能倍率**：

        base = atk - 目标防御;  if base < 1 { base = 1 }
        damage = base * 技能倍率 / 100

    所以攻击力只要低于防御，**任何技能倍率都只能打 1 点**。

  * 家族 5 阶 BOSS（`MonsterBase 50005`）PhyDef = 2,097,080 / SpiDef = 1,689,811，
    而实测队伍的攻击力只有 80 万 ~ 160 万 → 3 个物理/精神职业每次命中都被压成 1 点。

  * 角色的攻击 = `CharacterGrowth[AttributeType*10+职业族] × 对应六维`：

        | 职业族   | 攻击键 | 系数       | 吃哪一维 |
        |----------|--------|------------|----------|
        | 军官(1)  | 71     | `Str: 23`  | 力量     |
        | 运动员(2)| 72     | `Str: 38`  | 力量     |
        | 护士(3)  | 83     | `Wim: 28`  | 智慧     |
        | 超人(4)  | 84     | `Wim: 38`  | 智慧     |

    （军官系数 23 已由线上抓包 Lv1 PhyAtk=23 = 23×力量验证，见
     `online_capture_panel_stats_test.go`。护士/超人的攻击键 73/74 在表里是**空行**，
     两者都是法系，走 83/84 的智慧。）

  * 六维里 `str_add/quk_add/spi_add/wim_add/phy_add/sta_add` 是**手动加点**
    （`battle.go:onAddPoint` 20253，顺序固定为 力量/智慧/体质/耐力/敏捷/精神）。
    实测发现 a12312303（护士）把 104001 点加到了**力量**上 —— 护士的攻击走智慧，
    这 10 万点等于全废；而 a12312304（超人）加对了智慧，是全场唯一破得了防的人。

本脚本做什么：

  1. **夹具**（登录前写库，会话在 login 时读一次）：
     * `current_hp = -1` —— 服务端约定的「未初始化 = 满血」。上次实测 3903/3905
       被打死过，`onStartFamilyBossFight` 会先查 `battleHP() <= 0` 拒绝开战。
     * `family_boss_keys = 20` —— 家族 BOSS 每场扣 1 把钥匙
       （`Gameplay.dungeon.family_boss_daily_keys` 默认 2/天），当天已用完会报
       「家族 Boss 钥匙不足」。
     * **清空 `player_auto_skills`** —— 自动战斗的候选表是
       `autoBattleSkillIDsLocked()` = 「已配置的自动技能（去重、已学）+ 普攻兜底」，
       而 `battle_round.go:appendAutomaticCasts` 里**只要有一个技能释放成功就 `break`**。
       实测护士的自动表 `[310301, 310401, ...]` 全是**无伤害的 buff**，
       于是本回合的普攻被顶掉、整场零直接伤害；军官/超人配的 110604/410304 则
       长期被 `cooldown` / `not enough MP` 拒绝后回退到普攻。
       清空之后候选表只剩普攻兜底，**每个回合必然放普通攻击**——这正是玩家当时的实际形态。
       （`player_auto_skills` 原内容在脚本开头快照，跑完原样恢复。）
     * 顺带记录 `auto_battle` 原值并保持为 1（走服务端自动战斗，不用脚本手动发 20233）。

  2. **阶段 A（before）**：5 个号按当前加点各打一场 5 阶 BOSS，记录每人对 BOSS 的
     **直接伤害**；
  3. **阶段 B（after）**：用真实 `20253 C2M_AddPoint` 把各自的**剩余未加点数**按职业加到
     伤害属性（军官/运动员→力量，护士/超人→智慧），再打一场，重新记录；
  4. 打印对照表，并断言「加完点后至少有号每击直接伤害 > 1（破防）」。

判据来源（双通道）：

  * **服务端日志** `[combat.damage]`（权威）：`target=2:<bossUnit>`、`source=1:<uid>`、
    `periodic=false` 的行取 `actual` —— 这是**直接伤害**。
    不能只读 20078：`20078` 不带 `periodic` 标志，护士 300001 的中毒跳伤
    （每跳 6000 万）会把「破防没破防」完全盖住。
  * **TCP 帧**：`20078 M2C_BattleSkillRet{UnitId, ChangeHpValue}` 里出现落在 BOSS 单位、
    且 `ChangeHpValue < 0` 的帧 —— 证明这次出手在协议层真的打到了 BOSS。
    BOSS 单位 id 来自 `20144 M2C_SendFamilyBossInfo` 的 tag2（家族 BOSS 不走 20050）。

前置条件（脚本自己检查，不满足会明确报错）：

  * 本地服务端在 `127.0.0.1:7756` 运行，且**重启过**（家族 BOSS 血量是内存态，
    直接改库不生效）；
  * 家族 95 的 BOSS 5 **没有被打死**（`family_boss_states.hp > 0`）。打死过就先重置：

        UPDATE family_boss_states SET hp = max_hp, has_reward = 0, dead_at = 0
          WHERE family_id = 95;
        DELETE FROM family_boss_damage WHERE family_id = 95;

    然后**重启服务端**。

  * 测试账号当前**没有在线客户端**：脚本会用同一账号登录，原会话会收到
    `20328 G2C_ForceOffLine` 且只能心跳。客户端在线时请不要跑。

运行（在 `server-mysql/` 下，服务端已启动）：

    python .\\test\\verify_family_boss_breakdef.py

可用环境变量：

    MHQ_BOSS_WINDOW_SECONDS  每个号的观测窗口秒数（默认 14）
    MHQ_BOSS_SKIP_BEFORE=1   只跑阶段 B（跳过 before 一场，省 BOSS 血量）
    MHQ_BOSS_KEY_BUDGET      每个号补充的家族 BOSS 钥匙数（默认 20）
"""

import os
import pathlib
import re
import shutil
import socket
import struct
import subprocess
import time


# ===================== 本地 MySQL（复用服务端自己的 fallback DSN） =====================

def mysql(sql):
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


# ===================== protobuf 小工具 =====================

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
    if raw is None:
        return None
    raw &= 0xFFFFFFFF
    return raw - 0x100000000 if raw & 0x80000000 else raw


def rpc_of(body):
    return value(body, 90)


HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))

FAMILY_ID = int(os.environ.get("MHQ_TEST_FAMILY_ID", "95"))
BOSS_ID = 5
BOSS_MONSTER_ID = 50005
WINDOW_SECONDS = float(os.environ.get("MHQ_BOSS_WINDOW_SECONDS", "14"))
SKIP_BEFORE = os.environ.get("MHQ_BOSS_SKIP_BEFORE") == "1"
KEY_BUDGET = int(os.environ.get("MHQ_BOSS_KEY_BUDGET", "20"))

# 账号 → (uid, 职业族, 职业名, 加点列名, C2M_AddPoint.PointList 下标)
# PointList 顺序（battle.go:onAddPoint 注释）：力量 / 智慧 / 体质 / 耐力 / 敏捷 / 精神
ACCOUNTS = [
    ("a123123",   2,    1, "军官",   "str_add", 0),
    ("a12312301", 3902, 1, "军官",   "str_add", 0),
    ("a12312302", 3903, 2, "运动员", "str_add", 0),
    ("a12312303", 3904, 3, "护士",   "wim_add", 1),
    ("a12312304", 3905, 4, "超人",   "wim_add", 1),
]
PASSWORD = "b123123"


class Client:
    """带持久缓冲的读取器。

    刻意**不**用「先 settimeout 再 read_exactly(N)」：那样超时时可能已经吃掉帧头的
    一部分字节，字节流错位后读到的全是垃圾。改成把收到的字节全留在 `self.buf`，
    超时只可能发生在**一帧都还没开始**时，因此永远不丢半帧。
    （同 `verify_monster_reflect.py`，那里有完整踩坑记录。）
    """

    def __init__(self):
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.rpc = 0
        self.buf = b""
        self.timeline = []

    def close(self):
        try:
            self.sock.close()
        except OSError:
            pass

    def _fill(self, timeout):
        self.sock.settimeout(timeout)
        chunk = self.sock.recv(65536)
        if not chunk:
            raise ConnectionError("server closed")
        self.buf += chunk

    def recv(self, timeout=5):
        while len(self.buf) < 4:
            self._fill(timeout)
            timeout = 5.0
        length, opcode = struct.unpack("<HH", self.buf[:4])
        total = length + 2          # 线上的 len 字段**包含** 2 字节 opcode
        while len(self.buf) < total:
            self._fill(5.0)
        body = self.buf[4:total]
        self.buf = self.buf[total:]
        self.timeline.append((time.monotonic(), opcode, body))
        return opcode, body

    def drain(self, timeout=0.5):
        while True:
            try:
                self.recv(timeout)
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

    def collect(self, seconds):
        deadline = time.monotonic() + seconds
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return
            try:
                self.recv(max(0.05, remaining))
            except socket.timeout:
                return
            except ConnectionError:
                return


def newest_server_log():
    directory = pathlib.Path(__file__).resolve().parents[1] / "logs/runtime"
    candidates = sorted(directory.glob("local-server-*.log"),
                        key=lambda path: path.stat().st_mtime)
    if not candidates:
        raise RuntimeError("no server log in logs/runtime/")
    return candidates[-1]


DAMAGE_LINE = re.compile(r"\[combat\.damage\]")


def read_from(path, offset):
    """按字节定位读日志尾部（避免多字节字符导致偏移错位）。"""
    with open(path, "rb") as handle:
        handle.seek(offset)
        return handle.read().decode("utf-8", "replace")


def parse_line(line):
    return dict(re.findall(r"(\w+)=([\w:.\-]+)", line))


class PlayerResult:
    __slots__ = ("account", "uid", "job_name", "hits", "max_hit", "total")

    def __init__(self, account, uid, job_name):
        self.account = account
        self.uid = uid
        self.job_name = job_name
        self.hits = 0
        self.max_hit = 0
        self.total = 0


def login(client, account):
    opcode, body = client.call(20008, sf(1, account) + sf(2, PASSWORD))
    assert opcode == 20009 and not value(body, 91), "login failed for %s" % account
    key, gate = value(body, 2), value(body, 3)
    assert key and gate, "no gate voucher for %s" % account
    opcode, body = client.call(20014, vf(1, key) + vf(2, gate))
    assert opcode == 20015 and not value(body, 91), "LoginGate failed for %s" % account
    opcode, body = client.call(20027)
    assert opcode == 20028, "EnterGame failed for %s" % account
    return int(value(body, 1))


def run_one(account, uid, job_name, point_index, log_path, allocate):
    """登录 →（可选按职业加点）→ 开 5 阶 BOSS 战 → 观测窗口 → 统计直接伤害。"""
    client = Client()
    result = PlayerResult(account, uid, job_name)
    try:
        player_id = login(client, account)
        assert player_id == uid, "账号 %s 的 uid 是 %d，脚本写的是 %d" % (account, player_id, uid)

        if allocate:
            pending = int(mysql("SELECT char_point FROM players WHERE id = %d" % uid)[0][0])
            if pending > 0:
                point_list = [0] * 6
                point_list[point_index] = pending
                # C2M_AddPoint.PointList 是 **tag=1 的重复字段**（`protocol_dump.txt`
                # 里 `[1] PointList`，Hotfix 侧是字段级 List），不是 tag 1..6。
                # 写成 tag 1..6 时 protobuf 只会解析出 PointList=[第一个值]，
                # 服务端 `len(req.PointList) != 6` 判定"属性点参数错误"，
                # 而响应恒 Error=0 ⇒ **静默失败**（实测踩过：加完 30 万点，攻击力没变）。
                # 顺序为 力量/智慧/体质/耐力/敏捷/精神（battle.go:onAddPoint）。
                body = b"".join(vf(1, count) for count in point_list)
                opcode, response = client.call(20253, body)
                assert opcode == 20254, "AddPoint response opcode = %d" % opcode
                print("      %-10s 加点 %d 点 → PointList 下标 %d（%s 的伤害属性）"
                      % (account, pending, point_index, job_name))
            else:
                print("      %-10s 没有剩余属性点" % account)

        offset = pathlib.Path(log_path).stat().st_size
        opcode, body = client.call(20142, vf(1, BOSS_ID))
        assert opcode == 20143, "开 5 阶 BOSS 战的响应 opcode = %d" % opcode
        assert not value(body, 92, 2), "开 5 阶 BOSS 战被拒: %r" % (
            value(body, 92, 2).decode("utf-8", "replace"),)
        client.collect(1.5)

        units = {value(b, 2) for _, o, b in client.timeline if o == 20144 and value(b, 2)}
        assert units, "没有收到 20144 家族 BOSS 单位"
        boss_unit = sorted(units)[0]

        # 观测窗口：服务端自动战斗常驻（夹具已把自动技能表清空 → 每回合必然普攻）。
        # connIdleTimeout = 30s，窗口内按原生客户端节奏每 2 秒发一次 20333 心跳。
        deadline = time.monotonic() + WINDOW_SECONDS
        while time.monotonic() < deadline:
            client.call(20333)
            client.collect(min(2.0, max(0.05, deadline - time.monotonic())))

        # 判据 1（服务端日志，权威）：直接伤害 = periodic=false 且打到我方 uid → BOSS 单位
        for line in read_from(log_path, offset).splitlines():
            if not DAMAGE_LINE.search(line):
                continue
            item = parse_line(line)
            if item.get("target") != "2:%d" % boss_unit:
                continue
            if item.get("source") != "1:%d" % uid:
                continue
            if item.get("periodic") != "false":
                continue
            actual = int(item.get("actual", "0"))
            if actual <= 0:
                continue
            result.hits += 1
            result.total += actual
            result.max_hit = max(result.max_hit, actual)

        # 判据 2（协议）：BOSS 单位身上出现过掉血帧
        boss_frames = [signed32(value(b, 2)) for _, o, b in client.timeline
                       if o == 20078 and value(b, 1) == boss_unit]
        boss_frames = [d for d in boss_frames if d is not None and d < 0]

        print("      %-10s %-8s 直接命中 %-3d 次, 最大 %-10d 合计 %-12d (协议侧掉血帧 %d)"
              % (account, job_name, result.hits, result.max_hit, result.total,
                 len(boss_frames)))
        assert boss_frames or result.hits, (
            "%s 在本场对 BOSS 单位 %d 既没有日志里的直接伤害，也没有 20078 掉血帧。"
            "检查夹具：player_auto_skills 是否真的清空了（没清空会被 buff 技能顶掉普攻）。"
            % (account, boss_unit))
        return result
    finally:
        client.close()


def snapshot_auto_skills(ids):
    rows = mysql("SELECT player_id, position, skill_id FROM player_auto_skills "
                 "WHERE player_id IN (%s) ORDER BY player_id, position" % ids)
    return [(int(row[0]), int(row[1]), int(row[2])) for row in rows]


def restore_auto_skills(ids, snapshot):
    mysql("DELETE FROM player_auto_skills WHERE player_id IN (%s)" % ids)
    if not snapshot:
        return
    values = ", ".join("(%d, %d, %d)" % row for row in snapshot)
    mysql("INSERT INTO player_auto_skills (player_id, position, skill_id) VALUES %s" % values)


def main():
    log_path = newest_server_log()
    ids = ",".join(str(uid) for _account, uid, _f, _j, _c, _i in ACCOUNTS)
    print("服务端日志: %s" % log_path)

    rows = mysql("SELECT hp, max_hp FROM family_boss_states "
                 "WHERE family_id = %d AND boss_id = %d" % (FAMILY_ID, BOSS_ID))
    assert rows, "family %d boss %d 没有状态行" % (FAMILY_ID, BOSS_ID)
    hp, max_hp = int(rows[0][0]), int(rows[0][1])
    assert hp > 0, (
        "家族 %d 的 %d 阶 BOSS 已经被打死（hp=0）。先重置再跑：\n"
        "  UPDATE family_boss_states SET hp = max_hp, has_reward = 0, dead_at = 0 "
        "WHERE family_id = %d;\n"
        "  DELETE FROM family_boss_damage WHERE family_id = %d;\n"
        "  然后**重启服务端**（家族 BOSS 血量是内存态，改库不生效）。"
        % (FAMILY_ID, BOSS_ID, FAMILY_ID, FAMILY_ID))
    print("5 阶 BOSS 当前血量 %d / %d" % (hp, max_hp))
    print("BOSS 防御: PhyDef 2,097,080 / SpiDef 1,689,811（MonsterBase %d）"
          % BOSS_MONSTER_ID)
    print()

    auto_snapshot = snapshot_auto_skills(ids)
    print("已快照 player_auto_skills：%d 行" % len(auto_snapshot))

    before, after = {}, {}
    try:
        # 夹具：清空自动技能表 → autoBattleSkillIDsLocked() 只剩普攻兜底
        mysql("DELETE FROM player_auto_skills WHERE player_id IN (%s)" % ids)

        if not SKIP_BEFORE:
            print("\n[阶段 A] 按当前加点实测（每号 %.0fs 窗口，服务端自动战斗打普攻）"
                  % WINDOW_SECONDS)
            for account, uid, _f, job_name, _c, point_index in ACCOUNTS:
                mysql("UPDATE players SET current_hp = -1, current_mp = -1, auto_battle = 1 "
                      "WHERE id = %d" % uid)
                mysql("UPDATE player_activity SET family_boss_keys = %d WHERE player_id = %d"
                      % (KEY_BUDGET, uid))
                before[uid] = run_one(account, uid, job_name, point_index, log_path, False)

        print("\n[阶段 B] 把剩余属性点按职业加到伤害属性后实测")
        for account, uid, _f, job_name, _c, point_index in ACCOUNTS:
            mysql("UPDATE players SET current_hp = -1, current_mp = -1, auto_battle = 1 "
                  "WHERE id = %d" % uid)
            mysql("UPDATE player_activity SET family_boss_keys = %d WHERE player_id = %d"
                  % (KEY_BUDGET, uid))
            after[uid] = run_one(account, uid, job_name, point_index, log_path, True)
    finally:
        restore_auto_skills(ids, auto_snapshot)
        print("\n已恢复 player_auto_skills（%d 行）" % len(auto_snapshot))

    print()
    print("=" * 84)
    print("%-10s %-8s | %-26s | %s" % ("账号", "职业", "加点前 最大/合计", "加点后 最大/合计"))
    print("-" * 84)
    broke = []
    for account, uid, _f, job_name, _c, _i in ACCOUNTS:
        old = before.get(uid)
        new = after[uid]
        old_text = "（跳过）" if old is None else "%d / %d" % (old.max_hit, old.total)
        print("%-10s %-8s | %-26s | %d / %d"
              % (account, job_name, old_text, new.max_hit, new.total))
        if new.max_hit > 1:
            broke.append((account, job_name, new.max_hit))
    print("=" * 84)

    assert broke, (
        "加点后没有任何一个号的单次直接伤害 > 1 —— 仍然破不了防。"
        "检查 CharacterGrowth 系数与加点属性是否匹配（军官/运动员→力量、护士/超人→智慧）。")
    print("PASS 家族 5 阶 BOSS 破防实测（加点后）：")
    for account, job_name, max_hit in broke:
        print("    %-10s %-8s 单次直接伤害 %d（> 1，已破防）" % (account, job_name, max_hit))

    remaining = mysql("SELECT hp FROM family_boss_states WHERE family_id = %d AND boss_id = %d"
                      % (FAMILY_ID, BOSS_ID))
    print("5 阶 BOSS 剩余血量 %s / %d" % (remaining[0][0], max_hp))


if __name__ == "__main__":
    main()
