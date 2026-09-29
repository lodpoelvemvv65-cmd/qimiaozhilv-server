# -*- coding: utf-8 -*-
"""主城（城镇）挂机经验实测。

线上抓包（参考数据/抓包归档/online-base-attr-idle-20260913.pcapng、
online-lv1-idle-20260913.pcapng）结论：角色停在城镇不动时，服务器按整分钟
节拍用 20170 下发 1026 等级 + 1027 经验，每跳固定 24560 显示经验、与等级无关
（1 级新角色第一跳直接到 56 级）。本脚本用真实 TCP 验证：

  1. 新角色停在主城（20031 → 10004，客户端看到 20033 ChangeMap 1000401），
     逐跳核对线上那三跳的等级/经验余数（56/868、67/264、74/757）与每一跳的
     面板属性（1002/1004/1005/1006/1007/1008/1009/1011/1012/1017/1034/1035），
     全值等于线上抓包原值；
  2. 换到非城镇地图（主线场景 1000601）再跨过一个节拍，等级/经验不变。

需要服务端已启动（默认 127.0.0.1:7756）。脚本自身约运行 4 分钟。
"""
import socket, struct, time, sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

HOST, PORT = '127.0.0.1', 7756
EXP_PER_TICK = 24560  # Gameplay.yaml town_idle_exp.exp_per_tick（客户端显示单位）


def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7f; n >>= 7
        if n: out.append(b | 0x80)
        else: out.append(b); return bytes(out)
def vf(n, v):  return varint((n << 3) | 0) + varint(v)
def sf(n, s):
    b = s.encode('utf-8'); return varint((n << 3) | 2) + varint(len(b)) + b
def pack(op, body): return struct.pack('<HH', 2 + len(body), op) + body
def rpc_of(b):
    i = 0
    while i < len(b):
        t = 0; sh = 0
        while True:
            x = b[i]; i += 1; t |= (x & 0x7f) << sh; sh += 7
            if not x & 0x80: break
        f, wt = t >> 3, t & 7
        if wt == 0:
            v = 0; sh = 0
            while True:
                x = b[i]; i += 1; v |= (x & 0x7f) << sh; sh += 7
                if not x & 0x80: break
            if f == 90: return v
        elif wt == 2:
            l = 0; sh = 0
            while True:
                x = b[i]; i += 1; l |= (x & 0x7f) << sh; sh += 7
                if not x & 0x80: break
            i += l
        elif wt == 5: i += 4
        else: break
    return None
def fields(body):
    out = []; i = 0
    while i < len(body):
        t = 0; sh = 0
        while True:
            x = body[i]; i += 1; t |= (x & 0x7f) << sh; sh += 7
            if not x & 0x80: break
        f, wt = t >> 3, t & 7
        if wt == 0:
            v = 0; sh = 0
            while True:
                x = body[i]; i += 1; v |= (x & 0x7f) << sh; sh += 7
                if not x & 0x80: break
            out.append((f, 'var', v))
        elif wt == 2:
            l = 0; sh = 0
            while True:
                x = body[i]; i += 1; l |= (x & 0x7f) << sh; sh += 7
                if not x & 0x80: break
            out.append((f, 'bytes', body[i:i+l])); i += l
        elif wt == 5:
            out.append((f, 'float', struct.unpack('<f', body[i:i+4])[0])); i += 4
        else: break
    return out
def fld(body, tag):
    for f, _, v in fields(body):
        if f == tag: return v
    return None


class Stream:
    """带缓冲的帧读取：登录后的主动推送会与 RPC 响应交错，先落缓冲再匹配 rpc。

    服务端 30 秒无收包就断开连接（tcp_srv.go connIdleTimeout），而挂机验证要停在
    原地等整分钟节拍，因此等待期间按 20012 心跳保活，响应帧全部丢弃。
    """

    HEARTBEAT_RPC = 9001

    def __init__(self, sock):
        self.sock = sock
        self.buf = b''
        self.pending = []  # 已读取但还没被 drain 的推送帧
        self.last_beat = time.time()

    def _heartbeat_if_due(self):
        now = time.time()
        if now - self.last_beat < 8:
            return
        self.sock.sendall(pack(20012, vf(90, self.HEARTBEAT_RPC)))
        self.last_beat = now

    def _pump(self, deadline):
        while True:
            while len(self.buf) >= 4:
                total, op = struct.unpack('<HH', self.buf[:4])
                if len(self.buf) < 2 + total: break
                frame = (op, self.buf[4:2 + total])
                self.buf = self.buf[2 + total:]
                if op == 20013:  # G2C_HeartBeat 响应，丢弃
                    continue
                self.pending.append(frame)
            remaining = deadline - time.time()
            if remaining <= 0: return
            self._heartbeat_if_due()
            self.sock.settimeout(min(remaining, 5.0))
            try:
                chunk = self.sock.recv(65536)
            except (socket.timeout, TimeoutError):
                continue
            if not chunk: raise ConnectionError('closed')
            self.buf += chunk

    def call(self, op, body, rpc, timeout=10.0):
        """发一条请求，返回 (响应opcode, 响应body)；期间收到的推送留在 pending。"""
        self.sock.sendall(pack(op, body))
        self.last_beat = time.time()
        deadline = time.time() + timeout
        while True:
            if self.pending and rpc_of(self.pending[0][1]) == rpc:
                return self.pending.pop(0)
            if time.time() > deadline:
                raise TimeoutError('rpc %d on opcode %d' % (rpc, op))
            self._pump(deadline)

    def drain(self, seconds):
        """收满 seconds 秒的推送帧（含之前缓存的），返回 [(opcode, body)]。"""
        self._pump(time.time() + seconds)
        frames, self.pending = self.pending, []
        return frames


def level_exp_from(frames, level, exp):
    """跟踪 20169 单条与 20170 列表里的 1026 等级 / 1027 经验（显示单位）。"""
    for op, body in frames:
        if op == 20169:
            if fld(body, 2) == 1026: level = int(fld(body, 3))
            elif fld(body, 2) == 1027: exp = int(fld(body, 3))
        elif op == 20170:
            for f, _, v in fields(body):
                if f != 2: continue
                key = fld(v, 1); value = fld(v, 2)
                if key == 1026: level = int(value)
                elif key == 1027: exp = int(value)
    return level, exp


def attributes_from(frames):
    """把 20169 单条与 20170 列表里的属性收成 {NumericType: 值}（后到的覆盖先到的）。"""
    out = {}
    for op, body in frames:
        if op == 20169:
            key = fld(body, 2)
            if key is not None:
                out[int(key)] = fld(body, 3)
        elif op == 20170:
            for f, _, v in fields(body):
                if f != 2: continue
                key = fld(v, 1)
                if key is not None:
                    out[int(key)] = fld(v, 2)
    return out


# 线上抓包同一角色每跳后的等级/余数/面板（online-lv1-idle-20260913.pcapng
# 18:13:57 与 online-base-attr-idle-20260913.pcapng 16:37:58/16:38:58/16:39:58，
# 两个包里 56 级那一跳完全一致）。每跳固定 24560 显示经验。
ONLINE_LADDER = [
    (1, 56, 868, {1002: 18000, 1004: 972, 1005: 6, 1006: 6, 1007: 18, 1008: 6,
                  1009: 138, 1011: 990, 1012: 900, 1017: 0.0012, 1034: 18, 1035: 18}),
    (2, 67, 264, {1002: 19000, 1004: 1026, 1005: 7, 1006: 7, 1007: 19, 1008: 7,
                  1009: 161, 1011: 1045, 1012: 950, 1017: 0.0014, 1034: 19, 1035: 19}),
    (3, 74, 757, {1002: 20000, 1004: 1080, 1005: 8, 1006: 8, 1007: 20, 1008: 8,
                  1009: 184, 1011: 1100, 1012: 1000, 1017: 0.0016, 1034: 20, 1035: 20}),
]


def exp_need(level):
    """与服务端 expNeed 相同的显示单位阈值：⌊Lv³/100⌋（1~4 级为 0，无下限）。"""
    return level ** 3 // 100


def total_exp(level, exp):
    return sum(exp_need(l) for l in range(1, level)) + exp


def login(stream, acct, name):
    stream.call(20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    _, b = stream.call(20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
    key, gate = fld(b, 2), fld(b, 3)
    stream.call(20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    _, created = stream.call(20016, vf(2, 1) + sf(3, name) + vf(90, 5), 5)
    assert fld(created, 92) is None, '创建角色失败: %s' % fld(created, 3)
    stream.call(20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    _, entered = stream.call(20027, vf(90, 7), 7)
    assert fld(entered, 92) is None, '进入游戏失败: %s' % fld(entered, 3)


def enter_map(stream, map_id, rpc):
    op, b = stream.call(20031, vf(1, map_id) + vf(90, rpc), rpc)
    assert op == 20032 and fld(b, 92) is None, '进入 %d 失败: %s' % (map_id, fld(b, 3))
    # M2C_ChangeMap: X=2/Y=3(fixed32)、MapId=4(varint)。
    return [fld(bb, 4) for o, bb in stream.drain(1.0) if o == 20033]


def settle_into_window(stream, level, exp):
    """先落到节拍起点之后 ≥3 秒，把这段里已经结算完的跳读出来。

    服务端节拍是全局时钟，落在每分钟 :00 后的第一个 1 秒 tick。进主城正好卡在
    整分钟前 3 秒内时，那一跳已经结算、帧就在 socket 缓冲里；这里把它读出来并
    连同帧一起返回，交给调用方按线上梯子校验 —— 只并入基线而不校验的话，基线会
    悄悄前进一格，后面的断言就会把两跳算成一跳（曾误报「共 +49120」）。
    """
    offset = time.time() % 60
    if offset >= 3:
        return level, exp, []
    wait = 3.5 - offset
    print('  对齐节拍窗口（%.1fs，已越过整分钟 %.1fs）…' % (wait, offset))
    frames = stream.drain(wait)
    level, exp = level_exp_from(frames, level, exp)
    return level, exp, frames


def wait_for_next_tick(stream):
    """等到下一个整分钟节拍结算完成，返回本跳收到的帧。"""
    offset = time.time() % 60
    wait = 60 - offset + 4
    print('  等待下一个整分钟节拍（%.1fs，已越过整分钟 %.1fs）…' % (wait, offset))
    return stream.drain(wait)


def check_tick(ticks, want_level, want_exp, want_panel, frames, level, exp):
    """核对一跳：经验增量、等级、经验余数、面板逐项都必须等于线上抓包原值。

    level/exp 是本跳之前的基线，返回本跳之后的 (level, exp)。
    """
    new_level, new_exp = level_exp_from(frames, level, exp)
    gain = total_exp(new_level, new_exp) - total_exp(level, exp)
    print('  城镇内第 %d 跳：等级 %s→%s 经验 %s→%s，共 +%d（线上 %d）' % (
        ticks, level, new_level, exp, new_exp, gain, EXP_PER_TICK))
    assert gain == EXP_PER_TICK, (
        '城镇挂机第 %d 跳应为 %d，实际 %d' % (ticks, EXP_PER_TICK, gain))
    assert new_level == want_level, '线上抓包第 %d 跳是 %d 级，实际 %d' % (
        ticks, want_level, new_level)
    # 余数同时锁住升级阈值公式：线上 1~4 级门槛为 0（⌊Lv³/100⌋）。
    assert new_exp == want_exp, '线上抓包第 %d 跳后余 %d，实际 %d' % (ticks, want_exp, new_exp)

    # 面板逐项对账：本地新号（默认宠物 2101）在同等级的属性应等于线上抓包原值。
    panel = attributes_from(frames)
    for numeric, want in sorted(want_panel.items()):
        got = panel.get(numeric)
        if got is None:
            raise AssertionError('第 %d 跳没有推送属性 %d' % (ticks, numeric))
        if abs(float(got) - float(want)) > max(1e-6, abs(want) * 1e-4):
            raise AssertionError('属性 %d = %s，线上抓包是 %s' % (numeric, got, want))
    print('    %d 级面板 %d 项与线上抓包一致：%s' % (
        new_level, len(want_panel),
        ' '.join('%d=%g' % (k, panel[k]) for k in sorted(want_panel))))
    return new_level, new_exp


def main():
    acct = 'idleexp' + str(int(time.time()))
    sock = socket.create_connection((HOST, PORT)); sock.settimeout(10)
    stream = Stream(sock)
    print('登录并创建角色…')
    login(stream, acct, '挂' + str(int(time.time()) % 100000))
    # 新角色首进的主线场景 1000601：登录批次里的 20170 给出初始等级/经验。
    level, exp = level_exp_from(stream.drain(4.0), None, None)
    print('  初始 等级=%s 经验=%s' % (level, exp))
    assert level == 1 and exp == 0, '新角色应当是 1 级 0 经验，实际 %s/%s' % (level, exp)

    print('进入主城 10004…')
    changes = enter_map(stream, 10004, 11)
    print('  20033 ChangeMap:', changes)
    assert changes and changes[-1] == 1000401, '客户端主城地图应为 1000401，实际 %s' % changes

    # 进主城可能正好卡在整分钟节拍前 3 秒内：那一跳已经结算，先把它读出来当作
    # 线上第 1 跳校验（而不是悄悄并入基线，否则后面会把两跳算成一跳）。
    level, exp, aligned_frames = settle_into_window(stream, level, exp)
    ladder = list(ONLINE_LADDER)
    if (level, exp) != (1, 0):
        print('  对齐窗口内已结算线上第 1 跳（等级 %s 经验 %s），按梯子逐项核对' % (level, exp))
        level, exp = check_tick(*ladder.pop(0), aligned_frames, 1, 0)

    for ticks, want_level, want_exp, want_panel in ladder:
        level, exp = check_tick(
            ticks, want_level, want_exp, want_panel, wait_for_next_tick(stream), level, exp)

    print('换到非城镇地图 1000601（主线场景）…')
    changes = enter_map(stream, 1000601, 20)
    print('  20033 ChangeMap:', changes)
    assert changes and changes[-1] == 1000601, '应进入 1000601，实际 %s' % changes

    field_level, field_exp = level_exp_from(wait_for_next_tick(stream), level, exp)
    print('  非城镇地图跨过节拍：等级 %s→%s 经验 %s→%s' % (level, field_level, exp, field_exp))
    assert (field_level, field_exp) == (level, exp), '非城镇地图不应发挂机经验'

    sock.close()
    print()
    print('=== 主城挂机经验回归：通过（每跳 %d，只在城镇生效） ===' % EXP_PER_TICK)


main()
