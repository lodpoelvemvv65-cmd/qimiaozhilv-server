package main

import "testing"

type screenshotCharacterStats struct {
	name                     string
	jobID, trans, stageLevel int32
	petID, petLevel          int32
	charPoint                int32
	direct                   map[int32]float32
	hp, mp                   int32
	str, quk, spi, wim       int32
	phy, sta                 int32
}

func TestOnlineScreenshotCharacterStats(t *testing.T) {
	loadOnlineTablesForTest(t)
	tests := []screenshotCharacterStats{
		{
			name: "Sportsman 2-8500 Pet04-200", jobID: 3, trans: 2, stageLevel: 8500,
			petID: 2104, petLevel: 200, charPoint: 2125,
			direct: map[int32]float32{
				1002: 645848, 1005: 3224, 1006: 800, 1007: 1400,
				1034: 2609, 1035: 1665,
			},
			hp: 5125348, mp: 232500,
			str: 9574, quk: 7150, spi: 7750, wim: 850, phy: 8959, sta: 8015,
		},
		{
			name: "Sportsman 2-6070 Pet04-102", jobID: 3, trans: 2, stageLevel: 6070,
			petID: 2104, petLevel: 102, charPoint: 1517,
			direct: map[int32]float32{
				1005: 4103, 1006: 809, 1007: 1409, 1008: 9,
				1034: 1209, 1035: 1009,
			},
			hp: 2678000, mp: 166680,
			str: 8250, quk: 4956, spi: 5556, wim: 616, phy: 5356, sta: 5156,
		},
		{
			name: "Superman 2-8500 Pet05-200", jobID: 7, trans: 2, stageLevel: 8500,
			petID: 2105, petLevel: 200, charPoint: 2125,
			direct: map[int32]float32{
				1004: 25239, 1006: 1800, 1007: 1000, 1008: 3300,
				1034: 1000, 1035: 1000,
			},
			hp: 3822000, mp: 466239,
			str: 850, quk: 8150, spi: 7350, wim: 9650, phy: 7350, sta: 7350,
		},
		{
			// 该档阶段等级 2374（%10 == 4）是四个截图用例里唯一能区分
			// ⌈Lv/10⌉ 与 round(Lv/10) 的一档。2026-09-13 线上抓包已确认
			// 六维基值为 ⌈Lv/10⌉（见 growth.go naturalPrimaryStat 注），基值由
			// 237 修正为 238，这里把当时按 round 反推出来的转生加成拟合值各项 -1，
			// 使面板总值仍等于截图实测的 548/1138/2067/2174/2067/2067。
			name: "Superman 1-2374 Pet01-72", jobID: 7, trans: 1, stageLevel: 2374,
			petID: 2101, petLevel: 72, charPoint: 593,
			direct: map[int32]float32{
				1002: 760426, 1004: 52860, 1005: 309, 1006: 899,
				1007: 964, 1008: 1935, 1034: 964, 1035: 964,
			},
			hp: 1834746, mp: 176820,
			str: 547, quk: 1137, spi: 2066, wim: 2173, phy: 2066, sta: 2066,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ss := &session{
				jobID: test.jobID, trans: test.trans,
				level:      transStart(test.trans) + test.stageLevel,
				pet:        &petState{PetId: test.petID, Level: test.petLevel},
				transBonus: test.direct,
				charPoint:  test.charPoint,
			}
			gotPrimary := []int32{ss.playerStr(), ss.playerQuk(), ss.playerSpi(), ss.playerWim(), ss.playerPhy(), ss.playerSta()}
			wantPrimary := []int32{test.str, test.quk, test.spi, test.wim, test.phy, test.sta}
			for index := range wantPrimary {
				if gotPrimary[index] != wantPrimary[index] {
					t.Fatalf("primary[%d] = %d, want %d; all=%v", index, gotPrimary[index], wantPrimary[index], gotPrimary)
				}
			}
			if got := ss.playerMaxHp(); got != test.hp {
				t.Errorf("MaxHP = %d, want %d", got, test.hp)
			}
			if got := ss.playerMaxMp(); got != test.mp {
				t.Errorf("MaxMP = %d, want %d", got, test.mp)
			}
			if got := stageLevel(ss.level, ss.trans) / 4; got != test.charPoint {
				t.Errorf("character-point entitlement = %d, want %d", got, test.charPoint)
			}
		})
	}
}

func TestScreenshotDerivedSportsmanStatsUseCharacterGrowthOnly(t *testing.T) {
	loadOnlineTablesForTest(t)
	ss := &session{
		jobID: 3, trans: 2, level: transStart(2) + 6070,
		pet: &petState{PetId: 2104, Level: 102},
		transBonus: map[int32]float32{
			1005: 4103, 1006: 809, 1007: 1409, 1008: 9,
			1034: 1209, 1035: 1009,
		},
	}
	if got := ss.playerPhyAtk(); got != 313500 {
		t.Errorf("physical attack = %d, want 313500", got)
	}
	if got := ss.playerPhyDef(); got != 144968 {
		t.Errorf("physical defence = %d, want 144968", got)
	}
	if got := ss.playerSpiDef(); got != 144968 {
		t.Errorf("spiritual defence = %d, want 144968", got)
	}
}

// TestOnlineCaptureFreshCharacterBaseStats 锁定 2026-09-13 线上抓包里
// 未转生角色的六维基值（20170 推送的 1005/1006/1007/1008 原值）。
// 抓包：参考数据/抓包归档/online-base-attr-idle-20260913.pcapng（Lv1→79）、
// online-lv1-idle-20260913.pcapng（新角色 Lv1→56）。实测 力量=敏捷=智慧：
// Lv1→1、Lv56→6、Lv67→7、Lv74→8；精神/体质/耐力 = 基值 + 12（本地由加成给出）。
func TestOnlineCaptureFreshCharacterBaseStats(t *testing.T) {
	cases := []struct {
		level int32
		want  int32
	}{
		{level: 1, want: 1},
		{level: 10, want: 1},
		{level: 11, want: 2},
		{level: 56, want: 6},
		{level: 67, want: 7},
		{level: 74, want: 8},
	}
	for _, test := range cases {
		ss := &session{jobID: 2, level: test.level}
		if got := ss.naturalPrimaryStat(); got != test.want {
			t.Errorf("naturalPrimaryStat(Lv%d) = %d, want %d", test.level, got, test.want)
		}
	}
}
