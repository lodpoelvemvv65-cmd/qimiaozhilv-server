# -*- coding: utf-8 -*-
"""星币兑换（20314/20315 C2M/M2C_ChargeCoin）实测：花星币换铜币。

客户端 星币 旁边的 “+” 走 ET.BagUI::<AwakeAsync>b__14_3 的弹窗
（“请输入您要兑换的星币数量，星币：铜币=1：20”）→ C2M_ChargeCoin{Gem=输入值}。
弹窗里输入的是**星币数量**：扣 Gem 个星币，按 1 星币 = 20 铜币进账 Gem×20 铜币。
旧实现方向反了（扣铜币给星币），本脚本逐项核对修复后的方向：

  1. 新角色 100000 铜币 / 0 星币；
  2. 没有星币时请求被拒绝（Message="星币不足"、Error 必须为 0），余额不变；
  3. GM 造出 1000 星币后换 100 个：星币 -100（1041 与背包隐藏格物品 110205 同时减少），
     铜币 +2000（100 × 20）；
  4. 数量超过星币余额时被拒绝且余额不变；
  5. 星币不得出现在可见背包快照 20260 里（客户端只按 NumericType 1041 渲染）；
  6. 重登后铜币/星币落库一致，且 GM 侧读到的 player_items 里 110205 数量相同。

星币只能由 GM 产出，因此本脚本需要临时 GM 会话（与 verify_skill_point.py 同款）：
Run via: MHQ_TEST_GM_CHARGECOIN_TCP=1 go test ./cmd/gm-api -run TestChargeCoinLiveTCP -v
"""
import json
import os
import socket
import struct
import sys
import time
import urllib.error
import urllib.request
import uuid

sys.stdout.reconfigure(encoding='utf-8', errors='replace')

HOST = os.environ.get('MHQ_TEST_HOST', '127.0.0.1')
PORT = int(os.environ.get('MHQ_TEST_PORT', '7756'))
STAR_COIN_ITEM = 110205      # GoodsBase 110205，藏在 starCoinBagSlot
COPPER_PER_STAR = 20         # 客户端弹窗比例 星币：铜币 = 1：20

NT_COIN = 1028               # 铜币（客户端显示成 金/银/铜 = copper/10000, %10000/100, %100）
NT_STAR = 1041               # 星币


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
    HEARTBEAT_RPC = 9001

    def __init__(self, sock):
        self.sock = sock
        self.buf = b''
        self.pending = []
        self.last_beat = time.time()

    def _pump(self, deadline, stop=None):
        """读到 deadline 为止；stop() 为真可提前返回（call 用它等 RPC 响应）。"""
        while True:
            while len(self.buf) >= 4:
                total, op = struct.unpack('<HH', self.buf[:4])
                if len(self.buf) < 2 + total: break
                frame = (op, self.buf[4:2 + total])
                self.buf = self.buf[2 + total:]
                if op == 20013:
                    continue
                self.pending.append(frame)
            if stop is not None and stop():
                return
            remaining = deadline - time.time()
            if remaining <= 0: return
            if time.time() - self.last_beat >= 8:
                self.sock.sendall(pack(20012, vf(90, self.HEARTBEAT_RPC)))
                self.last_beat = time.time()
            self.sock.settimeout(min(remaining, 5.0))
            try:
                chunk = self.sock.recv(65536)
            except (socket.timeout, TimeoutError):
                continue
            if not chunk: raise ConnectionError('closed')
            self.buf += chunk

    def call(self, op, body, rpc, timeout=10.0):
        self.sock.sendall(pack(op, body))
        self.last_beat = time.time()
        deadline = time.time() + timeout
        while True:
            # 服务端的主动推送（20169/20170/20260 等）会和 RPC 响应交错，
            # 因此要在整个缓冲里找 RpcId 匹配的那一帧，不能只看队首。
            for i, (pop, obody) in enumerate(self.pending):
                if rpc_of(obody) == rpc:
                    del self.pending[i]
                    return pop, obody
            if time.time() > deadline:
                raise TimeoutError('rpc %d on opcode %d (待处理：%s)' % (
                    rpc, op, [(o, rpc_of(b)) for o, b in self.pending][:8]))
            self._pump(deadline, lambda: any(rpc_of(b) == rpc for _, b in self.pending))

    def drain(self, seconds):
        self._pump(time.time() + seconds)
        frames, self.pending = self.pending, []
        return frames


def money_from(frames, money):
    """把 20169 单条 / 20170 列表里的 NumericType 收成 dict（后到覆盖先到）。"""
    for op, body in frames:
        if op == 20169:
            key = fld(body, 2)
            if key is not None:
                money[int(key)] = fld(body, 3)
        elif op == 20170:
            for f, _, v in fields(body):
                if f != 2: continue
                key = fld(v, 1)
                if key is not None:
                    money[int(key)] = fld(v, 2)
    return money


def star_coin_items_from(frames):
    """背包快照 20260 里是否漏出星币物品 110205。

    星币在服务端是隐藏格物品，但 bagPairs 会把它从可见包列表里剔除（客户端只按
    NumericType 1041 渲染），所以这里恒为 0；留着是为了断言它不会变成普通格子。
    """
    total = 0
    for op, body in frames:
        if op != 20260:
            continue
        for f, kind, v in fields(body):
            if kind != 'bytes' or f != 1:
                continue
            if int(fld(v, 1) or 0) == STAR_COIN_ITEM:
                total += 1
    return total


def txt(body, tag):
    """字段 92/3 这类字符串是 UTF-8 bytes，转成可读文本。"""
    v = fld(body, tag)
    return v.decode('utf-8', 'replace') if isinstance(v, bytes) else v


def login(stream, acct, name, create=True):
    """登录（必要时建号）并进入游戏，返回 playerID（G2C_LoginGate 字段 1）。

    LoginGate Key 是一次性的，只能消费一次；建号后用同一个会话直接 20027 进游戏，
    不能再拿原来的 Key 请求一次 20014。
    """
    stream.call(20010, sf(1, acct) + sf(2, '123456') + vf(90, 1), 1)
    _, b = stream.call(20008, sf(1, acct) + sf(2, '123456') + vf(90, 2), 2)
    key, gate = fld(b, 2), fld(b, 3)
    _, login = stream.call(20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    assert fld(login, 91) in (None, 0), '登录网关失败: %s' % txt(login, 92)
    player_id = int(fld(login, 1))
    if create:
        _, created = stream.call(20016, vf(1, player_id) + vf(2, 1) + sf(3, name) + vf(90, 5), 5)
        assert fld(created, 92) is None, '创建角色失败: %s' % txt(created, 3)
    _, entered = stream.call(20027, vf(90, 7), 7)
    assert fld(entered, 92) is None, '进入游戏失败: %s' % txt(entered, 3)
    return player_id


def exchange(stream, gems, rpc):
    """发一条 C2M_ChargeCoin，返回 (响应, 该请求后的推送帧)。"""
    op, body = stream.call(20314, vf(1, gems) + vf(90, rpc), rpc)
    assert op == 20315, 'C2M_ChargeCoin 应当回 20315，实际 %d' % op
    return body, stream.drain(0.8)


def api(path, payload=None, key=None, expected=200):
    headers = {
        'Cookie': 'mhq_gm_session=%s; mhq_gm_csrf=%s' % (
            os.environ['GM_TEST_SESSION'], os.environ['GM_TEST_CSRF']),
        'X-CSRF-Token': os.environ['GM_TEST_CSRF'],
    }
    body = None
    if payload is not None:
        headers.update({'Content-Type': 'application/json',
                        'Idempotency-Key': key or uuid.uuid4().hex})
        body = json.dumps({'payload': payload}, ensure_ascii=False).encode('utf-8')
    request = urllib.request.Request(os.environ['GM_TEST_API_BASE'] + '/api/v1' + path,
                                     data=body, headers=headers)
    try:
        response = urllib.request.urlopen(request, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        envelope = json.load(response)
        assert response.status == expected, (response.status, expected, envelope)
        return envelope.get('data') if expected == 200 else envelope


def gm_coin(player_id):
    return int(api('/players/%d' % player_id)['coin'])


def gm_star_coins(player_id):
    """GM 侧读 player_items，汇总隐藏格里 110205 的数量（落库校验）。"""
    total = 0
    for row in api('/players/%d/inventory' % player_id)['items']:
        if int(row['item_id']) == STAR_COIN_ITEM:
            total += int(row['item_count'])
    return total


def main():
    account = os.environ['GM_TEST_ACCOUNT']
    assert account.startswith('chargecoin') and HOST in ('127.0.0.1', 'localhost'), \
        'local isolated tests only'
    name = '兑换' + account[-8:]

    sock = socket.create_connection((HOST, PORT)); sock.settimeout(10)
    stream = Stream(sock)
    print('登录并创建角色…')
    player_id = login(stream, account, name)
    path = '/players/%d' % player_id

    money = money_from(stream.drain(3.0), {})
    coin0, star0 = int(money.get(NT_COIN, -1)), int(money.get(NT_STAR, -1))
    print('  初始 铜币(1028)=%d 星币(1041)=%d' % (coin0, star0))
    assert coin0 == 100000, '新角色铜币应为 100000，实际 %d' % coin0
    assert star0 == 0, '新角色星币应为 0，实际 %d' % star0

    print('没有星币时兑换（20314 Gem=100）…')
    resp, frames = exchange(stream, 100, 21)
    print('  响应 Error=%s Message=%r' % (fld(resp, 91), txt(resp, 92)))
    assert fld(resp, 91) in (None, 0), '业务失败必须保持 Error=0，否则客户端会断线'
    assert txt(resp, 92) == '星币不足', 'Message 应为 星币不足，实际 %r' % txt(resp, 92)
    money = money_from(frames, money)
    assert int(money.get(NT_COIN, -1)) == coin0, '被拒绝的请求不得改动铜币'
    assert int(money.get(NT_STAR, -1)) == star0, '被拒绝的请求不得改动星币'

    print('GM 发放 1000 星币…')
    assert api(path + '/currency-adjust',
               {'currency': 'starcoin', 'delta': '1000'})['status'] == 'completed'
    money = money_from(stream.drain(1.5), money)
    assert int(money.get(NT_STAR, -1)) == 1000, 'GM 发放后星币应为 1000'

    print('用 100 星币换铜币（20314 Gem=100）…')
    resp, frames = exchange(stream, 100, 22)
    print('  响应 Error=%s Message=%r' % (fld(resp, 91), txt(resp, 92)))
    assert fld(resp, 91) in (None, 0), '兑换失败: %s' % txt(resp, 92)
    money = money_from(frames, money)
    coin1, star1 = int(money.get(NT_COIN, -1)), int(money.get(NT_STAR, -1))
    print('  结算后 铜币=%d 星币=%d；背包快照里 110205 出现 %d 次（应为 0）' % (
        coin1, star1, star_coin_items_from(frames)))
    assert star_coin_items_from(frames) == 0, '星币是隐藏格，不得出现在可见背包列表里'
    assert star0 + 1000 - star1 == 100, '星币应 -100，实际 %+d' % (star1 - star0 - 1000)
    assert coin1 - coin0 == 100 * COPPER_PER_STAR, (
        '铜币应 +%d（100 星币 × %d），实际 %+d' % (
            100 * COPPER_PER_STAR, COPPER_PER_STAR, coin1 - coin0))

    print('超过星币余额时拒绝（20314 Gem=100000000）…')
    resp, frames = exchange(stream, 100000000, 23)
    print('  响应 Error=%s Message=%r' % (fld(resp, 91), txt(resp, 92)))
    assert fld(resp, 91) in (None, 0), '业务失败必须保持 Error=0，否则客户端会断线'
    assert txt(resp, 92) == '星币不足', 'Message 应为 星币不足，实际 %r' % txt(resp, 92)
    money = money_from(frames, money)
    assert int(money.get(NT_COIN, -1)) == coin1, '被拒绝的请求不得改动铜币'
    assert int(money.get(NT_STAR, -1)) == star1, '被拒绝的请求不得改动星币'

    print('重登核对落库…')
    sock.close()
    sock = socket.create_connection((HOST, PORT)); sock.settimeout(10)
    stream = Stream(sock)
    login(stream, account, None, create=False)
    money = money_from(stream.drain(3.0), {})
    coin2, star2 = int(money.get(NT_COIN, -1)), int(money.get(NT_STAR, -1))
    print('  重登后 铜币=%d 星币=%d' % (coin2, star2))
    assert (coin2, star2) == (coin1, star1), '重登后余额不一致：%s/%s vs %s/%s' % (
        coin2, star2, coin1, star1)
    assert gm_coin(player_id) == coin1, 'GM 侧铜币落库不符：%d vs %d' % (
        gm_coin(player_id), coin1)
    assert gm_star_coins(player_id) == star1, 'GM 侧星币落库不符：%d vs %d' % (
        gm_star_coins(player_id), star1)

    sock.close()
    cleanup = Stream(socket.create_connection((HOST, PORT)))
    cleanup.sock.settimeout(10)
    print('通过原始删角协议删除临时角色…')
    cleanup.call(20010, sf(1, account) + sf(2, '123456') + vf(90, 1), 1)
    _, b = cleanup.call(20008, sf(1, account) + sf(2, '123456') + vf(90, 2), 2)
    cleanup.call(20014, vf(1, fld(b, 2)) + vf(2, fld(b, 3)) + vf(90, 4), 4)
    _, deleted = cleanup.call(20018, vf(1, player_id) + vf(90, 5), 5)
    assert fld(deleted, 92) is None, '删角失败: %s' % txt(deleted, 3)
    cleanup.sock.close()
    print()
    print('=== 星币兑换回归：通过（花 1 星币得 %d 铜币，重登与 GM 侧落库一致） ===' % COPPER_PER_STAR)


main()
