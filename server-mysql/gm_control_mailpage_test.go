package main

import "testing"

// GM 单人发信后固定推 Page:1，而 ss.mails 是插入序、mailPageInfo 从左往右切页
// （第 1 页 = 最旧的 10 封），新邮件 append 在末尾 —— 角色手上邮件 >= 10 封时
// 客户端看不到刚发的邮件。这组用例锁住「推的页码必须包含最后一封（新邮件）」。
func TestGMMailPushPageShowsNewestMail(t *testing.T) {
	for total := 1; total <= 200; total++ {
		page := gmMailPushPage(total)
		if page < 1 {
			t.Fatalf("total=%d 推的页码 %d 小于 1", total, page)
		}
		start, end, pages := mailPageInfo(int32(total), page)
		// 新邮件是 ss.mails 的最后一封，下标 total-1。
		if total-1 < int(start) || total-1 >= int(end) {
			t.Fatalf("total=%d 推 page=%d，返回区间 [%d,%d) 不含最后一封邮件（下标 %d）",
				total, page, start, end, total-1)
		}
		if page > pages {
			t.Fatalf("total=%d 推 page=%d 超过总页数 %d", total, page, pages)
		}
	}
}

// 旧行为（恒推 Page:1）在 total > mailPageSize 时就能被这组用例抓住：
// 这张表列出的是修复前会推错页的边界值。
func TestGMMailPushPageBoundaries(t *testing.T) {
	for _, tc := range []struct {
		total int
		want  int32
	}{
		{0, 1},                    // 空列表：退回第 1 页，交给 onGetMail 走空列表分支
		{1, 1},                    // 单封
		{mailPageSize, 1},         // 恰好一页，第 1 页就是最后一页（旧实现此处仍正确）
		{mailPageSize + 1, 2},     // 刚翻页：旧实现从这里开始看不到新邮件
		{mailPageSize * 2, 2},     // 恰好两页
		{mailPageSize*2 + 1, 3},   // 第三页只放一封
		{mailPageSize * 10, 10},   // 十页整，最后一封正好填满第十页
		{mailPageSize*10 + 1, 11}, // 刚翻到第十一页
		{-1, 1},                   // 负数防御
	} {
		if got := gmMailPushPage(tc.total); got != tc.want {
			t.Fatalf("gmMailPushPage(%d) = %d, want %d", tc.total, got, tc.want)
		}
	}
}

// total 用的是「append 之后、pruneExpiredMails 之前」的封数。若 onGetMail 内部
// 清掉了几封过期邮件，真实封数变小、总页数变少，此时 mailPageInfo 会把页码钳回
// 最后一页 —— 新邮件仍在最后一页，所以结果不受影响。这里显式锁住这个前提。
func TestGMMailPushPageSurvivesPrune(t *testing.T) {
	for appended := 1; appended <= 100; appended++ {
		page := gmMailPushPage(appended)
		for actual := 1; actual <= appended; actual++ {
			start, end, _ := mailPageInfo(int32(actual), page)
			if actual-1 < int(start) || actual-1 >= int(end) {
				t.Fatalf("append 后 total=%d 推 page=%d，清理到实际 %d 封后区间 [%d,%d) 不含最后一封",
					appended, page, actual, start, end)
			}
		}
	}
}
