package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func useOnlineRewardConfig(t *testing.T, interval time.Duration, voucher int32, expiryDays int32) {
	t.Helper()
	previous := gameplayConfigSnapshot()
	config := operationsconfig.Defaults()
	config.OnlineReward.IntervalMS = interval.Milliseconds()
	config.OnlineReward.VoucherAmount = voucher
	config.OnlineReward.MailExpireDays = expiryDays
	storeGameplayConfig(config)
	t.Cleanup(func() { storeGameplayConfig(previous) })
}

func newOnlineRewardTestSession(now time.Time, accumulatedMS int64) (*session, *channel) {
	ss := newSession()
	ss.state = sessInGame
	ss.playerID = 7
	ss.signin = &signinState{OnlineRewardMS: accumulatedMS}
	ss.startOnlineRewardClock(now)
	return ss, &channel{id: 1, session: ss}
}

func TestOnlineRewardAccumulatesWithoutEarlyMail(t *testing.T) {
	useOnlineRewardConfig(t, time.Hour, 100, 30)
	start := time.Unix(1_800_000_000, 0)
	ss, ch := newOnlineRewardTestSession(start, 10*time.Minute.Milliseconds())

	if count := checkpointOnlineReward(ch, start.Add(20*time.Minute)); count != 0 {
		t.Fatalf("early checkpoint generated %d rewards", count)
	}
	if len(ss.mails) != 0 || ss.signin.OnlineRewardMS != 30*time.Minute.Milliseconds() {
		t.Fatalf("early state mails=%d accumulated=%d", len(ss.mails), ss.signin.OnlineRewardMS)
	}
}

func TestOnlineRewardCreatesWelcomeAndVoucherMails(t *testing.T) {
	useOnlineRewardConfig(t, time.Hour, 100, 30)
	start := time.Unix(1_800_000_000, 0)
	ss, ch := newOnlineRewardTestSession(start, 30*time.Minute.Milliseconds())

	if count := checkpointOnlineReward(ch, start.Add(2*time.Hour+35*time.Minute)); count != 3 {
		t.Fatalf("completed rewards = %d, want 3", count)
	}
	if ss.signin.OnlineRewardMS != 5*time.Minute.Milliseconds() {
		t.Fatalf("residual = %d, want five minutes", ss.signin.OnlineRewardMS)
	}
	if len(ss.mails) != 4 || ss.mails[0].Id != 1 || ss.mails[0].Title != "欢迎回家！" {
		t.Fatalf("welcome/reward mails = %+v", ss.mails)
	}
	for index, mail := range ss.mails[1:] {
		if mail.Id != int64(index+2) || mail.Title != "在线奖励" || len(mail.Items) != 1 ||
			mail.Items[0].ItemId != onlineRewardVoucherItemID || mail.Items[0].Count != 100 ||
			mail.RemainTime != start.Add(2*time.Hour+35*time.Minute+30*24*time.Hour).UnixMilli() {
			t.Fatalf("reward mail %d = %+v", index, mail)
		}
	}
}

func TestDeleteMailAllowsConfirmedUnclaimedAttachmentsAndClearsLastPage(t *testing.T) {
	mail := &mailMsg{Id: 9, Items: []mailItemMsg{{ItemId: onlineRewardVoucherItemID, Count: 100, IsHasItem: true}}}
	ss := &session{playerID: 7, mailSeq: 9, mails: []*mailMsg{mail}}
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	server := &Server{}

	response := server.onDeleteMail(ch, &protocol.C2M_DeleteMail{RpcId: 2, MailId: 9})
	if response != nil || len(ss.mails) != 0 {
		t.Fatalf("unclaimed delete response=%#v mails=%d", response, len(ss.mails))
	}
	deleteFrame := findRecordedFrame(t, ch.conn.(*recordingConn).Bytes(), protocol.OpM2C_DeleteMail)
	deleteResponse := &protocol.M2C_DeleteMail{}
	if err := proto.Unmarshal(deleteFrame.body, deleteResponse); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleteResponse.MailCount != 0 {
		t.Fatalf("last-mail delete page count = %d, want 0", deleteResponse.MailCount)
	}

	ch.conn = &recordingConn{}
	if response := server.onGetMail(ch, &protocol.C2M_GetMail{RpcId: 4, Page: 1}); response != nil {
		t.Fatalf("empty mailbox query returned %#v", response)
	}
	getFrame := findRecordedFrame(t, ch.conn.(*recordingConn).Bytes(), protocol.OpM2C_GetMail)
	getResponse := &protocol.M2C_GetMail{}
	if err := proto.Unmarshal(getFrame.body, getResponse); err != nil {
		t.Fatalf("decode empty mailbox response: %v", err)
	}
	if getResponse.MailCount != 0 || len(ss.mails) != 0 {
		t.Fatalf("reopened mailbox count=%d mails=%d, want empty", getResponse.MailCount, len(ss.mails))
	}
}

func TestDeleteAllMailRemovesUnclaimedAttachmentsAfterClientConfirmation(t *testing.T) {
	plain := &mailMsg{Id: 1}
	pending := &mailMsg{Id: 2, Items: []mailItemMsg{{ItemId: onlineRewardVoucherItemID, Count: 100}}}
	ss := &session{playerID: 7, mails: []*mailMsg{plain, pending}}
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	server := &Server{}

	response := server.onDeleteAllMail(ch, &protocol.C2M_DeleteAllMail{RpcId: 4})
	if response != nil || len(ss.mails) != 0 {
		t.Fatalf("delete-all response=%#v mails=%d", response, len(ss.mails))
	}
	_ = pending

}

func TestMailPaginationAndExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ss := &session{playerID: 7}
	for id := int64(1); id <= 11; id++ {
		ss.mails = append(ss.mails, &mailMsg{Id: id, Title: "mail"})
	}
	ss.mails = append(ss.mails, &mailMsg{Id: 99, RemainTime: now.Add(-time.Second).UnixMilli()})
	if !pruneExpiredMails(ss, now) || len(ss.mails) != 11 {
		t.Fatalf("expired mail prune changed=%v count=%d", true, len(ss.mails))
	}
	start, end, pages := mailPageInfo(int32(len(ss.mails)), 2)
	if start != 10 || end != 11 || pages != 2 {
		t.Fatalf("page info start=%d end=%d pages=%d", start, end, pages)
	}
}
