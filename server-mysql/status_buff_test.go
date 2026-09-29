package main

import (
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

func combatStatesFromFrames(t *testing.T, data []byte) []*protocol.M2C_BattleChangeState {
	t.Helper()
	var states []*protocol.M2C_BattleChangeState
	for len(data) > 0 {
		if len(data) < 4 {
			t.Fatalf("truncated frame: %x", data)
		}
		length := int(binary.LittleEndian.Uint16(data[:2]))
		if length < 2 || length+2 > len(data) {
			t.Fatalf("invalid frame length %d", length)
		}
		if opcode := binary.LittleEndian.Uint16(data[2:4]); opcode != protocol.OpM2C_BattleChangeState {
			t.Fatalf("opcode = %d, want 20080", opcode)
		}
		state := &protocol.M2C_BattleChangeState{}
		if err := proto.Unmarshal(data[4:length+2], state); err != nil {
			t.Fatal(err)
		}
		states = append(states, state)
		data = data[length+2:]
	}
	return states
}

func TestAllConfiguredBuffIconsResolveInClientSkillPackage(t *testing.T) {
	if err := ensureSkillLogicCatalog(); err != nil {
		t.Fatal(err)
	}
	for id, modifier := range skillLogicCatalog.Modifiers {
		if modifier == nil {
			continue
		}
		icon := strings.TrimSpace(modifier.IconID)
		if icon == "" || icon == "0" {
			continue
		}
		if resolved, ok := normalizeClientBuffIcon(modifier.IconID); !ok {
			t.Errorf("modifier %d icon %q is absent from Skill_fui", id, modifier.IconID)
		} else if _, exists := clientSkillBuffIcons[resolved]; !exists {
			t.Errorf("modifier %d resolved icon %q is absent", id, resolved)
		}
	}
	if got, ok := normalizeClientBuffIcon("41060200"); !ok || got != "410602" {
		t.Fatalf("numeric modifier icon resolved to %q, %v", got, ok)
	}
	if _, ok := normalizeClientBuffIcon("99999900"); ok {
		t.Fatal("unknown numeric modifier icon was accepted")
	}
}

func TestAllPersistentGoodsHaveClientBuffVisuals(t *testing.T) {
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	wantTypes := map[int32]bool{2: true, 3: true, 6: true, 7: true, 16: true, 20: true, 21: true}
	seen := make(map[int32]int)
	for id, goods := range tables.goodsBase {
		effectType := int32(num(goods["EffectType"]))
		if !wantTypes[effectType] {
			continue
		}
		seen[effectType]++
		if itemBuffCategory(effectType) == 0 {
			t.Errorf("goods %d effect %d has no status category", id, effectType)
		}
		if icon, ok := normalizeClientBuffIcon(itemBuffIcon(effectType)); !ok || icon == "" {
			t.Errorf("goods %d effect %d has invalid Skill_fui icon", id, effectType)
		}
		if strings.TrimSpace(itemBuffDescription(goods, int32(id))) == "" {
			t.Errorf("goods %d has empty status description", id)
		}
	}
	for effectType := range wantTypes {
		if seen[effectType] == 0 {
			t.Errorf("no goods covered for effect type %d", effectType)
		}
	}
	// Horn ContinuedSeconds is banner display time, not a player status duration.
	if itemBuffCategory(8) != 0 {
		t.Fatal("horn was incorrectly classified as a player buff")
	}
}

func TestEveryPersistentGoodsItemEmitsACompleteClientState(t *testing.T) {
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	wantTypes := map[int32]bool{2: true, 3: true, 6: true, 7: true, 16: true, 20: true, 21: true}
	server := &Server{}
	seen := 0
	for id, goods := range tables.goodsBase {
		effectType := int32(num(goods["EffectType"]))
		if !wantTypes[effectType] {
			continue
		}
		seen++
		ss := newSession()
		ss.playerID = 9001
		conn := &recordingConn{}
		server.activateItemBuff(&channel{conn: conn, session: ss}, goods)
		states := combatStatesFromFrames(t, conn.Bytes())
		if len(states) != 1 {
			t.Errorf("goods %d emitted %d states", id, len(states))
			continue
		}
		state := states[0]
		if state.Id != id || state.TargetUnitId != ss.playerID || state.ActorId != ss.playerID ||
			state.Type != protocol.ChangeType_Add || !state.IsBuff {
			t.Errorf("goods %d emitted incomplete state: %+v", id, state)
		}
		if state.IconId == "" || state.IconDesc != itemBuffDescription(goods, int32(id)) {
			t.Errorf("goods %d emitted icon=%q description=%q", id, state.IconId, state.IconDesc)
		}
		switch effectType {
		case 2, 3:
			if state.Time != indefiniteClientBuffMS {
				t.Errorf("magic ball %d duration = %d", id, state.Time)
			}
		default:
			expected := int32(itemBuffDurationMS(goods, effectType))
			if state.Time < expected-1000 || state.Time > expected {
				t.Errorf("goods %d duration = %d, want about %d", id, state.Time, expected)
			}
		}
		if buff := ss.itemBuffs[itemBuffCategory(effectType)]; buff == nil || buff.ItemID != int32(id) {
			t.Errorf("goods %d was not retained in its status category", id)
		}
	}
	if seen != 29 {
		t.Fatalf("persistent goods count = %d, want 29", seen)
	}
}

func TestBattleAndIdleExperienceCardsUseIndependentCategories(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 100
	})
	ss := newSession()
	ss.ensureItemBuffs()
	now := time.Now().Add(time.Hour).UnixMilli()
	ss.itemBuffs[itemBuffBattleExp] = &activeItemBuff{Multiplier: 1.5, ExpiresAt: now}
	ss.itemBuffs[itemBuffIdleExp] = &activeItemBuff{Multiplier: 4, ExpiresAt: now}
	if got := experienceAfterMultiplier(ss, 100); got != 150 {
		t.Fatalf("battle experience = %d, want 150", got)
	}
	ss.idleBattle = true
	if got := experienceAfterMultiplier(ss, 100); got != 400 {
		t.Fatalf("idle experience = %d, want 400", got)
	}
}

func TestExperienceGainUsesHotReloadedGameplayPercent(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.Progression.ExperienceGainPercent = 200
	})
	ss := newSession()
	if got := experienceAfterMultiplier(ss, 100); got != 200 {
		t.Fatalf("200%% experience gain = %d, want 200", got)
	}

	config := gameplayConfigSnapshot()
	config.Progression.ExperienceGainPercent = 350
	storeGameplayConfig(config)
	if got := experienceAfterMultiplier(ss, 100); got != 350 {
		t.Fatalf("hot-reloaded 350%% experience gain = %d, want 350", got)
	}

	ss.ensureItemBuffs()
	ss.itemBuffs[itemBuffBattleExp] = &activeItemBuff{
		Multiplier: 1.5,
		ExpiresAt:  time.Now().Add(time.Hour).UnixMilli(),
	}
	if got := experienceAfterMultiplier(ss, 100); got != 525 {
		t.Fatalf("350%% experience with 1.5x card = %d, want 525", got)
	}
}

func TestCombatDefeatClearsIdleExperienceMode(t *testing.T) {
	ss := newSession()
	ss.playerID = 42
	ss.idleBattle = true
	ss.battle = &battleState{playerHP: 0, playerMaxHP: 100, playerMP: 10, playerMaxMP: 10}
	server := &Server{}
	if !server.settleCombatLocked(&channel{session: ss, conn: &recordingConn{}}, ss.battle) {
		t.Fatal("defeat was not settled")
	}
	if ss.idleBattle || ss.battle != nil {
		t.Fatalf("defeat left idle mode=%v battle=%v", ss.idleBattle, ss.battle)
	}
}

func TestItemBuffAddReplaceRestoreAndPersist(t *testing.T) {
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	ss := newSession()
	ss.playerID = 77
	firstConn := &recordingConn{}
	ch := &channel{conn: firstConn, session: ss}
	server := &Server{}

	server.activateItemBuff(ch, tables.goodsBase[110349])
	states := combatStatesFromFrames(t, firstConn.Bytes())
	if len(states) != 1 || states[0].Id != 110349 || states[0].IconId != "bufficon_atkAdd" ||
		states[0].Type != protocol.ChangeType_Add || states[0].Time < 3599000 || states[0].Time > 3600000 ||
		!strings.Contains(states[0].IconDesc, "1.5倍") {
		t.Fatalf("first experience state = %+v", states)
	}

	replaceConn := &recordingConn{}
	ch.conn = replaceConn
	server.activateItemBuff(ch, tables.goodsBase[110350])
	states = combatStatesFromFrames(t, replaceConn.Bytes())
	if len(states) != 2 || states[0].Id != 110349 || states[0].Type != protocol.ChangeType_Reduce || states[0].Time != 0 ||
		states[1].Id != 110350 || states[1].Type != protocol.ChangeType_Add {
		t.Fatalf("replacement states = %+v", states)
	}

	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "buffs.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accountID, err := store.CreateAccount("buff-persist", "password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "buff-persist", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	player.Relations.itemBuffs = ss.itemBuffs
	if err := store.SavePlayerState(playerID, player.JobID, player.Level, player.Exp, player.Energy,
		player.MapID, player.PosX, player.PosY, player.CharPoint, player.SkillPoint,
		player.StrAdd, player.QukAdd, player.SpiAdd, player.WimAdd, player.PhyAdd, player.StaAdd, player.AutoBattle > 0,
		player.Coin, player.YuanBao, player.Voucher, player.StoreCoin, player.Trans, player.SkinID, player.TitleID,
		player.FamilyID, player.FamilyContribute, player.PersonalContribute,
		player.Relations, player.Relations.starSoul); err != nil {
		t.Fatal(err)
	}
	player, err = store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	restored := newSession()
	restored.playerID = 77
	restored.itemBuffs = player.Relations.itemBuffs
	restored.syncLegacyItemBuffFields()
	restoreConn := &recordingConn{}
	server.pushActiveItemBuffs(&channel{conn: restoreConn, session: restored})
	states = combatStatesFromFrames(t, restoreConn.Bytes())
	if len(states) != 1 || states[0].Id != 110350 || states[0].Time <= 0 {
		t.Fatalf("restored states = %+v", states)
	}
	if restored.expMult != 2 || restored.expMultUntil <= time.Now().UnixMilli() {
		t.Fatalf("restored multiplier = %.1f until=%d", restored.expMult, restored.expMultUntil)
	}
}

func TestMagicBallUsesPersistentIconAndReservoir(t *testing.T) {
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	ss := newSession()
	ss.playerID = 88
	ss.level = 1
	ss.jobID = 1
	ss.phyAdd, ss.spiAdd = 1, 1
	ss.hp, ss.mp = 40, 5
	conn := &recordingConn{}
	ch := &channel{conn: conn, session: ss}
	server := &Server{}

	server.activateItemBuff(ch, tables.goodsBase[110327])
	states := combatStatesFromFrames(t, conn.Bytes())
	if len(states) != 1 || states[0].IconId != "bufficon_hpup" || states[0].Time != indefiniteClientBuffMS {
		t.Fatalf("hp ball state = %+v", states)
	}
	before := ss.itemBuffs[itemBuffHPBall].Capacity
	server.applyMagicBallRecover(ch)
	if ss.battleHP() != ss.playerMaxHp() || ss.itemBuffs[itemBuffHPBall].Capacity != before-int64(ss.playerMaxHp()-40) {
		t.Fatalf("hp reservoir after recover: hp=%d buff=%+v", ss.battleHP(), ss.itemBuffs[itemBuffHPBall])
	}
}

func TestExpiredItemBuffsAreNotRestored(t *testing.T) {
	buffs := map[int32]*activeItemBuff{
		itemBuffBattleExp: {ItemID: 110349, EffectType: 6, ExpiresAt: time.Now().Add(-time.Second).UnixMilli(), Multiplier: 1.5},
	}
	pruneExpiredItemBuffs(buffs, time.Now().UnixMilli())
	if len(buffs) != 0 {
		t.Fatalf("expired buffs restored: %+v", buffs)
	}
}

func TestDepletedMagicBallBuffsAreNotRestored(t *testing.T) {
	buffs := map[int32]*activeItemBuff{
		itemBuffHPBall:    {ItemID: 110327, EffectType: 2, Capacity: 0},
		itemBuffMPBall:    {ItemID: 110329, EffectType: 3, Capacity: -1},
		itemBuffBattleExp: {ItemID: 110349, EffectType: 6, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()},
	}
	pruneExpiredItemBuffs(buffs, time.Now().UnixMilli())
	if buffs[itemBuffHPBall] != nil || buffs[itemBuffMPBall] != nil {
		t.Fatalf("depleted magic balls restored: %+v", buffs)
	}
	if buffs[itemBuffBattleExp] == nil {
		t.Fatal("active timed buff was removed with depleted magic balls")
	}
}

func TestBuffStatusDurationUsesClientDayClockFormat(t *testing.T) {
	if got := formatBuffStatusDuration((26*time.Hour + 3*time.Minute + 4*time.Second).Milliseconds()); got != "01:02:03:04" {
		t.Fatalf("formatted duration = %q", got)
	}
	if got := formatBuffStatusDuration(-1); got != "00:00:00:00" {
		t.Fatalf("negative duration = %q", got)
	}
}

func TestDailyNormalRunAllowanceAccumulatesOncePerDay(t *testing.T) {
	withGameplayConfigForTest(t, func(config *operationsconfig.Config) {
		config.RunMap.DailyNormalDurationMS = int64(2 * time.Hour / time.Millisecond)
		config.RunMap.TimezoneOffsetHours = 8
	})
	ss := newSession()
	ss.itemBuffs = make(map[int32]*activeItemBuff)
	first := time.Date(2026, 9, 1, 8, 0, 0, 0, time.FixedZone("test", 8*60*60))
	if !ss.ensureDailyNormalRunAllowance(first) {
		t.Fatal("first operating day did not grant allowance")
	}
	got := ss.itemBuffs[itemBuffNormalRun]
	if got == nil || got.ExpiresAt != first.UnixMilli()+2*int64(time.Hour/time.Millisecond) || ss.signin.NormalRunDay != "20260901" {
		t.Fatalf("first allowance state=%+v signin=%+v", got, ss.signin)
	}
	if ss.ensureDailyNormalRunAllowance(first.Add(12 * time.Hour)) {
		t.Fatal("same operating day granted a second allowance")
	}
	second := first.Add(24 * time.Hour)
	if !ss.ensureDailyNormalRunAllowance(second) {
		t.Fatal("next operating day did not grant allowance")
	}
	if want := second.UnixMilli() + 2*int64(time.Hour/time.Millisecond); ss.itemBuffs[itemBuffNormalRun].ExpiresAt != want {
		t.Fatalf("accumulated expiry=%d want=%d", ss.itemBuffs[itemBuffNormalRun].ExpiresAt, want)
	}
}

func TestGetBuffTimePushesDynamicStatusTipAfterBuffStates(t *testing.T) {
	if tables == nil {
		loadDatatables("../datatable_json")
	}
	now := time.Now()
	ss := newSession()
	ss.playerID = 9002
	ss.energy = 10
	ss.signin = &signinState{
		DungeonQuotaDay: gameplayDungeonDay(now), SpaceTravelRemaining: 49,
		DeathTowerRemaining: 8, FamilyBossKeys: 1,
	}
	ss.itemBuffs = map[int32]*activeItemBuff{
		itemBuffHPBall:    {ItemID: 110327, EffectType: 2, Capacity: 985622},
		itemBuffMPBall:    {ItemID: 110328, EffectType: 3, Capacity: 998680},
		itemBuffNormalRun: {ItemID: 110341, EffectType: 20, ExpiresAt: now.Add(26*time.Hour + 3*time.Minute + 5*time.Second).UnixMilli()},
		itemBuffFastRun:   {ItemID: 110342, EffectType: 21, ExpiresAt: now.Add(2*time.Hour + 5*time.Second).UnixMilli()},
		itemBuffBattleExp: {ItemID: 110349, EffectType: 6, ExpiresAt: now.Add(90 * time.Minute).UnixMilli(), Multiplier: 1.5},
	}
	conn := &recordingConn{}
	server := &Server{}
	body, err := proto.Marshal(&protocol.C2M_GetBuffTime{ActorId: ss.playerID})
	if err != nil {
		t.Fatal(err)
	}
	server.handleOpcode(&channel{id: 1, conn: conn, session: ss}, protocol.OpC2M_GetBuffTime, body)
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 6 {
		t.Fatalf("GetBuffTime emitted %d frames, want 5 states plus one tip", len(frames))
	}
	for index, frame := range frames[:len(frames)-1] {
		if frame.opcode != protocol.OpM2C_BattleChangeState {
			t.Fatalf("frame %d opcode = %d, want 20080", index, frame.opcode)
		}
	}
	last := frames[len(frames)-1]
	if last.opcode != protocol.OpM2C_SendTip {
		t.Fatalf("last opcode = %d, want 20262", last.opcode)
	}
	tip := &protocol.M2C_SendTip{}
	if err := proto.Unmarshal(last.body, tip); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"血球Buff剩余量：985622", "蓝球Buff剩余量：998680",
		"普通跑图剩余量：01:02:03:", "畅爽跑图剩余量：00:02:00:",
		"时空旅行战斗次数：49", "死亡之塔战斗次数：8",
		"今日boss行动值：10", "今日家族boss钥匙：1",
		"战斗经验Buff剩余时间：90.00分钟",
	} {
		if !strings.Contains(tip.Message, expected) {
			t.Errorf("tip missing %q: %q", expected, tip.Message)
		}
	}
	if tip.ActorId != ss.playerID {
		t.Fatalf("tip ActorId = %d, want %d", tip.ActorId, ss.playerID)
	}
}

func TestGetBuffTimeStillOpensTipWithoutActiveBuffs(t *testing.T) {
	ss := newSession()
	ss.playerID = 9003
	ss.energy = 7
	ss.signin = &signinState{DungeonQuotaDay: gameplayDungeonDay(time.Now()), SpaceTravelRemaining: 2, DeathTowerRemaining: 1}
	conn := &recordingConn{}
	server := &Server{}
	server.pushActiveItemBuffs(&channel{conn: conn, session: ss})
	server.pushBuffStatusTip(&channel{conn: conn, session: ss})
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 1 || frames[0].opcode != protocol.OpM2C_SendTip {
		t.Fatalf("frames without active buffs = %+v", frames)
	}
	tip := &protocol.M2C_SendTip{}
	if err := proto.Unmarshal(frames[0].body, tip); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tip.Message, "血球Buff剩余量：0") ||
		!strings.Contains(tip.Message, "普通跑图剩余量：00:00:00:00") ||
		!strings.Contains(tip.Message, "战斗经验Buff剩余时间：0.00分钟") {
		t.Fatalf("empty status tip = %q", tip.Message)
	}
}
