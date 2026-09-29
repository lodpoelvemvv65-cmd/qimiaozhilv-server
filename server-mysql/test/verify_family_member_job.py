# -*- coding: utf-8 -*-
"""真实 TCP 实测：家族成员列表下发的职业字段，客户端能不能显示成正确职业。

背景（见 `文档/34-家族BOSS战斗数值与统计口径分析-2026-09-17.md` §7.2）：

  * 客户端 `ET.FriendUI::FamilyListItemRender`（HotfixView.dll）把职业标签写成

        m_txtJob.text = TabHelper.GetStrJob(member.Job, 0)

  * `ET.TabHelper::GetStrJob(job, trans)` 的映射（反汇编，远程原版客户端）：

        job=1 → "军官"     job=2 → "运动员"     job=3 → "护士"     job=4 → "超能力"
        其它  → ""（空串）

    即线上必须下发**职业族**（1..4，=`(jobID+1)/2`），不是 `players.job_id`（1/3/5/7）。

  * 服务端 `family_members.job_id` 在写入时已经是职业族：
    `family.go:541` / `family_membership.go:88` 都用 `jobTypeOf(ss.jobID)` 落库；
    而 `family.go:599` 读出来时**又调用了一次** `jobTypeOf(m.Job)` → 二次换算。
    实测库值 1/1/2/3/4 → 下发 1/1/1/2/2 → 客户端显示 军官/军官/军官/运动员/运动员。

本脚本做什么：登录家族成员账号 → 发 `20125 C2M_GetFamily` → 解 `20126` 的
`FamilyMemberInfoList`（字段级 List，tag1）→ 逐行打印 `Job`、客户端会显示的文字，
并与 `players.job_id` / `family_members.job_id` 的真实值对照，断言「显示文字 == 真实职业」。

运行（在 `server-mysql/` 下，本地服务端已启动）：

    python .\\test\\verify_family_member_job.py

可用环境变量：`MHQ_TEST_ACCOUNT`（默认 a123123）、`MHQ_TEST_PASSWORD`（默认 b123123）、
`MHQ_TEST_FAMILY_ID`（默认 95）。
"""

import os
import pathlib
import re
import shutil
import socket
import struct
import subprocess
import time

# 客户端 `ET.TabHelper::GetStrJob` 的职业族 → 文字（反汇编得出，见文件头）。
CLIENT_JOB_TEXT = {1: "军官", 2: "运动员", 3: "护士", 4: "超能力"}


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
        else:
            raise AssertionError("unsupported wire type %d" % wire)
    return result


def value(body, field, wire=0, default=None):
    return next((item for number, item_wire, item in fields(body)
                 if number == field and item_wire == wire), default)


def all_of(body, field, wire=2):
    return [item for number, item_wire, item in fields(body)
            if number == field and item_wire == wire]


def rpc_of(body):
    return value(body, 90, 0, 0)


HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
ACCOUNT = os.environ.get("MHQ_TEST_ACCOUNT", "a123123")
PASSWORD = os.environ.get("MHQ_TEST_PASSWORD", "b123123")
FAMILY_ID = int(os.environ.get("MHQ_TEST_FAMILY_ID", "95"))


class Client:
    """带持久缓冲的读取器（同 verify_monster_reflect.py，超时不会吃掉半帧）。"""

    def __init__(self):
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.rpc = 0
        self.buf = b""

    def close(self):
        self.sock.close()

    def recv(self, timeout=5):
        while len(self.buf) < 4:
            self.sock.settimeout(timeout)
            chunk = self.sock.recv(65536)
            if not chunk:
                raise ConnectionError("server closed")
            self.buf += chunk
            timeout = 5.0
        length, opcode = struct.unpack("<HH", self.buf[:4])
        total = length + 2
        while len(self.buf) < total:
            self.sock.settimeout(5.0)
            chunk = self.sock.recv(65536)
            if not chunk:
                raise ConnectionError("server closed")
            self.buf += chunk
        body = self.buf[4:total]
        self.buf = self.buf[total:]
        return opcode, body

    def drain(self, timeout=0.6):
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


def login(client, account, password):
    opcode, body = client.call(20008, sf(1, account) + sf(2, password))
    assert opcode == 20009 and not value(body, 91), \
        "login failed for %s: %r" % (account, value(body, 92, 2))
    key, gate = value(body, 2), value(body, 3)
    assert key and gate, "no LoginGate voucher for %s" % account
    opcode, body = client.call(20014, vf(1, key) + vf(2, gate))
    assert opcode == 20015 and not value(body, 91), "LoginGate failed for %s" % account
    client.call(20027)
    return None


def main():
    truth = {}
    for player_id, name, job_id in mysql(
            "SELECT p.id, p.name, p.job_id FROM players p "
            "JOIN family_members m ON m.player_id = p.id WHERE m.family_id = %d "
            "ORDER BY p.id" % FAMILY_ID):
        truth[int(player_id)] = (name, int(job_id))
    stored = {}
    for player_id, job_id in mysql(
            "SELECT player_id, job_id FROM family_members WHERE family_id = %d "
            "ORDER BY player_id" % FAMILY_ID):
        stored[int(player_id)] = int(job_id)
    assert truth, "家族 %d 没有成员，无法验证" % FAMILY_ID

    client = Client()
    try:
        login(client, ACCOUNT, PASSWORD)
        opcode, body = client.call(20125)
        assert opcode == 20126, "C2M_GetFamily 响应 opcode = %d" % opcode
        assert not value(body, 91), "GetFamily 报错: %r" % value(body, 92, 2)
        entries = all_of(body, 1)
        assert entries, "20126 里没有 FamilyMemberInfoList（tag1）"

        print("uid      名字            库 job_id  下发 Job  客户端显示      真实职业")
        bad = []
        for raw in entries:
            uid = value(raw, 1)
            name = (value(raw, 2, 2) or b"").decode("utf-8", "replace")
            job = value(raw, 3, 0, 0)
            shown = CLIENT_JOB_TEXT.get(job, "(空)")
            real = CLIENT_JOB_TEXT.get((truth.get(uid, ("", 0))[1] + 1) // 2, "?")
            flag = "" if shown == real else "   <== 不一致"
            if shown != real:
                bad.append(uid)
            print("%-8s %-14s %-10s %-9s %-15s %s%s"
                  % (uid, name, stored.get(uid, "?"), job, shown, real, flag))
        time.sleep(0.2)
    finally:
        client.close()

    if bad:
        print("FAIL: %d 个成员职业显示与真实职业不一致: %s" % (len(bad), bad))
        raise SystemExit(1)
    print("PASS: 家族成员职业显示与真实职业一致（%d 人）" % len(entries))


if __name__ == "__main__":
    main()
