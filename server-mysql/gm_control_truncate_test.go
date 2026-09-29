package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// truncateGM 曾经按字节截断（value[:max]），中文发件人名会被切在多字节字符
// 中间，产生非法 UTF-8；该值要写进 player_mails.sender_name（utf8mb4），MySQL
// 会以 "Incorrect string value" 拒绝，整封邮件保存失败。
// 这组用例锁住「结果必须是合法 UTF-8 且不超过 max 个字符」。
func TestTruncateGMKeepsValidUTF8(t *testing.T) {
	// 18 个字符 / 54 字节；两段拼接后 36 字符 / 108 字节，旧实现在第 64 字节切断。
	unit := "梦幻奇遇记运营团队线上公告专用发件人"
	for _, tc := range []struct {
		name      string
		value     string
		max       int
		wantRunes int
	}{
		{"短于上限不截断", unit, 64, 18},
		{"恰好等于上限", strings.Repeat("系", 64), 64, 64},
		{"刚好超一个字符", strings.Repeat("系", 65), 64, 64},
		{"超长中文必须按 rune 截断", strings.Repeat(unit, 4), 64, 64},
		{"纯 ASCII 超长", strings.Repeat("a", 200), 64, 64},
		{"混排超长", strings.Repeat("a系", 100), 64, 64},
		{"上限为零", unit, 0, 0},
		{"负上限", unit, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateGM(tc.value, tc.max)
			if !utf8.ValidString(got) {
				t.Fatalf("truncateGM(%q, %d) = %q 不是合法 UTF-8", tc.value, tc.max, got)
			}
			if n := utf8.RuneCountInString(got); n != tc.wantRunes {
				t.Fatalf("truncateGM(%q, %d) 字符数 = %d, want %d", tc.value, tc.max, n, tc.wantRunes)
			}
		})
	}
}

// 截断与未截断两条路径都必须去掉首尾空白：旧实现只在未超长时 TrimSpace，
// 超长的名字会带着首尾空白写进邮件发件人。而且要先 trim 再截断，否则首尾
// 空白会白占掉字符额度（70 个「系」两侧各两个空格时应保住完整 64 个「系」）。
func TestTruncateGMTrimsBothPaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		max   int
		want  string
	}{
		{"未超长去空白", "  系统  ", 64, "系统"},
		{"超长也要去空白", "  " + strings.Repeat("系", 70) + "  ", 64, strings.Repeat("系", 64)},
		{"截断落在空格上", "ab " + strings.Repeat("c", 70), 3, "ab"},
		{"纯空白", "   ", 64, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateGM(tc.value, tc.max); got != tc.want {
				t.Fatalf("truncateGM(%q, %d) = %q, want %q", tc.value, tc.max, got, tc.want)
			}
		})
	}
}
