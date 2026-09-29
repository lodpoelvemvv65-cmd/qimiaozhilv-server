# -*- coding: utf-8 -*-
"""战斗状态(20080)清理回归。

客户端 ET.M2C_BattleChangeStateHandler 对 20080 无条件调用 BuffComponent.AddBuff，
该方法以消息的 Id 作为字典键存放；ET.BuffComponent.Update（500ms 定时器）只按
leastTime 过期删除，IconId 仅用于显示。所以强制清理必须复用 Add 时相同的
(单位, Id)：固定 Id=0 或重放固定图标表既清不掉残留，又会给每次换图刷包。

本脚本走真实 TCP 跑完「登录 → 进战斗 → 打到 20054 胜利 → 换图」，逐帧断言：
  1. 任何 20080 的 Id 都不等于哨兵 0，也不再重放固定的图标表；
  2. 每个 Reduce 的 Id 都必须是本会话先 Add 过的 Id，且 Time=0；
  3. 20054 战斗胜利之后不再补发任何 Reduce：线上不为战斗结束伪造 Time=0 清理帧，
     技能特效（如盾牌 EffectId=2109）由客户端按 Add 包里的 Time 自行销毁；
     换图也不会重放本会话没 Add 过的清理帧。

普攻不产生战斗增益，所以本脚本覆盖的是真实包序与「不刷屏」；清理帧的字段语义
（同一 (TargetUnitId, Id) 且 Time=0、复用 Add 的图标）由同目录 Go 用例
battle_state_cleanup_test.go 逐帧钉住：go test -run TestCombatState .

用法：在 server-mysql/ 下运行 `python .\test\test_battle_state_cleanup.py`
"""
import os
import socket
import struct
import sys
import time

sys.stdout.reconfigure(encoding='utf-8', errors='replace')

HOST = os.environ.get('MHQ_TEST_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_TEST_PORT', '7756'))

CHANGE_TYPE_ADD = 1
CHANGE_TYPE_REDUCE = 2

# job=1(Officer) 的普攻技能；见 server-mysql/skill.go baseSkillOfJob。
BASE_ATTACK_SKILL = 100001


def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7f
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def vf(n, v):
    return varint((n << 3) | 0) + varint(v)


def sf(n, s):
    b = s.encode('utf-8')
    return varint((n << 3) | 2) + varint(len(b)) + b


def pack(op, body):
    return struct.pack('<HH', 2 + len(body), op) + body


def recv_one(s):
    hdr = b''
    while len(hdr) < 4:
        c = s.recv(4 - len(hdr))
        if not c:
            raise ConnectionError('closed')
        hdr += c
    total, op = struct.unpack('<HH', hdr)
    body = b''
    while len(body) < total - 2:
        c = s.recv(total - 2 - len(body))
        if not c:
            raise ConnectionError('closed')
        body += c
    return op, body


def drain(s, timeout=0.6):
    s.settimeout(timeout)
    out = []
    while True:
        try:
            out.append(recv_one(s))
        except Exception:
            break
    return out


def rpc_of(b):
    i = 0
    while i < len(b):
        t = 0
        sh = 0
        while True:
            x = b[i]
            i += 1
            t |= (x & 0x7f) << sh
            sh += 7
            if not x & 0x80:
                break
        f, wt = t >> 3, t & 7
        if wt == 0:
            v = 0
            sh = 0
            while True:
                x = b[i]
                i += 1
                v |= (x & 0x7f) << sh
                sh += 7
                if not x & 0x80:
                    break
            if f == 90:
                return v
        elif wt == 2:
            l = 0
            sh = 0
            while True:
                x = b[i]
                i += 1
                l |= (x & 0x7f) << sh
                sh += 7
                if not x & 0x80:
                    break
            i += l
        elif wt == 5:
            i += 4
        else:
            break
    return None


def fields(body):
    out = []
    i = 0
    while i < len(body):
        t = 0
        sh = 0
        while True:
            x = body[i]
            i += 1
            t |= (x & 0x7f) << sh
            sh += 7
            if not x & 0x80:
                break
        f, wt = t >> 3, t & 7
        if wt == 0:
            v = 0
            sh = 0
            while True:
                x = body[i]
                i += 1
                v |= (x & 0x7f) << sh
                sh += 7
                if not x & 0x80:
                    break
            out.append((f, 'var', v))
        elif wt == 2:
            l = 0
            sh = 0
            while True:
                x = body[i]
                i += 1
                l |= (x & 0x7f) << sh
                sh += 7
                if not x & 0x80:
                    break
            out.append((f, 'bytes', body[i:i + l]))
            i += l
        elif wt == 5:
            out.append((f, 'float', struct.unpack('<f', body[i:i + 4])[0]))
            i += 4
        else:
            break
    return out


def fld(body, tag):
    for f, _, v in fields(body):
        if f == tag:
            return v
    return None


def send_req(s, op, body, rpc):
    s.sendall(pack(op, body))
    pushes = []
    for _ in range(400):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes


def login(s, acct, name):
    op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    if fld(b, 91) is not None:
        send_req(s, 20010, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
        op, b, _ = send_req(s, 20008, sf(1, acct) + sf(2, '123456') + vf(90, 3), 3)
    key = fld(b, 2)
    gate = fld(b, 3)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    send_req(s, 20016, vf(2, 1) + sf(3, name) + vf(90, 5), 5)
    send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    op, b, pushes = send_req(s, 20027, vf(90, 7), 7)
    return pushes


def combat_state(body):
    """20080 M2C_BattleChangeState: Id=1 TargetUnitId=2 IconId=3 IconDesc=4 Type=6 Time=7 IsBuff=8"""
    d = dict((f, v) for f, w, v in fields(body))
    icon = d.get(3)
    if isinstance(icon, bytes):
        icon = icon.decode('utf-8', 'replace')
    return {
        'id': d.get(1, 0),
        'target': d.get(2, 0),
        'icon': icon,
        'type': d.get(6, 0),
        'time': d.get(7, 0),
    }


def change_map_of(frames):
    """最后一次 20033 ChangeMap 的 MapId（field 4）。"""
    map_id = None
    for op, body in frames:
        if op == 20033:
            map_id = fld(body, 4)
    return map_id


class Recorder(object):
    def __init__(self):
        self.adds = set()
        self.reduces = []
        self.frames = []
        # 收到 20054 那一刻已记录的 Reduce 条数；战斗结束后的清理帧必须全部
        # 发生在它之前（线上胜利后不再补发 Reduce）。
        self.victory_mark = None

    def feed(self, frames, mark_victory=False):
        for op, body in frames:
            if mark_victory and op == 20054 and self.victory_mark is None:
                self.victory_mark = len(self.reduces)
            if op != 20080:
                continue
            state = combat_state(body)
            self.frames.append(state)
            if state['type'] == CHANGE_TYPE_ADD:
                self.adds.add(state['id'])
            elif state['type'] == CHANGE_TYPE_REDUCE:
                self.reduces.append(state)


ok = 0


def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  OK  ' + msg)


def main():
    acct = 'bsc' + str(int(time.time()))
    s = socket.create_connection((HOST, PORT))
    s.settimeout(5)
    print('connected %s:%d' % (HOST, PORT))

    recorder = Recorder()
    pushes = login(s, acct, '状态清理' + str(int(time.time()) % 100000))
    recorder.feed(pushes)

    current_map = change_map_of(pushes)
    check(current_map is not None, '登录后进入场景 map=%s' % current_map)

    # 等场景初始化完成再开战，避免落在地图装载窗口内。
    time.sleep(1.0)
    recorder.feed(drain(s, 1.0))

    # 新角色快捷栏是空的（学技能不会自动进槽），直接点槽 0 会被 server
    # 判为 bad slot 而静默拒绝，战斗就打不起来。先把职业普攻放进槽 0。
    _, body, pushes = send_req(s, 20229, vf(1, 0) + vf(2, BASE_ATTACK_SKILL) + vf(90, 11), 11)
    recorder.feed(pushes)
    check(fld(body, 91) is None and fld(body, 92) is None and fld(body, 1) is not None,
          '普攻技能已放入快捷栏槽 0: %s' % (fld(body, 92),))

    # 海滩场景的 20048.Region 是本地遭遇槽位（0/1），不是 MainStory._id。
    _, body, pushes = send_req(s, 20048, vf(1, 0) + vf(90, 12), 12)
    recorder.feed(pushes)
    check(fld(body, 92) is None, '进入主线战斗成功: %s' % (fld(body, 92),))
    check(any(op == 20050 for op, _ in pushes), '收到 20050 怪物列表')

    # 连打普攻直到 20054 战斗胜利；期间收齐全部 20080。
    # 普攻有公共 CD（20237 带剩余 CD），冷却内的施放会被静默拒绝，所以按
    # 是否收到 20237 判断这次是否真的放出，放出了才等 CD。
    # 最后一击后服务端要等 battleVictoryDelay(4.2s) 才推 20054，可能落在循环
    # 之外，所以循环结束后继续收包直到拿到胜利帧。
    damaged = False
    victory = False
    casts = 0
    for index in range(160):
        _, body, pushes = send_req(s, 20233, vf(1, 0) + vf(90, 20 + index), 20 + index)
        recorder.feed(pushes, mark_victory=True)
        opcodes = set(op for op, _ in pushes)
        damaged = damaged or 20078 in opcodes
        if 20054 in opcodes:
            victory = True
            break
        if 20237 in opcodes:
            casts += 1
            time.sleep(5.4)
        else:
            time.sleep(0.6)
    deadline = time.time() + 8.0
    while not victory and time.time() < deadline:
        pushes = drain(s, 0.5)
        recorder.feed(pushes, mark_victory=True)
        victory = any(op == 20054 for op, _ in pushes)
    check(casts >= 1 and damaged, '普攻真实释放并命中（20237 CD + 20078 伤害帧）')
    check(victory, '连打普攻直到 20054 战斗胜利')
    print('战斗期间 20080: Add=%d Reduce=%d %s' % (len(recorder.adds), len(recorder.reduces), recorder.frames))

    unknown = [st for st in recorder.reduces if st['id'] not in recorder.adds]
    check(not unknown, '每个 Reduce 都复用先前 Add 的 Id: %s' % unknown)
    check(all(st['time'] == 0 for st in recorder.reduces), '清理帧 Time=0')
    check(all(st['id'] != 0 for st in recorder.frames), '任何 20080 都不使用固定哨兵 Id=0')
    if recorder.victory_mark is not None:
        after_victory = recorder.reduces[recorder.victory_mark:]
        check(not after_victory,
              '20054 胜利后不补发清理帧（线上由客户端按特效自身 Time 销毁）: %s' % after_victory)

    # 胜利后换图：清理帧只允许复用本会话 Add 过的 Id，绝不能再重放固定图标表。
    before = len(recorder.frames)
    target_map = 1000601 if current_map != 1000601 else 1000401
    _, _, pushes = send_req(s, 20031, vf(1, target_map) + vf(90, 31), 31)
    recorder.feed(pushes)
    transition = recorder.frames[before:]
    check(any(op == 20033 for op, _ in pushes), '换图收到 20033 ChangeMap')
    replayed = [st for st in transition
                if st['type'] == CHANGE_TYPE_REDUCE and st['id'] not in recorder.adds]
    check(not replayed, '换图没有重放未 Add 过的清理帧: %s' % transition)
    check(all(st['id'] != 0 for st in transition), '换图不推哨兵 Id=0: %s' % transition)
    print('换图期间 20080: %s' % transition)

    s.close()
    print()
    print('=== 战斗状态清理回归全部通过: %d 项 ===' % ok)


if __name__ == '__main__':
    main()
