package main

import (
	"fmt"
	"math"
	"testing"
)

// 线上抓包里的干净角色面板（1 级新建、无装备/宠物/星魂，只靠挂机升级）。
//
// 依据 2026-09-13 抓包（参考数据/抓包归档/online-lv1-idle-20260913.pcapng、
// online-base-attr-idle-20260913.pcapng），20170 每次节拍推送的原值：
//
//	Lv1  1002=13000 1004=702  1005/1006/1008=1 1007=13 1009=23 1011=715 1012=650 1017=0.0002 1034/1035=13
//	Lv56 1002=18000 1004=972  1005/1006/1008=6 1007=18 1009=138 1011=990 1012=900 1017=0.0012 1034/1035=18
//	Lv67 1002=19000 1004=1026 1005/1006/1008=7 1007=19 1009=161 1011=1045 1012=950 1017=0.0014 1034/1035=19
//	Lv74 1002=20000 1004=1080 1005/1006/1008=8 1007=20 1009=184 1011=1100 1012=1000 1017=0.0016 1034/1035=20
//
// 抓包里 1010 SpiAtk 一直缺失（该职业为 0，零值不推送），1026/1027 另见
// idle_exp.go 与 TestOnlineCaptureExperienceCurve 的等级经验曲线。
type onlineCapturePanel struct {
	level                          int32
	hp, mp, phyAtk, phyDef, spiDef int32
	str, quk, spi, wim, phy, sta   int32
	dvo                            float64
}

var onlineCapturePanels = []onlineCapturePanel{
	{level: 1, hp: 13000, mp: 702, phyAtk: 23, phyDef: 715, spiDef: 650,
		str: 1, quk: 1, spi: 13, wim: 1, phy: 13, sta: 13, dvo: 0.0002},
	{level: 56, hp: 18000, mp: 972, phyAtk: 138, phyDef: 990, spiDef: 900,
		str: 6, quk: 6, spi: 18, wim: 6, phy: 18, sta: 18, dvo: 0.0012},
	{level: 67, hp: 19000, mp: 1026, phyAtk: 161, phyDef: 1045, spiDef: 950,
		str: 7, quk: 7, spi: 19, wim: 7, phy: 19, sta: 19, dvo: 0.0014},
	{level: 74, hp: 20000, mp: 1080, phyAtk: 184, phyDef: 1100, spiDef: 1000,
		str: 8, quk: 8, spi: 20, wim: 8, phy: 20, sta: 20, dvo: 0.0016},
}

// TestOnlineCaptureCleanCharacterPanel：逐等级核对原版面板。
//
// 抓包角色是带新手默认宠物 2101 的干净号（无装备/星魂/加点）：PetConfig 2101 的
// AddAttibuteMap 是 Key 5/20/21 各 +12 每级，即 精神/体质/耐力 在 1 级宠物下各 +12 ——
// 这正是原版面板里 精神/体质/耐力 = 力量+12 的来源（1~74 级宠物都是 1 级）。
func TestOnlineCaptureCleanCharacterPanel(t *testing.T) {
	loadOnlineTablesForTest(t)
	for _, test := range onlineCapturePanels {
		t.Run(fmt.Sprintf("Lv%d", test.level), func(t *testing.T) {
			ss := &session{jobID: 1, level: test.level, pet: &petState{PetId: 2101, Level: 1}}
			for _, check := range []struct {
				name string
				got  int32
				want int32
			}{
				{"MaxHp(1002)", ss.playerMaxHp(), test.hp},
				{"MaxMp(1004)", ss.playerMaxMp(), test.mp},
				{"Str(1005)", ss.playerStr(), test.str},
				{"Quk(1006)", ss.playerQuk(), test.quk},
				{"Spi(1007)", ss.playerSpi(), test.spi},
				{"Wim(1008)", ss.playerWim(), test.wim},
				{"PhyAtk(1009)", ss.playerPhyAtk(), test.phyAtk},
				{"SpiAtk(1010)", ss.playerSpiAtk(), 0},
				{"PhyDef(1011)", ss.playerPhyDef(), test.phyDef},
				{"SpiDef(1012)", ss.playerSpiDef(), test.spiDef},
				{"Phy(1034)", ss.playerPhy(), test.phy},
				{"Sta(1035)", ss.playerSta(), test.sta},
			} {
				if check.got != check.want {
					t.Errorf("%s = %d, want online %d", check.name, check.got, check.want)
				}
			}
			if got := ss.playerExtraNumeric(1017); math.Abs(got-test.dvo) > 1e-6 {
				t.Errorf("Dvo(1017) = %v, want online %v", got, test.dvo)
			}
		})
	}
}
