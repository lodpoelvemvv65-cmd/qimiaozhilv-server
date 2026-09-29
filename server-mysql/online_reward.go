package main

import (
	"log"
	"time"
)

const onlineRewardVoucherItemID int32 = 110204

func (s *session) startOnlineRewardClock(now time.Time) {
	if s == nil {
		return
	}
	s.onlineRewardMu.Lock()
	s.onlineRewardCheckpoint = now
	s.onlineRewardMu.Unlock()
}

// checkpointOnlineReward records real elapsed session time. Completed periods
// become individual mails, while the remaining milliseconds survive logout.
func checkpointOnlineReward(ch *channel, now time.Time) int {
	if ch == nil || ch.session == nil || ch.session.state != sessInGame {
		return 0
	}
	ss := ch.session
	intervalMS, voucherAmount, mailExpireDays := gameplayOnlineRewardSettings()
	if intervalMS <= 0 || voucherAmount <= 0 || mailExpireDays <= 0 {
		return 0
	}

	ss.onlineRewardMu.Lock()
	defer ss.onlineRewardMu.Unlock()
	if ss.onlineRewardCheckpoint.IsZero() {
		ss.onlineRewardCheckpoint = now
		return 0
	}
	if !now.After(ss.onlineRewardCheckpoint) {
		return 0
	}
	elapsedMS := now.Sub(ss.onlineRewardCheckpoint).Milliseconds()
	if elapsedMS <= 0 {
		return 0
	}
	ss.onlineRewardCheckpoint = ss.onlineRewardCheckpoint.Add(time.Duration(elapsedMS) * time.Millisecond)
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	if ss.signin.OnlineRewardMS < 0 {
		ss.signin.OnlineRewardMS = 0
	}
	totalMS := ss.signin.OnlineRewardMS + elapsedMS
	completed := totalMS / intervalMS
	ss.signin.OnlineRewardMS = totalMS % intervalMS
	if completed == 0 {
		return 0
	}

	seedWelcomeMail(ss, now)
	expiresAt := now.Add(time.Duration(mailExpireDays) * 24 * time.Hour).UnixMilli()
	for index := int64(0); index < completed; index++ {
		ss.mailSeq++
		ss.mails = append(ss.mails, &mailMsg{
			Id:         ss.mailSeq,
			Title:      "在线奖励",
			Content:    "感谢你的陪伴，在线奖励已发送，请及时领取。",
			SenderName: "系统",
			RemainTime: expiresAt,
			Items: []mailItemMsg{{
				ItemId: onlineRewardVoucherItemID, Count: voucherAmount, IsHasItem: true,
			}},
		})
	}
	log.Printf("[S=%d] online reward periods=%d voucher=%d residual_ms=%d",
		ch.id, completed, voucherAmount, ss.signin.OnlineRewardMS)
	return int(completed)
}
