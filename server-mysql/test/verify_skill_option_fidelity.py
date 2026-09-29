"""本地真实 TCP：A 类技能选项修复的落点核对。

从 server-mysql 运行 python test/verify_skill_option_fidelity.py。

覆盖：
  110602 军官 殊途同归 —— 反伤选项必须把 11060211 状态挂在施法者身上（10 秒）。
  210601 运动员 猛虎式射门 —— 事件 24 的「自身」加攻必须落在施法者身上，
          不能落在子弹持有者（敌人）身上。修复前该状态会加到敌人，
          而敌人的状态不会走 20080，所以修复后必然出现施法者那一帧。

判据全部来自客户端 SkillLogicConfig：
  21060111 stateK=0 iconId=bufficon_atkAdd valueV Lv3=30 continueTime=8s
  11060211 stateK=101 iconId=bufficon_Thorns continueTime=10s
只使用本次新注册的临时账号，结束时删除账号与角色。
"""

import json
import os
import re
import secrets
import shutil
import socket
import struct
import subprocess
import sys
import threading
import time
from pathlib import Path


SERVER = Path(__file__).resolve().parents[1]
HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
TEST_PASSWORD = "OptionFixTest1"
BATTLE_SCENE = 1000601
MONSTER_ICON_ID = "bufficon_atkAdd"


def vi(value):
    value &= (1 << 64) - 1
    result = bytearray()
    while value > 127:
        result.append((value & 127) | 128)
        value >>= 7
    return bytes(result + bytes([value]))


def vf(tag, value):
    return vi(tag << 3) + vi(value)


def sf(tag, text):
    raw = text.encode("utf-8")
    return vi((tag << 3) | 2) + vi(len(raw)) + raw


def read_vi(raw, offset):
    result = shift = 0
    while True:
        byte = raw[offset]
        offset += 1
        result |= (byte & 127) << shift
        if byte < 128:
            return result, offset
        shift += 7


def fields(raw):
    offset = 0
    while offset < len(raw):
        tag, offset = read_vi(raw, offset)
        number, wire = tag >> 3, tag & 7
        if wire == 0:
            value, offset = read_vi(raw, offset)
        elif wire == 2:
            size, offset = read_vi(raw, offset)
            value, offset = raw[offset:offset + size], offset + size
        elif wire in (1, 5):
            size = 8 if wire == 1 else 4
            value, offset = raw[offset:offset + size], offset + size
        else:
            raise ValueError("unsupported protobuf wire %d" % wire)
        yield number, wire, value


def field(raw, number, default=0):
    return next((value for tag, _, value in fields(raw) if tag == number), default)


def exact(sock, size):
    raw = bytearray()
    while len(raw) < size:
        part = sock.recv(size - len(raw))
        if not part:
            raise ConnectionError("TCP closed")
        raw.extend(part)
    return bytes(raw)


def mysql(sql):
    dsn = os.environ.get("MHQ_TEST_MYSQL_DSN") or os.environ.get("MHQ_MYSQL_DSN")
    if not dsn:
        source = (SERVER / "internal/mysqlschema/schema.go").read_text(encoding="utf-8")
        match = re.search(r'fallbackDSN\s*=\s*"([^"]+)"', source)
        if not match:
            raise RuntimeError("Set MHQ_TEST_MYSQL_DSN for the local database")
        dsn = match[1]
    match = re.fullmatch(r"([^:]+):?(.*?)@tcp\(([^:]+):(\d+)\)/([^?]+)(?:\?.*)?", dsn)
    if not match:
        raise RuntimeError("Expected a TCP MySQL DSN")
    user, password, host, port, database = match.groups()
    if host not in ("127.0.0.1", "localhost", "::1"):
        raise RuntimeError("This regression only provisions accounts in a local MySQL")
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


class Client:
    def __init__(self, label, recorder=None):
        self.label = label
        self.recorder = recorder
        self.socket = socket.create_connection((HOST, PORT), timeout=5)
        self.socket.settimeout(None)
        self.frames, self.rpc, self.player_id = [], 0, 0
        self.closed, self.error = False, None
        self.thread = threading.Thread(target=self.receive, daemon=True)
        self.thread.start()

    def receive(self):
        try:
            while not self.closed:
                total, opcode = struct.unpack("<HH", exact(self.socket, 4))
                body = exact(self.socket, total - 2)
                self.frames.append((time.monotonic(), opcode, body))
                if self.recorder:
                    self.recorder(self.label, opcode, body)
        except (OSError, ConnectionError) as error:
            if not self.closed:
                self.error = error

    def send(self, opcode, body=b""):
        self.socket.sendall(struct.pack("<HH", len(body) + 2, opcode) + body)

    def wait(self, predicate, timeout=6):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if predicate():
                return
            if self.error:
                raise self.error
            time.sleep(0.02)
        raise AssertionError("%s timed out; recent opcodes=%s" %
                             (self.label, [op for _, op, _ in self.frames[-20:]]))

    def call(self, opcode, expected, body=b""):
        self.rpc += 1
        request_id, mark = self.rpc, len(self.frames)
        self.send(opcode, body + vf(90, request_id))
        matches = lambda: [(op, raw) for _, op, raw in self.frames[mark:]
                           if op == expected and field(raw, 90) == request_id]
        self.wait(lambda: bool(matches()))
        raw = matches()[0][1]
        assert field(raw, 91) == 0, (opcode, list(fields(raw)))
        message = field(raw, 92, b"")
        assert not message, (opcode, message.decode("utf-8", "replace"))
        return raw

    def close(self):
        if self.closed:
            return
        self.closed = True
        try:
            self.socket.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        self.socket.close()
        self.thread.join(timeout=1)


def provision(account, role, recorder=None):
    client = Client("create-" + role, recorder)
    try:
        response = client.call(20010, 20011, sf(1, account) + sf(2, TEST_PASSWORD))
        client.call(20014, 20015, vf(1, field(response, 2)) + vf(2, field(response, 3)))
        client.call(20016, 20017, vf(2, 1) + sf(3, role))
    finally:
        client.close()


def login(account, recorder=None):
    client = Client("player", recorder)
    response = client.call(20008, 20009, sf(1, account) + sf(2, TEST_PASSWORD))
    gate = client.call(20014, 20015, vf(1, field(response, 2)) + vf(2, field(response, 3)))
    client.player_id = field(gate, 1)
    client.call(20027, 20028)
    client.send(20030)
    return client


def state_frames(client, mark, modifier_id):
    """20080 frames the session received for this modifier: (target, time, type)."""
    return [(field(raw, 2), field(raw, 7), field(raw, 6))
            for _, op, raw in client.frames[mark:]
            if op == 20080 and field(raw, 1) == modifier_id]


def run_case(skill, level, job, basic, modifier, expected_time_ms, observe, label,
             recorder=None):
    account = "sof%s" % (secrets.token_hex(5))
    role = "SO" + account[-6:]
    quoted = "'%s'" % account
    client = None
    try:
        provision(account, role, recorder)
        time.sleep(0.3)
        player_id = int(mysql("SELECT p.id FROM players p JOIN accounts a ON p.account_id=a.id "
                              "WHERE a.account=%s" % quoted)[0][0])
        mysql("UPDATE players SET level=1,job_id=%d,skin_id=%d,current_hp=-1,current_mp=-1,"
              "phy_add=5000,wim_add=5000 WHERE id=%d;"
              "DELETE FROM player_skills WHERE player_id=%d AND skill_id=100001;"
              "INSERT INTO player_skills(player_id,skill_id,level,sort_order) VALUES"
              "(%d,%d,1,0) ON DUPLICATE KEY UPDATE level=1;"
              "INSERT INTO player_skills(player_id,skill_id,level,sort_order) VALUES"
              "(%d,%d,%d,1) ON DUPLICATE KEY UPDATE level=VALUES(level)"
              % (job, job, player_id, player_id, player_id, basic,
                 player_id, skill, level))
        client = login(account, recorder)
        assert client.player_id == player_id, (client.player_id, player_id)
        client.call(20229, 20230, vf(1, 0) + vf(2, skill))
        time.sleep(0.5)
        client.call(20031, 20032, vf(1, BATTLE_SCENE))
        client.send(20030)
        time.sleep(1.0)
        mark = len(client.frames)
        client.call(20048, 20049, vf(1, 0))
        client.wait(lambda: any(op == 20050 for _, op, _ in client.frames[mark:]))
        battle = next(raw for _, op, raw in client.frames[mark:] if op == 20050)
        # 20050 carries one nested monster message per entry; its field 1 is the id.
        monsters = [field(sub, 1) for tag, wire, sub in fields(battle)
                    if tag == 1 and wire == 2]
        assert monsters, "no monster in 20050"
        client.send(20030)
        client.call(20073, 20074, vf(1, monsters[0]))

        mark = len(client.frames)
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            client.call(20233, 20234, vf(1, 0))
            if any(op == 20075 and field(raw, 1) == player_id and field(raw, 2) == skill
                   for _, op, raw in client.frames[mark:]):
                break
            time.sleep(0.4)
        else:
            raise AssertionError("%s: skill %d never cast" % (label, skill))

        # Give the delayed impact the same window the runtime uses.
        deadline = time.monotonic() + observe
        while time.monotonic() < deadline:
            if state_frames(client, mark, modifier):
                break
            time.sleep(0.1)

        frames = state_frames(client, mark, modifier)
        assert frames, ("%s: modifier %d produced no 20080 for the player; "
                        "the state landed on the monster instead" % (label, modifier))
        targets = {target for target, _, _ in frames}
        assert player_id in targets, ("%s: modifier %d targets %s, want the caster %d"
                                      % (label, modifier, sorted(targets), player_id))
        added = [(target, when, change) for target, when, change in frames if change == 1]
        assert added, "%s: no Add frame for modifier %d" % (label, modifier)
        times = {when for _, when, _ in added}
        assert expected_time_ms in times, ("%s: modifier %d duration %s ms, want %d"
                                           % (label, modifier, sorted(times), expected_time_ms))
        print("PASS %s: skill=%d lv%d modifier=%d landed on caster=%d time=%dms"
              % (label, skill, level, modifier, player_id, expected_time_ms), flush=True)
        return {"case": label, "skill": skill, "level": level, "modifier": modifier,
                "caster": player_id, "monsters": monsters, "targets": sorted(targets),
                "time_ms": expected_time_ms}
    finally:
        if client is not None:
            client.close()
        time.sleep(0.3)
        assert re.fullmatch(r"sof[0-9a-f]+", account), account
        mysql("DELETE p FROM players p JOIN accounts a ON p.account_id=a.id "
              "WHERE a.account IN (%s);DELETE FROM accounts WHERE account IN (%s)"
              % (quoted, quoted))


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    if HOST not in ("127.0.0.1", "localhost", "::1"):
        raise RuntimeError("This protocol regression runs against the local server only")
    # 抓包必须进工作区的抓包归档目录；运行摘要仍写 logs/runtime。
    capture_dir = SERVER.parent / "参考数据/抓包归档"
    capture_dir.mkdir(parents=True, exist_ok=True)
    capture = capture_dir / ("skill-option-fidelity-local-%s.jsonl" % time.strftime("%Y%m%d-%H%M%S"))
    handle = capture.open("w", encoding="utf-8")
    lock = threading.Lock()
    watched = {20075, 20080, 20081, 20086}

    def recorder(label, opcode, body):
        if opcode not in watched:
            return
        with lock:
            handle.write(json.dumps({"time": time.time(), "client": label, "opcode": opcode,
                                     "body_hex": body.hex()}, ensure_ascii=False) + "\n")
            handle.flush()

    summary = {"host": HOST, "port": PORT, "capture": str(capture), "cases": []}
    try:
        summary["config_revision"] = int(mysql("SELECT MAX(revision) FROM game_config_revisions")[0][0])
        print("MySQL revision=%d" % summary["config_revision"], flush=True)
        cases = [
            # 军官 殊途同归：反伤状态 11060211 必须在施法者身上，持续 10 秒。
            dict(skill=110602, level=3, job=1, basic=100001, modifier=11060211,
                 expected_time_ms=10000, observe=3.0, label="110602-reflect-self"),
            # 运动员 猛虎式射门：事件 24 的自身加攻必须落在施法者身上，
            # 修复前会落到子弹持有者（敌人）上，而敌人不发 20080。
            dict(skill=210601, level=3, job=3, basic=200001, modifier=21060111,
                 expected_time_ms=8000, observe=4.0, label="210601-self-buff"),
        ]
        for case in cases:
            summary["cases"].append(run_case(recorder=recorder, **case))
        summary["passed"] = True
        print("PASS skill-option fidelity live TCP: %d cases; temporary accounts removed"
              % len(cases), flush=True)
    finally:
        handle.close()
        output = SERVER / "logs/runtime" / ("skill-option-fidelity-%s.json"
                                            % time.strftime("%Y%m%d-%H%M%S"))
        output.write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
        print("Summary:", output, flush=True)
        print("Frame archive:", capture, flush=True)


if __name__ == "__main__":
    main()
