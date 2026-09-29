"""本地真实 TCP：战术反击的五人状态、广播、职业普攻反击和自身反击对照。

从 server-mysql 运行 python test/verify_tactical_counter.py。
只修改本次注册的临时账号；技能配置直接核对 MySQL，不改运营配置。
原始战斗协议帧归档到工作区的 参考数据/抓包归档，运行摘要写 logs/runtime。
"""

import argparse
import hashlib
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
WORKSPACE = SERVER.parent
HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
TEST_PASSWORD = "CounterTest123"
CAPTURE_OPS = {20030, 20031, 20032, 20033, 20047, 20048, 20049, 20050,
               20053, 20054, 20055, 20073, 20074, 20075, 20076, 20077,
               20078, 20080, 20081, 20082, 20083, 20085, 20086, 20162,
               20169, 20170, 20171, 20172, 20229, 20230, 20233, 20234, 20240}


def vi(value):
    value &= (1 << 64) - 1
    result = bytearray()
    while value > 127:
        result.append((value & 127) | 128)
        value >>= 7
    return bytes(result + bytes([value]))


def vf(tag, value):
    return vi(tag << 3) + vi(value)


def bf(tag, raw):
    return vi((tag << 3) | 2) + vi(len(raw)) + raw


def sf(tag, text):
    return bf(tag, text.encode("utf-8"))


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


def signed32(value):
    return struct.unpack("<i", struct.pack("<I", value & 0xffffffff))[0]


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
        # Reuse the server's existing local development default without
        # copying a password into another script or printing it.
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


def live_skill(skill_id):
    rows = mysql(
        "WITH RECURSIVE branch AS (SELECT * FROM game_config_nodes WHERE node_id="
        "(SELECT parent_id FROM game_config_nodes WHERE config_name='SkillLogicConfig' "
        "AND field_name='skillId' AND int_value=%d) UNION ALL "
        "SELECT n.* FROM game_config_nodes n JOIN branch p ON n.parent_id=p.node_id "
        "WHERE n.config_name='SkillLogicConfig') "
        "SELECT node_id,parent_id,HEX(field_name),array_index,node_kind,value_type,"
        "int_value,float_value,HEX(string_value),bool_value FROM branch ORDER BY node_id" % skill_id
    )
    nodes, children = {}, {}
    for row in rows:
        node = int(row[0])
        nodes[node] = row
        children.setdefault(int(row[1]), []).append(node)
    roots = [node for node, row in nodes.items() if int(row[1]) not in nodes]
    assert len(roots) == 1, "skill %d root missing/duplicated" % skill_id

    def build(node):
        row = nodes[node]
        kind, scalar = int(row[4]), int(row[5])
        if kind == 1:
            return {bytes.fromhex(nodes[child][2]).decode("utf-8"): build(child)
                    for child in children.get(node, [])}
        if kind == 2:
            ordered = sorted(children.get(node, []), key=lambda child: int(nodes[child][3]))
            return [build(child) for child in ordered]
        if scalar == 0:
            return None
        if scalar == 1:
            return int(row[6])
        if scalar == 2:
            return float(row[7])
        if scalar == 3:
            return bytes.fromhex(row[8]).decode("utf-8")
        return bool(int(row[9]))

    return build(roots[0])


class Recorder:
    def __init__(self, path):
        path.parent.mkdir(parents=True, exist_ok=True)
        self.file = path.open("w", encoding="utf-8")
        self.lock = threading.Lock()
        self.case = "setup"

    def record(self, label, direction, opcode, body):
        if opcode not in CAPTURE_OPS:
            return
        record = {"time": time.time(), "case": self.case, "client": label,
                  "direction": direction, "opcode": opcode, "body_hex": body.hex()}
        with self.lock:
            self.file.write(json.dumps(record, ensure_ascii=False) + "\n")
            self.file.flush()


class Client:
    def __init__(self, label, recorder):
        self.label, self.recorder = label, recorder
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
                self.recorder.record(self.label, "recv", opcode, body)
        except (OSError, ConnectionError) as error:
            if not self.closed:
                self.error = error

    def send(self, opcode, body=b""):
        self.recorder.record(self.label, "send", opcode, body)
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


def provision(account, role, recorder):
    client = Client("create-" + role, recorder)
    try:
        response = client.call(20010, 20011, sf(1, account) + sf(2, TEST_PASSWORD))
        client.call(20014, 20015, vf(1, field(response, 2)) + vf(2, field(response, 3)))
        client.call(20016, 20017, vf(2, 1) + sf(3, role))
    finally:
        client.close()


def login(account, index, recorder):
    client = Client("player-%d" % index, recorder)
    try:
        response = client.call(20008, 20009, sf(1, account) + sf(2, TEST_PASSWORD))
        gate = client.call(20014, 20015, vf(1, field(response, 2)) + vf(2, field(response, 3)))
        client.player_id = field(gate, 1)
        client.call(20027, 20028)
        client.send(20030)
        return client
    except Exception:
        client.close()
        raise


def team_members(body):
    result = []
    for tag, wire, value in fields(body):
        if tag != 2:
            continue
        if wire == 0:
            result.append(value)
        else:
            offset = 0
            while offset < len(value):
                item, offset = read_vi(value, offset)
                result.append(item)
    return set(result)


def query_states(client):
    response = client.call(20085, 20086, vf(1, client.player_id))
    return [field(raw, 1) for tag, wire, raw in fields(response) if tag == 1 and wire == 2]


def counter_evidence(client, mark, jobs, monsters):
    result = []
    frames = client.frames[mark:]
    used_launches = set()
    basics = {1: 100001, 3: 200001, 5: 300001, 7: 400001}
    # 线上抓包里 20081 M2C_BattleTouchState 一条都没有（12 份样本、963 个 20080
    # 对照 0 个 20081），客户端处理器也是空实现，服务端不再发送。它一旦回来就是
    # 回归，必须直接失败，不能因为下面匹配不到就悄悄跳过。
    assert not any(opcode == 20081 for _, opcode, _ in frames), \
        "server still sends 20081 M2C_BattleTouchState"
    # 反击不发任何施法包：20075 的客户端处理器会拿 SkillConfig[SkillId*100].Name
    # 在单位头上弹技能名（反击的普攻因此被报成「普通攻击」横幅），20076 虽然只播
    # PlayAnimation_Attack，但反击连这个动作都不要。玩家自己/自动战斗的普攻仍然
    # 走 20075；原版该包只有 UnitId/SkillId，CD 由 20237 独立承载。
    for _, opcode, body in frames:
        if field(body, 1) not in jobs:
            continue
        if opcode == 20075 and field(body, 2) == basics[jobs[field(body, 1)]]:
            raise AssertionError("counter still uses the named 20075 path: %r" % body.hex())
        if opcode == 20076:
            raise AssertionError("counter still announces an attack action: %r" % body.hex())
    # 反击的弹道：UnitId=反击者、TargetId=怪，且 Time>0——Time 是客户端子弹
    # DOMove 的飞行时长，归零等于让子弹瞬移，客户端看不到任何弹道（实测回归）。
    # 反击和普通普攻的区别只剩「没有施法包」和「由怪物弹道落地当帧触发」：
    # 普通普攻要先等一个 windup（1000ms）+ 施法包，所以下面用触发点判据。
    for index, (at, opcode, body) in enumerate(frames):
        if opcode != 20077 or index in used_launches:
            continue
        source, target = field(body, 1), field(body, 4)
        if source not in jobs or target not in monsters or field(body, 6) <= 0:
            continue
        # 触发点：这条弹道之前最近一次打在玩家身上的负面 20078（怪物弹道落地）。
        trigger = next(((frames[i][0], i) for i in range(index - 1, -1, -1)
                        if frames[i][1] == 20078 and field(frames[i][2], 1) in jobs
                        and signed32(field(frames[i][2], 2)) < 0), None)
        if trigger is None:
            continue
        trigger_at, trigger_index = trigger
        # 触发和反击之间不允许出现这个反击者的任何施法包——有就不是「不通过动作」。
        midair = [op for _, op, frame in frames[trigger_index:index]
                  if op in (20075, 20076) and field(frame, 1) == source]
        if midair:
            continue
        launch_delay = at - trigger_at
        assert launch_delay <= 0.4, \
            "counter launched %.3fs after the triggering projectile, want an immediate answer" % launch_delay
        damage_index = next((i for i in range(index + 1, min(index + 16, len(frames)))
                             if frames[i][1] == 20078 and field(frames[i][2], 1) == target
                             and signed32(field(frames[i][2], 2)) < 0), None)
        if damage_index is None:
            continue
        # 伤害只在普攻弹道飞完（配置的 DelayTime）之后落地，和普通普攻一致。
        flight = frames[damage_index][0] - at
        assert 0.5 <= flight <= 1.6, \
            "counter damage landed %.3fs after launch, want the basic attack's projectile flight" % flight
        used_launches.add(index)
        result.append({"source": source, "target": target, "skill": basics[jobs[source]],
                       "amount": -signed32(field(frames[damage_index][2], 2)),
                       "signature": "no-cast/projectile-flight", "flight_ms": field(body, 6),
                       "cast_at": trigger_at, "impact_at": frames[damage_index][0], "at": at})
    return result


def run_case(skill, level, count, caster_index, suffix, recorder, observe):
    recorder.case = label = "%d-lv%d-party%d-caster%d" % (skill, level, count, caster_index + 1)
    accounts = ["tca%s%d" % (suffix, index) for index in range(count)]
    quoted = ",".join("'%s'" % account for account in accounts)
    clients = []
    try:
        for index, account in enumerate(accounts):
            provision(account, "TC%s%d" % (suffix[-6:], index), recorder)
        time.sleep(0.3)
        rows = mysql("SELECT a.account,p.id FROM players p JOIN accounts a ON p.account_id=a.id "
                     "WHERE a.account IN (%s)" % quoted)
        player_ids = {account: int(player_id) for account, player_id in rows}
        assert len(player_ids) == count
        caster_id = player_ids[accounts[caster_index]]
        mysql("UPDATE players p JOIN accounts a ON p.account_id=a.id SET p.level=1,"
              "p.current_hp=-1,p.current_mp=-1,p.energy=1000,p.phy_add=5000,p.wim_add=5000 "
              "WHERE a.account IN (%s); INSERT INTO player_skills(player_id,skill_id,level,sort_order) "
              "VALUES(%d,%d,%d,1) ON DUPLICATE KEY UPDATE level=VALUES(level)" %
              (quoted, caster_id, skill, level))
        jobs = {player_id: 1 for player_id in player_ids.values()}
        if count == 2:
            sportsman = player_ids[accounts[(caster_index + 1) % count]]
            jobs[sportsman] = 3
            mysql("UPDATE players SET job_id=3,skin_id=3 WHERE id=%d; "
                  "DELETE FROM player_skills WHERE player_id=%d AND skill_id=100001; "
                  "INSERT INTO player_skills(player_id,skill_id,level,sort_order) VALUES(%d,200001,1,0) "
                  "ON DUPLICATE KEY UPDATE level=1" % (sportsman, sportsman, sportsman))
        if count > 1:
            planner = Client("team-plan", recorder)
            try:
                body = vf(1, 1) + sf(2, accounts[0])
                body += b"".join(bf(3, sf(1, account) + sf(2, TEST_PASSWORD)) for account in accounts)
                planner.send(65000, body)
                planner.wait(lambda: any(op == 65001 for _, op, _ in planner.frames))
                response = next(raw for _, op, raw in planner.frames if op == 65001)
                assert field(response, 1) == 1, list(fields(response))
            finally:
                planner.close()
        for index, account in enumerate(accounts):
            clients.append(login(account, index + 1, recorder))
        participants = {client.player_id for client in clients}
        assert participants == set(player_ids.values())
        if count > 1:
            for client in clients:
                client.wait(lambda c=client: any(op == 20162 and team_members(raw) == participants
                                                for _, op, raw in c.frames), timeout=10)
        leader, caster = clients[0], clients[caster_index]
        caster.call(20229, 20230, vf(1, 0) + vf(2, skill))
        time.sleep(1.1)
        leader.call(20031, 20032, vf(1, 1000601))
        for client in clients:
            client.send(20030)
        time.sleep(1.1)
        marks = {client.player_id: len(client.frames) for client in clients}
        leader.call(20048, 20049, vf(1, 0))
        monsters = set()
        for client in clients:
            client.wait(lambda c=client: any(op == 20050 for _, op, _ in c.frames[marks[c.player_id]:]))
            battle = next(raw for _, op, raw in client.frames[marks[client.player_id]:] if op == 20050)
            current = {field(raw, 1) for tag, wire, raw in fields(battle) if tag == 1 and wire == 2}
            assert current and (not monsters or current == monsters), "battle participants do not share monsters"
            monsters = current
            client.send(20030)
        selected = clients[(caster_index + 1) % count].player_id
        caster.call(20073, 20074, vf(1, selected))
        marks = {client.player_id: len(client.frames) for client in clients}
        deadline = time.monotonic() + 7
        while time.monotonic() < deadline:
            caster.call(20233, 20234, vf(1, 0))
            if any(op == 20075 and field(raw, 1) == caster_id and field(raw, 2) == skill
                   for _, op, raw in caster.frames[marks[caster_id]:]):
                break
            time.sleep(0.4)
        else:
            raise AssertionError("skill %d did not cast" % skill)
        cast_at = time.monotonic()
        modifier = skill * 100 + 11
        expected = participants if skill == 110604 else {caster_id}
        broadcasts = {}
        for client in clients:
            def added(c=client):
                return {field(raw, 2) for _, op, raw in c.frames[marks[c.player_id]:]
                        if op == 20080 and field(raw, 1) == modifier and field(raw, 6) == 1
                        and field(raw, 7) == 14000}
            client.wait(lambda: added() == expected)
            broadcasts[client.player_id] = sorted(added())
        states = {client.player_id: query_states(client) for client in clients}
        actual_holders = {player_id for player_id, state in states.items() if 102 in state}
        assert actual_holders == expected, (label, "authoritative states", states, expected)
        print("PASS %s: caster=%d selected=%d holders=%s; %d perspectives agree" %
              (label, caster_id, selected, sorted(actual_holders), count), flush=True)
        while time.monotonic() - cast_at < observe:
            time.sleep(0.2)
        counters = counter_evidence(leader, marks[leader.player_id], jobs, monsters)
        teammate_counters = sorted({event["source"] for event in counters if event["source"] != caster_id})
        if skill == 110604 and level == 3 and count > 1:
            assert teammate_counters, "no teammate basic counter attack observed during the buff"
            assert any(event["amount"] > 1 for event in counters if event["source"] != caster_id), counters
            expected_attacks = {(event["source"], event["target"], event["skill"]) for event in counters}
            for client in clients:
                client.wait(lambda c=client: expected_attacks <= {
                    (event["source"], event["target"], event["skill"])
                    for event in counter_evidence(c, marks[c.player_id], jobs, monsters)})
        if count == 2:
            assert any(event["source"] == sportsman and event["skill"] == 200001 for event in counters), counters
        print("  basic counter sources=%s teammates=%s skills=%s" %
              (sorted({event["source"] for event in counters}), teammate_counters,
               sorted({event["skill"] for event in counters})), flush=True)
        return {"case": label, "skill": skill, "level": level, "caster": caster_id,
                "selected": selected, "participants": sorted(participants), "monsters": sorted(monsters),
                "holders": sorted(actual_holders), "broadcasts": broadcasts, "states": states,
                "jobs": jobs, "counters": counters, "teammate_counters": teammate_counters,
                "buff_time_ms": 14000, "counter_mechanism": "basic_attack"}
    finally:
        for client in clients:
            client.close()
        time.sleep(0.4)
        # Every account name is freshly generated and comes from this case.
        assert all(re.fullmatch(r"tca[0-9a-f]+", account) for account in accounts)
        mysql("DELETE p FROM players p JOIN accounts a ON p.account_id=a.id WHERE a.account IN (%s);"
              "DELETE FROM accounts WHERE account IN (%s)" % (quoted, quoted))


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--case", choices=("all", "tactical3", "sportsman"), default="all")
    options = parser.parse_args()
    if HOST not in ("127.0.0.1", "localhost", "::1"):
        raise RuntimeError("This protocol regression runs against the local server only")
    stamp = time.strftime("%Y%m%d-%H%M%S") + "-" + secrets.token_hex(2)
    capture_path = WORKSPACE / "参考数据/抓包归档" / ("tactical-counter-local-" + stamp + ".jsonl")
    recorder = Recorder(capture_path)
    summary = {"host": HOST, "port": PORT, "capture": str(capture_path), "cases": []}
    try:
        summary["config_revision"] = int(mysql("SELECT MAX(revision) FROM game_config_revisions")[0][0])
        catalog = {skill_id: live_skill(skill_id) for skill_id in (100001, 200001, 300001, 400001, 110404, 110604)}
        summary["config_sha256"] = hashlib.sha256(json.dumps(catalog, sort_keys=True).encode()).hexdigest()
        tactical = catalog[110604]
        selection = dict(tactical["skillEventDic"])[1][0]["selectTarget"]
        summary["tactical_target_config"] = selection
        print("MySQL revision=%d; tactical teamType=%d selector=%s" %
              (summary["config_revision"], tactical["teamType"], json.dumps(selection, ensure_ascii=False)), flush=True)
        cases = [(110604, 3, 5, 0, 12)]
        if options.case == "all":
            cases += [(110604, 1, 5, 1, 3), (110604, 2, 5, 4, 3),
                      (110404, 5, 5, 0, 3), (110604, 3, 1, 0, 3),
                      (110604, 3, 2, 0, 12)]
        elif options.case == "sportsman":
            cases = [(110604, 3, 2, 0, 12)]
        for index, (skill, level, count, caster_index, observe) in enumerate(cases):
            suffix = "%x%s%x" % (int(time.time()), secrets.token_hex(3), index)
            summary["cases"].append(run_case(skill, level, count, caster_index, suffix, recorder, observe))
        summary["passed"] = True
        print("PASS tactical-counter live TCP: %d cases; temporary accounts removed" % len(cases), flush=True)
    finally:
        recorder.file.close()
        output = SERVER / "logs/runtime" / ("tactical-counter-result-" + stamp + ".json")
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
        print("Summary:", output, flush=True)
        print("Combat frame archive:", capture_path, flush=True)


if __name__ == "__main__":
    main()
