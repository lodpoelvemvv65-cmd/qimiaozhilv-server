package main

import (
	"path/filepath"
	"testing"

	"mhqserver/internal/operationsconfig"
)

// 新角色代金券必须来自 Gameplay.new_role.voucher，而不是 players 表的列默认值，
// 这样运营只改 YAML 就能调整注册赠送数量，且不需要改表结构。
func TestNewRoleVoucherFollowsGameplayConfig(t *testing.T) {
	loadOnlineTablesForTest(t)
	store, err := OpenStore(mysqlTestDSN(t, filepath.Join(t.TempDir(), "new-role-voucher")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	previous := gameplayConfigSnapshot()
	configured := operationsconfig.Defaults()
	configured.NewRole.Voucher = 4321
	storeGameplayConfig(configured)
	defer storeGameplayConfig(previous)

	accountID, err := store.CreateAccount("new-role-voucher", "password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlayer(accountID, "代金券角色", 1, 1); err != nil {
		t.Fatal(err)
	}
	player, err := store.FirstPlayer(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if player.Voucher != 4321 {
		t.Fatalf("new role voucher = %d, want 4321", player.Voucher)
	}
}

// 未配置 new_role 的旧 YAML 在 Decode 时必须回退到默认 100，否则已部署的配置
// 会在升级后把新角色代金券发成 0。
func TestNewRoleVoucherDefaultsWhenSectionMissing(t *testing.T) {
	config, err := operationsconfig.Decode(map[string]interface{}{"version": 1})
	if err != nil {
		t.Fatal(err)
	}
	if config.NewRole.Voucher != operationsconfig.DefaultNewRoleVoucher {
		t.Fatalf("default new role voucher = %d, want %d",
			config.NewRole.Voucher, operationsconfig.DefaultNewRoleVoucher)
	}
	if operationsconfig.DefaultNewRoleVoucher != 100 {
		t.Fatalf("DefaultNewRoleVoucher = %d, want the stock 100", operationsconfig.DefaultNewRoleVoucher)
	}
}
