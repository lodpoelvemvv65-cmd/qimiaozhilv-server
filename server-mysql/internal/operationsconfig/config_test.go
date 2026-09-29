package operationsconfig

import (
	"math"
	"testing"
)

func TestDecodeKeepsDefaultsForNewMissingSections(t *testing.T) {
	root := map[string]interface{}{
		"version": 1,
		"quiz":    map[string]interface{}{"questions_per_round": 10},
		"pet": map[string]interface{}{
			"play_duration_ms": 3600000, "explore_duration_ms": 3600000,
			"experience_duration_ms": 3600000, "quick_end_voucher": 10,
		},
		"voucher_gift": map[string]interface{}{"random_ranges": map[string]interface{}{}},
		"market": map[string]interface{}{
			"minimum_percent": 85, "maximum_percent": 125, "timezone_offset_hours": 8,
		},
		"equipment": map[string]interface{}{
			"minimum_main_attribute_percent": -25, "maximum_main_attribute_percent": 25,
		},
		"family_boss": map[string]interface{}{
			"bonus_drop_chance_percent": 5, "skin_share_percent": 10,
			"gem_item_ids": []interface{}{}, "skin_item_ids": []interface{}{},
		},
	}
	config, err := Decode(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.Dungeon.SpaceTravelDailyAttempts != 50 || config.Dungeon.DeathTowerDailyAttempts != 10 ||
		config.Dungeon.FamilyBossDailyKeys != 2 || config.Dungeon.TimezoneOffsetHours != 8 {
		t.Fatalf("missing dungeon section did not inherit defaults: %+v", config.Dungeon)
	}
	if config.WorldBoss.RewardParentsetID != 10125 {
		t.Fatalf("missing world boss section did not inherit default: %+v", config.WorldBoss)
	}
	if config.HardMainStory.NormalRewardMultiplier != 3 {
		t.Fatalf("missing hard main-story section did not inherit default: %+v", config.HardMainStory)
	}
	if config.PersonalPVP.VictoryScoreDelta != 10 || config.PersonalPVP.DefeatScoreDelta != -5 ||
		config.PersonalPVP.TimezoneOffsetHours != 8 {
		t.Fatalf("missing personal PVP section did not inherit default: %+v", config.PersonalPVP)
	}
	if config.OnlineReward.IntervalMS != 3600000 || config.OnlineReward.VoucherAmount != 100 ||
		config.OnlineReward.MailExpireDays != 30 {
		t.Fatalf("missing online reward section did not inherit defaults: %+v", config.OnlineReward)
	}
	if len(config.Pet.DurationTiers) != 6 || config.Pet.DurationTiers[0].MinimumLevel != 1 ||
		config.Pet.DurationTiers[0].DurationMS != 600000 || config.Pet.DurationTiers[5].MinimumLevel != 101 ||
		config.Pet.DurationTiers[5].DurationMS != 3600000 {
		t.Fatalf("missing pet duration tiers did not inherit defaults: %+v", config.Pet.DurationTiers)
	}
	if config.MapCoin.SpawnChancePercent != 5 || config.MapCoin.MinimumReward != 3_000_000 ||
		config.MapCoin.MaximumReward != 5_000_000 || config.MapCoin.DailyBaseCount != 1 ||
		config.MapCoin.ExtraCountVoucher != 20 {
		t.Fatalf("missing map coin section did not inherit defaults: %+v", config.MapCoin)
	}
	if config.Progression.ExperienceGainPercent != 200 || config.Progression.SkillPointLevels != 200 {
		t.Fatalf("missing progression section did not inherit defaults: %+v", config.Progression)
	}
	if config.Combat.EffectMaxStacks != DefaultEffectMaxStacks ||
		config.Combat.EffectTickIntervalMS != DefaultEffectTickIntervalMS {
		t.Fatalf("missing combat section did not inherit the defaults: %+v", config.Combat)
	}
	if config.Combat.PeriodicTickOverridesMS["50001033"] != 3500 {
		t.Fatalf("missing periodic override default: %+v", config.Combat.PeriodicTickOverridesMS)
	}
	if config.FamilyBoss.AttributeBaseMultiplier != 1.20 || config.FamilyBoss.AttributeStackIntervalMS != 4710 {
		t.Fatalf("missing family boss attribute defaults: %+v", config.FamilyBoss)
	}
	partial, err := Decode(map[string]interface{}{
		"version": 1,
		"progression": map[string]interface{}{
			"experience_gain_percent": 150,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if partial.Progression.ExperienceGainPercent != 150 || partial.Progression.SkillPointLevels != 200 {
		t.Fatalf("partial progression did not keep default skill point interval: %+v", partial.Progression)
	}
	if !config.ShopSale.UseOnlinePrices || config.ShopSale.FallbackPriceCoin != 500 || config.ShopSale.MarketRecoveryPercent != 85 ||
		config.ShopSale.ManualEquipment != (ShopSaleCategory{Enabled: true, PriceCoin: 500}) || len(config.ShopSale.Items) != 0 {
		t.Fatalf("missing shop sale section did not inherit defaults: %+v", config.ShopSale)
	}
	if config.ManualEquip.Normal.MinStar != 3 || !config.ManualEquip.Normal.Bound ||
		config.ManualEquip.Rare.RedPercent != 5 || config.ManualEquip.Rare.PityMinQuality != 5 ||
		config.ManualEquip.Epic.MinQuality != 3 || config.ManualEquip.Epic.PityMinQuality != 6 ||
		!config.ManualEquip.UpgradeRare.Bound {
		t.Fatalf("missing manual equip section did not inherit defaults: %+v", config.ManualEquip)
	}
}

func TestValidateRejectsInvalidPetDurationTiers(t *testing.T) {
	tests := []struct {
		name  string
		tiers []PetDurationTier
	}{
		{"first tier above level one", []PetDurationTier{{MinimumLevel: 2, DurationMS: 600000}}},
		{"duplicate level", []PetDurationTier{{MinimumLevel: 1, DurationMS: 600000}, {MinimumLevel: 1, DurationMS: 1200000}}},
		{"decreasing duration", []PetDurationTier{{MinimumLevel: 1, DurationMS: 1200000}, {MinimumLevel: 21, DurationMS: 600000}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Defaults()
			config.Pet.DurationTiers = test.tiers
			if err := Validate(config); err == nil {
				t.Fatal("invalid pet duration tiers were accepted")
			}
		})
	}
}

func TestValidateRejectsInvalidOnlineReward(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"short interval", func(config *Config) { config.OnlineReward.IntervalMS = 59999 }},
		{"zero voucher", func(config *Config) { config.OnlineReward.VoucherAmount = 0 }},
		{"zero expiry", func(config *Config) { config.OnlineReward.MailExpireDays = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Defaults()
			test.mutate(&config)
			if err := Validate(config); err == nil {
				t.Fatal("invalid online reward configuration was accepted")
			}
		})
	}
}

func TestValidateRejectsInvalidSkillMPCostPercent(t *testing.T) {
	for _, value := range []float64{-0.01, 100.01} {
		config := Defaults()
		config.Combat.SkillMPCostPercent = value
		if err := Validate(config); err == nil {
			t.Fatalf("skill MP cost percent %v was accepted", value)
		}
	}
}

func TestValidateRejectsInvalidEffectMaxStacks(t *testing.T) {
	for _, value := range []int{0, -1, 101} {
		config := Defaults()
		config.Combat.EffectMaxStacks = value
		if err := Validate(config); err == nil {
			t.Fatalf("effect max stacks %d was accepted", value)
		}
	}
	config := Defaults()
	if config.Combat.EffectMaxStacks != DefaultEffectMaxStacks {
		t.Fatalf("default effect max stacks = %d, want %d", config.Combat.EffectMaxStacks, DefaultEffectMaxStacks)
	}
}

func TestValidateRejectsInvalidEffectTickIntervalMS(t *testing.T) {
	for _, value := range []int64{0, -1, 99, 60001} {
		config := Defaults()
		config.Combat.EffectTickIntervalMS = value
		if err := Validate(config); err == nil {
			t.Fatalf("effect tick interval %dms was accepted", value)
		}
	}
	config := Defaults()
	if config.Combat.EffectTickIntervalMS != DefaultEffectTickIntervalMS {
		t.Fatalf("default effect tick interval = %dms, want the captured %dms",
			config.Combat.EffectTickIntervalMS, DefaultEffectTickIntervalMS)
	}
}

func TestValidateRejectsInvalidPeriodicTickOverrides(t *testing.T) {
	tests := []map[string]int64{
		{"not-a-modifier": 3500},
		{"0": 3500},
		{"50001033": 99},
		{"50001033": 60001},
	}
	for _, overrides := range tests {
		config := Defaults()
		config.Combat.PeriodicTickOverridesMS = overrides
		if err := Validate(config); err == nil {
			t.Fatalf("invalid periodic overrides were accepted: %+v", overrides)
		}
	}
}

func TestValidateRejectsInvalidFamilyBossStackIntervalMS(t *testing.T) {
	for _, value := range []int64{0, -1, 99, 60001} {
		config := Defaults()
		config.FamilyBoss.AttributeStackIntervalMS = value
		if err := Validate(config); err == nil {
			t.Fatalf("family boss stack interval %dms was accepted", value)
		}
	}
}

func TestValidateRejectsInvalidFamilyBossAttributeBaseMultiplier(t *testing.T) {
	for _, value := range []float64{0, -1, 10.01, math.NaN(), math.Inf(1)} {
		config := Defaults()
		config.FamilyBoss.AttributeBaseMultiplier = value
		if err := Validate(config); err == nil {
			t.Fatalf("family boss attribute base multiplier %v was accepted", value)
		}
	}
}

func TestValidateRejectsInvalidExperienceGainPercent(t *testing.T) {
	for _, value := range []float64{0, 10000.01, math.NaN()} {
		config := Defaults()
		config.Progression.ExperienceGainPercent = value
		if err := Validate(config); err == nil {
			t.Fatalf("experience gain percent %v was accepted", value)
		}
	}
}

func TestValidateRejectsInvalidSkillPointLevels(t *testing.T) {
	for _, value := range []int32{0, 100001} {
		config := Defaults()
		config.Progression.SkillPointLevels = value
		if err := Validate(config); err == nil {
			t.Fatalf("skill point levels %v was accepted", value)
		}
	}
}

func TestValidateRejectsEmptyWorldBossRewardChain(t *testing.T) {
	config := Defaults()
	config.WorldBoss.RewardParentsetID = 0
	if err := Validate(config); err == nil {
		t.Fatal("zero world boss reward parentset was accepted")
	}
}

func TestValidateRejectsInvalidHardMainStoryRewardMultiplier(t *testing.T) {
	config := Defaults()
	config.HardMainStory.NormalRewardMultiplier = 0
	if err := Validate(config); err == nil {
		t.Fatal("zero hard main-story reward multiplier was accepted")
	}
}

func TestValidateRejectsInvalidMapCoinSettings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"negative chance", func(config *Config) { config.MapCoin.SpawnChancePercent = -1 }},
		{"chance above one hundred", func(config *Config) { config.MapCoin.SpawnChancePercent = 101 }},
		{"zero minimum reward", func(config *Config) { config.MapCoin.MinimumReward = 0 }},
		{"reversed reward range", func(config *Config) { config.MapCoin.MaximumReward = config.MapCoin.MinimumReward - 1 }},
		{"negative daily count", func(config *Config) { config.MapCoin.DailyBaseCount = -1 }},
		{"negative voucher cost", func(config *Config) { config.MapCoin.ExtraCountVoucher = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Defaults()
			test.mutate(&config)
			if err := Validate(config); err == nil {
				t.Fatal("invalid map coin settings were accepted")
			}
		})
	}
}

func TestValidateRejectsInvalidManualEquipRecipe(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"reversed star range", func(config *Config) { config.ManualEquip.Normal.MaxStar = 2 }},
		{"empty weights", func(config *Config) { config.ManualEquip.Rare.QualityWeights = nil }},
		{"red percent too high", func(config *Config) { config.ManualEquip.Rare.RedPercent = 101 }},
		{"pity range reversed", func(config *Config) { config.ManualEquip.Epic.PityMaxCount = 10 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Defaults()
			test.mutate(&config)
			if err := Validate(config); err == nil {
				t.Fatal("invalid manual equip recipe was accepted")
			}
		})
	}
}

func TestValidateRejectsInvalidShopSaleRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero manual equipment price", func(config *Config) { config.ShopSale.ManualEquipment.PriceCoin = 0 }},
		{"zero fallback price", func(config *Config) { config.ShopSale.FallbackPriceCoin = 0 }},
		{"recovery over 100", func(config *Config) { config.ShopSale.MarketRecoveryPercent = 101 }},
		{"non-positive item id", func(config *Config) {
			config.ShopSale.Items = []ShopSaleRule{{ItemID: 0, Enabled: true, PriceCoin: 1}}
		}},
		{"duplicate item id", func(config *Config) {
			config.ShopSale.Items = []ShopSaleRule{{ItemID: 9001, Enabled: true, PriceCoin: 1}, {ItemID: 9001, Enabled: false, PriceCoin: 1}}
		}},
		{"zero price", func(config *Config) {
			config.ShopSale.Items = []ShopSaleRule{{ItemID: 9001, Enabled: true, PriceCoin: 0}}
		}},
		{"excessive price", func(config *Config) {
			config.ShopSale.Items = []ShopSaleRule{{ItemID: 9001, Enabled: true, PriceCoin: 1_000_000_000_001}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Defaults()
			test.mutate(&config)
			if err := Validate(config); err == nil {
				t.Fatal("invalid shop sale rule was accepted")
			}
		})
	}
}
