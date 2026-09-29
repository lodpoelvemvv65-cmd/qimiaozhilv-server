package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlayerEnergyDefaultsAndPersists(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "energy.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	accountID, err := store.CreateAccount("energy-test", "password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "energy-player", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if player.Energy != 1000 {
		t.Fatalf("default energy = %d, want 1000", player.Energy)
	}
	if player.MapID != 1000601 || player.PosX != -1.8 || player.PosY != -0.84 {
		t.Fatalf("default location = (%d, %.2f, %.2f)", player.MapID, player.PosX, player.PosY)
	}
	if player.TitleID != 0 {
		t.Fatalf("default title = %d, want 0", player.TitleID)
	}

	itemBuffs := map[int32]*activeItemBuff{1: {ItemID: 110349, EffectType: 6, ExpiresAt: 4102444800000, Multiplier: 1.5}}
	relations := playerRelations{itemBuffs: itemBuffs}
	if err := store.SavePlayerState(playerID, 1, 1, 0, 997, 1000606, 3.25, -1.5,
		0, 0, 0, 0, 0, 0, 0, 0, true, 100000, 100, 100, 0,
		0, 0, 120890, 0, 0, 0, relations, newStarSoulBag()); err != nil {
		t.Fatal(err)
	}
	player, err = store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if player.Energy != 997 {
		t.Fatalf("persisted energy = %d, want 997", player.Energy)
	}
	if player.MapID != 1000606 || player.PosX != 3.25 || player.PosY != -1.5 {
		t.Fatalf("persisted location = (%d, %.2f, %.2f)", player.MapID, player.PosX, player.PosY)
	}
	if player.SkillPoint != 0 {
		t.Fatalf("default skill point = %d, want 0", player.SkillPoint)
	}
	if player.TitleID != 120890 {
		t.Fatalf("persisted title = %d, want 120890", player.TitleID)
	}
	gotBuff := player.Relations.itemBuffs[1]
	if gotBuff == nil || gotBuff.ItemID != itemBuffs[1].ItemID || gotBuff.EffectType != itemBuffs[1].EffectType || gotBuff.ExpiresAt != itemBuffs[1].ExpiresAt || gotBuff.Multiplier != itemBuffs[1].Multiplier {
		t.Fatalf("persisted item buffs = %#v, want %#v", gotBuff, itemBuffs[1])
	}
}

func TestPlayerProgressSurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.db")
	dsn := mysqlTestDSN(t, path)
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := store.CreateAccount("progress-test", "password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "progress-player", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	relations := playerRelations{
		skills: map[int32]int32{100001: 1, 110101: 1}, skillOrder: []int32{100001, 110101},
		autoSkills: []int32{110101}, tasks: map[int32]int32{1001: 2},
		bag: map[int32]*bagItem{0: {ItemId: 110101, ItemType: 1, Count: 1, Level: 7}},
		worn: map[int32]*bagItem{
			1: {ItemId: 110201, ItemType: 1, Count: 1, Level: 12},
			// 称号槽。title_id 是派生的：重开库时 repairPlayerTitles 会用这一格
			// 覆盖 players.title_id（见 internal/mysqlschema/schema.go），
			// 所以要验证称号持久化就必须真的穿上一件，只写 title_id 会被盖掉。
			titleEquipSlot: {ItemId: 120888, ItemType: 1, Count: 1},
		},
	}
	relations.mainUI[0] = mainUISlot{Type: 1, Id: 110101}
	relations.mainUI[4] = mainUISlot{Type: 2, Id: 110101}
	starSoul := newStarSoulBag()
	starSoul.Items[91001] = &starSoulItem{
		ID: 91001, TypeID: 1001, Level: 20, PosType: 0, Quality: 6, Main: 1103,
		Vice: []int32{1104, 1115}, ViceAdd: []float32{0, 1}, ViceLevel: 20,
	}
	relations.starSoul = starSoul
	if err = store.SavePlayerState(playerID, 1, 20, 516152, 1973, 10004, -6.5, 2.25,
		19, 18, 4, 5, 6, 4, 7, 8, true, 123456, 789, 456, 321,
		2, 1, 120890, 0, 0, 0, relations, starSoul); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if player.Level != 20 || player.Exp != 516152 || player.Energy != 1973 ||
		player.MapID != 10004 || player.PosX != -6.5 || player.PosY != 2.25 ||
		player.CharPoint != 19 || player.SkillPoint != 18 ||
		player.StrAdd != 4 || player.QukAdd != 5 || player.SpiAdd != 6 || player.WimAdd != 4 ||
		player.PhyAdd != 7 || player.StaAdd != 8 ||
		player.Relations.skills[100001] != 1 || player.Relations.skills[110101] != 1 ||
		!reflect.DeepEqual(player.Relations.skillOrder, []int32{100001, 110101}) ||
		len(player.Relations.autoSkills) != 1 || player.Relations.autoSkills[0] != 110101 || player.AutoBattle != 1 ||
		player.Relations.mainUI[0] != (mainUISlot{Type: 1, Id: 110101}) ||
		player.Relations.mainUI[4] != (mainUISlot{Type: 2, Id: 110101}) ||
		player.Relations.bag[0] == nil || player.Relations.bag[0].ItemId != 110101 || player.Relations.bag[0].Level != 7 ||
		player.Relations.worn[1] == nil || player.Relations.worn[1].ItemId != 110201 || player.Relations.worn[1].Level != 12 ||
		player.Relations.starSoul.Items[91001] == nil || player.Relations.starSoul.Items[91001].ViceLevel != 20 ||
		len(player.Relations.starSoul.Items[91001].Vice) != 2 ||
		// title_id 不是直接存的：上面 SavePlayerState 写的 120890 会被重开库时的
		// repairPlayerTitles 用称号槽覆盖掉，最终值来自 worn[titleEquipSlot]。
		player.Trans != 2 || player.TitleID != 120888 {
		t.Fatalf("progress changed after reopen: %+v", player)
	}
}

func TestDailyDungeonQuotasSurviveDatabaseReopen(t *testing.T) {
	dsn := mysqlTestDSN(t, filepath.Join(t.TempDir(), "daily-dungeon-quotas"))
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := store.CreateAccount("quota-test", "password")
	if err != nil {
		t.Fatal(err)
	}
	playerID, err := store.CreatePlayer(accountID, "quota-player", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	player.Relations.signin = &signinState{
		PVPScore: 77, PVPBattleDay: "20260820", PVPBattleCount: 33,
		ManualEquipTier1: 2, ManualEquipTier2: 3, ManualEquipTier3: 4,
		ManualRareCraftCount: 17, ManualRarePityAt: 88, ManualEpicCraftCount: 9, ManualEpicPityAt: 96,
		DungeonQuotaDay: "20260820", SpaceTravelRemaining: 17,
		DeathTowerRemaining: 4, FamilyBossKeys: 1, OnlineRewardMS: 123456,
	}
	if err := store.SavePlayerState(playerID, player.JobID, player.Level, player.Exp, player.Energy,
		player.MapID, player.PosX, player.PosY, player.CharPoint, player.SkillPoint,
		player.StrAdd, player.QukAdd, player.SpiAdd, player.WimAdd, player.PhyAdd, player.StaAdd, player.AutoBattle > 0,
		player.Coin, player.YuanBao, player.Voucher, player.StoreCoin, player.Trans, player.SkinID, player.TitleID,
		player.FamilyID, player.FamilyContribute, player.PersonalContribute,
		player.Relations, player.Relations.starSoul); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	player, err = store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	got := player.Relations.signin
	if got == nil || got.PVPScore != 77 || got.PVPBattleDay != "20260820" || got.PVPBattleCount != 33 ||
		got.ManualEquipTier1 != 2 || got.ManualEquipTier2 != 3 || got.ManualEquipTier3 != 4 ||
		got.ManualRareCraftCount != 17 || got.ManualRarePityAt != 88 || got.ManualEpicCraftCount != 9 || got.ManualEpicPityAt != 96 ||
		got.DungeonQuotaDay != "20260820" || got.SpaceTravelRemaining != 17 ||
		got.DeathTowerRemaining != 4 || got.FamilyBossKeys != 1 || got.OnlineRewardMS != 123456 {
		t.Fatalf("persisted dungeon quotas = %+v", got)
	}
}

func TestAccountAndPlayerNameUniqueness(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "identity-uniqueness"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	firstAccount, err := store.CreateAccount("duplicate-account", "password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAccount("duplicate-account", "other-password"); !isDuplicateKeyError(err) {
		t.Fatalf("duplicate account error = %v, want duplicate-key error", err)
	}
	secondAccount, err := store.CreateAccount("second-account", "password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlayer(firstAccount, "unique-name", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlayer(secondAccount, "unique-name", 2, 2); !errors.Is(err, ErrPlayerNameExists) {
		t.Fatalf("duplicate create-role name error = %v, want ErrPlayerNameExists", err)
	}
	secondPlayer, err := store.CreatePlayer(secondAccount, "second-name", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE players SET voucher = 1500 WHERE id = ?`, secondPlayer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenamePlayer(secondPlayer, "unique-name", 1000); !errors.Is(err, ErrPlayerNameExists) {
		t.Fatalf("duplicate rename error = %v, want ErrPlayerNameExists", err)
	}
	player, err := store.FirstPlayer(secondAccount)
	if err != nil {
		t.Fatal(err)
	}
	if player.Name != "second-name" || player.Voucher != 1500 {
		t.Fatalf("failed rename changed player: name=%q voucher=%d", player.Name, player.Voucher)
	}
	remaining, err := store.RenamePlayer(secondPlayer, "renamed-player", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 500 {
		t.Fatalf("remaining voucher = %d, want 500", remaining)
	}
	player, err = store.FirstPlayer(secondAccount)
	if err != nil {
		t.Fatal(err)
	}
	if player.Name != "renamed-player" || player.Voucher != 500 {
		t.Fatalf("successful rename did not persist: name=%q voucher=%d", player.Name, player.Voucher)
	}
}

func TestPlayerIDReservationPersistsAndDrivesRoleCreation(t *testing.T) {
	dsn := mysqlTestDSN(t, "player-id-reservation")
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	firstAccount, err := store.CreateAccount("reserved-first", "password")
	if err != nil {
		t.Fatal(err)
	}
	secondAccount, err := store.CreateAccount("reserved-second", "password")
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := store.ReservePlayerID(firstAccount)
	if err != nil {
		t.Fatal(err)
	}
	firstAgain, err := store.ReservePlayerID(firstAccount)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := store.ReservePlayerID(secondAccount)
	if err != nil {
		t.Fatal(err)
	}
	if firstID <= 0 || firstAgain != firstID || secondID <= 0 || secondID == firstID {
		t.Fatalf("reservations first/again/second = %d/%d/%d", firstID, firstAgain, secondID)
	}
	store.Close()

	store, err = OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	persistedID, err := store.ReservePlayerID(firstAccount)
	if err != nil || persistedID != firstID {
		t.Fatalf("reservation after reopen = %d, %v; want %d", persistedID, err, firstID)
	}
	if err := store.CreatePlayerWithID(firstAccount, secondID, "wrong-reservation", 1, 1); !errors.Is(err, ErrPlayerIDMismatch) {
		t.Fatalf("mismatched reservation error = %v", err)
	}
	if err := store.CreatePlayerWithID(firstAccount, firstID, "reserved-role", 1, 1); err != nil {
		t.Fatal(err)
	}
	player, err := store.FirstPlayer(firstAccount)
	if err != nil {
		t.Fatal(err)
	}
	if player.ID != firstID {
		t.Fatalf("created player id = %d, want reserved %d", player.ID, firstID)
	}
}
