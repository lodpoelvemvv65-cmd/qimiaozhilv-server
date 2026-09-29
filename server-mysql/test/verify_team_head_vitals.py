# -*- coding: utf-8 -*-
"""组队头像血量/蓝量跨视角一致性实测：真实 TCP + 真实 MySQL 落库。

覆盖 2026-09-14 修的「各个视角看着都很怪」：客户端只在数值**变化**时刷新头像格，
重建头像只写上限，补间动画又持续约 4 秒并被 FGUI 还池复用，于是迟到的补间会把别人
的血量写进这一格。服务端因此改成重建后按「抖动值 → 精确值」分四轮补发 1001/1003
（见 `team_head_vitals.go`），本脚本实测这四件事：

1. 每个视角对每个队友都收到多轮补发，且每轮都停在精确值上；
2. 同一队友的血量/蓝量在所有视角完全一致；
3. 每个视角看到的数值都等于 MySQL 里的 `players.current_hp/current_mp`；
4. 0 血/0 蓝就是显示 0，服务端不会自动复活把问题盖掉。

脚本自己注册 5 个一次性账号和角色、自己改自己的 HP/MP、跑完自己清理，
不碰任何现有角色。默认连本地 127.0.0.1:7756：

    python .\\test\\verify_team_head_vitals.py

跑临时实例（例如新二进制先在 7757 验证，不影响 7756 上正在玩的客户端）：

    $env:MHQ_TEST_PORT = '7757'
    python .\\test\\verify_team_head_vitals.py
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

USE_GOODS_OPCODE = 20275
# 紫药：GoodsBase 110342，PercentHp=1 / PercentMp=1 → 一口回满血和精力。
POTION_ITEM_ID = 110342
POTION_ITEM_TYPE = 2
POTION_SLOT = 0

PARTY_SIZE = 5
# 服务端补 4 轮，这里放宽到 3 轮，避免机器慢时误报；最后一轮在 5 秒。
MIN_RELOAD_ROUNDS = 3
RELOAD_SETTLE_SECONDS = 6.5


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
        self.closed = False
        self.thread = threading.Thread(target=self._read_loop, daemon=True)
        self.thread.start()

    def _read_loop(self):
        while True:
            try:
                frame_tuple = recv_frame(self.conn)
            except Exception:
                return
            self.frames.append(frame_tuple)

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


def vitals_after_last_rebuild(frames):
    """取最后一次全队头像重建之后的 1001-1004 推送序列。"""
    last_rebuild = -1
    for index, (opcode, body) in enumerate(frames):
        if opcode == TEAM_MEMBER_OPCODE and len(team_member_ids(body)) > 1:
            last_rebuild = index
    if last_rebuild < 0:
        return None
    pushes = {}
    for opcode, body in frames[last_rebuild + 1 :]:
        if opcode != SYNC_ATTRIBUTE_OPCODE:
            continue
        unit_id, numeric, value = decode_sync_attribute(body)
        pushes.setdefault((unit_id, numeric), []).append(value)
    return pushes


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


def login_party(accounts):
    """重新登录整队，返回 account -> (session, player_id) 以及各自的收帧列表。"""
    sessions = {}
    for index, account in enumerate(accounts):
        session = connect()
        player_id = enter_game(session, account, 1000 + index * 10)
        sessions[account] = (session, player_id)
        time.sleep(1.0)
    return sessions


def main():
    suffix = "%06d%s" % (int(time.time()) % 1000000, secrets.token_hex(2))
    accounts = ["thv%d%s" % (index + 1, suffix) for index in range(PARTY_SIZE)]
    roles = ["T%d%s" % (index + 1, suffix[-6:]) for index in range(PARTY_SIZE)]
    sessions = {}
    try:
        for index, account in enumerate(accounts):
            session = connect()
            try:
                provision(session, account, roles[index], 10 + index * 10)
            finally:
                session.close()
        print("已注册 %d 个一次性账号：%s" % (PARTY_SIZE, ", ".join(accounts)))

        # 第一轮：拿到每个角色自己的血量/蓝量上限，用它算第二轮要改成的值。
        ok, message = submit_plan(accounts[0], accounts)
        check(ok, "自动组队计划被接受（%s）" % message)
        sessions = login_party(accounts)
        time.sleep(RELOAD_SETTLE_SECONDS)
        maxima = {}
        for account, (session, player_id) in sessions.items():
            pushes = vitals_after_last_rebuild(session.frames)
            check(pushes is not None, "角色 %d 收到全队头像重建" % player_id)
            for (unit_id, numeric), values in pushes.items():
                if unit_id == player_id:
                    continue
                if numeric in (MAX_HP, MAX_MP):
                    maxima.setdefault(unit_id, {})[numeric] = int(round(values[-1]))
        for account, (session, player_id) in sessions.items():
            session.close()
        time.sleep(1.0)

        targets = {}
        for index, account in enumerate(accounts):
            player_id = sessions[account][1]
            max_hp = maxima.get(player_id, {}).get(MAX_HP, 0)
            max_mp = maxima.get(player_id, {}).get(MAX_MP, 0)
            check(max_hp > 0 and max_mp > 0, "角色 %d 的上限可见（血 %d 蓝 %d）" % (player_id, max_hp, max_mp))
            targets[player_id] = [
                [0, 0],                      # 1 号：没血没蓝
                [max_hp // 2, 0],            # 2 号：半血、没蓝
                [0, max_mp // 3],            # 3 号：没血、有蓝
                [1, 1],                      # 4 号：1 血 1 蓝
                [max_hp - 1, max_mp - 1],    # 5 号：差一点满
            ][index]

        cases = []
        for account, (_, player_id) in sorted(sessions.items(), key=lambda item: item[1][1]):
            hp, mp = targets[player_id]
            cases.append(
                "UPDATE players SET current_hp=%d, current_mp=%d WHERE id=%d" % (hp, mp, player_id)
            )
            # 每人格子 0 塞一瓶紫药：第三轮要用它验证「半血吃药回满，队友那边同步吗」。
            cases.append(
                "REPLACE INTO player_items (player_id, location, slot_index, item_id, item_type, "
                "item_count, get_source) VALUES (%d, 1, %d, %d, %d, 10, '')"
                % (player_id, POTION_SLOT, POTION_ITEM_ID, POTION_ITEM_TYPE)
            )
        mysql(";".join(cases))
        seeded = stored_vitals(accounts)
        for account in accounts:
            player_id, hp, mp = seeded[account]
            check(
                [hp, mp] == targets[player_id],
                "落库改写成血 %d 蓝 %d（角色 %d）" % (hp, mp, player_id),
            )

        # 第二轮：带着改过的血量重新登录，看每个视角收到的补发。
        ok, message = submit_plan(accounts[0], accounts)
        check(ok, "重新提交自动组队计划被接受（%s）" % message)
        sessions = login_party(accounts)
        time.sleep(RELOAD_SETTLE_SECONDS)

        finals = {}
        for account, (session, player_id) in sessions.items():
            pushes = vitals_after_last_rebuild(session.frames)
            check(pushes is not None, "角色 %d 重新登录后收到全队头像重建" % player_id)
            for unit_id in sorted(targets):
                if unit_id == player_id:
                    continue
                for numeric in (HP, MP):
                    values = pushes.get((unit_id, numeric))
                    check(
                        values is not None and len(values) >= 1 + MIN_RELOAD_ROUNDS * 2,
                        "视角 %d 看到队友 %d 的%s补发 %s 条（≥%d）"
                        % (
                            player_id, unit_id, NUMERIC_NAMES[numeric],
                            len(values) if values else 0, 1 + MIN_RELOAD_ROUNDS * 2,
                        ),
                    )
                    stored = seeded[account_of(accounts, sessions, unit_id)][1 if numeric == HP else 2]
                    check(
                        close_enough(values[-1], stored),
                        "视角 %d 看到队友 %d 的%s停在精确值 %.0f（落库 %d）"
                        % (player_id, unit_id, NUMERIC_NAMES[numeric], values[-1], stored),
                    )
                    check(
                        values[-2] != values[-1],
                        "视角 %d 看到队友 %d 的%s末轮是「抖动 → 精确」"
                        % (player_id, unit_id, NUMERIC_NAMES[numeric]),
                    )
                    finals.setdefault((unit_id, numeric), {})[player_id] = values[-1]

        for (unit_id, numeric), per_viewer in sorted(finals.items()):
            distinct = {round(value) for value in per_viewer.values()}
            check(
                len(distinct) == 1,
                "队友 %d 的%s在 %d 个视角一致（值=%s）"
                % (unit_id, NUMERIC_NAMES[numeric], len(per_viewer), sorted(distinct)),
            )

        after = stored_vitals(accounts)
        for account in accounts:
            player_id, hp, mp = after[account]
            check(
                [hp, mp] == targets[player_id],
                "跑完落库仍是血 %d 蓝 %d（角色 %d，没有被自动复活盖掉）" % (hp, mp, player_id),
            )
        for player_id in sorted(targets):
            hp, mp = targets[player_id]
            if hp == 0:
                check(
                    all(round(value) == 0 for value in finals[(player_id, HP)].values()),
                    "0 血角色 %d 在所有视角都显示 0" % player_id,
                )
            if mp == 0:
                check(
                    all(round(value) == 0 for value in finals[(player_id, MP)].values()),
                    "0 蓝角色 %d 在所有视角都显示 0" % player_id,
                )

        # 第三轮：半血的人当场吃药回满，队友的组队头像必须跟着满。
        drinker = sessions[accounts[1]][1]
        drinker_hp, drinker_mp = targets[drinker]
        check(drinker_hp > 0, "吃药前角色 %d 是半血（%d）" % (drinker, drinker_hp))
        max_hp = maxima[drinker][MAX_HP]
        max_mp = maxima[drinker][MAX_MP]

        before = {account: len(session.frames) for account, (session, _) in sessions.items()}
        opcode, body = sessions[accounts[1]][0].call(
            USE_GOODS_OPCODE, vf(1, POTION_SLOT) + vf(90, 700), 700
        )
        check(
            opcode == USE_GOODS_OPCODE + 1 and field(body, 91, 0) == 0 and not field(body, 92, b""),
            "角色 %d 使用紫药成功（20275 → 20276）" % drinker,
        )
        time.sleep(1.5)

        healed = {}
        for account, (session, player_id) in sessions.items():
            values = {}
            for opcode, body in session.frames[before[account] :]:
                if opcode != SYNC_ATTRIBUTE_OPCODE:
                    continue
                unit_id, numeric, value = decode_sync_attribute(body)
                if unit_id == drinker and numeric in (HP, MP):
                    values[numeric] = value
            check(
                values.get(HP) is not None and close_enough(values[HP], max_hp),
                "视角 %d 看到角色 %d 吃药后血量回满（%.0f / 上限 %d）"
                % (player_id, drinker, values.get(HP, -1), max_hp),
            )
            check(
                values.get(MP) is not None and close_enough(values[MP], max_mp),
                "视角 %d 看到角色 %d 吃药后蓝量回满（%.0f / 上限 %d）"
                % (player_id, drinker, values.get(MP, -1), max_mp),
            )
            healed[player_id] = values
        check(
            len({round(values[HP]) for values in healed.values()}) == 1,
            "吃药回满后的血量在 %d 个视角一致（%s）"
            % (len(healed), sorted({round(values[HP]) for values in healed.values()})),
        )
        drinker_after = stored_vitals(accounts)[accounts[1]]
        check(
            [drinker_after[1], drinker_after[2]] == [max_hp, max_mp],
            "吃药回满已落库（血 %d 蓝 %d）" % (drinker_after[1], drinker_after[2]),
        )

        # 满血再吃一口应被拒，并且不能凭空推出血蓝变化把队友那一格带偏。
        before = {account: len(session.frames) for account, (session, _) in sessions.items()}
        opcode, body = sessions[accounts[1]][0].call(
            USE_GOODS_OPCODE, vf(1, POTION_SLOT) + vf(90, 701), 701
        )
        check(
            opcode == USE_GOODS_OPCODE + 1 and field(body, 91, 0) == 0 and field(body, 92, b""),
            "满血再吃药被拒且带提示（%s）" % field(body, 92, b"").decode("utf-8", "replace"),
        )
        time.sleep(1.0)
        for account, (session, player_id) in sessions.items():
            extra = [
                decode_sync_attribute(body)
                for opcode, body in session.frames[before[account] :]
                if opcode == SYNC_ATTRIBUTE_OPCODE
                and decode_sync_attribute(body)[0] == drinker
                and decode_sync_attribute(body)[1] in (HP, MP)
            ]
            check(
                not extra,
                "被拒的那一口没有给视角 %d 推角色 %d 的血蓝" % (player_id, drinker),
            )

        print("PASS 组队头像血量/蓝量跨视角一致性：%d 项断言" % checks)
    finally:
        for session, _ in sessions.values():
            session.close()
        quoted = ", ".join("'%s'" % account for account in accounts)
        mysql(
            "DELETE p FROM players p JOIN accounts a ON a.id = p.account_id "
            "WHERE a.account IN (%s);DELETE FROM accounts WHERE account IN (%s)"
            % (quoted, quoted)
        )


def account_of(accounts, sessions, player_id):
    for account, (_, current) in sessions.items():
        if current == player_id:
            return account
    raise AssertionError("未知角色 %d" % player_id)


main()
