package main

import (
	"database/sql"
	"fmt"
	"sync/atomic"

	"mhqserver/internal/operationsconfig"
)

// affixWashConfigName 是洗词缀专用配置的配置名，与 Gameplay 分开独立热更。
const affixWashConfigName = operationsconfig.AffixWashConfigName

var affixWashConfig atomic.Pointer[operationsconfig.AffixWashConfig]

// affixWashConfigSnapshot 在未加载到配置时返回出厂默认值，保证洗练永远有确定行为。
func affixWashConfigSnapshot() operationsconfig.AffixWashConfig {
	if config := affixWashConfig.Load(); config != nil {
		return *config
	}
	return operationsconfig.DefaultAffixWash()
}

func storeAffixWashConfig(config operationsconfig.AffixWashConfig) {
	copy := config
	affixWashConfig.Store(&copy)
}

// buildAffixWashConfigFromDB 从 MySQL 读取 AffixWash 配置树。表中没有该配置名时
// 回退出厂默认值，这样没发布过 AffixWash 的服务器也能正常运行。
func buildAffixWashConfigFromDB(db *sql.DB) (operationsconfig.AffixWashConfig, error) {
	defaults := operationsconfig.DefaultAffixWash()
	if db == nil {
		return defaults, nil
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM game_config_nodes WHERE config_name = ?`, affixWashConfigName).Scan(&count); err != nil {
		return operationsconfig.AffixWashConfig{}, err
	}
	if count == 0 {
		return defaults, nil
	}
	root, err := buildConfigRootFromDB(db, affixWashConfigName)
	if err != nil {
		return operationsconfig.AffixWashConfig{}, err
	}
	config, err := operationsconfig.DecodeAffixWash(root)
	if err != nil {
		return operationsconfig.AffixWashConfig{}, fmt.Errorf("%s: %w", affixWashConfigName, err)
	}
	return config, nil
}
