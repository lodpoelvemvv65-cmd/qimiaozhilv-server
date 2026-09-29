package main

import "testing"

func TestSplitAccountsTrimsAndDeduplicates(t *testing.T) {
	got := splitAccounts(" a12312301,,a12312302,a12312301, a12312303 ")
	want := []string{"a12312301", "a12312302", "a12312303"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSQLLiteralUsesUTF8HexForText(t *testing.T) {
	if got, want := sqlLiteral("密码'测试"), "CONVERT(0xe5af86e7a08127e6b58be8af95 USING utf8mb4)"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if got := sqlLiteral(""); got != "''" {
		t.Fatalf("empty string got %s", got)
	}
}
