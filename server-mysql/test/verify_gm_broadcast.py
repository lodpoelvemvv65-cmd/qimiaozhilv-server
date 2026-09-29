# -*- coding: utf-8 -*-
"""GM HTTP -> Redis Stream -> 游戏服 TCP 的全服公告 + 全服邮件实测。

运行方式：MHQ_TEST_GM_BROADCAST_TCP=1 go test ./cmd/gm-api -run TestGMBroadcastLiveTCP -v
Go 侧只提供临时内存会话（审计 admin_id=0），不需要 GM 密码，也不碰任何真实管理员。

为什么至少要两个角色在线：只测一个角色无法区分「真的广播了」和「恰好发给了某一个」。
为什么要有离线角色：公告不落库，离线角色重登后不该收到历史公告 —— 而且这条断言必须
建立在本轮公告确实发出去过之上（B 的收到就是证据），否则它可能只是空转。全服邮件反过来
必须在离线角色身上验：它落库，离线角色重登后一定要看到。
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

sys.stdout.reconfigure(encoding="utf-8")
HOST = os.environ.get("MHQ_TEST_HOST", "127.0.0.1")
PORT = int(os.environ.get("MHQ_TEST_PORT", "7756"))
API = os.environ["GM_TEST_API_BASE"]
SESSIONS = {
    "operator": (os.environ["GM_TEST_SESSION"], os.environ["GM_TEST_CSRF"]),
    # support 有单角色命令权限，但没有 server.broadcast：全服权限必须真的只给显式授权角色。
    "noperm": (os.environ["GM_TEST_NOPERM_SESSION"], os.environ["GM_TEST_NOPERM_CSRF"]),
    # 只授了 server.broadcast 的角色：证明全服邮件是被 server.mail_all 单独把守的，
    # 而不是「有任何一个全服权限就能发全服邮件」。
    "announceonly": (os.environ["GM_TEST_ANNOUNCEONLY_SESSION"], os.environ["GM_TEST_ANNOUNCEONLY_CSRF"]),
}
SYSTEM_CHAT_OPCODE = 20301
CHAT_TYPE_SYSTEM = 7
GET_BAG_OPCODE = 20258
GET_MAIL_OPCODE = 20281
RECEIVE_MAIL_OPCODE = 20283
# 超小型生命药水。带它的全服邮件能同时验证「附件进邮箱」和「领取后进背包」，
# 而且它不在 110201-110204 那几种直接结算的货币 ID 里。
ITEM_ID = 110305


def varint(value):
    result = bytearray()
    while value > 127:
        result.append((value & 127) | 128)
        value >>= 7
    result.append(value)
    return bytes(result)


def vf(tag, value):
    return varint(tag << 3) + varint(value)


def sf(tag, text):
    raw = text.encode("utf-8")
    return varint((tag << 3) | 2) + varint(len(raw)) + raw


def fields(raw):
    pos = 0

    def read_int():
        nonlocal pos
        value = shift = 0
        while True:
            byte = raw[pos]
            pos += 1
            value |= (byte & 127) << shift
            if not byte & 128:
                return value
            shift += 7

    while pos < len(raw):
        key = read_int()
        tag, wire = key >> 3, key & 7
        if wire == 0:
            value = read_int()
        elif wire == 2:
            size = read_int()
            value = raw[pos:pos + size]
            pos += size
        elif wire in (1, 5):
            size = 8 if wire == 1 else 4
            value = raw[pos:pos + size]
            pos += size
        else:
            raise AssertionError(f"unsupported protobuf wire {wire}")
        yield tag, value


def field(raw, tag, default=0):
    return next((value for key, value in fields(raw) if key == tag), default)


def notice_lines(body):
    """M2C_SendSystemChat 的 [1] 是 repeat string：每一行各占一个 tag 1 字段。"""
    return [raw.decode("utf-8") for tag, raw in fields(body) if tag == 1]


# Mail{Title=2, Content=3, RemainTime=4, SenderName=5, RewordArr=6(repeated), State=7, Id=8}
# 与 server-mysql/mail.go 的 encodeMail 一一对应；RewordArr 是字段级 repeated，
# protoc 生成代码里没有，只能按 tag 手工拆。
def mail_list(body):
    """M2C_GetMail 的 [1] 是 repeat Mail：每封各占一个 tag 1 字段。"""
    return [raw for tag, raw in fields(body) if tag == 1]


def mail_title(mail):
    return field(mail, 2, b"").decode("utf-8")


def mail_sender(mail):
    return field(mail, 5, b"").decode("utf-8")


def mail_state(mail):
    return field(mail, 7, 0)


def mail_id_of(mail):
    return field(mail, 8, 0)


def mail_items(mail):
    """RewordArr：每条 MailItem 是 {ItemId=1, Count=4}。"""
    return [(field(raw, 1, 0), field(raw, 4, 0)) for tag, raw in fields(mail) if tag == 6]


def bag_counts(body):
    """M2C_GetBag 的 [1] 是 repeat BagMap：{Index=1, NetItem=2{ItemId=1, Count=4}}。"""
    counts = {}
    for tag, raw in fields(body):
        if tag != 1:
            continue
        net = field(raw, 2, b"")
        if net:
            item_id = field(net, 1, 0)
            counts[item_id] = counts.get(item_id, 0) + field(net, 4, 0)
    return counts


class Game:
    def __init__(self):
        self.sock = socket.create_connection((HOST, PORT), timeout=5)
        self.sock.settimeout(5)
        self.rpc_id = 0
        self.pushes = []

    def read(self, size):
        data = b""
        while len(data) < size:
            more = self.sock.recv(size - len(data))
            if not more:
                raise ConnectionError("game disconnected")
            data += more
        return data

    def rpc(self, opcode, payload=b"", allow_message=False):
        self.rpc_id += 1
        raw = payload + vf(90, self.rpc_id)
        self.sock.sendall(struct.pack("<HH", len(raw) + 2, opcode) + raw)
        for _ in range(1000):
            total, op = struct.unpack("<HH", self.read(4))
            body = self.read(total - 2)
            if op == opcode + 1 and field(body, 90) == self.rpc_id:
                assert field(body, 91) == 0, (op, list(fields(body)))
                if not allow_message:
                    assert not field(body, 92, b""), (op, field(body, 92))
                return body
            self.pushes.append((op, body))
        raise TimeoutError("RPC response missing")

    def login(self, account, password, create=False, enter=True):
        if create:
            self.rpc(20010, sf(1, account) + sf(2, password))
        login = self.rpc(20008, sf(1, account) + sf(2, password))
        gate = self.rpc(20014, vf(1, field(login, 2)) + vf(2, field(login, 3, 1)))
        player_id = field(gate, 1)
        if create:
            self.rpc(20016, vf(1, player_id) + vf(2, 1) + sf(3, "全服公告" + account[-6:]))
        if enter:
            self.rpc(20027)
        return player_id

    def collect(self):
        """把服务端已经推过来的帧收进 self.pushes。

        发一个必定成功的只读 RPC：rpc() 在等到匹配响应之前会把队列里排在它前面的
        主动推送全部记进 pushes，所以这一下等价于「把 socket 读空」。
        """
        self.rpc(20258)
        return self.pushes

    def notices(self, since=0):
        return [body for op, body in self.pushes[since:] if op == SYSTEM_CHAT_OPCODE]

    def mailbox(self, page=1):
        """拉一页邮件，返回 (本页邮件, 总页数)。"""
        body = self.rpc(GET_MAIL_OPCODE, vf(1, page))
        return mail_list(body), field(body, 2, 0)

    def all_mails(self):
        """把所有页拼起来。新邮件追加在末尾，角色已有 10 封以上时不在第 1 页 ——
        这正是「GM 发信固定推第 1 页」那个 bug 的由来，所以这里不能只看第 1 页。"""
        mails, pages = self.mailbox(1)
        for page in range(2, max(1, pages) + 1):
            mails = mails + self.mailbox(page)[0]
        return mails

    def find_mail(self, title):
        return next((mail for mail in self.all_mails() if mail_title(mail) == title), None)

    def mails_titled(self, title):
        return [mail for mail in self.all_mails() if mail_title(mail) == title]

    def receive(self, mail_id, page=1):
        """领取附件。允许带 Message：被拒（如「邮件已领取」）也走同一条返回路径。"""
        return self.rpc(RECEIVE_MAIL_OPCODE, vf(1, mail_id) + vf(2, page), allow_message=True)

    def bag_count(self, item_id):
        return bag_counts(self.rpc(GET_BAG_OPCODE)).get(item_id, 0)


def call(path, payload=None, key=None, expected=200, session="operator", csrf=True):
    """调用 GM API。key="" 表示故意不带 Idempotency-Key，csrf=False 表示故意不带 CSRF。"""
    headers = {"Accept": "application/json"}
    if session:
        session_id, token = SESSIONS[session]
        headers["Cookie"] = f"mhq_gm_session={session_id}" + (f"; mhq_gm_csrf={token}" if csrf else "")
        if csrf:
            headers["X-CSRF-Token"] = token
    body = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        if key != "":
            headers["Idempotency-Key"] = key if key else uuid.uuid4().hex
        body = json.dumps({"payload": payload}, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(API + "/api/v1" + path, data=body, headers=headers)
    try:
        response = urllib.request.urlopen(request, timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        envelope = json.load(response)
        assert response.status == expected, (response.status, expected, envelope)
        return envelope.get("data") if expected == 200 else envelope


def expect_rejected(lines, why, center=False):
    """被拒的公告必须落在 422 且带中文原因，而且一个字符都不许广播出去。"""
    error = call("/broadcast/announce", {"lines": lines, "centerBroadcast": center}, expected=422)
    assert error["error"]["code"] == "invalid_payload", (why, error)
    assert error["error"]["message"], (why, "拒绝时必须给出中文原因")
    return error


def report_mail_id(mail_id):
    """把刚投递出去的全服邮件 id 立刻打出来（flush），供 Go 侧跑完精确清理。

    全服邮件的收件人是库里每一个角色，测试角色只是其中两个：本机库还有上百个别的
    角色，跑一轮测试就往他们邮箱里塞一封。删行必须按 mail_id 精确删 —— 库里正常的
    单收件人邮件（战斗奖励、在线奖励）同样是 nanotime 生成的 id，按阈值一刀切会毁
    掉真实数据。立刻打印而不是最后统一打印：脚本崩在半路也不会漏掉已投递的那几封。
    """
    print(f"BROADCAST_MAIL_ID={mail_id}", flush=True)


def wait_online(expected, timeout=15.0):
    """等在线数降到 expected。断线的 marker 清理不是瞬时的，直接断言会偶发假失败，
    而「B 确实离线」是这一节的前提 —— 不能靠「我刚关了 socket」自行宣布。"""
    deadline = time.time() + timeout
    online = None
    while time.time() < deadline:
        online = int(call("/broadcast/audience")["online"])
        if online == expected:
            return online
        time.sleep(0.3)
    raise AssertionError(f"在线数没有降到 {expected}，{timeout} 秒后仍是 {online}")


def broadcast_mail_section(a, b, account_a, account_b, password, id_a, id_b, expected_players):
    """全服邮件：在线即时收到 + 离线落库补收、附件领取、幂等、权限分离。

    返回可能被重连过的 (a, b)，调用方要接住 —— 后面的清理和断言用的是新连接。
    """
    # 先让 B 下线：全服邮件必须靠落库送到不在线的人，这是它与公告最本质的区别。
    b.sock.close()
    wait_online(1)

    title = "全服补偿"
    a.collect()
    mark_a = len(a.pushes)
    key = uuid.uuid4().hex
    sent = call("/broadcast/mail", {"title": title, "content": "感谢各位的支持。", "items": [{"itemId": ITEM_ID, "count": 7}]}, key)
    assert sent["status"] == "completed", sent
    data = sent["data"]
    assert data["title"] == title, data
    # 收件人必须是全部角色：B 已经离线却仍在总数里，说明批量落库覆盖了离线角色。
    assert int(data["total"]) == expected_players, (data, expected_players)
    assert int(data["online"]) + int(data["offline"]) == int(data["total"]), data
    assert int(data["offline"]) >= 1, data
    assert int(data["items"]) == 1, data
    mail_id = int(data["mailId"])
    assert mail_id > 0, data
    report_mail_id(mail_id)
    print(f"PASS 全服邮件落库 {data['total']} 个角色（在线 {data['online']} / 离线 {data['offline']}），mail_id={mail_id}")

    # 在线角色必须立刻收到推送，否则运营发完看不到任何反馈，会以为没发出去。
    a.collect()
    pushed = [body for op, body in a.pushes[mark_a:] if op == GET_MAIL_OPCODE + 1]
    assert pushed, "在线的 A 没有收到新邮件推送"
    pushed_titles = [mail_title(mail) for body in pushed for mail in mail_list(body)]
    assert title in pushed_titles, pushed_titles
    print("PASS 在线角色立刻收到 20282 推送，新邮件在推送的那一页里")

    mail_a = a.find_mail(title)
    assert mail_a is not None, "A 的邮箱里没有这封全服邮件"
    assert mail_items(mail_a) == [(ITEM_ID, 7)], mail_items(mail_a)
    assert mail_state(mail_a) == 0, "刚收到的邮件不该是已领取状态"
    assert mail_sender(mail_a) == "系统", mail_sender(mail_a)
    assert mail_id_of(mail_a) == mail_id, (mail_id_of(mail_a), mail_id)

    before = a.bag_count(ITEM_ID)
    a.receive(mail_id)
    after = a.bag_count(ITEM_ID)
    assert after - before == 7, (before, after)
    print("PASS A 领取附件后物品进背包，数量正确（+7）")

    again = a.receive(mail_id)
    assert field(again, 92, b"").decode("utf-8") == "邮件已领取", list(fields(again))
    assert a.bag_count(ITEM_ID) == after, "重复领取又把附件发了一次"
    print("PASS 重复领取被拒（邮件已领取），背包数量不变")

    # 附件 ID 非法：在 GM API 就被拒，一个角色都不许收到。批量直插是一整条 SQL，
    # 「部分写入」意味着漏掉了回滚 —— 那样运营会看到一条失败的命令记录，角色手上却有东西。
    bad_title = "不该出现的邮件"
    error = call("/broadcast/mail", {"title": bad_title, "content": "正文", "items": [{"itemId": 999999999, "count": 1}]}, expected=422)
    assert "不存在" in error["error"]["message"], error
    assert a.mails_titled(bad_title) == [], "A 收到了附件非法的邮件"
    print("PASS 附件物品 ID 不存在时整条命令被拒，没有角色收到邮件")

    # 幂等：同一封信重复投递是运营最难收拾的事故，键必须真的生效。
    once_title = "幂等实测邮件"
    replay_key = uuid.uuid4().hex
    first = call("/broadcast/mail", {"title": once_title, "content": "只应有一封"}, replay_key)
    assert first["status"] == "completed", first
    report_mail_id(int(first["data"]["mailId"]))
    second = call("/broadcast/mail", {"title": once_title, "content": "只应有一封"}, replay_key)
    assert second["data"]["mailId"] == first["data"]["mailId"], (first, second)
    assert second["data"]["total"] == first["data"]["total"], (first, second)
    conflict = call("/broadcast/mail", {"title": "换了标题", "content": "只应有一封"}, replay_key, expected=409)
    assert conflict["error"]["message"], conflict
    same = a.mails_titled(once_title)
    assert len(same) == 1, f"幂等重放产生了 {len(same)} 封同名邮件"
    assert mail_id_of(same[0]) == int(first["data"]["mailId"]), (mail_id_of(same[0]), first["data"]["mailId"])
    print("PASS 幂等：同键重放不产生第二封邮件，同键换参数被 409 拒绝")

    # 权限分离：只授了 server.broadcast 的角色能过公告那关、被全服邮件挡下。
    payload = {"title": "无权发送", "content": "无权发送"}
    error = call("/broadcast/mail", payload, expected=403, session="announceonly")
    assert error["error"]["code"] == "forbidden", error
    # 公告能过权限关：会被更靠后的 CSRF 挡下，errorCode 因此不是 forbidden。
    error = call("/broadcast/announce", {"lines": ["权限分离实测"]}, expected=403, session="announceonly", csrf=False)
    assert error["error"]["code"] == "csrf_failed", error
    error = call("/broadcast/mail", payload, expected=403, session="noperm")
    assert error["error"]["code"] == "forbidden", error
    error = call("/broadcast/mail", payload, expected=403, csrf=False)
    assert error["error"]["code"] == "csrf_failed", error
    error = call("/broadcast/mail", payload, key="", expected=400)
    assert error["error"]["code"] == "idempotency_required", error
    print("PASS 只授公告权的角色发不了全服邮件；无 server.mail_all 403、缺 CSRF 403、缺幂等键 400")

    # 离线角色重登后必须看到这封邮件。这是「批量直插 + 在线会话追加」两条路径里的
    # 落库那一条，也是全服邮件唯一对离线玩家生效的方式。
    b = Game()
    assert b.login(account_b, password) == id_b
    mail_b = b.find_mail(title)
    assert mail_b is not None, "离线角色重登后没有看到这封全服邮件"
    assert mail_items(mail_b) == [(ITEM_ID, 7)], mail_items(mail_b)
    assert mail_state(mail_b) == 0, "离线角色收到的邮件状态不对"
    assert mail_id_of(mail_b) == mail_id, (mail_id_of(mail_b), mail_id)
    b.receive(mail_id)
    assert b.bag_count(ITEM_ID) >= 7, "离线角色领取附件后背包里没有物品"
    print("PASS 离线角色 B 重登后收到同一封邮件（同 mail_id），附件能正常领取")

    # A 重连一次：在线会话追加的那份邮件必须和库里那一行是同一封。如果 mail_id
    # 不一致，重连后要么看到两封，要么这一封消失 —— 合并两条路径的唯一纽带就是它。
    a.sock.close()
    a = Game()
    assert a.login(account_a, password) == id_a
    rehydrated = a.mails_titled(title)
    assert len(rehydrated) == 1, f"重连后同名邮件有 {len(rehydrated)} 封"
    assert mail_id_of(rehydrated[0]) == mail_id, (mail_id_of(rehydrated[0]), mail_id)
    assert mail_state(rehydrated[0]) == 1, "已领取的状态没有持久化"
    assert a.bag_count(ITEM_ID) >= 7, "重连后背包里的附件没了"
    print("PASS 在线角色重连后邮件不重不丢：同 mail_id、已领取状态与背包都持久化")
    return a, b


def main():
    account_a = os.environ["GM_TEST_ACCOUNT_A"]
    account_b = os.environ["GM_TEST_ACCOUNT_B"]
    assert account_a.startswith("gmbcast") and HOST in ("127.0.0.1", "localhost"), "local isolated tests only"
    password = "Test" + uuid.uuid4().hex[:12]
    a = Game()
    id_a = a.login(account_a, password, create=True)
    b = Game()
    id_b = b.login(account_b, password, create=True)
    try:
        # 受众预览必须报出真实角色总数。GM_TEST_PLAYERS_BEFORE 是 Go 侧在本脚本
        # 建角色之前读到的 COUNT(*)；本机测试期间没有别人登录，+2 就是这两个角色。
        audience = call("/broadcast/audience")
        expected_players = int(os.environ["GM_TEST_PLAYERS_BEFORE"]) + 2
        assert int(audience["totalPlayers"]) == expected_players, (audience, expected_players)
        assert int(audience["online"]) >= 2, audience
        assert audience["onlineSource"], audience
        print(f"PASS 受众预览：全服 {audience['totalPlayers']} 个角色，在线 {audience['online']} 人")

        text = ["停机维护通知", "预计 22:00 恢复服务"]
        mark_a, mark_b = len(a.pushes), len(b.pushes)
        result = call("/broadcast/announce", {"lines": text, "centerBroadcast": True})
        assert result["status"] == "completed", result
        assert result["data"]["lines"] == text, result
        assert result["data"]["sent"] >= 2 and result["data"]["online"] >= 2, result
        a.collect()
        b.collect()
        for game, mark, who in ((a, mark_a, "A"), (b, mark_b, "B")):
            frames = [body for body in game.notices(mark) if notice_lines(body) == text]
            assert frames, f"角色 {who} 没有收到全服公告"
            frame = frames[0]
            assert field(frame, 2) == CHAT_TYPE_SYSTEM, (who, list(fields(frame)))
            assert field(frame, 4).decode("utf-8") == "系统", who
            # 中央横幅走的是 IsSystemBrocast，客户端只有收到 1 才会弹屏幕正中那条。
            assert field(frame, 5) == 1, (who, "中央横幅标志没有传到客户端")
        print("PASS 两个在线角色都收到 20301：系统频道、发件人「系统」、中央横幅标志正确")

        # 关掉横幅时必须真的不带该标志（proto3 的 false 不会上线，字段应缺省）。
        mark_a = len(a.pushes)
        plain = ["例行维护已完成"]
        assert call("/broadcast/announce", {"lines": plain, "centerBroadcast": False})["status"] == "completed"
        a.collect()
        frames = [body for body in a.notices(mark_a) if notice_lines(body) == plain]
        assert frames and field(frames[0], 5) == 0, list(fields(frames[0])) if frames else "没收到"
        print("PASS 不开横幅时 IsSystemBrocast 为 0，公告仍进系统频道")

        # 内容校验只在游戏服做，这里确认它真的会拒绝，而不是「前端拦住了」。
        for lines, why in (([], "空公告"), (["1"] * 11, "11 行超限"), (["维"] * 121, "单行 121 字超限")):
            a.collect()  # 取标记前先把 socket 读空，标记才是「从这一刻起有没有新推送」
            mark_a = len(a.pushes)
            expect_rejected(lines, why)
            a.collect()
            assert not a.notices(mark_a), f"{why} 被拒之后仍然广播了出去"
        print("PASS 空公告 / 行数超限 / 单行超长都在游戏服被拒，且拒绝时一个字符都没发出去")

        # 幂等：全服公告不可撤回，双击和网络重试都不能变成第二条公告。
        replay = ["幂等实测"]
        key = uuid.uuid4().hex
        first = call("/broadcast/announce", {"lines": replay, "centerBroadcast": False}, key)
        # 先把第一条公告的推送读干净再取标记：否则重放之后读到的会是第一条，而不是
        # 证据表明重放又发了一次（同一个 socket 缓冲里排队，len(pushes) 追不上）。
        a.collect()
        mark_a = len(a.pushes)
        second = call("/broadcast/announce", {"lines": replay, "centerBroadcast": False}, key)
        assert second["requestId"] == first["requestId"], (first, second)
        assert second["data"]["sent"] == first["data"]["sent"], (first, second)
        a.collect()
        assert not a.notices(mark_a), "同一幂等键重放又广播了一次"
        conflict = call("/broadcast/announce", {"lines": ["换了内容"], "centerBroadcast": False}, key, expected=409)
        assert conflict["error"]["message"], conflict
        print("PASS 幂等：同键重放不重复广播，同键换内容被 409 拒绝")

        # 权限与入口校验（这三个路径不在 main.go 的权限分发表里，漏校验会直接放行）。
        for path, payload in (("/broadcast/audience", None), ("/broadcast/announce", {"lines": replay}), ("/broadcast/mail", {"title": "无权", "content": "无权"})):
            error = call(path, payload, expected=403, session="noperm")
            assert error["error"]["code"] == "forbidden", (path, error)
        error = call("/broadcast/announce", {"lines": replay}, expected=403, csrf=False)
        assert error["error"]["code"] == "csrf_failed", error
        error = call("/broadcast/announce", {"lines": replay}, key="", expected=400)
        assert error["error"]["code"] == "idempotency_required", error
        print("PASS 无 server.broadcast 的角色 403；缺 CSRF 403；缺 Idempotency-Key 400")

        # 离线不补发。这条断言只有在 B 确实收到了本轮公告的前提下才有意义，所以先验 B。
        offline = ["离线角色不该收到这条"]
        mark_b = len(b.pushes)
        a.sock.close()
        assert call("/broadcast/announce", {"lines": offline, "centerBroadcast": False})["status"] == "completed"
        b.collect()
        assert any(notice_lines(body) == offline for body in b.notices(mark_b)), \
            "B 没收到这条公告，离线断言会退化成空转"
        a = Game()
        assert a.login(account_a, password) == id_a
        a.collect()
        # 只解析 20301 的帧：重登那一串 login 流程帧里还有别的消息，按系统频道去解
        # 会撞上非字符串字段的 wire type。
        assert not [body for body in a.notices() if notice_lines(body) == offline], "离线角色重登后收到了历史公告"
        print("PASS 公告只发给在线角色：A 离线期间 B 收到，A 重登后没有补发")

        a, b = broadcast_mail_section(a, b, account_a, account_b, password, id_a, id_b, expected_players)

        # 公告只写会话内存、不改角色数据，这里确认重连后角色仍能被 GM API 正常读到。
        assert call(f"/players/{id_a}")["id"] == str(id_a)
        assert call(f"/players/{id_b}")["id"] == str(id_b)
        print("PASS 公告流程没有影响角色持久化数据，两个测试角色仍可正常读取")
    finally:
        for game in (a, b):
            try:
                game.sock.close()
            except OSError:
                pass
        for account, player_id in ((account_a, id_a), (account_b, id_b)):
            cleanup = Game()
            cleanup.login(account, password, enter=False)
            cleanup.rpc(20018, vf(1, player_id))
            cleanup.sock.close()
        print("PASS 两个临时测试角色已通过原生删除角色协议移除")


if __name__ == "__main__":
    main()
