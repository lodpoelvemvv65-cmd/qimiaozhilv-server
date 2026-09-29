package main

import (
	"database/sql"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"mhqserver/internal/operationsconfig"
)

const gameplayConfigName = "Gameplay"

var gameplayConfig atomic.Pointer[operationsconfig.Config]

func gameplayConfigSnapshot() operationsconfig.Config {
	if config := gameplayConfig.Load(); config != nil {
		return *config
	}
	return operationsconfig.Defaults()
}

func storeGameplayConfig(config operationsconfig.Config) {
	copy := config
	gameplayConfig.Store(&copy)
}

func buildGameplayConfigFromDB(db *sql.DB) (operationsconfig.Config, error) {
	defaults := operationsconfig.Defaults()
	if db == nil {
		return defaults, nil
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM game_config_nodes WHERE config_name = ?`, gameplayConfigName).Scan(&count); err != nil {
		return operationsconfig.Config{}, err
	}
	if count == 0 {
		return defaults, nil
	}
	root, err := buildConfigRootFromDB(db, gameplayConfigName)
	if err != nil {
		return operationsconfig.Config{}, err
	}
	config, err := operationsconfig.Decode(root)
	if err != nil {
		return operationsconfig.Config{}, fmt.Errorf("%s: %w", gameplayConfigName, err)
	}
	return config, nil
}

func gameplayQuizQuestionCount() int32 {
	return gameplayConfigSnapshot().Quiz.QuestionsPerRound
}

// gameplayNewRoleVoucher：新建角色发放的代金券数量。原版由 players 表的列默认值
// 100 提供；现在由 Gameplay.new_role.voucher 决定，热重载后对下一个新建角色生效，
// 已存在的角色不会被改写。
func gameplayNewRoleVoucher() int64 {
	return gameplayConfigSnapshot().NewRole.Voucher
}

// gameplayTownIdleExp：主城挂机经验配置（开关 / 节拍 / 每跳经验 / 生效地图）。
func gameplayTownIdleExp() operationsconfig.TownIdleExp {
	return gameplayConfigSnapshot().TownIdleExp
}

func gameplayPetDurationMS(actionType int64, petLevel int32) int64 {
	config := gameplayConfigSnapshot()
	switch actionType {
	case petExploreTypePlay, petExploreTypeExplore, petExploreTypeExperience:
	default:
		return 0
	}
	if len(config.Pet.DurationTiers) > 0 {
		durationMS := config.Pet.DurationTiers[0].DurationMS
		for _, tier := range config.Pet.DurationTiers[1:] {
			if petLevel < tier.MinimumLevel {
				break
			}
			durationMS = tier.DurationMS
		}
		return durationMS
	}
	switch actionType {
	case petExploreTypePlay:
		return config.Pet.PlayDurationMS
	case petExploreTypeExplore:
		return config.Pet.ExploreDurationMS
	case petExploreTypeExperience:
		return config.Pet.ExperienceDurationMS
	default:
		return 0
	}
}

func gameplayPetQuickEndVoucher() int64 {
	return gameplayConfigSnapshot().Pet.QuickEndVoucher
}

func gameplayOnlineRewardSettings() (intervalMS int64, voucherAmount, mailExpireDays int32) {
	config := gameplayConfigSnapshot().OnlineReward
	return config.IntervalMS, config.VoucherAmount, config.MailExpireDays
}

func gameplayVoucherRange(itemID int32) (int64, int64, bool) {
	value, ok := gameplayConfigSnapshot().VoucherGift.RandomRanges[strconv.FormatInt(int64(itemID), 10)]
	return value.Minimum, value.Maximum, ok
}

func gameplayMapCoinSettings() (chance float64, minimumReward, maximumReward int64, dailyBase int32, voucherCost int64) {
	config := gameplayConfigSnapshot().MapCoin
	return config.SpawnChancePercent, config.MinimumReward, config.MaximumReward,
		config.DailyBaseCount, config.ExtraCountVoucher
}

func gameplayNormalRunSettings() (durationMS int64, location *time.Location) {
	config := gameplayConfigSnapshot().RunMap
	location = time.FixedZone("run-map", config.TimezoneOffsetHours*60*60)
	return config.DailyNormalDurationMS, location
}

func gameplayExperienceGainMultiplier() float64 {
	return gameplayConfigSnapshot().Progression.ExperienceGainPercent / 100
}

func gameplaySkillPointLevels() int32 {
	return gameplayConfigSnapshot().Progression.SkillPointLevels
}

func gameplaySkillMPCostMultiplier() float64 {
	return gameplayConfigSnapshot().Combat.SkillMPCostPercent / 100
}

// gameplayEffectMaxStacks：同一效果最多叠多少层。原版客户端表里没有任何地方
// 给出这个上限（modifier 只有「可叠加」一个布尔位），所以它由运营配置决定，
// 默认 10 保持历史行为。校验保证配置值在 1..100，这里的兜底只防手工构造的快照。
func gameplayEffectMaxStacks() int {
	value := gameplayConfigSnapshot().Combat.EffectMaxStacks
	if value < 1 {
		return operationsconfig.DefaultEffectMaxStacks
	}
	return value
}

// gameplayEffectTickInterval：周期效果（持续伤害/持续治疗）每隔多久生效一次。
// 原版客户端表没有任何节拍依据，这个数来自线上抓包实测 4000ms，见
// battle_periodic.go 的完整说明。校验保证配置值在 100..60000 毫秒，这里的
// 兜底只防手工构造的快照。
func gameplayEffectTickInterval() time.Duration {
	value := gameplayConfigSnapshot().Combat.EffectTickIntervalMS
	if value < 100 || value > 60000 {
		value = operationsconfig.DefaultEffectTickIntervalMS
	}
	return time.Duration(value) * time.Millisecond
}

func gameplayPeriodicTickInterval(modifierID int64) time.Duration {
	config := gameplayConfigSnapshot().Combat
	if value, ok := config.PeriodicTickOverridesMS[strconv.FormatInt(modifierID, 10)]; ok && value >= 100 && value <= 60000 {
		return time.Duration(value) * time.Millisecond
	}
	return gameplayEffectTickInterval()
}

func gameplayMarketSettings() (uint64, uint64, *time.Location) {
	config := gameplayConfigSnapshot().Market
	zone := time.FixedZone("market", config.TimezoneOffsetHours*60*60)
	return config.MinimumPercent, config.MaximumPercent, zone
}

func gameplayShopSaleRule(itemID int32, manualEquipment bool) (enabled bool, priceCoin int64, configured bool) {
	config := gameplayConfigSnapshot().ShopSale
	for _, rule := range config.Items {
		if rule.ItemID == itemID {
			return rule.Enabled, rule.PriceCoin, true
		}
	}
	if manualEquipment {
		return config.ManualEquipment.Enabled, config.ManualEquipment.PriceCoin, true
	}
	if !config.UseOnlinePrices {
		return false, 0, true
	}
	return false, 0, false
}

func gameplayShopSaleSettings() (fallbackPriceCoin, marketRecoveryPercent int64) {
	config := gameplayConfigSnapshot().ShopSale
	return config.FallbackPriceCoin, config.MarketRecoveryPercent
}

func gameplayEquipmentDeltaRange() (float64, float64) {
	config := gameplayConfigSnapshot().Equipment
	return config.MinimumMainAttributePercent / 100, config.MaximumMainAttributePercent / 100
}

func gameplayStrengthSafetyLevels() []int32 {
	levels := gameplayConfigSnapshot().Equipment.StrengthSafetyLevels
	return append([]int32(nil), levels...)
}

func gameplayDungeonSettings() (spaceTravel, deathTower, familyBossKeys int32, location *time.Location) {
	config := gameplayConfigSnapshot().Dungeon
	location = time.FixedZone("dungeon", config.TimezoneOffsetHours*60*60)
	return config.SpaceTravelDailyAttempts, config.DeathTowerDailyAttempts, config.FamilyBossDailyKeys, location
}

func gameplayWorldBossRewardParentsetID() int32 {
	return gameplayConfigSnapshot().WorldBoss.RewardParentsetID
}

func gameplayHardMainStoryRewardMultiplier() int32 {
	return gameplayConfigSnapshot().HardMainStory.NormalRewardMultiplier
}

func gameplayPersonalPVPSettings() (victoryDelta, defeatDelta int32, location *time.Location) {
	config := gameplayConfigSnapshot().PersonalPVP
	location = time.FixedZone("personal-pvp", config.TimezoneOffsetHours*60*60)
	return config.VictoryScoreDelta, config.DefeatScoreDelta, location
}

func gameplayFamilyBossSettings() (float64, float64, []int32, []int32) {
	config := gameplayConfigSnapshot().FamilyBoss
	return config.BonusDropChancePercent / 100, config.SkinSharePercent / 100,
		config.GemItemIDs, config.SkinItemIDs
}

func gameplayFamilyBossAttributeBaseMultiplier() float64 {
	value := gameplayConfigSnapshot().FamilyBoss.AttributeBaseMultiplier
	if !(value > 0 && value <= 10) {
		value = 1.20
	}
	return value
}

func gameplayFamilyBossAttributeStackInterval() time.Duration {
	value := gameplayConfigSnapshot().FamilyBoss.AttributeStackIntervalMS
	if value < 100 || value > 60000 {
		value = 4710
	}
	return time.Duration(value) * time.Millisecond
}

func gameplayManualEquipRecipe(rarity int32) operationsconfig.ManualEquipRecipe {
	config := gameplayConfigSnapshot().ManualEquip
	switch rarity {
	case 0:
		return config.Normal
	case 1:
		return config.Rare
	case 2:
		return config.Epic
	case 3:
		return config.UpgradeRare
	default:
		return operationsconfig.ManualEquipRecipe{}
	}
}
