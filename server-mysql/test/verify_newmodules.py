# -*- coding: utf-8 -*-
"""新增模块回归验证：邮件/商店/市场/仓库/宠物/聊天/签到/传送门选图/组队。

运行：从 server-mysql/ 执行 python .\test\verify_newmodules.py
（需 127.0.0.1:7756 已启动 mhqserver-mysql）
"""
import socket, struct, time, sys, random
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

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

def recv_one(s):
    hdr = b''
    while len(hdr) < 4:
        c = s.recv(4 - len(hdr))
        if not c: raise ConnectionError('closed')
        hdr += c
    total, op = struct.unpack('<HH', hdr)
    body = b''
    while len(body) < total - 2:
        c = s.recv(total - 2 - len(body))
        if not c: raise ConnectionError('closed')
        body += c
    return op, body

def drain(s, timeout=0.6):
    s.settimeout(timeout)
    out = []
    while True:
        try: out.append(recv_one(s))
        except (socket.timeout, ConnectionError): break
    return out

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

def container_item(body, list_tag, item_id):
    """Return (global_index, count) for one BagMap list entry."""
    for f, wt, pair in fields(body):
        if f != list_tag or wt != 'bytes':
            continue
        index = fld(pair, 1)
        net_item = next((v for ff, ww, v in fields(pair) if ff == 2 and ww == 'bytes'), None)
        if net_item is not None and fld(net_item, 1) == item_id:
            return index, fld(net_item, 4)
    return None

def send_req(s, op, body, rpc):
    s.sendall(pack(op, body))
    pushes = []
    for _ in range(200):
        op2, b = recv_one(s)
        if rpc_of(b) == rpc:
            pushes.extend(drain(s))
            return op2, b, pushes
        pushes.append((op2, b))
    return None, None, pushes

def login(account, name, job=1):
    s = socket.create_connection(('127.0.0.1', 7756)); s.settimeout(5)
    op, b, _ = send_req(s, 20008, sf(1, account) + sf(2, '123456') + vf(90, 1), 1)
    if fld(b, 91) is not None:  # 账号不存在 → 注册
        send_req(s, 20010, sf(1, account) + sf(2, '123456') + vf(90, 2), 2)
        op, b, _ = send_req(s, 20008, sf(1, account) + sf(2, '123456') + vf(90, 3), 3)
    key = fld(b, 2); gate = fld(b, 3)
    op, b, _ = send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 4), 4)
    if fld(b, 7) is False or fld(b, 7) is None:
        send_req(s, 20016, vf(2, job) + sf(3, name) + vf(90, 5), 5)
        send_req(s, 20014, vf(1, key) + vf(2, gate) + vf(90, 6), 6)
    op, b, pushes = send_req(s, 20027, vf(90, 7), 7)
    return s, pushes

ok = 0
def check(cond, msg):
    global ok
    assert cond, 'FAIL: ' + msg
    ok += 1
    print('  ✓', msg)

acct = 'nm' + str(int(time.time())) + str(random.randint(10, 99))
print('=== 新模块回归 ===')

# ---------- 1. 登录 + 货币推送 ----------
s, pushes = login(acct, '新模块测试')
coins = [(fld(bb, 2), fld(bb, 3)) for o, bb in pushes if o == 20169 and fld(bb, 2) in (1024, 1025, 1028)]
check(any(t == 1028 and v == 100000 for t, v in coins), '进游戏推送铜币 1028=100000: %s' % coins)
petpush = [bb for o, bb in pushes if o == 20370]
check(len(petpush) == 1 and fld(petpush[0], 1) == 2101, '进游戏推送宠物 20370 petId=2101')

# ---------- 2. 邮件 ----------
op, b, pushes = send_req(s, 20281, vf(90, 10), 10)
mails = [dict((f, v) for f, wt, v in fields(v)) for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
check(op == 20282 and len(mails) >= 1, 'GetMail 返回欢迎邮件: %d 封' % len(mails))
mail_id = mails[0].get(8)
op, b, pushes = send_req(s, 20283, vf(1, mail_id) + vf(2, 0) + vf(90, 11), 11)
bag_after = [f for f, wt, v in fields(b) if f == 1]
check(len(bag_after) > 0, '领取邮件附件后回包带 BagMapList')
op, b, pushes = send_req(s, 20285, vf(1, mail_id) + vf(2, 0) + vf(90, 12), 12)
remain_mails = [v for f, wt, v in fields(b) if f == 1 and wt == 'bytes']
check(op == 20286 and len(remain_mails) == 0, '删除邮件后 MailList 为空')

# ---------- 3. 商店购买 + 卖出 ----------
# ShopBase 10001 = 110305 生命药水 500 铜币；买 5 个验证数量路径
op, b, pushes = send_req(s, 20174, vf(1, 0) + vf(2, 0) + vf(3, 5) + vf(90, 13), 13)
check(op == 20175 and not fld(b, 92), '普通商店购买成功 (20175)')
sell_idx = None
for f, wt, v in fields(b):
    if f == 1 and wt == 'bytes':
        bag = dict((ff, vv) for ff, wt2, vv in fields(v))
        item = dict((ff, vv) for ff, wt2, vv in fields(bag.get(2, b'')))
        if item.get(1) == 110305:
            sell_idx = bag.get(1)
check(sell_idx is not None, '购买后背包出现 110305')
op, b, pushes = send_req(s, 20178, vf(2, sell_idx) + vf(3, 1) + vf(90, 14), 14)
check(op == 20179 and not fld(b, 92), '卖出物品成功 (20179)')

# ---------- 4. 市场查询 + 购买 ----------
op, b, pushes = send_req(s, 20172, vf(1, 0) + vf(90, 15), 15)
market_ids = [v for f, wt, v in fields(b) if f == 2]
check(op == 20173 and len(market_ids) > 0, 'GetMarket 返回 %d 个 MarketIdList' % len(market_ids))
# 元宝市场购买第一个（10001=110342 紫药 10 元宝）
op, b, pushes = send_req(s, 20176, vf(1, 0) + vf(2, 0) + vf(3, 1) + vf(4, 2) + vf(90, 16), 16)
check(op == 20177 and not fld(b, 92), '元宝市场购买成功 (20177)')

# ---------- 5. 仓库存取 ----------
op, b, pushes = send_req(s, 20188, vf(1, 0) + vf(90, 17), 17)
store_items = [f for f, wt, v in fields(b) if f == 1]
check(op == 20189 and fld(b, 3) == 2, 'GetStore 响应默认 Total=2 (20189)')
# 从同一背包格批量存 3 个，再从仓库 0 号格批量取 2 个。
op, b, pushes = send_req(s, 20192, vf(1, sell_idx) + vf(3, 3) + vf(4, 0) + vf(90, 171), 171)
stored = container_item(b, 1, 110305)
check(op == 20193 and stored == (0, 3), '批量存入 3 个到仓库第 1 页第 1 格')
op, b, pushes = send_req(s, 20194, vf(1, 0) + vf(3, 2) + vf(4, 0) + vf(90, 172), 172)
stored = container_item(b, 2, 110305)
check(op == 20195 and stored == (0, 1), '批量取出 2 个后仓库剩 1 个')
# 存 100 铜币进仓库
op, b, pushes = send_req(s, 20196, vf(2, 100) + vf(90, 18), 18)
check(op == 20197 and fld(b, 2) == 100, '存铜币 100 进仓库 (20197)')
op, b, pushes = send_req(s, 20198, vf(2, 50) + vf(90, 19), 19)
check(op == 20199 and fld(b, 2) == 50, '取铜币 50 (20199) 库内剩 50')

# ---------- 6. 宠物 ----------
op, b, pushes = send_req(s, 20375, vf(90, 20), 20)
check(op == 20376 and (fld(b, 3) or 0) == 0, 'GetPetInfo petState=0 等待')
op, b, pushes = send_req(s, 20377, vf(90, 21), 21)
check(op == 20378 and not fld(b, 92), 'StartPetPlay 成功')
op, b, pushes = send_req(s, 20375, vf(90, 25), 25)
check(op == 20376 and (fld(b, 3) or 0) == 2, 'StartPetPlay 后 GetPetInfo petState=2')
remain = fld(b, 4) or 0
check(590000 <= remain <= 600000, '1级宠物嬉戏倒计时使用10分钟档（毫秒）')
op, b, pushes = send_req(s, 20385, vf(90, 22), 22)
check(op == 20386 and fld(b, 92), '未到点领奖应报错')
op, b, pushes = send_req(s, 20387, vf(90, 23), 23)
check(op == 20388 and fld(b, 1) == 10, 'GetPetQuickEndPrice voucher=10')
op, b, pushes = send_req(s, 20383, vf(90, 24), 24)
check(op == 20384 and not fld(b, 92), 'EndPetAction 快速完成成功')
op, b, pushes = send_req(s, 20375, vf(90, 26), 26)
check(op == 20376 and (fld(b, 3) or 0) == 0, '快速完成后 petState 回 0')

# ---------- 7. 聊天广播 ----------
op, b, pushes = send_req(s, 20298, sf(1, '大家好，我是测试玩家') + vf(2, 1) + vf(90, 26), 26)
chat = [bb for o, bb in pushes if o == 20300]
check(op == 20299 and not chat, 'world chat succeeds without server echo (client local echo)')

# ---------- 8. 签到 ----------
op, b, pushes = send_req(s, 20419, vf(90, 27), 27)
check(op == 20420 and not fld(b, 92), '今日签到成功 (20420)')
op, b, pushes = send_req(s, 20419, vf(90, 28), 28)
check(fld(b, 92), '重复签到应报错')

# ---------- 9. 传送门选图（主城→奇妙广场 10005） ----------
op, b, pushes = send_req(s, 20031, vf(1, 1000501) + vf(90, 29), 29)
cm = [bb for o, bb in pushes if o == 20033]
check(cm and fld(cm[0], 4) == 1000501, '传送门选图 1000501 -> M2C_ChangeMap')
op, b, pushes = send_req(s, 20031, vf(1, 1000401) + vf(90, 30), 30)
cm = [bb for o, bb in pushes if o == 20033]
check(cm and fld(cm[0], 4) == 1000401, '从广场回主城 1000401')

# ---------- 10. 星空旅行选图（10039 场景） ----------
op, b, pushes = send_req(s, 20031, vf(1, 1003901) + vf(90, 31), 31)
cm = [bb for o, bb in pushes if o == 20033]
check(cm and fld(cm[0], 4) == 1003901, '星空旅行选图 1003901 -> M2C_ChangeMap')
op, b, pushes = send_req(s, 20031, vf(1, 1000401) + vf(90, 32), 32)
op, b, pushes = send_req(s, 20393, vf(1, 0) + vf(90, 33), 33)
check(op == 20394 and not fld(b, 92), 'StartSpaceTravel index=0 成功')

# ---------- 11. 组队（双账号） ----------
s2, _ = login(acct + 'b', '新模块队友')
# 新账号出生在海滩；请求条 handler 依赖双方同场景 Unit，先把队友带到主城。
send_req(s2, 20031, vf(1, 10004) + vf(90, 39), 39)
# 互查 id：FindFriend(20109) 响应带 Id 字段
op, b, pushes = send_req(s, 20109, sf(1, '新模块队友') + vf(90, 40), 40)
target = fld(b, 1)
check(target is not None, 'FindFriend 查到队友 id=%s' % target)
op, b, pushes = send_req(s2, 20109, sf(1, '新模块测试') + vf(90, 41), 41)
my_id = fld(b, 1)
check(my_id is not None, 'FindFriend 查到我 id=%s' % my_id)
op, b, pushes = send_req(s, 20156, vf(1, my_id) + vf(90, 42), 42)
check(fld(b, 92), '邀请自己应报错')
op, b, pushes = send_req(s, 20156, vf(1, target) + vf(90, 43), 43)
check(op == 20157 and fld(b, 92).decode('utf-8') == '正在邀请...',
      '邀请组队显示发送中状态 (20157)')
op, b, pushes = send_req(s2, 20419, vf(90, 44), 44)  # 先 drain 掉 s2 上可能的杂帧
# s2 应收到 InviteList(20159)：但该推送到达 s2 的时间点不确定，重发一次邀请再查
op, b, pushes = send_req(s, 20156, vf(1, target) + vf(90, 45), 45)
inv = drain(s2, 1.0)
check(any(o == 20159 for o, _ in inv), 's2 收到 InviteList(20159)')
# s2 处理邀请（type=0 → IsRequest=false）：我加入邀请者(my_id)的队伍
# HandleInfo 是嵌套消息：field1(LEN) = [Id tag1, Bool tag2]；field2 = IsRequest varint
def msg_field(n, payload): return varint((n << 3) | 2) + varint(len(payload)) + payload
op, b, pushes = send_req(s2, 20160, msg_field(1, vf(1, my_id) + vf(2, 1)) + vf(2, 0) + vf(90, 48), 48)
check(op == 20161 and not fld(b, 92), '同意邀请 (20161)')
tm = drain(s2, 1.0) + drain(s, 1.0)
check(any(o == 20162 and fld(bb, 1) == my_id for o, bb in tm), '组队后推送 TeamMember(20162) LeaderId=自己')
op, b, pushes = send_req(s2, 20165, vf(90, 49), 49)
check(op == 20166 and not fld(b, 92), '退出队伍成功 (20166)')

s.close(); s2.close()
print()
print('=== 新模块回归全部通过：%d 项 ===' % ok)
