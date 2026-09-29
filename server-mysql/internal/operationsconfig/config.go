package operationsconfig

import (
	"bytes"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

const CurrentVersion = 1

// DefaultEffectMaxStacks is how many layers one stacking combat effect may hold
// before further applications are dropped. Nothing in the online client tables
// states such a cap, so it stays an operator lever in Gameplay.combat.
const DefaultEffectMaxStacks = 10

// DefaultEffectTickIntervalMS is how often a 持续伤害/持续治疗 effect takes effect.
// The online client tables state no cadence either (see battle_periodic.go), so
// this value comes from the online server capture and stays an operator lever
// in Gameplay.combat as effect_tick_interval_ms.
const DefaultEffectTickIntervalMS = 4000

// DefaultNewRoleVoucher is the 代金券 a freshly created character starts with.
// The stock players table used to supply this through its column default (100);
// it now stays an operator lever in Gameplay.new_role so the registration gift
// can change without a schema migration.
const DefaultNewRoleVoucher = 100

type IntegerRange struct {
	Minimum int64 `yaml:"minimum"`
	Maximum int64 `yaml:"maximum"`
}

type PetDurationTier struct {
	MinimumLevel int32 `yaml:"minimum_level"`
	DurationMS   int64 `yaml:"duration_ms"`
}

// TownIdleExp 主城（城镇）挂机经验。线上按整分钟节拍只对停在城镇地图的角色
// 下发固定经验，见 server-mysql/idle_exp.go 的抓包说明。
type TownIdleExp struct {
	Enabled         bool    `yaml:"enabled"`
	IntervalSeconds int64   `yaml:"interval_seconds"`
	ExpPerTick      int64   `yaml:"exp_per_tick"`
	MapIDs          []int32 `yaml:"map_ids"`
}

type ShopSaleRule struct {
	ItemID    int32 `yaml:"item_id"`
	Enabled   bool  `yaml:"enabled"`
	PriceCoin int64 `yaml:"price_coin"`
}

type ShopSaleCategory struct {
	Enabled   bool  `yaml:"enabled"`
	PriceCoin int64 `yaml:"price_coin"`
}

type Config struct {
	Version int `yaml:"version"`
	Quiz    struct {
		QuestionsPerRound int32 `yaml:"questions_per_round"`
	} `yaml:"quiz"`
	NewRole struct {
		Voucher int64 `yaml:"voucher"`
	} `yaml:"new_role"`
	Pet struct {
		PlayDurationMS       int64             `yaml:"play_duration_ms"`
		ExploreDurationMS    int64             `yaml:"explore_duration_ms"`
		ExperienceDurationMS int64             `yaml:"experience_duration_ms"`
		QuickEndVoucher      int64             `yaml:"quick_end_voucher"`
		DurationTiers        []PetDurationTier `yaml:"duration_tiers"`
	} `yaml:"pet"`
	OnlineReward struct {
		IntervalMS     int64 `yaml:"interval_ms"`
		VoucherAmount  int32 `yaml:"voucher_amount"`
		MailExpireDays int32 `yaml:"mail_expire_days"`
	} `yaml:"online_reward"`
	VoucherGift struct {
		RandomRanges map[string]IntegerRange `yaml:"random_ranges"`
	} `yaml:"voucher_gift"`
	MapCoin struct {
		SpawnChancePercent float64 `yaml:"spawn_chance_percent"`
		MinimumReward      int64   `yaml:"minimum_reward"`
		MaximumReward      int64   `yaml:"maximum_reward"`
		DailyBaseCount     int32   `yaml:"daily_base_count"`
		ExtraCountVoucher  int64   `yaml:"extra_count_voucher"`
	} `yaml:"map_coin"`
	RunMap struct {
		DailyNormalDurationMS int64 `yaml:"daily_normal_duration_ms"`
		TimezoneOffsetHours   int   `yaml:"timezone_offset_hours"`
	} `yaml:"run_map"`
	Progression struct {
		ExperienceGainPercent float64 `yaml:"experience_gain_percent"`
		SkillPointLevels      int32   `yaml:"skill_point_levels"`
	} `yaml:"progression"`
	TownIdleExp TownIdleExp `yaml:"town_idle_exp"`
	Combat      struct {
		SkillMPCostPercent float64 `yaml:"skill_mp_cost_percent"`
		// EffectMaxStacks caps the layers one stacking effect may accumulate.
		// The online tables carry no such cap, so it is an operator lever.
		EffectMaxStacks int `yaml:"effect_max_stacks"`
		// EffectTickIntervalMS is how often a periodic effect ticks.
		// The online tables carry no cadence at all, so it is an operator lever.
		EffectTickIntervalMS    int64            `yaml:"effect_tick_interval_ms"`
		PeriodicTickOverridesMS map[string]int64 `yaml:"periodic_tick_overrides_ms"`
	} `yaml:"combat"`
	Market struct {
		MinimumPercent      uint64 `yaml:"minimum_percent"`
		MaximumPercent      uint64 `yaml:"maximum_percent"`
		TimezoneOffsetHours int    `yaml:"timezone_offset_hours"`
	} `yaml:"market"`
	ShopSale struct {
		UseOnlinePrices bool `yaml:"use_online_prices"`
		// LegacyUseOfficialPrices 是改名前的旧键，仅用于兼容已发布的配置树。
		// 新写入的配置一律使用 use_online_prices；两者同时出现时以新键为准。
		LegacyUseOfficialPrices bool             `yaml:"use_official_prices"`
		FallbackPriceCoin       int64            `yaml:"fallback_price_coin"`
		MarketRecoveryPercent   int64            `yaml:"market_recovery_percent"`
		ManualEquipment         ShopSaleCategory `yaml:"manual_equipment"`
		Items                   []ShopSaleRule   `yaml:"items"`
	} `yaml:"shop_sale"`
	Equipment struct {
		MinimumMainAttributePercent float64 `yaml:"minimum_main_attribute_percent"`
		MaximumMainAttributePercent float64 `yaml:"maximum_main_attribute_percent"`
		StrengthSafetyLevels        []int32 `yaml:"strength_safety_levels"`
	} `yaml:"equipment"`
	Dungeon struct {
		TimezoneOffsetHours      int   `yaml:"timezone_offset_hours"`
		SpaceTravelDailyAttempts int32 `yaml:"space_travel_daily_attempts"`
		DeathTowerDailyAttempts  int32 `yaml:"death_tower_daily_attempts"`
		FamilyBossDailyKeys      int32 `yaml:"family_boss_daily_keys"`
	} `yaml:"dungeon"`
	WorldBoss struct {
		RewardParentsetID int32 `yaml:"reward_parentset_id"`
	} `yaml:"world_boss"`
	HardMainStory struct {
		NormalRewardMultiplier int32 `yaml:"normal_reward_multiplier"`
	} `yaml:"hard_main_story"`
	PersonalPVP struct {
		VictoryScoreDelta   int32 `yaml:"victory_score_delta"`
		DefeatScoreDelta    int32 `yaml:"defeat_score_delta"`
		TimezoneOffsetHours int   `yaml:"timezone_offset_hours"`
	} `yaml:"personal_pvp"`
	FamilyBoss struct {
		BonusDropChancePercent   float64 `yaml:"bonus_drop_chance_percent"`
		SkinSharePercent         float64 `yaml:"skin_share_percent"`
		AttributeBaseMultiplier  float64 `yaml:"attribute_base_multiplier"`
		AttributeStackIntervalMS int64   `yaml:"attribute_stack_interval_ms"`
		GemItemIDs               []int32 `yaml:"gem_item_ids"`
		SkinItemIDs              []int32 `yaml:"skin_item_ids"`
	} `yaml:"family_boss"`
	ManualEquip ManualEquipConfig `yaml:"manual_equip"`
}

func Defaults() Config {
	var config Config
	config.Version = CurrentVersion
	config.Quiz.QuestionsPerRound = 10
	config.NewRole.Voucher = DefaultNewRoleVoucher
	config.Pet.PlayDurationMS = 60 * 60 * 1000
	config.Pet.ExploreDurationMS = 60 * 60 * 1000
	config.Pet.ExperienceDurationMS = 60 * 60 * 1000
	config.Pet.QuickEndVoucher = 10
	config.Pet.DurationTiers = []PetDurationTier{
		{MinimumLevel: 1, DurationMS: 10 * 60 * 1000},
		{MinimumLevel: 21, DurationMS: 20 * 60 * 1000},
		{MinimumLevel: 41, DurationMS: 30 * 60 * 1000},
		{MinimumLevel: 61, DurationMS: 40 * 60 * 1000},
		{MinimumLevel: 81, DurationMS: 50 * 60 * 1000},
		{MinimumLevel: 101, DurationMS: 60 * 60 * 1000},
	}
	config.OnlineReward.IntervalMS = 60 * 60 * 1000
	config.OnlineReward.VoucherAmount = 100
	config.OnlineReward.MailExpireDays = 30
	config.VoucherGift.RandomRanges = map[string]IntegerRange{
		"110429": {Minimum: 1, Maximum: 500},
	}
	config.MapCoin.SpawnChancePercent = 5
	config.MapCoin.MinimumReward = 3_000_000
	config.MapCoin.MaximumReward = 5_000_000
	config.MapCoin.DailyBaseCount = 1
	config.MapCoin.ExtraCountVoucher = 20
	config.RunMap.DailyNormalDurationMS = 2 * 60 * 60 * 1000
	config.RunMap.TimezoneOffsetHours = 8
	config.Progression.ExperienceGainPercent = 200
	config.Progression.SkillPointLevels = 200
	config.TownIdleExp.Enabled = true
	config.TownIdleExp.IntervalSeconds = 60
	config.TownIdleExp.ExpPerTick = 24560
	config.TownIdleExp.MapIDs = []int32{10004, 1000401}
	config.Combat.SkillMPCostPercent = 50
	config.Combat.EffectMaxStacks = DefaultEffectMaxStacks
	config.Combat.EffectTickIntervalMS = DefaultEffectTickIntervalMS
	config.Combat.PeriodicTickOverridesMS = map[string]int64{"50001033": 3500}
	config.Market.MinimumPercent = 85
	config.Market.MaximumPercent = 125
	config.Market.TimezoneOffsetHours = 8
	config.ShopSale.UseOnlinePrices = true
	// 兼容旧键：已发布的配置树里只有 use_official_prices，新键缺失时沿用旧值。
	if !config.ShopSale.UseOnlinePrices && config.ShopSale.LegacyUseOfficialPrices {
		config.ShopSale.UseOnlinePrices = true
	}
	config.ShopSale.FallbackPriceCoin = 500
	config.ShopSale.MarketRecoveryPercent = 85
	config.ShopSale.ManualEquipment = ShopSaleCategory{Enabled: true, PriceCoin: 500}
	config.Equipment.MinimumMainAttributePercent = -25
	config.Equipment.MaximumMainAttributePercent = 25
	config.Equipment.StrengthSafetyLevels = []int32{8, 13, 17}
	config.Dungeon.TimezoneOffsetHours = 8
	config.Dungeon.SpaceTravelDailyAttempts = 50
	config.Dungeon.DeathTowerDailyAttempts = 10
	config.Dungeon.FamilyBossDailyKeys = 2
	config.WorldBoss.RewardParentsetID = 10125
	config.HardMainStory.NormalRewardMultiplier = 3
	config.PersonalPVP.VictoryScoreDelta = 10
	config.PersonalPVP.DefeatScoreDelta = -5
	config.PersonalPVP.TimezoneOffsetHours = 8
	config.FamilyBoss.BonusDropChancePercent = 5
	config.FamilyBoss.SkinSharePercent = 10
	config.FamilyBoss.AttributeBaseMultiplier = 1.20
	config.FamilyBoss.AttributeStackIntervalMS = 4710
	config.ManualEquip = defaultManualEquip()
	return config
}

func Decode(value interface{}) (Config, error) {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return Config{}, fmt.Errorf("encode normalized config: %w", err)
	}
	var supplied map[string]interface{}
	if err := yaml.Unmarshal(raw, &supplied); err != nil {
		return Config{}, fmt.Errorf("decode normalized config tree: %w", err)
	}
	defaultRaw, err := yaml.Marshal(Defaults())
	if err != nil {
		return Config{}, fmt.Errorf("encode default gameplay config: %w", err)
	}
	var merged map[string]interface{}
	if err := yaml.Unmarshal(defaultRaw, &merged); err != nil {
		return Config{}, fmt.Errorf("decode default gameplay config tree: %w", err)
	}
	mergeConfigMapping(merged, supplied)
	raw, err = yaml.Marshal(merged)
	if err != nil {
		return Config{}, fmt.Errorf("encode merged gameplay config: %w", err)
	}
	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode gameplay config: %w", err)
	}
	if err := Validate(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func mergeConfigMapping(target, supplied map[string]interface{}) {
	for key, value := range supplied {
		suppliedMap, suppliedIsMap := value.(map[string]interface{})
		targetMap, targetIsMap := target[key].(map[string]interface{})
		if suppliedIsMap && targetIsMap {
			mergeConfigMapping(targetMap, suppliedMap)
			continue
		}
		target[key] = value
	}
}

func Validate(config Config) error {
	if config.Version != CurrentVersion {
		return fmt.Errorf("version=%d, want %d", config.Version, CurrentVersion)
	}
	if config.Quiz.QuestionsPerRound < 1 || config.Quiz.QuestionsPerRound > 1000 {
		return fmt.Errorf("quiz.questions_per_round must be 1..1000")
	}
	if config.NewRole.Voucher < 0 || config.NewRole.Voucher > 1_000_000_000 {
		return fmt.Errorf("new_role.voucher must be 0..1000000000")
	}
	for name, value := range map[string]int64{
		"play_duration_ms":       config.Pet.PlayDurationMS,
		"explore_duration_ms":    config.Pet.ExploreDurationMS,
		"experience_duration_ms": config.Pet.ExperienceDurationMS,
	} {
		if value < 1000 || value > 7*24*60*60*1000 {
			return fmt.Errorf("pet.%s must be 1000..604800000", name)
		}
	}
	if config.Pet.QuickEndVoucher < 0 || config.Pet.QuickEndVoucher > 1_000_000_000 {
		return fmt.Errorf("pet.quick_end_voucher must be 0..1000000000")
	}
	for index, tier := range config.Pet.DurationTiers {
		if tier.MinimumLevel < 1 || tier.MinimumLevel > 1_000_000 {
			return fmt.Errorf("pet.duration_tiers[%d].minimum_level must be 1..1000000", index)
		}
		if tier.DurationMS < 1000 || tier.DurationMS > 7*24*60*60*1000 {
			return fmt.Errorf("pet.duration_tiers[%d].duration_ms must be 1000..604800000", index)
		}
		if index == 0 && tier.MinimumLevel != 1 {
			return fmt.Errorf("pet.duration_tiers[0].minimum_level must be 1")
		}
		if index > 0 && tier.MinimumLevel <= config.Pet.DurationTiers[index-1].MinimumLevel {
			return fmt.Errorf("pet.duration_tiers minimum levels must be strictly increasing")
		}
		if index > 0 && tier.DurationMS <= config.Pet.DurationTiers[index-1].DurationMS {
			return fmt.Errorf("pet.duration_tiers durations must be strictly increasing")
		}
	}
	if config.OnlineReward.IntervalMS < 60*1000 || config.OnlineReward.IntervalMS > 30*24*60*60*1000 {
		return fmt.Errorf("online_reward.interval_ms must be 60000..2592000000")
	}
	if config.OnlineReward.VoucherAmount < 1 || config.OnlineReward.VoucherAmount > 1_000_000_000 {
		return fmt.Errorf("online_reward.voucher_amount must be 1..1000000000")
	}
	if config.OnlineReward.MailExpireDays < 1 || config.OnlineReward.MailExpireDays > 3650 {
		return fmt.Errorf("online_reward.mail_expire_days must be 1..3650")
	}
	for itemID, value := range config.VoucherGift.RandomRanges {
		parsedID, err := strconv.ParseInt(itemID, 10, 32)
		if err != nil || parsedID <= 0 || value.Minimum < 0 || value.Maximum < value.Minimum || value.Maximum > 1_000_000_000_000 {
			return fmt.Errorf("voucher_gift.random_ranges[%q] is invalid", itemID)
		}
	}
	if config.MapCoin.SpawnChancePercent < 0 || config.MapCoin.SpawnChancePercent > 100 {
		return fmt.Errorf("map_coin.spawn_chance_percent must be 0..100")
	}
	if config.MapCoin.MinimumReward < 1 || config.MapCoin.MaximumReward < config.MapCoin.MinimumReward ||
		config.MapCoin.MaximumReward > 1_000_000_000_000 {
		return fmt.Errorf("map_coin reward range must be ordered within 1..1000000000000")
	}
	if config.MapCoin.DailyBaseCount < 0 || config.MapCoin.DailyBaseCount > 1_000_000 {
		return fmt.Errorf("map_coin.daily_base_count must be 0..1000000")
	}
	if config.MapCoin.ExtraCountVoucher < 0 || config.MapCoin.ExtraCountVoucher > 1_000_000_000 {
		return fmt.Errorf("map_coin.extra_count_voucher must be 0..1000000000")
	}
	if config.RunMap.DailyNormalDurationMS < 0 || config.RunMap.DailyNormalDurationMS > 7*24*60*60*1000 {
		return fmt.Errorf("run_map.daily_normal_duration_ms must be 0..604800000")
	}
	if config.RunMap.TimezoneOffsetHours < -12 || config.RunMap.TimezoneOffsetHours > 14 {
		return fmt.Errorf("run_map.timezone_offset_hours must be -12..14")
	}
	if !(config.Progression.ExperienceGainPercent >= 1 && config.Progression.ExperienceGainPercent <= 10000) {
		return fmt.Errorf("progression.experience_gain_percent must be 1..10000")
	}
	if config.Progression.SkillPointLevels < 1 || config.Progression.SkillPointLevels > 100000 {
		return fmt.Errorf("progression.skill_point_levels must be 1..100000")
	}
	if config.TownIdleExp.IntervalSeconds < 1 || config.TownIdleExp.IntervalSeconds > 86400 {
		return fmt.Errorf("town_idle_exp.interval_seconds must be 1..86400")
	}
	if config.TownIdleExp.ExpPerTick < 0 || config.TownIdleExp.ExpPerTick > 1_000_000_000_000 {
		return fmt.Errorf("town_idle_exp.exp_per_tick must be 0..1000000000000")
	}
	if config.TownIdleExp.Enabled && len(config.TownIdleExp.MapIDs) == 0 {
		return fmt.Errorf("town_idle_exp.map_ids must not be empty while enabled")
	}
	for _, mapID := range config.TownIdleExp.MapIDs {
		if mapID <= 0 {
			return fmt.Errorf("town_idle_exp.map_ids contains invalid map %d", mapID)
		}
	}
	if config.Combat.SkillMPCostPercent < 0 || config.Combat.SkillMPCostPercent > 100 {
		return fmt.Errorf("combat.skill_mp_cost_percent must be 0..100")
	}
	if config.Combat.EffectMaxStacks < 1 || config.Combat.EffectMaxStacks > 100 {
		return fmt.Errorf("combat.effect_max_stacks must be 1..100")
	}
	// 100ms keeps a tick from turning into a per-frame flood; 60s keeps the
	// longest shipped 26-second 中毒 (six ticks at the captured 4s) sane.
	if config.Combat.EffectTickIntervalMS < 100 || config.Combat.EffectTickIntervalMS > 60000 {
		return fmt.Errorf("combat.effect_tick_interval_ms must be 100..60000")
	}
	for rawModifierID, intervalMS := range config.Combat.PeriodicTickOverridesMS {
		modifierID, err := strconv.ParseInt(rawModifierID, 10, 64)
		if err != nil || modifierID <= 0 {
			return fmt.Errorf("combat.periodic_tick_overrides_ms contains invalid modifier %q", rawModifierID)
		}
		if intervalMS < 100 || intervalMS > 60000 {
			return fmt.Errorf("combat.periodic_tick_overrides_ms[%s] must be 100..60000", rawModifierID)
		}
	}
	if config.Market.MinimumPercent < 1 || config.Market.MaximumPercent > 1000 ||
		config.Market.MaximumPercent < config.Market.MinimumPercent {
		return fmt.Errorf("market percent range must be ordered within 1..1000")
	}
	if config.Market.TimezoneOffsetHours < -12 || config.Market.TimezoneOffsetHours > 14 {
		return fmt.Errorf("market.timezone_offset_hours must be -12..14")
	}
	if config.ShopSale.ManualEquipment.PriceCoin <= 0 || config.ShopSale.ManualEquipment.PriceCoin > 1_000_000_000_000 {
		return fmt.Errorf("shop_sale.manual_equipment.price_coin must be 1..1000000000000")
	}
	if config.ShopSale.FallbackPriceCoin <= 0 || config.ShopSale.FallbackPriceCoin > 1_000_000_000_000 {
		return fmt.Errorf("shop_sale.fallback_price_coin must be 1..1000000000000")
	}
	if config.ShopSale.MarketRecoveryPercent < 1 || config.ShopSale.MarketRecoveryPercent > 100 {
		return fmt.Errorf("shop_sale.market_recovery_percent must be 1..100")
	}
	seenSaleItems := make(map[int32]struct{}, len(config.ShopSale.Items))
	for index, rule := range config.ShopSale.Items {
		if rule.ItemID <= 0 {
			return fmt.Errorf("shop_sale.items[%d].item_id must be positive", index)
		}
		if _, duplicate := seenSaleItems[rule.ItemID]; duplicate {
			return fmt.Errorf("shop_sale.items contains duplicate item_id %d", rule.ItemID)
		}
		seenSaleItems[rule.ItemID] = struct{}{}
		if rule.PriceCoin <= 0 || rule.PriceCoin > 1_000_000_000_000 {
			return fmt.Errorf("shop_sale.items[%d].price_coin must be 1..1000000000000", index)
		}
	}
	if config.Equipment.MinimumMainAttributePercent < -100 ||
		config.Equipment.MaximumMainAttributePercent > 100 ||
		config.Equipment.MaximumMainAttributePercent < config.Equipment.MinimumMainAttributePercent {
		return fmt.Errorf("equipment main attribute range must be ordered within -100..100")
	}
	for index, level := range config.Equipment.StrengthSafetyLevels {
		if level < 1 || level >= 20 {
			return fmt.Errorf("equipment.strength_safety_levels[%d] must be 1..19", index)
		}
		if index > 0 && level <= config.Equipment.StrengthSafetyLevels[index-1] {
			return fmt.Errorf("equipment.strength_safety_levels must be strictly increasing")
		}
	}
	if config.Dungeon.TimezoneOffsetHours < -12 || config.Dungeon.TimezoneOffsetHours > 14 {
		return fmt.Errorf("dungeon.timezone_offset_hours must be -12..14")
	}
	for name, value := range map[string]int32{
		"space_travel_daily_attempts": config.Dungeon.SpaceTravelDailyAttempts,
		"death_tower_daily_attempts":  config.Dungeon.DeathTowerDailyAttempts,
		"family_boss_daily_keys":      config.Dungeon.FamilyBossDailyKeys,
	} {
		if value < 0 || value > 1_000_000 {
			return fmt.Errorf("dungeon.%s must be 0..1000000", name)
		}
	}
	if config.WorldBoss.RewardParentsetID <= 0 {
		return fmt.Errorf("world_boss.reward_parentset_id must be positive")
	}
	if config.HardMainStory.NormalRewardMultiplier < 1 || config.HardMainStory.NormalRewardMultiplier > 100 {
		return fmt.Errorf("hard_main_story.normal_reward_multiplier must be 1..100")
	}
	if config.PersonalPVP.VictoryScoreDelta <= 0 || config.PersonalPVP.VictoryScoreDelta > 1_000_000 {
		return fmt.Errorf("personal_pvp.victory_score_delta must be 1..1000000")
	}
	if config.PersonalPVP.DefeatScoreDelta > 0 || config.PersonalPVP.DefeatScoreDelta < -1_000_000 {
		return fmt.Errorf("personal_pvp.defeat_score_delta must be -1000000..0")
	}
	if config.PersonalPVP.TimezoneOffsetHours < -12 || config.PersonalPVP.TimezoneOffsetHours > 14 {
		return fmt.Errorf("personal_pvp.timezone_offset_hours must be -12..14")
	}
	if config.FamilyBoss.BonusDropChancePercent < 0 || config.FamilyBoss.BonusDropChancePercent > 100 {
		return fmt.Errorf("family_boss.bonus_drop_chance_percent must be 0..100")
	}
	if config.FamilyBoss.SkinSharePercent < 0 || config.FamilyBoss.SkinSharePercent > 100 {
		return fmt.Errorf("family_boss.skin_share_percent must be 0..100")
	}
	if !(config.FamilyBoss.AttributeBaseMultiplier > 0 && config.FamilyBoss.AttributeBaseMultiplier <= 10) {
		return fmt.Errorf("family_boss.attribute_base_multiplier must be greater than 0 and at most 10")
	}
	if config.FamilyBoss.AttributeStackIntervalMS < 100 || config.FamilyBoss.AttributeStackIntervalMS > 60000 {
		return fmt.Errorf("family_boss.attribute_stack_interval_ms must be 100..60000")
	}
	if err := validateIDs("family_boss.gem_item_ids", config.FamilyBoss.GemItemIDs); err != nil {
		return err
	}
	if err := validateIDs("family_boss.skin_item_ids", config.FamilyBoss.SkinItemIDs); err != nil {
		return err
	}
	return validateManualEquip(config.ManualEquip)
}

func validateIDs(name string, values []int32) error {
	seen := make(map[int32]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			return fmt.Errorf("%s contains non-positive id %d", name, value)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains duplicate id %d", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
