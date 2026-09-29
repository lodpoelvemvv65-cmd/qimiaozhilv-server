package main

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

// gatedWriteConn pauses the first server write so a competing battle action
// can test whether the potion's resource update and snapshot share battleMu.
type gatedWriteConn struct {
	recordingConn
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedWriteConn() *gatedWriteConn {
	return &gatedWriteConn{started: make(chan struct{}), release: make(chan struct{})}
}

func (conn *gatedWriteConn) Write(data []byte) (int, error) {
	conn.once.Do(func() { close(conn.started) })
	<-conn.release
	return conn.recordingConn.Write(data)
}

func TestPotionSyncCannotRaceBattleDamage(t *testing.T) {
	withFeatureTables(t, &datatables{
		characterGrowth: map[int64]map[string]interface{}{
			11: {"Phy": int64(5000)}, 21: {"Spi": int64(1000)},
		},
		goodsBase: map[int64]map[string]interface{}{
			110305: {"_id": int64(110305), "EffectType": int64(11), "FixedHp": int64(500)},
		},
	})
	ss := featureTestSession(1001)
	ss.level = 10
	ss.bag[3] = &bagItem{ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	ss.battle = &battleState{owner: ss, playerHP: 1000, playerMaxHP: 5000, playerMP: 100, playerMaxMP: 1000}
	conn := newGatedWriteConn()
	ch := &channel{id: 1, conn: conn, session: ss}
	server := &Server{}

	useDone := make(chan struct{})
	go func() {
		if response := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 3}); response != nil {
			t.Errorf("onUseGoods response = %+v, want raw push", response)
		}
		close(useDone)
	}()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("potion use did not start its first write")
	}

	attackDone := make(chan struct{})
	go func() {
		ss.battleMu.Lock()
		ss.setBattleHP(1000) // damage from the healed 1500 HP state
		ss.battleMu.Unlock()
		close(attackDone)
	}()
	select {
	case <-attackDone:
		t.Fatal("battle damage acquired battleMu before potion snapshot was written")
	case <-time.After(50 * time.Millisecond):
	}
	close(conn.release)
	select {
	case <-useDone:
	case <-time.After(time.Second):
		t.Fatal("potion use did not finish")
	}
	select {
	case <-attackDone:
	case <-time.After(time.Second):
		t.Fatal("battle damage remained blocked after potion use")
	}

	// The next authoritative damage update must reflect the post-hit value,
	// while the potion packet remains the absolute healed value (not +500).
	server.pushCombatHP(ch, ss.playerID, ss.battleHP())
	var hpValues []float32
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
			continue
		}
		message := &protocol.M2C_SyncUnitAttribute{}
		if err := proto.Unmarshal(frame.body, message); err != nil {
			t.Fatalf("decode potion HP sync: %v", err)
		}
		if message.NumericType == 1001 {
			hpValues = append(hpValues, message.Value)
		}
	}
	if want := []float32{1500, 1000}; !reflect.DeepEqual(hpValues, want) {
		t.Fatalf("HP sync values = %v, want %v", hpValues, want)
	}
}

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
		phyAdd:        0, // Lv1 六维基值已给 1 点体质（线上 ⌈Lv/10⌉），加点置 0 使体质仍为 1
		spiAdd:        1,
	}
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	maxHP := ss.playerMaxHp()
	if maxHP != 1000 {
		t.Fatalf("playerMaxHp = %d, want 1000 from Phy 1 * CharacterGrowth 1000", maxHP)
	}
	ss.hp = -1
	if got := ss.battleHP(); got != ss.playerMaxHp() {
		t.Fatalf("uninitialized battleHP = %d, want max %d", got, ss.playerMaxHp())
	}
	// 模拟战斗残血（85/93）
	ss.setBattleHP(maxHP - 8)
	ss.setBattleMP(20)
	if ss.hp != maxHP-8 || ss.mp != 20 {
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
	if ss.battleHP() != maxHP || ss.battleMP() != 20 {
		t.Fatalf("after heal hp=%d mp=%d, want %d/20", ss.battleHP(), ss.battleMP(), maxHP)
	}

	// 战斗中吃药：读 battleState 并写回
	battle := &battleState{
		playerHP:    40,
		playerMaxHP: maxHP,
		playerMP:    5,
		playerMaxMP: 31,
		monsters:    nil,
		owner:       ss,
	}
	ss.battle = battle
	healHP, _ = goodsHeal(ss, gb)
	if healHP != 500 {
		t.Fatalf("in-battle heal hp = %d, want fixed heal 500", healHP)
	}
	if battle.playerHP != 540 {
		t.Fatalf("battle.playerHP = %d, want 540", battle.playerHP)
	}
	syncBattleHealthToSession(ss)
	if ss.hp != 540 || ss.mp != 5 {
		t.Fatalf("sync after battle hp=%d mp=%d, want 540/5", ss.hp, ss.mp)
	}
	ss.battle = nil
	// 战斗结束回主城，再吃生命药水：HP 已满、无 MP 回复 → 0/0
	healHP, healMP = goodsHeal(ss, gb)
	if healHP != 460 || healMP != 0 {
		t.Fatalf("second heal hp=%d mp=%d, want 460/0", healHP, healMP)
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

func TestGoodsHealAtInt32CeilingDoesNotWrapAfterSkinMaxChange(t *testing.T) {
	withFeatureTables(t, &datatables{
		roleGrowth: map[int64]map[string]interface{}{
			1: {"Hp1": int64(1), "Vig1": int64(1)},
		},
		equipBase: map[int64]map[string]interface{}{
			130001: {
				"Type": int64(2),
				"Hp":   int64(5_000_000_000),
				"Mp":   int64(5_000_000_000),
			},
		},
		skinBase: map[int64]map[string]interface{}{130001: {}},
		goodsBase: map[int64]map[string]interface{}{
			110342: {
				"_id":        int64(110342),
				"EffectType": int64(1),
				"PercentHp":  float64(1),
				"PercentMp":  float64(1),
			},
		},
	})
	ss := featureTestSession(1002)
	ss.level = 1
	ss.skinID = 130001
	ss.worn = map[int32]*bagItem{2: {ItemId: 130001, ItemType: 1, Count: 1}}
	ss.hp, ss.mp = int32(maxInt32Value), int32(maxInt32Value)

	if got := ss.playerMaxHp(); got != int32(maxInt32Value) {
		t.Fatalf("skin max HP = %d, want int32 ceiling %d", got, maxInt32Value)
	}
	if got := ss.playerMaxMp(); got != int32(maxInt32Value) {
		t.Fatalf("skin max MP = %d, want int32 ceiling %d", got, maxInt32Value)
	}
	gb := tables.goodsBase[110342]
	// Simulate the old, lower maximum carried through a skin swap.  The first
	// full-recovery potion must use the new saturated maximum and never wrap.
	ss.hp, ss.mp = 1_000_000_000, 900_000_000
	healHP, healMP := goodsHeal(ss, gb)
	if healHP != int32(int64(maxInt32Value)-1_000_000_000) ||
		healMP != int32(int64(maxInt32Value)-900_000_000) ||
		ss.hp != int32(maxInt32Value) || ss.mp != int32(maxInt32Value) {
		t.Fatalf("first ceiling potion wrapped after skin max change: heal=%d/%d hp/mp=%d/%d", healHP, healMP, ss.hp, ss.mp)
	}

	// A damaged character must also recover to the saturated maximum on the
	// first use; the second use is intentionally a no-op rather than a repair.
	ss.hp, ss.mp = int32(maxInt32Value-7), int32(maxInt32Value-11)
	healHP, healMP = goodsHeal(ss, gb)
	if healHP != 7 || healMP != 11 || ss.hp != int32(maxInt32Value) || ss.mp != int32(maxInt32Value) {
		t.Fatalf("ceiling potion heal=%d/%d hp/mp=%d/%d", healHP, healMP, ss.hp, ss.mp)
	}
	healHP, healMP = goodsHeal(ss, gb)
	if healHP != 0 || healMP != 0 || ss.hp != int32(maxInt32Value) || ss.mp != int32(maxInt32Value) {
		t.Fatalf("second ceiling potion changed full resources: heal=%d/%d hp/mp=%d/%d", healHP, healMP, ss.hp, ss.mp)
	}

	// Exercise the real bag-use and protocol path as well. Numeric values are
	// float32 on the wire, so MaxInt32 is displayed as 2^31; that rounded
	// presentation value must never replace the exact server-side int32 state.
	ss.hp, ss.mp = 1_000_000_000, 900_000_000
	ss.bag[7] = &bagItem{ItemId: 110342, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}
	ss.storeTeamHeadHP(ss.hp, ss.playerMaxHp())
	conn := &recordingConn{}
	ch := &channel{id: 2, conn: conn, session: ss}
	server := &Server{}
	if response := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 7}); response != nil {
		t.Fatalf("first purple potion response=%+v, want raw push", response)
	}
	resourceWire := make(map[int32]float32)
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_SyncUnitAttribute {
			continue
		}
		var message protocol.M2C_SyncUnitAttribute
		if err := proto.Unmarshal(frame.body, &message); err != nil {
			t.Fatal(err)
		}
		if message.NumericType == 1001 || message.NumericType == 1003 {
			resourceWire[message.NumericType] = message.Value
		}
	}
	wantWire := float32(int32(maxInt32Value))
	if resourceWire[1001] != wantWire || resourceWire[1003] != wantWire || resourceWire[1001] <= 0 {
		t.Fatalf("purple potion wire HP/MP=%v/%v, want positive %v", resourceWire[1001], resourceWire[1003], wantWire)
	}
	if int64(resourceWire[1001]) == int64(ss.hp) {
		t.Fatal("test did not exercise float32 rounding at the int32 ceiling")
	}
	if ss.hp != int32(maxInt32Value) || ss.mp != int32(maxInt32Value) {
		t.Fatalf("wire rounding replaced authority: hp/mp=%d/%d", ss.hp, ss.mp)
	}
	if cachedHP, cachedMax, ok := ss.loadTeamHeadHP(); !ok || cachedHP != int32(maxInt32Value) || cachedMax != int32(maxInt32Value) {
		t.Fatalf("team cache after ceiling potion=%d/%d ready=%t", cachedHP, cachedMax, ok)
	}
	response := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 7})
	useResp, ok := response.(*protocol.M2C_UseGoods)
	if !ok || useResp.Message != goodsHPMPFullMessage {
		t.Fatalf("second purple potion response=%+v, want %q", response, goodsHPMPFullMessage)
	}
	if ss.bag[7] == nil || ss.bag[7].Count != 1 {
		t.Fatalf("full dual potion was consumed: %+v", ss.bag[7])
	}
	if ss.hp != int32(maxInt32Value) || ss.mp != int32(maxInt32Value) {
		t.Fatalf("second protocol potion changed full resources: hp/mp=%d/%d", ss.hp, ss.mp)
	}
}

// mainUISlotList 自定义快捷栏：新号默认全空；DropItem 配置的物品槽显示
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
		phyAdd:        1,
		spiAdd:        1,
	}
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	// 默认：9 格全部为空，基础攻击也必须由玩家自行拖入。
	slots := ss.mainUISlotList()
	if len(slots) != 9 {
		t.Fatalf("slot count = %d, want 9", len(slots))
	}
	for i, slot := range slots {
		if slot.MainUIType != protocol.MainUIType_NoneSlot || slot.Id != 0 || slot.Index != int32(i) {
			t.Fatalf("slot%d = %+v, want empty slot", i, slot)
		}
	}
	// 玩家把 110305 药水拖到槽 8（DropItem）
	ss.mainUISlots[8] = mainUISlot{Type: 2, Id: 110305}
	slots = ss.mainUISlotList()
	got := slots[8]
	if got.MainUIType != protocol.MainUIType_ItemSlot || got.Id != 110305 || got.ItemCount != 5 {
		t.Fatalf("slot8 = %+v, want ItemSlot 110305 x5", got)
	}
	// Index 始终连续 0..8，不越界。
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
	if got.MainUIType != protocol.MainUIType_NoneSlot || got.Id != 0 || got.ItemCount != 0 {
		t.Fatalf("slot8 after empty = %+v, want NoneSlot", got)
	}
	if ss.mainUISlots[8] != (mainUISlot{}) {
		t.Fatalf("empty shortcut slot was kept: %+v", ss.mainUISlots[8])
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
		phyAdd:        1,
		spiAdd:        1,
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
	if handled, rejection := srv.useGoodsEffect(ch, gb, 1); !handled || rejection != "" {
		t.Fatal("useGoodsEffect(110327) = false")
	}
	if ss.hpBallUntil <= before || ss.hpBallCap <= 0 {
		t.Fatalf("hp ball buff not set: until=%d cap=%d", ss.hpBallUntil, ss.hpBallCap)
	}
	// 残血 40/93 → 战斗结束自动回满
	maxHP := ss.playerMaxHp()
	ss.setBattleHP(maxHP - 53)
	srv.applyMagicBallRecover(ch)
	if got := ss.battleHP(); got != maxHP {
		t.Fatalf("after recover hp = %d, want %d", got, maxHP)
	}
	if ss.hpBallUntil == 0 || ss.hpBallCap != 1000000-53 {
		t.Fatalf("hp ball reservoir = until %d cap %d, want active cap %d", ss.hpBallUntil, ss.hpBallCap, 1000000-53)
	}
	// 已满血：不扣魔法球储量。
	remainingHP := ss.hpBallCap
	srv.applyMagicBallRecover(ch)
	if got := ss.battleHP(); got != maxHP {
		t.Fatalf("after full hp = %d, want %d", got, maxHP)
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
	if handled, rejection := srv.useGoodsEffect(ch, gb2, 1); !handled || rejection != "" {
		t.Fatal("useGoodsEffect(110329) = false")
	}
	srv.applyMagicBallRecover(ch)
	if got := ss.battleMP(); got != ss.playerMaxMp() {
		t.Fatalf("after mp recover mp = %d, want %d", got, ss.playerMaxMp())
	}
}

func TestMagicBallPreservesCapacityAboveInt32AtStatCeiling(t *testing.T) {
	withFeatureTables(t, &datatables{
		roleGrowth: map[int64]map[string]interface{}{
			1: {"Hp1": int64(1), "Vig1": int64(1)},
		},
		equipBase: map[int64]map[string]interface{}{
			130001: {"Type": int64(2), "Hp": int64(5_000_000_000)},
		},
	})
	ss := featureTestSession(1003)
	ss.level = 1
	ss.worn = map[int32]*bagItem{2: {ItemId: 130001, ItemType: 1, Count: 1}}
	ss.hp = int32(maxInt32Value - 100)
	const capacity int64 = 800_000_000_000
	ss.itemBuffs = map[int32]*activeItemBuff{
		itemBuffHPBall: {ItemID: 110833, EffectType: 2, Capacity: capacity},
	}
	ch := &channel{id: 3, conn: &recordingConn{}, session: ss}

	(&Server{}).applyMagicBallRecover(ch)

	if ss.hp != int32(maxInt32Value) {
		t.Fatalf("large magic ball recovered HP=%d, want %d", ss.hp, maxInt32Value)
	}
	if got := ss.itemBuffs[itemBuffHPBall].Capacity; got != capacity-100 {
		t.Fatalf("large magic ball capacity=%d, want %d", got, capacity-100)
	}
	if ss.hpBallCap != capacity-100 {
		t.Fatalf("legacy large magic ball capacity=%d, want %d", ss.hpBallCap, capacity-100)
	}
}

func TestMagicBallExhaustionRemovesBuffImmediately(t *testing.T) {
	ss := featureTestSession(1004)
	ss.level, ss.jobID = 1, 1
	maxHP := ss.playerMaxHp()
	maxMP := ss.playerMaxMp()
	ss.hp, ss.mp = maxHP-7, maxMP-5
	ss.itemBuffs = map[int32]*activeItemBuff{
		itemBuffHPBall: {ItemID: 110327, EffectType: 2, Capacity: 7},
		itemBuffMPBall: {ItemID: 110329, EffectType: 3, Capacity: 5},
	}
	ss.syncLegacyItemBuffFields()
	conn := &recordingConn{}
	ch := &channel{id: 4, conn: conn, session: ss}

	(&Server{}).applyMagicBallRecover(ch)

	if ss.hp != maxHP || ss.mp != maxMP {
		t.Fatalf("magic ball recovery hp/mp=%d/%d, want %d/%d", ss.hp, ss.mp, maxHP, maxMP)
	}
	if ss.itemBuffs[itemBuffHPBall] != nil || ss.itemBuffs[itemBuffMPBall] != nil {
		t.Fatalf("depleted magic ball buffs remain: %+v", ss.itemBuffs)
	}
	if ss.hpBallCap != 0 || ss.mpBallCap != 0 || ss.hpBallUntil != 0 || ss.mpBallUntil != 0 {
		t.Fatalf("legacy magic ball state remains: hp=%d/%d mp=%d/%d",
			ss.hpBallCap, ss.hpBallUntil, ss.mpBallCap, ss.mpBallUntil)
	}
	removed := map[int64]bool{}
	for _, frame := range decodeRecordedFrames(t, conn.Bytes()) {
		if frame.opcode != protocol.OpM2C_BattleChangeState {
			continue
		}
		state := &protocol.M2C_BattleChangeState{}
		if err := proto.Unmarshal(frame.body, state); err != nil {
			t.Fatal(err)
		}
		if state.Type == protocol.ChangeType_Reduce && state.Time == 0 {
			removed[state.Id] = true
		}
	}
	if !removed[110327] || !removed[110329] {
		t.Fatalf("missing depleted buff removals: %+v", removed)
	}
}

func TestRandomVoucherCouponUsesDescriptionRange(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.VoucherGift.RandomRanges["110429"] = operationsconfig.IntegerRange{Minimum: 37, Maximum: 37}
	})
	description := "使用后随机获得1-500代金券"
	minimum, maximum, ok := couponDescriptionRange(description)
	if !ok || minimum != 1 || maximum != 500 {
		t.Fatalf("coupon range = %d-%d, ok=%v; want 1-500", minimum, maximum, ok)
	}
	seenAboveMinimum := false
	for draw := 0; draw < 20; draw++ {
		amount := couponDescriptionAmount(description)
		if amount < minimum || amount > maximum {
			t.Fatalf("coupon draw = %d, want %d-%d", amount, minimum, maximum)
		}
		seenAboveMinimum = seenAboveMinimum || amount > minimum
	}
	if !seenAboveMinimum {
		t.Fatal("random coupon draws always returned the minimum")
	}

	ss := newSession()
	ss.playerID = 1
	server := &Server{}
	ch := &channel{id: 1, session: ss, conn: &recordingConn{}}
	server.applyCouponGoods(ch, map[string]interface{}{
		"_id":         int64(110429),
		"Description": description,
	}, 1)
	if ss.voucher != 37 {
		t.Fatalf("random voucher reward = %d, want configured value 37", ss.voucher)
	}
}

func TestRestoreGoodsRejectedWhenResourcesAreFull(t *testing.T) {
	withFeatureTables(t, &datatables{
		characterGrowth: map[int64]map[string]interface{}{
			11: {"Phy": int64(500)}, 21: {"Spi": int64(200)},
		},
		goodsBase: map[int64]map[string]interface{}{
			110305: {"_id": int64(110305), "FixedHp": int64(500)},
			110316: {"_id": int64(110316), "FixedMp": int64(500)},
			110341: {"_id": int64(110341), "PercentHp": float64(0.5), "PercentMp": float64(0.5)},
			110342: {"_id": int64(110342), "FixedHp": int64(100), "FixedMp": int64(100)},
			110327: {"_id": int64(110327), "EffectType": int64(2), "PercentHp": float64(1), "Capacity": int64(100)},
		},
	})
	server := &Server{}

	t.Run("hp potion", func(t *testing.T) {
		ss := featureTestSession(2001)
		ss.hp, ss.mp = ss.playerMaxHp(), 10
		ss.bag[3] = &bagItem{ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 4}
		ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
		resp := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 3, RpcId: 11}).(*protocol.M2C_UseGoods)
		if resp.Message != goodsHPFullMessage {
			t.Fatalf("hp potion message=%q, want %q", resp.Message, goodsHPFullMessage)
		}
		if ss.bag[3].Count != 4 || ss.hp != ss.playerMaxHp() || ss.mp != 10 {
			t.Fatalf("hp potion consumed or changed resources: bag=%+v hp=%d mp=%d", ss.bag[3], ss.hp, ss.mp)
		}
	})

	t.Run("mp potion", func(t *testing.T) {
		ss := featureTestSession(2002)
		ss.hp, ss.mp = 10, ss.playerMaxMp()
		ss.bag[4] = &bagItem{ItemId: 110316, ItemType: int32(protocol.ItemType_GoodsItem), Count: 3}
		ch := &channel{id: 2, conn: &recordingConn{}, session: ss}
		resp := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 4}).(*protocol.M2C_UseGoods)
		if resp.Message != goodsMPFullMessage {
			t.Fatalf("mp potion message=%q, want %q", resp.Message, goodsMPFullMessage)
		}
		if ss.bag[4].Count != 3 || ss.hp != 10 || ss.mp != ss.playerMaxMp() {
			t.Fatalf("mp potion consumed or changed resources: bag=%+v hp=%d mp=%d", ss.bag[4], ss.hp, ss.mp)
		}
	})

	t.Run("dual hp full mp missing", func(t *testing.T) {
		ss := featureTestSession(2003)
		ss.hp, ss.mp = ss.playerMaxHp(), 40
		ss.bag[5] = &bagItem{ItemId: 110341, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}
		ch := &channel{id: 3, conn: &recordingConn{}, session: ss}
		if response := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 5}); response != nil {
			t.Fatalf("dual potion with missing mp = %+v, want consume", response)
		}
		if ss.bag[5] == nil || ss.bag[5].Count != 1 {
			t.Fatalf("dual potion count=%v, want 1", ss.bag[5])
		}
		if ss.hp != ss.playerMaxHp() || ss.mp != 40+ss.playerMaxMp()/2 {
			t.Fatalf("dual potion hp/mp=%d/%d, want %d/%d", ss.hp, ss.mp, ss.playerMaxHp(), 40+ss.playerMaxMp()/2)
		}
	})

	t.Run("dual both full", func(t *testing.T) {
		ss := featureTestSession(2004)
		ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
		ss.bag[6] = &bagItem{ItemId: 110342, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}
		ch := &channel{id: 4, conn: &recordingConn{}, session: ss}
		resp := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 6}).(*protocol.M2C_UseGoods)
		if resp.Message != goodsHPMPFullMessage {
			t.Fatalf("dual full message=%q, want %q", resp.Message, goodsHPMPFullMessage)
		}
		if ss.bag[6].Count != 2 {
			t.Fatalf("dual full potion consumed: %+v", ss.bag[6])
		}
	})

	t.Run("main ui hp potion", func(t *testing.T) {
		ss := featureTestSession(2005)
		ss.hp, ss.mp = ss.playerMaxHp(), 20
		ss.bag[1] = &bagItem{ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 5}
		ss.mainUISlots[8] = mainUISlot{Type: 2, Id: 110305}
		ch := &channel{id: 5, conn: &recordingConn{}, session: ss}
		resp := server.onUseMainUIGoods(ch, &protocol.C2M_UseMainUIGoods{SlotId: 8, RpcId: 21}).(*protocol.M2C_UseMainUIGoods)
		if resp.Message != goodsHPFullMessage {
			t.Fatalf("main ui hp potion message=%q, want %q", resp.Message, goodsHPFullMessage)
		}
		if ss.bag[1].Count != 5 {
			t.Fatalf("main ui hp potion consumed: %+v", ss.bag[1])
		}
	})

	t.Run("hp ball still usable at full hp", func(t *testing.T) {
		ss := featureTestSession(2006)
		ss.hp = ss.playerMaxHp()
		ss.bag[2] = &bagItem{ItemId: 110327, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
		ch := &channel{id: 6, conn: &recordingConn{}, session: ss}
		if response := server.onUseGoods(ch, &protocol.C2M_UseGoods{Index: 2}); response != nil {
			t.Fatalf("hp ball at full hp = %+v, want consume", response)
		}
		if _, exists := ss.bag[2]; exists {
			t.Fatal("hp ball was not consumed")
		}
		if ss.itemBuffs[itemBuffHPBall] == nil {
			t.Fatal("hp ball buff was not applied")
		}
	})
}

func TestGoodsRestoreRejectionUsesRestoreFields(t *testing.T) {
	withFeatureTables(t, &datatables{
		characterGrowth: map[int64]map[string]interface{}{
			11: {"Phy": int64(500)}, 21: {"Spi": int64(200)},
		},
	})
	ss := featureTestSession(2007)
	maxHP, maxMP := ss.playerMaxHp(), ss.playerMaxMp()
	ss.hp, ss.mp = maxHP, maxMP
	if msg := goodsRestoreRejection(ss, map[string]interface{}{"FixedHp": int64(10)}); msg != goodsHPFullMessage {
		t.Fatalf("hp-only full = %q", msg)
	}
	ss.hp, ss.mp = 10, maxMP
	if msg := goodsRestoreRejection(ss, map[string]interface{}{"PercentMp": float64(0.2)}); msg != goodsMPFullMessage {
		t.Fatalf("mp-only full = %q", msg)
	}
	ss.hp, ss.mp = maxHP, 10
	if msg := goodsRestoreRejection(ss, map[string]interface{}{"FixedHp": int64(1), "FixedMp": int64(1)}); msg != "" {
		t.Fatalf("dual with missing mp rejected: %q", msg)
	}
	ss.hp, ss.mp = 10, maxMP
	if msg := goodsRestoreRejection(ss, map[string]interface{}{"PercentHp": float64(1), "PercentMp": float64(1)}); msg != "" {
		t.Fatalf("dual with missing hp rejected: %q", msg)
	}
	ss.hp, ss.mp = maxHP, maxMP
	if msg := goodsRestoreRejection(ss, map[string]interface{}{"FixedHp": int64(1), "PercentMp": float64(1)}); msg != goodsHPMPFullMessage {
		t.Fatalf("dual both full = %q", msg)
	}
	if msg := goodsRestoreRejection(ss, map[string]interface{}{"EffectType": int64(11)}); msg != "" {
		t.Fatalf("effect type without restore fields = %q", msg)
	}
}

func TestUseMainUIGoodsClearsShortcutWhenCountHitsZero(t *testing.T) {
	withFeatureTables(t, &datatables{
		characterGrowth: map[int64]map[string]interface{}{
			11: {"Phy": int64(500)}, 21: {"Spi": int64(200)},
		},
		goodsBase: map[int64]map[string]interface{}{
			110305: {"_id": int64(110305), "FixedHp": int64(500)},
			110341: {"_id": int64(110341), "PercentHp": float64(0.5), "PercentMp": float64(0.5)},
		},
	})
	server := &Server{}

	ss := featureTestSession(3001)
	ss.hp, ss.mp = 10, ss.playerMaxMp()
	ss.bag[1] = &bagItem{ItemId: 110305, ItemType: int32(protocol.ItemType_GoodsItem), Count: 1}
	ss.mainUISlots[8] = mainUISlot{Type: 2, Id: 110305}
	ch := &channel{id: 7, conn: &recordingConn{}, session: ss}
	if response := server.onUseMainUIGoods(ch, &protocol.C2M_UseMainUIGoods{SlotId: 8}); response != nil {
		t.Fatalf("last shortcut potion = %+v, want consume", response)
	}
	if ss.bagCountOf(110305) != 0 {
		t.Fatal("last potion was not consumed")
	}
	if ss.mainUISlots[8] != (mainUISlot{}) {
		t.Fatalf("shortcut still bound: %+v", ss.mainUISlots[8])
	}
	slots := ss.mainUISlotList()
	if slots[8].MainUIType != protocol.MainUIType_NoneSlot || slots[8].Id != 0 {
		t.Fatalf("shortcut wire slot=%+v, want NoneSlot", slots[8])
	}

	ss = featureTestSession(3002)
	ss.hp, ss.mp = ss.playerMaxHp(), 20
	ss.bag[2] = &bagItem{ItemId: 110341, ItemType: int32(protocol.ItemType_GoodsItem), Count: 2}
	ss.mainUISlots[7] = mainUISlot{Type: 2, Id: 110341}
	ch = &channel{id: 8, conn: &recordingConn{}, session: ss}
	if response := server.onUseMainUIGoods(ch, &protocol.C2M_UseMainUIGoods{SlotId: 7}); response != nil {
		t.Fatalf("shortcut dual potion with missing mp = %+v, want consume", response)
	}
	if ss.hp != ss.playerMaxHp() || ss.mp <= 20 || ss.bag[2].Count != 1 {
		t.Fatalf("shortcut dual potion hp/mp/count=%d/%d/%d", ss.hp, ss.mp, ss.bag[2].Count)
	}
	ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
	resp := server.onUseMainUIGoods(ch, &protocol.C2M_UseMainUIGoods{SlotId: 7}).(*protocol.M2C_UseMainUIGoods)
	if resp.Message != goodsHPMPFullMessage {
		t.Fatalf("shortcut dual full message=%q, want %q", resp.Message, goodsHPMPFullMessage)
	}
	if ss.bag[2].Count != 1 {
		t.Fatalf("shortcut dual potion consumed while full: %+v", ss.bag[2])
	}
}
