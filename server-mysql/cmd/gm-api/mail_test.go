package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
	"mhqserver/internal/mysqlschema"
)

func TestMailAttachmentsValidation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"items":[]}`, `{"items":null}`, `{"items":[{"itemId":110305,"count":1}]}`} {
		if _, err := parseMailAttachments(json.RawMessage(raw)); err != nil {
			t.Fatalf("valid %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"items":[{"itemId":0,"count":1}]}`, `{"items":[{"itemId":110305,"count":0}]}`,
		`{"items":[{"itemId":110305,"count":-1}]}`, `{"items":[{"itemId":110305,"count":1.5}]}`,
		`{"items":[{"itemId":110305,"count":1000000001}]}`, `{"items":[{"itemId":110305,"count":2147483648}]}`,
		`{"items":[{"itemId":110305.5,"count":1}]}`, `{"items":[null]}`,
		fmt.Sprintf(`{"items":[%s]}`, strings.TrimSuffix(strings.Repeat(`{"itemId":110305,"count":1},`, 101), ",")),
	} {
		if _, err := parseMailAttachments(json.RawMessage(raw)); err == nil {
			t.Errorf("invalid attachment accepted: %s", raw)
		}
	}
}

func TestMailAttachmentsPublishedMySQL(t *testing.T) {
	if os.Getenv("MHQ_TEST_ITEM_CATALOG_MYSQL") != "1" {
		t.Skip("requires published local MySQL config")
	}
	db, err := sql.Open("mysql", mysqlschema.DefaultDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{db: db}
	if err := app.validateMailAttachments(context.Background(), json.RawMessage(`{"items":[{"itemId":110305,"count":2},{"itemId":120227,"count":1},{"itemId":120367,"count":1}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := app.validateMailAttachments(context.Background(), json.RawMessage(`{"items":[{"itemId":2147483647,"count":1}]}`)); err == nil {
		t.Fatal("unknown item accepted")
	}
}
