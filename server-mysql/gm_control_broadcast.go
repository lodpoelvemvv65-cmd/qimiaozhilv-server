package main

// gm_control_broadcast.go：全服作用域 GM 动作（公告、邮件）。
//
// 按 CLAUDE.md 的 GM 修改原则，这里不碰游戏服的既有实现文件：广播本身只是
// 「快照在线会话 + 逐个复用 sendSystemNotice」，放新文件里就够了，不需要改
// system_notice.go 或 boss.go。

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	json "github.com/goccy/go-json"
	"mhqserver/protocol"
)

const (
	// 公告的行数与单行长度上限。系统频道是按行渲染的，单行过长会顶破客户端 UI；
	// 而每次广播都要给每个在线角色各发一份，两个上限一起给单次命令的开销封顶。
	gmAnnounceMaxLines     = 10
	gmAnnounceMaxLineRunes = 120

	// 全服邮件的默认发件人。载荷里没写发件人时用它，避免全服邮件显示成空白发件人 ——
	// 单人 GM 邮件现在就是这个表现，几百个角色同时收到一封没有署名的信更像事故。
	gmMailAllDefaultSender = "系统"
)

func init() {
	registerGMServerAction("server.announce", gmBroadcastAnnounce)
	registerGMServerAction("server.mail_all", gmMailAll)
}

type gmAnnouncePayload struct {
	Lines           []string `json:"lines"`
	CenterBroadcast bool     `json:"centerBroadcast"`
}

// broadcastSystemNotice 向当前全部在线角色发一条系统公告，返回
// (成功发送人数, 在线角色数)。
//
// 必须在持有 s.mu 写锁之外调用：onlineChannels 内部取读锁，而 sync.RWMutex
// 不可重入，持写锁时调用会在同一个 goroutine 里自锁死（见 tcp_srv.go onClose
// 的注释「玩家离线广播卡死服务器」）。这里只在开头快照一次在线列表，发送全程
// 在锁外；也不像 boss.go 那样为了统计人数再取一次列表 —— 公告是逐连接写
// socket，两次快照之间玩家上下线会让「在线数」和「实际发送数」对不上。
func (s *Server) broadcastSystemNotice(lines []string, centerBroadcast bool) (int, int) {
	if s == nil {
		return 0, 0
	}
	channels := s.onlineChannels()
	sent := 0
	for _, ch := range channels {
		if ch == nil || ch.session == nil {
			continue
		}
		if s.sendSystemNotice(ch, lines, centerBroadcast) {
			sent++
		}
	}
	return sent, len(channels)
}

func gmBroadcastAnnounce(s *Server, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmAnnouncePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "公告参数错误"
	}
	lines, code, message := gmNormalizeAnnounceLines(payload.Lines)
	if code != "" {
		return "rejected", nil, code, message
	}
	sent, online := s.broadcastSystemNotice(lines, payload.CenterBroadcast)
	// lines 回传的是清洗后真正发出去的内容，而不是收到的参数：命令的 payload 在
	// 系统里只留了 payload_hash，事后想查「当时到底播了什么」只能靠结果。顺带
	// 也让前端能把实际发出的文本回显给操作人。
	return "completed", map[string]any{"sent": sent, "online": online, "lines": lines}, "", ""
}

// gmNormalizeAnnounceLines 清洗并校验公告内容，返回 (有效行, errorCode, message)。
//
// 空白行直接丢弃（sendSystemNotice 内部也会丢），因为 GM 后台的多行输入框很
// 容易多出一个空行；但整条公告一行有效内容都没有时必须报错 —— 否则运营会拿到
// 一个「发送 0 人」的假成功，以为公告已经发到线上了。上限也只数有效行，空行
// 不占额度。
func gmNormalizeAnnounceLines(raw []string) ([]string, string, string) {
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if utf8.RuneCountInString(trimmed) > gmAnnounceMaxLineRunes {
			return nil, "invalid_payload", "单行公告过长"
		}
		lines = append(lines, trimmed)
	}
	if len(lines) == 0 {
		return nil, "invalid_payload", "公告内容不能为空"
	}
	if len(lines) > gmAnnounceMaxLines {
		return nil, "invalid_payload", "公告行数过多"
	}
	return lines, "", ""
}

// ===================== 全服邮件（server.mail_all） =====================

var (
	gmBroadcastMailMu  sync.Mutex
	gmBroadcastMailSeq int64
)

// nextBroadcastMailID 返回全服邮件用的全局唯一 mail_id。
//
// player_mails 的主键是 (player_id, mail_id)，所以 mail_id 本来只需要在一个角色
// 内部唯一。全服邮件要一张 SQL 写完所有角色，就必须让所有角色共用同一个 id ——
// 否则每个角色各分一个 id，那就不再是一条 INSERT ... SELECT 能解决的事了。
// 用单调 nanotime 而不是自增计数：进程重启后计数会从 0 重来，撞上库里已有的
// 邮件就是覆盖（INSERT 会直接失败并让整批回滚），nanotime 不会。
func nextBroadcastMailID() int64 {
	gmBroadcastMailMu.Lock()
	defer gmBroadcastMailMu.Unlock()
	next := time.Now().UnixNano()
	if next <= gmBroadcastMailSeq {
		next = gmBroadcastMailSeq + 1
	}
	gmBroadcastMailSeq = next
	return next
}

func gmMailAll(s *Server, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmMailPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "邮件内容错误"
	}
	// 标题和正文的上限与单人 gmSendMail 逐字一致（含按字节计）。两个入口对
	// 「这封信能不能发」必须给同一个答案，否则运营会看到同一封邮件单人能发、
	// 全服被拒，而且 db 列宽不会因为收件人变多而变化。
	title := strings.TrimSpace(payload.Title)
	if title == "" || len(title) > 191 || len(payload.Content) > 4000 {
		return "rejected", nil, "invalid_payload", "邮件内容错误"
	}
	if len(payload.Items) > 100 {
		return "rejected", nil, "invalid_payload", "邮件附件过多"
	}
	sender := truncateGM(payload.SenderName, 64)
	if sender == "" {
		sender = gmMailAllDefaultSender
	}
	mail := &mailMsg{Id: nextBroadcastMailID(), Title: title, Content: payload.Content,
		SenderName: sender, State: 0, RemainTime: payload.RemainTime}
	for _, item := range payload.Items {
		if item.ItemID <= 0 || item.Count <= 0 || item.Count > 1000000000 || !gmItemExists(item.ItemID) {
			return "rejected", nil, "invalid_item", "邮件附件无效"
		}
		mail.Items = append(mail.Items, mailItemMsg{ItemId: item.ItemID, Count: item.Count, IsHasItem: true})
	}
	if s == nil || s.store == nil || s.store.db == nil {
		return "rejected", nil, "store_unavailable", "邮件保存失败"
	}
	total, err := s.persistBroadcastMail(mail)
	if err != nil {
		return "rejected", nil, "persist_failed", "邮件保存失败"
	}
	online := s.deliverBroadcastMail(mail)
	return "completed", map[string]any{
		"mailId": strconvI64(mail.Id), "total": total, "online": online, "offline": total - online,
		"items": len(mail.Items), "title": title,
	}, "", ""
}

// persistBroadcastMail 用一个事务把同一封邮件写给全部角色，返回收件角色数。
//
// 不能对每个角色调 saveGMOffline：那条路是 DELETE 掉 15 张表再逐条重写，还要抢
// 全局 gmOfflineMu 串行化，上百个角色就是上万次写。这里只插 player_mails 和
// player_mail_items 两张表，附件有多少项就几条语句，与角色数无关。
func (s *Server) persistBroadcastMail(mail *mailMsg) (int, error) {
	tx, err := s.store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO player_mails (player_id, mail_id, title, content, sender_name, state, remain_time)
		SELECT p.id, ?, ?, ?, ?, ?, ? FROM players p`,
		mail.Id, mail.Title, mail.Content, mail.SenderName, mail.State, mail.RemainTime)
	if err != nil {
		return 0, err
	}
	for position, item := range mail.Items {
		if _, err := tx.Exec(`INSERT INTO player_mail_items (player_id, mail_id, position, item_id, item_count, is_locked, is_has_item)
			SELECT p.id, ?, ?, ?, ?, ?, ? FROM players p`,
			mail.Id, position, item.ItemId, item.Count, item.IsLock, item.IsHasItem); err != nil {
			return 0, err
		}
	}
	// 行数要在 Commit 之前取：Commit 之后这个 Result 就不再保证有定义了。
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(affected), nil
}

// deliverBroadcastMail 把刚落库的全服邮件追加进每个在线角色的会话，并让客户端
// 重新拉一次邮件页，返回送达的在线角色数。
//
// 必须在 SQL commit 之后调用。在线角色的邮件在库里已经有一行了（批量直插连他们
// 一起写了），这里只是把同一封邮件放进内存会话，让他们立刻看到；他们下一次保存会
// 按内存内容重写同一行，mail_id 相同所以是覆盖而不是新增 —— 这个「自愈」依赖的
// 正是两条路径用同一个 mail_id。
func (s *Server) deliverBroadcastMail(mail *mailMsg) int {
	if s == nil || mail == nil {
		return 0
	}
	channels := s.onlineChannels()
	delivered := 0
	for _, ch := range channels {
		if ch == nil || ch.session == nil {
			continue
		}
		ss := ch.session
		ss.battleMu.Lock()
		ss.mails = append(ss.mails, cloneBroadcastMail(mail))
		if mail.Id > ss.mailSeq {
			ss.mailSeq = mail.Id
		}
		page := gmMailPushPage(len(ss.mails))
		ss.battleMu.Unlock()
		// 推送放在锁外：onGetMail 内部可能落库，而且广播要顺序走过所有在线角色，
		// 不能把他们各自的 battleMu（战斗线程也在抢）串成一条长队。
		if ch.conn != nil {
			_ = s.onGetMail(ch, &protocol.C2M_GetMail{Page: page})
		}
		delivered++
	}
	return delivered
}

// cloneBroadcastMail 给每个在线角色一份独立的邮件副本。
//
// 领取会把 mailMsg 的 State 改成已领取、附件标成已取走，所以绝对不能把同一个
// 指针塞进所有会话 —— 一个人领了，其他人打开邮件就变成已领取。
func cloneBroadcastMail(mail *mailMsg) *mailMsg {
	clone := *mail
	clone.Items = append([]mailItemMsg(nil), mail.Items...)
	return &clone
}
