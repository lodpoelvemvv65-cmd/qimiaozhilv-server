package main

import (
	"testing"
	"time"

	"mhqserver/protocol"
)

// goodsHeal / syncBattleHealthToSession 单测：
// 非战斗残血（战斗退出写回 ss.hp）→ 吃药按 GoodsBase 恢复字段回满并写回。
func TestGoodsHealNonBattle(t *testing.T) {
	ss := &session{
		level:         1,
		jobID:         1,
		hp:            -1, // 未初始化
		mp:            -1,
		skillOrder:    []int32{100001},
		skills:        map[int32]int32{100001: 1},
		battle:        nil,
		bag:           make(map[int32]*bagItem),
		store:         make(map[int32]*bagItem),
		friends:       make(map[int64]*friendInfo),
		friendReqFrom: make(map[int64]bool),
		friendReqTo:   make(map[int64]bool),
	}
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	if got := ss.playerMaxHp(); got != 93 {
		t.Fatalf("playerMaxHp = %d, want 93 (role 1 lv1)", got)
	}
	ss.hp = -1
	if got := ss.battleHP(); got != ss.playerMaxHp() {
		t.Fatalf("uninitialized battleHP = %d, want max %d", got, ss.playerMaxHp())
	}
	// 模拟战斗残血（85/93）
	ss.setBattleHP(85)
	ss.setBattleMP(20)
	if ss.hp != 85 || ss.mp != 20 {
		t.Fatalf("setBattleHP/MP non-battle did not persist: hp=%d mp=%d", ss.hp, ss.mp)
	}
	// 药品：110305 超小型生命药水 FixedHp=500 → 回满 93
	gb := tables.goodsBase[110305]
	if gb == nil {
		t.Fatal("goodsBase missing 110305")
	}
	healHP, healMP := goodsHeal(ss, gb)
	if healHP != 8 {
		t.Fatalf("goodsHeal hp = %d, want 8 (85->93)", healHP)
	}
	if ss.battleHP() != 93 || ss.battleMP() != 20 {
		t.Fatalf("after heal hp=%d mp=%d, want 93/20", ss.battleHP(), ss.battleMP())
	}

	// 战斗中吃药：读 battleState 并写回
	battle := &battleState{
		playerHP:    40,
		playerMaxHP: 93,
		playerMP:    5,
		playerMaxMP: 31,
		monsters:    nil,
		owner:       ss,
	}
	ss.battle = battle
	healHP, _ = goodsHeal(ss, gb)
	if healHP != 53 {
		t.Fatalf("in-battle heal hp = %d, want 53 (40->93)", healHP)
	}
	if battle.playerHP != 93 {
		t.Fatalf("battle.playerHP = %d, want 93", battle.playerHP)
	}
	syncBattleHealthToSession(ss)
	if ss.hp != 93 || ss.mp != 5 {
		t.Fatalf("sync after battle hp=%d mp=%d, want 93/5", ss.hp, ss.mp)
	}
	ss.battle = nil
	// 战斗结束回主城，再吃生命药水：HP 已满、无 MP 回复 → 0/0
	healHP, healMP = goodsHeal(ss, gb)
	if healHP != 0 || healMP != 0 {
		t.Fatalf("second heal hp=%d mp=%d, want 0/0 (hp full, hp-only potion)", healHP, healMP)
	}
	// The potion caps at the authoritative maximum MP, including the online
	// CharacterGrowth primary-stat conversion.
	ss.setBattleMP(5)
	mpGB := tables.goodsBase[110316]
	if mpGB == nil {
		t.Fatal("goodsBase missing 110316")
	}
	healHP, healMP = goodsHeal(ss, mpGB)
	wantHealMP := ss.playerMaxMp() - 5
	if healMP != wantHealMP {
		t.Fatalf("mp potion heal mp = %d, want %d", healMP, wantHealMP)
	}
	if ss.battleMP() != ss.playerMaxMp() {
		t.Fatalf("after mp potion mp = %d, want %d", ss.battleMP(), ss.playerMaxMp())
	}
}

// mainUISlotList 自定义快捷栏：默认槽 0 = 基础攻击；DropItem 配置的物品槽显示
// ItemSlot（Id+Count）；空槽 = NoneSlot（不越界、不发 ItemSlot+Id=0）。
func TestMainUISlotCustomBar(t *testing.T) {
	ss := &session{
		level:         1,
		jobID:         1,
		hp:            -1,
		mp:            -1,
		skillOrder:    []int32{100001},
		skills:        map[int32]int32{100001: 1},
		bag:           map[int32]*bagItem{1: {ItemId: 110305, ItemType: 2, Count: 5}},
		store:         make(map[int32]*bagItem),
		friends:       make(map[int64]*friendInfo),
		friendReqFrom: make(map[int64]bool),
		friendReqTo:   make(map[int64]bool),
	}
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	// 默认：槽 0 = 基础攻击技能槽
	slots := ss.mainUISlotList()
	if len(slots) != 9 {
		t.Fatalf("slot count = %d, want 9", len(slots))
	}
	if slots[0].MainUIType != protocol.MainUIType_SkillSlot || slots[0].Id != 100001 {
		t.Fatalf("slot0 = %+v, want SkillSlot 100001", slots[0])
	}
	// 玩家把 110305 药水拖到槽 8（DropItem）
	ss.mainUISlots[8] = mainUISlot{Type: 2, Id: 110305}
	slots = ss.mainUISlotList()
	got := slots[8]
	if got.MainUIType != protocol.MainUIType_ItemSlot || got.Id != 110305 || got.ItemCount != 5 {
		t.Fatalf("slot8 = %+v, want ItemSlot 110305 x5", got)
	}
	// 空槽恒 NoneSlot（Index 连续 0..8，不越界）
	for i, s := range slots {
		if s.Index != int32(i) {
			t.Fatalf("slot index %d != %d", s.Index, i)
		}
	}
	// 用光后：槽配置保留（玩家自定义），ItemCount 归 0——Id 仍在 GoodsBase，
	// 客户端安全显示 0（不发 ItemSlot+Id=0 即 GoodsBase 查空崩渲染）。
	delete(ss.bag, 1)
	slots = ss.mainUISlotList()
	got = slots[8]
	if got.MainUIType != protocol.MainUIType_ItemSlot || got.Id != 110305 || got.ItemCount != 0 {
		t.Fatalf("slot8 after empty = %+v, want ItemSlot 110305 x0", got)
	}
}

func TestFirstBagIndexOfAcceptsZeroSlot(t *testing.T) {
	ss := &session{bag: map[int32]*bagItem{
		0: {ItemId: 110305, ItemType: 2, Count: 3},
	}}
	if got := ss.firstBagIndexOf(110305); got != 0 {
		t.Fatalf("firstBagIndexOf returned %d, want valid slot 0", got)
	}
	if got := ss.firstBagIndexOf(999999); got != invalidBagSlot {
		t.Fatalf("missing item returned %d, want %d", got, invalidBagSlot)
	}
}

// 魔法球（EffectType 2/3）战斗结束自动回满：按实际恢复量扣除 Capacity，
// 储量未耗尽时继续生效，满血/满蓝时不扣储量。
func TestMagicBallAutoRecover(t *testing.T) {
	ss := &session{
		level:         1,
		jobID:         1,
		hp:            -1,
		mp:            -1,
		skillOrder:    []int32{100001},
		skills:        map[int32]int32{100001: 1},
		bag:           make(map[int32]*bagItem),
		store:         make(map[int32]*bagItem),
		friends:       make(map[int64]*friendInfo),
		friendReqFrom: make(map[int64]bool),
		friendReqTo:   make(map[int64]bool),
	}
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	// 使用生命魔法球（110327 EffectType 2）→ 设置 buff
	gb := tables.goodsBase[110327]
	if gb == nil {
		t.Fatal("goodsBase missing 110327")
	}
	srv := &Server{}
	ch := &channel{session: ss, conn: &recordingConn{}}
	before := time.Now().UnixMilli()
	if !srv.useGoodsEffect(ch, gb, 1) {
		t.Fatal("useGoodsEffect(110327) = false")
	}
	if ss.hpBallUntil <= before || ss.hpBallCap <= 0 {
		t.Fatalf("hp ball buff not set: until=%d cap=%d", ss.hpBallUntil, ss.hpBallCap)
	}
	// 残血 40/93 → 战斗结束自动回满
	ss.setBattleHP(40)
	srv.applyMagicBallRecover(ch)
	if got := ss.battleHP(); got != 93 {
		t.Fatalf("after recover hp = %d, want 93", got)
	}
	if ss.hpBallUntil == 0 || ss.hpBallCap != 1000000-53 {
		t.Fatalf("hp ball reservoir = until %d cap %d, want active cap %d", ss.hpBallUntil, ss.hpBallCap, 1000000-53)
	}
	// 已满血：不扣魔法球储量。
	remainingHP := ss.hpBallCap
	srv.applyMagicBallRecover(ch)
	if got := ss.battleHP(); got != 93 {
		t.Fatalf("after full hp = %d, want 93", got)
	}
	if ss.hpBallCap != remainingHP {
		t.Fatalf("full hp consumed reservoir: %d -> %d", remainingHP, ss.hpBallCap)
	}
	// 精力魔法球（110329 EffectType 3）
	gb2 := tables.goodsBase[110329]
	if gb2 == nil {
		t.Fatal("goodsBase missing 110329")
	}
	ss.setBattleMP(5)
	if !srv.useGoodsEffect(ch, gb2, 1) {
		t.Fatal("useGoodsEffect(110329) = false")
	}
	srv.applyMagicBallRecover(ch)
	if got := ss.battleMP(); got != ss.playerMaxMp() {
		t.Fatalf("after mp recover mp = %d, want %d", got, ss.playerMaxMp())
	}
}
