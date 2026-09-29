package itemcatalog

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestEquipmentProfessionIsNotCharacterSkinID(t *testing.T) {
	for _, tc := range []struct {
		job  int32
		want string
	}{{0, "通用"}, {1, "军官"}, {2, "运动员"}, {3, "护士"}, {4, "超能力"}} {
		item := Item{ID: 120211, Source: "EquipBase", Name: "练习针筒", JobType: tc.job}
		classify(&item, 0)
		if item.JobName != tc.want || item.ItemType != 1 || item.Category != "装备" {
			t.Fatalf("job=%d item=%+v", tc.job, item)
		}
	}
}

func TestCSVPreservesChineseAndEscapesCells(t *testing.T) {
	items := []Item{{ID: 123, Name: "中文,\"名字\"", Description: "说明第一行\n第二行", JobName: "护士"}, {ID: 124, Name: "=1+1", Description: "  @SUM(1)"}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, items); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "\uFEFF") {
		t.Fatal("missing UTF-8 BOM for Windows Excel")
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(buf.String(), "\uFEFF"))).ReadAll()
	if err != nil || len(records) != 3 {
		t.Fatalf("CSV parse: %v, rows=%d", err, len(records))
	}
	if records[1][1] != items[0].Name || records[1][4] != "护士" || records[1][10] != items[0].Description {
		t.Fatalf("Chinese/quoted/multiline values changed: %+v", records[1])
	}
	if records[2][1] != "'=1+1" || records[2][10] != "'  @SUM(1)" {
		t.Fatalf("formula cells not escaped: %+v", records[2])
	}
}

func TestEquipmentSlotsIncludeWeaponsTitlesAndClothes(t *testing.T) {
	for _, tc := range []struct {
		slot int32
		name string
	}{{0, "武器"}, {2, "形象"}, {4, "称号"}, {8, "衣服"}, {11, "鞋子"}} {
		item := Item{Source: "EquipBase", EquipSlot: tc.slot}
		classify(&item, 0)
		if item.EquipSlotName != tc.name {
			t.Fatalf("slot %d = %s, want %s", tc.slot, item.EquipSlotName, tc.name)
		}
	}
	item := Item{Source: "GoodsBase", EquipSlot: 1}
	classify(&item, 0)
	if item.EquipSlot != -1 || item.EquipSlotName != "" {
		t.Fatalf("goods Type misread as equipment slot: %+v", item)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCSVPropagatesWriteFailure(t *testing.T) {
	if err := WriteCSV(failingWriter{}, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error = %v", err)
	}
}
