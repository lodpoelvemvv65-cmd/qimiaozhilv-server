# -*- coding: utf-8 -*-
"""队伍里打「单人战斗」时，队友的组队头像能不能即时跟上血蓝实测：真实 TCP + 真实 MySQL。

覆盖 2026-09-14 修的问题：`battleRecipients` 在队伍战斗时返回全队，所以组队打怪本来就是
同步的；但**单人**战斗（battle.party == nil）只返回自己，于是战斗中的掉血和收尾写回的残血
都只推给打怪的人，队友的头像要等下一次重建（换图、进出战斗、重新组队、队伍快照）才对齐。
修复见 `team_vital_sync.go` 的 `scheduleTeamVitalSync`：战斗路径全在 battleMu 里，不能直接
取 teamMu，所以合并成 50ms 一次、在锁外重读权威血蓝再补推。

## 为什么用「家族 BOSS + 没有家族的队友」制造这个场面

`battle.party` 只在战斗参与者 > 1 时才会装上（battle.go finishStartBattleWithPresentation）。
这套服务端把队伍钉得很紧：`changeMapForTeamLeader` 会把队员一起搬到队长所在的图，单人副本
（试炼 10004、决斗 10008）进场时 `leaveTeamForSoloInstance` 直接把人移出队伍，活动场景会把
参与者一起搬走，启动器计划还会拒绝试炼图和战斗中的成员——所以「人在队伍里、队伍还完整、
打出来的却是单人战斗」在正常流程里只剩一条路：

    家族 BOSS 战（20142）不换图，参与者还要再按家族过滤
    （battle.go battleParticipantsForPresentation：presentationFamilyBoss 只留 familyID 相同的成员）。

于是：打怪的人建个家族、队友不入家族，两人组队待在同一个场景里开家族 BOSS——队友被家族过滤
掉，参与者只剩打怪的人，`battle.party == nil`，而队伍本身完好无损。这是一个真·单人战斗：
服务端给它的收件人只有打怪的人自己，队友那格上的每一个血蓝值都只可能来自本次修复的补推。

## 断言

1. 队友在战斗期间收到 fighter 的血量/蓝量变化——修复前一条都不会有；
2. 队友最后一格上的残血/残蓝 = fighter 自己看到的 = MySQL `players.current_hp/current_mp`，
   三处一致（这是被修的那个 bug：队友停在战前值上）；
3. 这些帧不可能是「头像重建」那条老路（team_head_vitals.go）送来的，两条触发方式都排掉：
   窗口里没有 `20162`（补发只由重建排期：`scheduleTeamHeadVitalReload` 的唯一调用点在
   `sendTeamMember` 里），而且补发是「抖动值 → 精确值」成对发的，紧挨着精确值的前一帧
   必然是同一属性的抖动值——收尾残血那一帧前面不是；
4. 收尾失败会把 fighter 送回主城、队友留在原场景，两条老链路都要过 `scenePlayerVisibleTo`
   过滤，队友那格却仍然对——只可能是 `pushTeamVitals`（按队伍成员推，不看场景）；
5. 队友没有收到任何 BOSS 属性推送——证明这确实是单人战斗（battle.party == nil），
   不是全队广播；
6. 战斗结束后队伍依然完整（家族 BOSS 没有把队伍拆开）。

脚本自己注册 2 个一次性账号和角色、跑完自己清理，不碰任何现有角色。
默认连本地 127.0.0.1:7756：

    python .\\test\\verify_solo_battle_team_vitals.py

跑临时实例（例如新二进制先在 7757 验证，不影响 7756 上正在玩的客户端）：

    $env:MHQ_TEST_PORT = '7757'
    python .\\test\\verify_solo_battle_team_vitals.py

`MHQ_TEST_SBV_SETTLE` 覆盖「开战后等多久才出手」的秒数（默认 6.5）。BOSS 两轮之间有
几秒安静，出手早了会撞上 BOSS 的反击轮次、战斗拉长到十几秒——两种时长都要能过，
这是 `MHQ_TEST_SBV_SETTLE=3.0` 覆盖到的分支。
"""

import os
import secrets
import shutil
import socket
import struct
import subprocess
import sys
import threading
import time

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
PASSWORD = os.environ.get("MHQ_TEST_PASSWORD", "Pass123456")

MYSQL_HOST = os.environ.get("MHQ_TEST_MYSQL_HOST", "127.0.0.1")
MYSQL_PORT = os.environ.get("MHQ_TEST_MYSQL_PORT", "3306")
MYSQL_USER = os.environ.get("MHQ_TEST_MYSQL_USER", "root")
MYSQL_PASSWORD = os.environ.get("MHQ_TEST_MYSQL_PASSWORD", "123456")
MYSQL_DATABASE = os.environ.get("MHQ_TEST_MYSQL_DATABASE", "mhq")

TEAM_MEMBER_OPCODE = 20162
SYNC_ATTRIBUTE_OPCODE = 20169
FORM_PLAN_OPCODE = 65000

HP, MAX_HP, MP, MAX_MP = 1001, 1002, 1003, 1004
NUMERIC_NAMES = {HP: "血量", MAX_HP: "血量上限", MP: "蓝量", MAX_MP: "蓝量上限"}

DROP_SKILL_OPCODE = 20229
USE_MAIN_UI_SKILL_OPCODE = 20233
QUIT_BATTLE_OPCODE = 20171
BATTLE_DEFEAT_OPCODE = 20055
CHANGE_MAP_OPCODE = 20033
BASIC_SKILL_ID = 100001
SKILL_SLOT = 0

# 20123 C2M_CrateFamily{Name=1} → 20124 M2C_CrateFamily{Info=3, Error=91, Message=92}
FAMILY_CREATE_OPCODE = 20123
# 20142 C2M_StartFamilyBossFight{BossId=1} → 20143 M2C_StartFamilyBossFight
FAMILY_BOSS_FIGHT_OPCODE = 20142
# 20144 M2C_SendFamilyBossInfo{BossId=1, UnitId=2, Hp=3}
FAMILY_BOSS_INFO_OPCODE = 20144
FAMILY_BOSS_ID = 1

# 等级 1 的角色打不动 3000 万血的家族 BOSS，BOSS 一击却能把它打死；把 fighter 的血先改成 2
# 让这场单人战斗以原生"失败"收尾（残血写回 1，见 recoverPlayerAfterDefeat），收尾时间可控。
# 改血走 MySQL，和 verify_team_head_vitals.py 一样由脚本自己种、自己清理。
FIGHTER_START_HP = 2

# 客户端开局节奏是 5 秒：等场景稳定再出手，服务端才会按原生节拍放出 BOSS 那一轮。
OPENING_SETTLE_SECONDS = float(os.environ.get("MHQ_TEST_SBV_SETTLE", "6.5"))
# BOSS 按自己的动作时钟出手，首击在开战后数秒；给足一轮的时间等它把血打下去。
DAMAGE_WAIT_SECONDS = 25.0
# 原生公共动作间隔 6000ms，第二次出手必须等够，否则会被 CD 静默拒绝（不回包）。
ACTION_COOLDOWN_SECONDS = 6.2
# 补推合并窗口 50ms，收尾还要等落库，留足余量。
SETTLE_SECONDS = 2.5


# ---------------------------------------------------------------- protobuf

def varint(value):
    output = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        output.append(byte | (0x80 if value else 0))
        if not value:
            return bytes(output)


def vf(tag, value):
    return varint(tag << 3) + varint(value)


def bf(tag, value):
    return varint((tag << 3) | 2) + varint(len(value)) + value


def sf(tag, value):
    return bf(tag, value.encode("utf-8"))


def frame(opcode, body):
    return struct.pack("<HH", len(body) + 2, opcode) + body


def recv_exact(conn, length):
    output = bytearray()
    while len(output) < length:
        chunk = conn.recv(length - len(output))
        if not chunk:
            raise ConnectionError("connection closed")
        output.extend(chunk)
    return bytes(output)


def recv_frame(conn):
    total, opcode = struct.unpack("<HH", recv_exact(conn, 4))
    return opcode, recv_exact(conn, total - 2)


def read_varint(body, offset):
    value = 0
    shift = 0
    while offset < len(body):
        byte = body[offset]
        offset += 1
        value |= (byte & 0x7F) << shift
        if not byte & 0x80:
            return value, offset
        shift += 7
    raise AssertionError("truncated varint")


def fields(body):
    output = []
    offset = 0
    while offset < len(body):
        key, offset = read_varint(body, offset)
        tag, wire_type = key >> 3, key & 7
        if wire_type == 0:
            value, offset = read_varint(body, offset)
        elif wire_type == 2:
            length, offset = read_varint(body, offset)
            value = body[offset : offset + length]
            offset += length
        elif wire_type == 5:
            value = body[offset : offset + 4]
            offset += 4
        else:
            raise AssertionError("unsupported protobuf wire type %d" % wire_type)
        output.append((tag, wire_type, value))
    return output


def field(body, tag, default=None):
    for field_tag, _, value in fields(body):
        if field_tag == tag:
            return value
    return default


def as_int(value):
    if value >= 1 << 63:
        value -= 1 << 64
    return value


# ---------------------------------------------------------------- 连接

class Session:
    """一条登录中的 TCP 连接：读线程把收到的帧按顺序堆在 frames 里。"""

    def __init__(self, conn):
        self.conn = conn
        self.frames = []
        # stamps 与 frames 一一对应（同一个读线程按序追加），用来判断一帧是不是
        # 「几十毫秒前刚推过一轮」的重复帧，还是战斗收尾当场推出来的。
        self.stamps = []
        self.closed = False
        # 连接阶段用的 5 秒超时只服务于握手；读线程必须一直阻塞等推送，否则战斗里
        # 安静超过 5 秒（BOSS 两轮之间就是几秒）读线程会超时退出，之后所有推送都收不到。
        conn.settimeout(None)
        self.thread = threading.Thread(target=self._read_loop, daemon=True)
        self.thread.start()

    def _read_loop(self):
        while True:
            try:
                frame_tuple = recv_frame(self.conn)
            except socket.timeout:
                continue
            except Exception:
                return
            self.frames.append(frame_tuple)
            self.stamps.append(time.monotonic())

    def sendall(self, opcode, body):
        """即发即忘：公共动作 CD 内的请求服务端会静默拒绝（不回包），不能等 RPC。"""
        self.conn.sendall(frame(opcode, body))

    def call(self, opcode, body, expected_rpc, timeout=5.0):
        self.conn.sendall(frame(opcode, body))
        deadline = time.time() + timeout
        index = 0
        while time.time() < deadline:
            while index < len(self.frames):
                response_opcode, response_body = self.frames[index]
                index += 1
                if field(response_body, 90) == expected_rpc:
                    return response_opcode, response_body
            time.sleep(0.01)
        raise AssertionError("RPC %d 没有回包" % expected_rpc)

    def close(self):
        if self.closed:
            return
        self.closed = True
        try:
            self.conn.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        self.conn.close()


def connect():
    conn = socket.create_connection((HOST, PORT), timeout=5)
    conn.settimeout(5)
    return Session(conn)


def provision(session, account, role_name, rpc_base):
    """注册账号并建号，和启动器建号走同一条协议链路。"""
    opcode, body = session.call(
        20010, sf(1, account) + sf(2, PASSWORD) + vf(90, rpc_base), rpc_base
    )
    assert opcode == 20011 and field(body, 91, 0) == 0, fields(body)
    opcode, body = session.call(
        20008, sf(1, account) + sf(2, PASSWORD) + vf(90, rpc_base + 1), rpc_base + 1
    )
    assert opcode == 20009 and field(body, 91, 0) == 0, fields(body)
    key, gate = field(body, 2), field(body, 3)
    opcode, body = session.call(
        20014, vf(1, key) + vf(2, gate) + vf(90, rpc_base + 2), rpc_base + 2
    )
    assert opcode == 20015 and field(body, 91, 0) == 0, fields(body)
    opcode, body = session.call(
        20016, vf(2, 1) + sf(3, role_name) + vf(90, rpc_base + 3), rpc_base + 3
    )
    assert opcode == 20017 and field(body, 91, 0) == 0, fields(body)


def enter_game(session, account, rpc_base):
    opcode, body = session.call(
        20008, sf(1, account) + sf(2, PASSWORD) + vf(90, rpc_base), rpc_base
    )
    assert opcode == 20009 and field(body, 91, 0) == 0, fields(body)
    key, gate = field(body, 2), field(body, 3)
    opcode, body = session.call(
        20014, vf(1, key) + vf(2, gate) + vf(90, rpc_base + 1), rpc_base + 1
    )
    assert opcode == 20015 and field(body, 91, 0) == 0, fields(body)
    player_id = as_int(field(body, 1))
    opcode, body = session.call(20027, vf(90, rpc_base + 2), rpc_base + 2)
    assert opcode == 20028 and field(body, 91, 0) == 0, fields(body)
    return player_id


def submit_plan(leader, accounts):
    body = vf(1, 1) + sf(2, leader)
    for account in accounts:
        body += bf(3, sf(1, account) + sf(2, PASSWORD))
    conn = socket.create_connection((HOST, PORT), timeout=5)
    conn.settimeout(5)
    try:
        conn.sendall(frame(FORM_PLAN_OPCODE, body))
        opcode, response = recv_frame(conn)
        assert opcode == FORM_PLAN_OPCODE + 1, opcode
        return field(response, 1, 0) == 1, field(response, 2, b"").decode("utf-8")
    finally:
        conn.close()


# ---------------------------------------------------------------- 帧解析

def decode_sync_attribute(body):
    raw = field(body, 3)
    value = struct.unpack("<f", raw)[0] if raw is not None else 0.0
    return as_int(field(body, 1, 0)), field(body, 2, 0), value


def team_member_ids(body):
    members = []
    for tag, wire_type, value in fields(body):
        if tag != 2:
            continue
        if wire_type == 0:
            members.append(as_int(value))
        elif wire_type == 2:
            offset = 0
            while offset < len(value):
                item, offset = read_varint(value, offset)
                members.append(as_int(item))
    return members


def attributes_for(frames, unit_id, numerics):
    """按顺序取出 frames 里某个 UnitId 的 20169 值：[(numeric, value)]。"""
    output = []
    for opcode, body in frames:
        if opcode != SYNC_ATTRIBUTE_OPCODE:
            continue
        current_unit, numeric, value = decode_sync_attribute(body)
        if current_unit == unit_id and numeric in numerics:
            output.append((numeric, value))
    return output


def values_for(frames, unit_id, numeric):
    return [value for current, value in attributes_for(frames, unit_id, (numeric,)) if current == numeric]


def vital_pushes(frames, unit_id):
    """按帧顺序返回 [(帧下标, numeric, value)]：unit_id 的血蓝推送（1001/1003）。"""
    output = []
    for index, (opcode, body) in enumerate(frames):
        if opcode != SYNC_ATTRIBUTE_OPCODE:
            continue
        current_unit, numeric, value = decode_sync_attribute(body)
        if current_unit == unit_id and numeric in (HP, MP):
            output.append((index, numeric, value))
    return output


def first_frame_index(frames, opcode):
    for index, (current, _) in enumerate(frames):
        if current == opcode:
            return index
    return None


def f32(value):
    return struct.unpack("<f", struct.pack("<f", value))[0]


def nextafter32(value, toward):
    packed = struct.unpack("<I", struct.pack("<f", value))[0]
    packed = packed - 1 if toward < value else packed + 1
    return struct.unpack("<f", struct.pack("<I", packed))[0]


def team_head_nudge(exact, max_value):
    """镜像服务端 teamHeadNudge：抖动值优先取「真值 +1」，满血往 -1 偏，0 血固定用 1。"""
    value = f32(exact)
    if value <= 0:
        return f32(1)
    if exact < max_value and f32(exact + 1) != value:
        return f32(exact + 1)
    if f32(exact - 1) != value:
        return f32(exact - 1)
    return nextafter32(value, 0)


def preceded_by_nudge(frames, index, numeric, exact, max_value):
    """frames[index] 是某个 numeric 的精确值；紧挨着它的前一帧如果是同一个 numeric 的
    抖动值，这一帧就出自 team_head_vitals.go 那套「抖动值→精确值」补发，而不是战斗
    路径的直接推送。"""
    if index == 0:
        return False
    opcode, body = frames[index - 1]
    if opcode != SYNC_ATTRIBUTE_OPCODE:
        return False
    _, previous_numeric, previous_value = decode_sync_attribute(body)
    return previous_numeric == numeric and previous_value == team_head_nudge(exact, max_value)


def family_boss_unit_id(frames):
    """从 20144（M2C_SendFamilyBossInfo）取家族 BOSS 的 UnitId 与开场血量。"""
    for opcode, body in frames:
        if opcode != FAMILY_BOSS_INFO_OPCODE:
            continue
        unit_id = as_int(field(body, 2, 0))
        if unit_id:
            return unit_id, as_int(field(body, 3, 0))
    return 0, 0


def has_opcode(frames, opcode):
    return any(current == opcode for current, _ in frames)


def describe_frame(opcode, body):
    if opcode == SYNC_ATTRIBUTE_OPCODE:
        unit_id, numeric, value = decode_sync_attribute(body)
        return "20169 UnitId=%d %s=%.0f" % (
            unit_id,
            NUMERIC_NAMES.get(numeric, "NumericType=%d" % numeric),
            value,
        )
    if opcode == TEAM_MEMBER_OPCODE:
        return "20162 队伍重建 %s" % sorted(team_member_ids(body))
    if opcode == BATTLE_DEFEAT_OPCODE:
        return "20055 战斗失败"
    if opcode == CHANGE_MAP_OPCODE:
        return "20033 换图"
    return str(opcode)


def dump_window(label, session, mark, mark_time, interesting):
    """打印窗口里的关键帧和相对窗口起点的毫秒偏移——用来证明一帧到底是战斗收尾
    当场推的，还是几十毫秒前那轮「抖动值→精确值」补发的尾巴。"""
    window = session.frames[mark:]
    shown = 0
    print("  -- %s 窗口共 %d 帧，关键帧时间线：" % (label, len(window)))
    for offset, (opcode, body) in enumerate(window):
        if opcode not in interesting:
            continue
        shown += 1
        if shown > 40:
            print("     ...（略）")
            break
        print(
            "     +%6.0fms %s"
            % ((session.stamps[mark + offset] - mark_time) * 1000, describe_frame(opcode, body))
        )


# ---------------------------------------------------------------- MySQL

def mysql_executable():
    candidates = [
        os.environ.get("MHQ_TEST_MYSQL_EXE"),
        shutil.which("mysql"),
        r"C:\Program Files\MySQL\MySQL Server 8.0\bin\mysql.exe",
    ]
    for candidate in candidates:
        if candidate and os.path.isfile(candidate):
            return candidate
    raise RuntimeError("找不到 mysql 客户端；用 MHQ_TEST_MYSQL_EXE 指定")


def mysql(sql):
    env = os.environ.copy()
    env["MYSQL_PWD"] = MYSQL_PASSWORD
    result = subprocess.run(
        [
            mysql_executable(), "--batch", "--raw", "--skip-column-names",
            "-h", MYSQL_HOST, "-P", MYSQL_PORT, "-u", MYSQL_USER, MYSQL_DATABASE,
            "-e", sql,
        ],
        check=True,
        capture_output=True,
        text=True,
        env=env,
    )
    return [line.split("\t") for line in result.stdout.splitlines() if line]


def player_id_of(account):
    rows = mysql(
        "SELECT p.id FROM players p JOIN accounts a ON a.id = p.account_id "
        "WHERE a.account = '%s'" % account
    )
    assert rows, "账号 %s 还没有角色" % account
    return int(rows[0][0])


def family_id_of(account):
    rows = mysql(
        "SELECT p.family_id FROM players p JOIN accounts a ON a.id = p.account_id "
        "WHERE a.account = '%s'" % account
    )
    assert rows, "账号 %s 还没有角色" % account
    return int(rows[0][0])


def stored_vitals(accounts):
    """按账号读回落库血量/蓝量：account -> (player_id, hp, mp)。"""
    quoted = ", ".join("'%s'" % account for account in accounts)
    rows = mysql(
        "SELECT a.account, p.id, p.current_hp, p.current_mp FROM players p "
        "JOIN accounts a ON a.id = p.account_id WHERE a.account IN (%s)" % quoted
    )
    return {row[0]: (int(row[1]), int(row[2]), int(row[3])) for row in rows}


# ---------------------------------------------------------------- 断言

checks = 0


def check(condition, description):
    global checks
    if not condition:
        raise AssertionError("FAIL: " + description)
    checks += 1
    print("  OK", description)


def close_enough(wire_value, stored_value):
    """float32 只在 2^24 内能精确表示整数，超出后按相对误差放宽。"""
    return abs(wire_value - stored_value) <= max(0.5, abs(stored_value) * 1e-6)


def main():
    suffix = "%06d%s" % (int(time.time()) % 1000000, secrets.token_hex(2))
    accounts = ["sbv%d%s" % (index + 1, suffix) for index in range(2)]
    roles = ["B%d%s" % (index + 1, suffix[-6:]) for index in range(2)]
    sessions = {}
    try:
        for index, account in enumerate(accounts):
            session = connect()
            try:
                provision(session, account, roles[index], 10 + index * 10)
            finally:
                session.close()
        print("已注册 2 个一次性账号：%s" % ", ".join(accounts))
        mate_account, fighter_account = accounts[0], accounts[1]

        # 先把 fighter 的血改小再登录：等级 1 打不动 3000 万血的 BOSS，BOSS 却能一击打死
        # 满血角色，改成 2 血可以让这场单人战斗以原生"失败"收尾，收尾时间可控。
        fighter_id = player_id_of(fighter_account)
        mysql("UPDATE players SET current_hp=%d WHERE id=%d" % (FIGHTER_START_HP, fighter_id))
        check(
            stored_vitals(accounts)[fighter_account][1] == FIGHTER_START_HP,
            "fighter 角色 %d 的血已改成 %d" % (fighter_id, FIGHTER_START_HP),
        )

        # fighter 建家族（20123 没有任何等级/花费门槛），队友不入家族：家族 BOSS 战会按
        # familyID 过滤参与者，队友因此被排除出战斗——这就是「队伍完整、战斗却是单人」。
        fighter = connect()
        fighter_id = enter_game(fighter, fighter_account, 1000)
        sessions[fighter_account] = (fighter, fighter_id)
        time.sleep(1.5)
        family_name = "SBV%s" % suffix[:7]
        opcode, body = fighter.call(
            FAMILY_CREATE_OPCODE, sf(1, family_name) + vf(90, 200), 200
        )
        check(
            opcode == FAMILY_CREATE_OPCODE + 1 and field(body, 91, 0) == 0 and bool(field(body, 3)),
            "fighter 建立家族 %s（%s）" % (family_name, field(body, 92, b"").decode("utf-8")),
        )
        fighter_family = family_id_of(fighter_account)
        check(fighter_family > 0, "fighter 的 family_id = %d" % fighter_family)

        mate = connect()
        mate_id = enter_game(mate, mate_account, 1010)
        sessions[mate_account] = (mate, mate_id)
        check(
            family_id_of(mate_account) == 0,
            "mate 没有家族：家族 BOSS 战会把它过滤出参与者（familyID 不同）",
        )
        time.sleep(1.5)

        opcode, body = fighter.call(
            DROP_SKILL_OPCODE, vf(1, SKILL_SLOT) + vf(2, BASIC_SKILL_ID) + vf(90, 201), 201
        )
        check(
            opcode == DROP_SKILL_OPCODE + 1 and field(body, 91, 0) == 0,
            "fighter 把普攻放进快捷栏槽 %d" % SKILL_SLOT,
        )

        # 组队：两人都在主城，启动器计划把队伍建出来（两个在线角色都会被拉进队长的场景）。
        ok, message = submit_plan(fighter_account, accounts)
        check(ok, "自动组队计划被接受（%s）" % message)
        time.sleep(3.0)

        rebuilds = [body for opcode, body in mate.frames if opcode == TEAM_MEMBER_OPCODE]
        check(
            bool(rebuilds) and set(team_member_ids(rebuilds[-1])) == {mate_id, fighter_id},
            "组队成立：视角 %d 在 20162 里看到 {%d, %d}" % (mate_id, mate_id, fighter_id),
        )
        max_hp = values_for(mate.frames, fighter_id, MAX_HP)
        max_mp = values_for(mate.frames, fighter_id, MAX_MP)
        check(
            bool(max_hp) and bool(max_mp),
            "队友视角拿到 fighter 的上限（血 %.0f 蓝 %.0f）"
            % (max_hp[-1] if max_hp else -1, max_mp[-1] if max_mp else -1),
        )
        fighter_max_hp = int(round(max_hp[-1]))
        fighter_max_mp = int(round(max_mp[-1]))

        # 从这里开始算「战斗期间」：队友这一窗口里的每一个 20169 都只能来自这次战斗。
        mate_mark = len(mate.frames)
        mate_mark_time = time.monotonic()

        opcode, body = fighter.call(
            FAMILY_BOSS_FIGHT_OPCODE, vf(1, FAMILY_BOSS_ID) + vf(90, 300), 300
        )
        check(
            opcode == FAMILY_BOSS_FIGHT_OPCODE + 1 and field(body, 91, 0) == 0,
            "fighter 开家族 BOSS %d（%s）"
            % (FAMILY_BOSS_ID, field(body, 92, b"").decode("utf-8")),
        )
        time.sleep(1.5)

        boss_unit_id, boss_hp = family_boss_unit_id(fighter.frames[mate_mark:])
        check(
            boss_unit_id != 0,
            "fighter 收到家族 BOSS 展示（%d UnitId=%d 血量=%d）"
            % (FAMILY_BOSS_INFO_OPCODE, boss_unit_id, boss_hp),
        )
        check(
            has_opcode(fighter.frames[mate_mark:], CHANGE_MAP_OPCODE) is False,
            "开战没有换图（家族 BOSS 就地开打，队伍不会被搬走）",
        )

        time.sleep(OPENING_SETTLE_SECONDS)

        # 出手一次触发 BOSS 那一轮反击。技能用即发即忘：公共动作 CD 内的请求服务端会
        # 静默拒绝（不回 RpcId），等回包会一直挂住。
        def battle_ended():
            return has_opcode(fighter.frames[mate_mark:], BATTLE_DEFEAT_OPCODE)

        casts = 0
        last_cast = 0.0
        deadline = time.time() + DAMAGE_WAIT_SECONDS
        while time.time() < deadline and not battle_ended():
            if time.time() - last_cast >= ACTION_COOLDOWN_SECONDS:
                fighter.sendall(
                    USE_MAIN_UI_SKILL_OPCODE, vf(1, SKILL_SLOT) + vf(90, 400 + casts)
                )
                casts += 1
                last_cast = time.time()
            time.sleep(0.4)

        if battle_ended():
            outcome = "失败"
        else:
            # BOSS 没能打死 fighter：用原生 20171 退出战斗收尾，这条路径同样把当前残血
            # 写回并同步给队友（battleexit.go clearBattleForTransition）。
            outcome = "退出战斗"
            fighter.call(QUIT_BATTLE_OPCODE, vf(90, 500), 500)
            time.sleep(1.0)
        print(
            "  -- 战斗结果：%s（出手 %d 次，BOSS UnitId=%d，fighter 自己看到的血量 %s）"
            % (
                outcome,
                casts,
                boss_unit_id,
                [round(value) for value in values_for(fighter.frames[mate_mark:], fighter_id, HP)][:12],
            )
        )

        # 收尾：补推合并窗口 50ms + 落库。
        time.sleep(SETTLE_SECONDS)

        fighter_frames = fighter.frames[mate_mark:]
        mate_frames = mate.frames[mate_mark:]

        dump_window(
            "队友",
            mate,
            mate_mark,
            mate_mark_time,
            {SYNC_ATTRIBUTE_OPCODE, TEAM_MEMBER_OPCODE, BATTLE_DEFEAT_OPCODE, CHANGE_MAP_OPCODE},
        )

        stored = stored_vitals(accounts)
        residual_hp, residual_mp = stored[fighter_account][1], stored[fighter_account][2]
        print(
            "  -- 落库残血：血 %d 蓝 %d（fighter 上限血 %d）"
            % (residual_hp, residual_mp, fighter_max_hp)
        )

        # 1) 队友必须收到 fighter 的血蓝变化——修复前单人战斗一条都不会推给队友。
        pushes = vital_pushes(mate_frames, fighter_id)
        mate_hp = [(index, value) for index, numeric, value in pushes if numeric == HP]
        mate_mp = [(index, value) for index, numeric, value in pushes if numeric == MP]
        check(
            bool(mate_hp),
            "队友在 fighter 单人战斗期间收到 %d 条血量变化（%s）"
            % (len(mate_hp), [round(value) for _, value in mate_hp[:8]]),
        )
        check(
            bool(mate_mp),
            "队友在 fighter 单人战斗期间收到 %d 条蓝量变化（%s）"
            % (len(mate_mp), [round(value) for _, value in mate_mp[:8]]),
        )

        # 2) 三处一致：队友头像、fighter 自己的推送、MySQL 落库。
        fighter_hp = values_for(fighter_frames, fighter_id, HP)
        fighter_mp = values_for(fighter_frames, fighter_id, MP)
        check(
            residual_hp >= 0 and residual_mp >= 0,
            "fighter 的残血已落库（血 %d 蓝 %d）" % (residual_hp, residual_mp),
        )
        check(
            bool(fighter_hp) and close_enough(fighter_hp[-1], residual_hp),
            "fighter 自己看到的残血 %.0f 等于落库 %d" % (fighter_hp[-1], residual_hp),
        )
        check(
            close_enough(mate_hp[-1][1], residual_hp),
            "队友头像上的残血 %.0f 等于落库 %d（战斗收尾即时同步）"
            % (mate_hp[-1][1], residual_hp),
        )
        check(
            bool(fighter_mp) and close_enough(fighter_mp[-1], residual_mp),
            "fighter 自己看到的残蓝 %.0f 等于落库 %d" % (fighter_mp[-1], residual_mp),
        )
        check(
            close_enough(mate_mp[-1][1], residual_mp),
            "队友头像上的残蓝 %.0f 等于落库 %d（战斗收尾即时同步）"
            % (mate_mp[-1][1], residual_mp),
        )
        check(
            residual_hp < fighter_max_hp,
            "残血严格小于上限（%d < %d），说明这一局真的挨了打" % (residual_hp, fighter_max_hp),
        )

        # 3) 这条变化不可能是「头像重建」那条老路送来的。老路（team_head_vitals.go）只有
        #    两种触发方式，两个都被排掉：
        #    a. 组队头像重建后的 4 轮补发——窗口里没有任何 20162，而补发只由重建排期
        #       （scheduleTeamHeadVitalReload 的唯一调用点在 sendTeamMember 里）；
        #    b. 补发本身是「抖动值 → 精确值」成对发的，紧挨着精确值的前一帧必然是同一个
        #       属性的抖动值。收尾残血这一帧前面不是。
        rebuild_index = first_frame_index(mate_frames, TEAM_MEMBER_OPCODE)
        check(
            rebuild_index is None,
            "整个战斗期间队友没有收到 20162（没有头像重建，补发链路不可能被排期）",
        )
        residual_index = mate_hp[-1][0]
        check(
            not preceded_by_nudge(mate_frames, residual_index, HP, residual_hp, fighter_max_hp),
            "队友拿到的残血帧（帧 %d）前面不是同一个属性的抖动值——不是「抖动值→精确值」补发"
            % residual_index,
        )
        residual_mp_index = mate_mp[-1][0]
        check(
            not preceded_by_nudge(mate_frames, residual_mp_index, MP, residual_mp, fighter_max_mp),
            "队友拿到的残蓝帧（帧 %d）同样不是补发" % residual_mp_index,
        )

        # 3b) 收尾把 fighter 送回了主城（失败回城），队友还在原场景——两人已经不同场景。
        #     两条老的补发链路都要走 scenePlayerVisibleTo 过滤，不同场景一律跳过；收尾残血
        #     仍然送到了队友那格，只可能是 pushTeamVitals（它按队伍成员推，不看场景）。
        check(
            has_opcode(fighter_frames, CHANGE_MAP_OPCODE),
            "收尾把 fighter 送回了主城（fighter 收到 20033）",
        )
        check(
            not has_opcode(mate_frames, CHANGE_MAP_OPCODE),
            "队友留在原场景（队友没有收到 20033）——两人已不同场景",
        )
        if outcome == "失败":
            check(
                residual_hp < FIGHTER_START_HP,
                "失败收尾写回的残血小于战前血量（%d < %d）" % (residual_hp, FIGHTER_START_HP),
            )

        # 4) 这确实是单人战斗：队友一条 BOSS 属性都不该收到（battle.party == nil，
        #    battleRecipients 只返回打怪的人自己）。
        boss_pushes = [
            decode_sync_attribute(body)[0]
            for opcode, body in mate_frames
            if opcode == SYNC_ATTRIBUTE_OPCODE
            and decode_sync_attribute(body)[0] == boss_unit_id
        ]
        check(
            not boss_pushes,
            "队友没有收到任何 BOSS 属性（单人战斗，%d 条）" % len(boss_pushes),
        )

        # 5) 战斗结束后队伍依然完整（家族 BOSS 没有把队伍拆开）。
        late_rebuilds = [
            team_member_ids(body) for opcode, body in mate_frames if opcode == TEAM_MEMBER_OPCODE
        ]
        late_rebuilds = [ids for ids in late_rebuilds if len(ids) > 1]
        if late_rebuilds:
            check(
                set(late_rebuilds[-1]) == {mate_id, fighter_id},
                "战斗期间队伍仍然完整（%s）" % sorted(late_rebuilds[-1]),
            )
        else:
            print("  -- 战斗期间队友侧没有新的 20162（队伍快照保持原样）")

        print("PASS 队伍内单人战斗的血蓝跨视角同步：%d 项断言" % checks)
    finally:
        for session, _ in sessions.values():
            session.close()
        quoted = ", ".join("'%s'" % account for account in accounts)
        mysql(
            "DELETE p FROM players p JOIN accounts a ON a.id = p.account_id "
            "WHERE a.account IN (%s);DELETE FROM accounts WHERE account IN (%s)"
            % (quoted, quoted)
        )


main()
