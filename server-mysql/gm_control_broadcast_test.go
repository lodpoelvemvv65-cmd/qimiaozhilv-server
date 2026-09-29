package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	json "github.com/goccy/go-json"
)

func TestGMAnnounceActionRegisteredAsServerScope(t *testing.T) {
	found := false
	for action := range gmServerActionRegistry {
		if action == "server.announce" {
			found = true
		}
	}
	if !found {
		t.Fatal("server.announce 没有注册到全服动作注册表")
	}
	// 也必须真的能通过分发找到（注册表写错名字会静默失效）。
	if ok, _, _, _, _ := dispatchRegisteredGMServerAction(nil, "server.announce", json.RawMessage(`{"lines":["a"]}`)); !ok {
		t.Fatal("server.announce 分发失败")
	}
}

func TestGMNormalizeAnnounceLines(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       []string
		wantLines []string
		wantCode  string
	}{
		{"单行", []string{"停机维护"}, []string{"停机维护"}, ""},
		{"多行保序", []string{"第一行", "第二行"}, []string{"第一行", "第二行"}, ""},
		{"丢弃空行与空白行", []string{"", "  第一行  ", "\n", "第二行"}, []string{"第一行", "第二行"}, ""},
		{"全部为空", []string{"", "   ", "\t"}, nil, "invalid_payload"},
		{"空切片", nil, nil, "invalid_payload"},
		// 上限只数有效行：10 行内容夹着空行仍然合法，空行不占额度。
		{"十行夹空行", []string{"", "1", "", "2", "3", "4", "5", "6", "7", "8", "9", "10"}, []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}, ""},
		{"十一行超限", []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, nil, "invalid_payload"},
		{"单行恰好上限", []string{strings.Repeat("系", gmAnnounceMaxLineRunes)}, []string{strings.Repeat("系", gmAnnounceMaxLineRunes)}, ""},
		{"单行超一个字符", []string{strings.Repeat("系", gmAnnounceMaxLineRunes+1)}, nil, "invalid_payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines, code, message := gmNormalizeAnnounceLines(tc.raw)
			if code != tc.wantCode {
				t.Fatalf("errorCode = %q, want %q", code, tc.wantCode)
			}
			if code != "" {
				if message == "" {
					t.Fatal("拒绝时必须给出中文原因，否则运营只看到「请求失败」")
				}
				return
			}
			if len(lines) != len(tc.wantLines) {
				t.Fatalf("行数 = %d, want %d (%v)", len(lines), len(tc.wantLines), lines)
			}
			for i := range lines {
				if lines[i] != tc.wantLines[i] {
					t.Fatalf("第 %d 行 = %q, want %q", i, lines[i], tc.wantLines[i])
				}
			}
		})
	}
}

// 单行长度按 rune 计：同样一句中文文案的「字数」和字节数差 3 倍，按字节算会让
// 运营看到的额度随文案语言变化。同时确认合法内容不被切断。
func TestGMAnnounceLineLimitCountsRunes(t *testing.T) {
	content := strings.Repeat("梦幻奇遇记", 24) // 120 个字符 / 360 字节
	if utf8.RuneCountInString(content) != gmAnnounceMaxLineRunes {
		t.Fatalf("用例前提错误：字符数 = %d", utf8.RuneCountInString(content))
	}
	lines, code, _ := gmNormalizeAnnounceLines([]string{content})
	if code != "" {
		t.Fatalf("恰好 %d 个中文字符应当合法，却返回 %q", gmAnnounceMaxLineRunes, code)
	}
	if len(lines) != 1 || lines[0] != content {
		t.Fatalf("合法内容被改动了：%q", strings.Join(lines, "|"))
	}
}

func TestGMBroadcastAnnounceRejectsBadPayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"非法 JSON", `{"lines":`},
		{"全部为空白行", `{"lines":["  ",""]}`},
		{"空数组", `{"lines":[]}`},
		{"缺 lines 字段", `{"centerBroadcast":true}`},
		{"行数超限", `{"lines":["1","2","3","4","5","6","7","8","9","10","11"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, code, message := gmBroadcastAnnounce(nil, json.RawMessage(tc.raw))
			if status != "rejected" {
				t.Fatalf("status = %q, want rejected", status)
			}
			if code != "invalid_payload" {
				t.Fatalf("errorCode = %q, want invalid_payload", code)
			}
			if message == "" {
				t.Fatal("拒绝时必须给出中文原因，否则运营只看到「请求失败」")
			}
		})
	}
}

// nil Server 下（没有在线角色）合法公告必须报 completed 而不是崩溃：
// 全服广播是「0 个在线角色也算执行成功」，与单角色命令的「角色不存在」不同。
func TestGMBroadcastAnnounceWithNoOnlinePlayers(t *testing.T) {
	status, data, code, msg := gmBroadcastAnnounce(nil, json.RawMessage(`{"lines":["停机维护"],"centerBroadcast":true}`))
	if status != "completed" || code != "" || msg != "" {
		t.Fatalf("status=%q code=%q msg=%q", status, code, msg)
	}
	if data["sent"] != 0 || data["online"] != 0 {
		t.Fatalf("data = %v, want sent=0 online=0", data)
	}
	// 结果要回传实际发出的文本，命令记录里才留得下「当时播了什么」。
	lines, ok := data["lines"].([]string)
	if !ok || len(lines) != 1 || lines[0] != "停机维护" {
		t.Fatalf("data[lines] = %#v, want [停机维护]", data["lines"])
	}
}

// ===================== 全服邮件（server.mail_all） =====================

func TestGMMailAllActionRegisteredAsServerScope(t *testing.T) {
	found := false
	for action := range gmServerActionRegistry {
		if action == "server.mail_all" {
			found = true
		}
	}
	if !found {
		t.Fatal("server.mail_all 没有注册到全服动作注册表")
	}
	if ok, _, _, _, _ := dispatchRegisteredGMServerAction(nil, "server.mail_all", json.RawMessage(`{"title":"标题"}`)); !ok {
		t.Fatal("server.mail_all 分发失败")
	}
}

func TestGMMailAllRejectsBadPayload(t *testing.T) {
	tooManyItems := make([]string, 101)
	for i := range tooManyItems {
		tooManyItems[i] = fmt.Sprintf(`{"itemId":110305,"count":1}`)
	}
	for _, tc := range []struct {
		name     string
		raw      string
		wantCode string
	}{
		{"非法 JSON", `{"title":`, "invalid_payload"},
		{"空标题", `{"title":"   ","content":"正文"}`, "invalid_payload"},
		// 64 个中文字符 = 192 字节，超过 191：上限按字节算，和单人发信一致。
		{"标题超 191 字节", `{"title":"` + strings.Repeat("维", 64) + `","content":"正文"}`, "invalid_payload"},
		{"正文超 4000 字节", `{"title":"标题","content":"` + strings.Repeat("维", 1334) + `"}`, "invalid_payload"},
		{"附件超过 100 项", `{"title":"标题","items":[` + strings.Join(tooManyItems, ",") + `]}`, "invalid_payload"},
		{"附件数量为 0", `{"title":"标题","items":[{"itemId":110305,"count":0}]}`, "invalid_item"},
		{"附件数量超上限", `{"title":"标题","items":[{"itemId":110305,"count":1000000001}]}`, "invalid_item"},
		{"附件 ID 为负", `{"title":"标题","items":[{"itemId":-1,"count":1}]}`, "invalid_item"},
		// tables 为 nil 时 gmItemExists 恒为 false，所以这条同时也是「未收录物品被拒」。
		{"附件物品不存在", `{"title":"标题","items":[{"itemId":110305,"count":1}]}`, "invalid_item"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, code, message := gmMailAll(nil, json.RawMessage(tc.raw))
			if status != "rejected" {
				t.Fatalf("status = %q, want rejected", status)
			}
			if code != tc.wantCode {
				t.Fatalf("errorCode = %q, want %q", code, tc.wantCode)
			}
			if message == "" {
				t.Fatal("拒绝时必须给出中文原因，否则运营只看到「请求失败」")
			}
		})
	}
}

// 校验全过之后才轮到存储。没有 store 时必须如实报错：既不能崩，也不能回一个
// 「已发送」——那会让运营以为几百个角色都收到了。无附件本身是合法的。
func TestGMMailAllWithoutStoreIsRejected(t *testing.T) {
	status, _, code, message := gmMailAll(nil, json.RawMessage(`{"title":"全服补偿","content":"感谢支持"}`))
	if status != "rejected" || code != "store_unavailable" || message == "" {
		t.Fatalf("status=%q code=%q msg=%q", status, code, message)
	}
}

// mail_id 全局唯一是那张 INSERT ... SELECT 的前提：所有角色共用同一个 id，
// 两次广播撞上同一个 id 就是主键冲突、整批回滚。这里有并发，因为命令消费可能在
// 多条流上并行（XReadGroup 的分发语义不保证单线程）。
func TestNextBroadcastMailIDIsMonotonicAndUnique(t *testing.T) {
	const workers, perWorker = 8, 200
	results := make(chan int64, workers*perWorker)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				results <- nextBroadcastMailID()
			}
		}()
	}
	wg.Wait()
	close(results)
	seen := make(map[int64]bool, workers*perWorker)
	for id := range results {
		if id <= 0 {
			t.Fatalf("mail_id 必须为正数：%d", id)
		}
		if seen[id] {
			t.Fatalf("mail_id 重复：%d", id)
		}
		seen[id] = true
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("拿到 %d 个不同的 mail_id, want %d", len(seen), workers*perWorker)
	}
}

// 一个角色领取邮件会把 mailMsg 标成已领取、附件标成已取走。如果所有在线角色共用
// 同一个指针，任何一个人领取都会让其他人的邮件变成「已领取」—— 全服邮件最惨的一种
// 故障：先点的人拿走全部，后点的人什么也没有。
func TestCloneBroadcastMailIsolatesRecipients(t *testing.T) {
	original := &mailMsg{Id: nextBroadcastMailID(), Title: "全服补偿", SenderName: "系统", State: 0,
		Items: []mailItemMsg{{ItemId: 110305, Count: 3, IsHasItem: true}}}
	first := cloneBroadcastMail(original)
	second := cloneBroadcastMail(original)
	// 第一个角色领取（onReceiveMail 会做的改动），还顺手加了一项附件。
	first.State = 1
	first.Items[0].IsHasItem = false
	first.Items = append(first.Items, mailItemMsg{ItemId: 120227, Count: 1, IsHasItem: true})
	if second.State != 0 || !second.Items[0].IsHasItem || len(second.Items) != 1 {
		t.Fatalf("一个角色领取影响到了另一个角色：%+v", second)
	}
	if original.State != 0 || !original.Items[0].IsHasItem || len(original.Items) != 1 {
		t.Fatalf("副本改动了模板本身：%+v", original)
	}
	if second.Id != original.Id || second.Title != original.Title || second.SenderName != original.SenderName {
		t.Fatalf("副本的邮件属性与模板不一致：%+v", second)
	}
}
