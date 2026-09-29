package main

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"mhqserver/protocol"
)

func TestRegistrationValidationMatchesOriginalClientRequirements(t *testing.T) {
	tests := []struct {
		name     string
		account  string
		password string
		code     int32
		message  string
	}{
		{name: "missing account", password: "Pass123", code: errRegAccountRequired, message: "请输入账号"},
		{name: "missing password", account: "Account123", code: errRegPasswordRequired, message: "请输入密码"},
		{name: "account punctuation", account: "bad_name", password: "Pass123", code: errRegAccountFormat, message: "账号格式不正确"},
		{name: "account whitespace", account: "bad name", password: "Pass123", code: errRegAccountFormat, message: "账号格式不正确"},
		{name: "account unicode", account: "测试账号", password: "Pass123", code: errRegAccountFormat, message: "账号格式不正确"},
		{name: "account too long", account: strings.Repeat("a", 192), password: "Pass123", code: errRegAccountFormat, message: "账号格式不正确"},
		{name: "password too short", account: "ShortPwd1", password: "Ab123", code: errRegPasswordFormat, message: "密码格式不正确"},
		{name: "password too long", account: "LongPwd1", password: "Abcdefghijklmnop1", code: errRegPasswordFormat, message: "密码格式不正确"},
		{name: "password punctuation", account: "Punctuation1", password: "Pass_123", code: errRegPasswordFormat, message: "密码格式不正确"},
		{name: "password unicode", account: "UnicodePwd1", password: "密码abc123", code: errRegPasswordFormat, message: "密码格式不正确"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, message := validateRegistration(test.account, test.password)
			if code != test.code || message != test.message {
				t.Fatalf("validateRegistration(%q, %q) = %d/%q, want %d/%q", test.account, test.password, code, message, test.code, test.message)
			}
			if code <= 200000 {
				t.Fatalf("registration error %d cannot reach the client's ShowTipUI", code)
			}
		})
	}

	for _, valid := range []struct{ account, password string }{
		{account: "A", password: "Abc123"},
		{account: "Account123", password: "1234567890123456"},
	} {
		if code, message := validateRegistration(valid.account, valid.password); code != errOK || message != "" {
			t.Errorf("valid registration %q/%q rejected with %d/%q", valid.account, valid.password, code, message)
		}
	}
}

func TestRejectedRegistrationDoesNotCreateAccount(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "registration-validation"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	server := &Server{store: store, conns: make(map[int64]*channel)}
	invalid := []struct {
		account, password string
		code              int32
	}{
		{account: "bad-account", password: "Pass123", code: errRegAccountFormat},
		{account: "BadPassword1", password: "12345", code: errRegPasswordFormat},
	}
	for i, test := range invalid {
		ch := &channel{id: int64(i + 1), session: newSession()}
		resp := server.onRegist(ch, &protocol.C2R_Regist{Account: test.account, Password: test.password, RpcId: int32(i + 1)}).(*protocol.R2C_Regist)
		if resp.Error != test.code || resp.Key != 0 || ch.session.accountID != 0 {
			t.Fatalf("invalid registration %q = %+v sessionAccount=%d", test.account, resp, ch.session.accountID)
		}
		if _, _, err := store.FindAccount(test.account); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("rejected account %q was stored: %v", test.account, err)
		}
	}

	ch := &channel{id: 10, session: newSession()}
	first := server.onRegist(ch, &protocol.C2R_Regist{Account: "ValidAccount1", Password: "Pass123", RpcId: 10}).(*protocol.R2C_Regist)
	if first.Error != errOK || first.Key <= 0 || ch.session.accountID == 0 {
		t.Fatalf("valid registration = %+v sessionAccount=%d", first, ch.session.accountID)
	}
	duplicate := server.onRegist(&channel{id: 11, session: newSession()}, &protocol.C2R_Regist{Account: "ValidAccount1", Password: "Pass123", RpcId: 11}).(*protocol.R2C_Regist)
	if duplicate.Error != errAccountExists || duplicate.Message != "注册失败，账号已存在" || duplicate.Key != 0 {
		t.Fatalf("duplicate registration = %+v", duplicate)
	}
}
