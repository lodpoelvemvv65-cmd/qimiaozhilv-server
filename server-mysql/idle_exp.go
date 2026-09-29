package main

import (
	"log"
	"math"
	"time"

	"mhqserver/internal/operationsconfig"
)

// 主城（城镇）挂机经验。
//
// 线上抓包实测（参考数据/抓包归档/online-base-attr-idle-20260913.pcapng、
// online-lv1-idle-20260913.pcapng，用 test/inspect_idle_attribute_capture.py 解析）：
//   - 角色停在城镇不动时，服务器按整分钟节拍下发经验，实测落在每分钟的
//     16:37:58 / 16:38:58 / 16:39:58 / 16:40:58 与 18:13:57，是全局时钟节拍，
//     不是「进图后每 60 秒」（18:13:08 进游戏，18:13:57 就吃到第一跳）；
//   - 每跳固定 24560 点经验，与角色等级无关（Lv1 的新角色与 Lv74 的角色同值）；
//   - 该跳由 20170 携带 1026 等级 + 1027 经验下发，不需要战斗、不需要挂机状态包；
//   - 1 级新角色第一跳直接升到 56 级，与线上经验表 ⌊0.01·Lv³⌋ 的累计值吻合。
//
// 问题.txt 第 4 条同时要求：挂机经验只在城镇生效，其他地图不发。
// 数值、节拍和生效地图都在 config/operations/Gameplay.yaml 的 town_idle_exp 段，
// 可通过 MySQL + Redis 热重载。

// townIdleExpLoop：按配置的节拍窗口给城镇中的在线角色结算挂机经验。
// 窗口序号 = 当前 Unix 秒 / 节拍秒数，因此默认 60 秒时与线上一样对齐整分钟，
// 不会因为玩家何时进游戏而漂移。
func (s *Server) townIdleExpLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastWindow := s.townIdleExpWindow(time.Now())
	for now := range ticker.C {
		lastWindow = s.townIdleExpStep(now, lastWindow)
	}
}

// townIdleExpStep：进入新节拍窗口时结算一跳，同一窗口内重复调用不再结算。
// 返回当前窗口号，供循环下一轮比较。
func (s *Server) townIdleExpStep(now time.Time, lastWindow int64) int64 {
	window := s.townIdleExpWindow(now)
	if window == lastWindow {
		return lastWindow
	}
	s.grantTownIdleExp()
	return window
}

func (s *Server) townIdleExpWindow(now time.Time) int64 {
	config := gameplayTownIdleExp()
	if config.IntervalSeconds <= 0 {
		return 0
	}
	return now.Unix() / config.IntervalSeconds
}

// grantTownIdleExp：给所有停在城镇地图、且不在战斗中的在线角色发一跳挂机经验。
func (s *Server) grantTownIdleExp() {
	config := gameplayTownIdleExp()
	if !config.Enabled || config.ExpPerTick <= 0 || len(config.MapIDs) == 0 {
		return
	}
	for _, ch := range s.onlineChannels() {
		ss := ch.session
		if ss == nil || ss.playerID == 0 || ss.battle != nil {
			continue
		}
		if !townIdleExpMap(config, ss.mapID) {
			continue
		}
		granted := s.gainExpAmount(ch, townIdleExpStorageAmount(config))
		log.Printf("[S=%d] town idle exp map=%d exp=%d level=%d", ch.id, ss.mapID, granted, ss.level)
	}
}

func townIdleExpMap(config operationsconfig.TownIdleExp, mapID int32) bool {
	for _, id := range config.MapIDs {
		if id == mapID {
			return true
		}
	}
	return false
}

// expStoragePerDisplay：session.exp 以 1/100 客户端显示单位存储（见 expNeed 的 *100），
// 而 town_idle_exp.exp_per_tick 用客户端可见的显示单位书写 —— 线上抓包里那一跳
// 就是 24560 显示单位：1 级新角色吃到第一跳后 56 级余 868、第二跳 67 级余 264、
// 第三跳 74 级余 757，与线上经验表 Σ⌊0.01·Lv³⌋ 逐跳吻合。
const expStoragePerDisplay = 100

// townIdleExpStorageAmount：把配置里的显示单位换算成 session.exp 的存储单位。
func townIdleExpStorageAmount(config operationsconfig.TownIdleExp) int64 {
	if config.ExpPerTick <= 0 {
		return 0
	}
	if config.ExpPerTick > math.MaxInt64/expStoragePerDisplay {
		return math.MaxInt64
	}
	return config.ExpPerTick * expStoragePerDisplay
}
