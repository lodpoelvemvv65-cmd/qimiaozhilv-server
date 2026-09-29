package operationsconfig

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// AffixWashConfigName 是洗词缀专用配置在 MySQL game_config_nodes 中的配置名。
// 它与 Gameplay 相互独立：只改洗词缀概率时不需要重新发布整棵 Gameplay 配置树。
const AffixWashConfigName = "AffixWash"

// AffixWashTier 把装备品质色映射到 EquipAffixConfig 的档位（1..5）。
// 档位即 EquipAffixConfig.AffixArr 的下标 +1，决定该词条这一档的数值。
type AffixWashTier struct {
	Quality int32 `yaml:"quality"`
	Tier    int32 `yaml:"tier"`
}

// AffixWashRule 是洗词缀（20347）的抽取规则。三个百分比在一次洗练里按
// 满六维 -> 六维 -> 无词缀 的顺序依次判定，命中即决定本次结果，因此互斥。
type AffixWashRule struct {
	// EmptyPercent：本次洗练整体落空的概率（百分数）。命中时下发空词缀列表，
	// 客户端 RefreshEquipUI 的 ShowAffixAttribute 显示“无词缀”。
	EmptyPercent float64 `yaml:"empty_percent"`
	// SixDimensionPercent：本次洗练直接产出“六维”词条的概率（百分数）。
	// 六维 = 力量/敏捷/精神/智慧/体质/耐力相关的词条（单、双、三属性都算）。
	// 命中时本次只产出 1 条六维词条，不再叠加普通词条。
	SixDimensionPercent float64 `yaml:"six_dimension_percent"`
	// SixDimensionFullPercent：本次洗练直接产出“满六维”的概率（百分数）。
	// 满六维 = 三属性六维词条取最高档（例如 510/255/255）。它独立于
	// SixDimensionPercent，命中时不再判定后者。
	SixDimensionFullPercent float64 `yaml:"six_dimension_full_percent"`
	// SixDimensionTiers：六维（含满六维）按装备品质色取哪一档。必须覆盖 1..6。
	SixDimensionTiers []AffixWashTier `yaml:"six_dimension_tiers"`
}

type AffixWashConfig struct {
	Version int32         `yaml:"version"`
	Affix   AffixWashRule `yaml:"affix"`
}

// DefaultAffixWash 与 config/operations/AffixWash.yaml 的出厂值保持一致，
// 由 TestShippedAffixWashYAMLMatchesDefaults 强制。这不只是文档约定：
// affix_wash_config.go 在 MySQL 里读不到 AffixWash 节点时会静默退回这组值，
// 一旦两边跑偏，配置丢失时洗练的出奖率会跟着变（six_dimension_full_percent
// 决定出 510 的期望花费，0.03 → 333 万金币、0.1 → 100 万金币），
// 所以改动这里必须同步改 YAML，反之亦然。
//
// 六维档位整体上抬一档：白/绿取档2、蓝/紫取档3、橙/红取档4，让稀有的六维
// 一出场就有存在感，而不是像原版那样白装六维只有个位数。
func DefaultAffixWash() AffixWashConfig {
	return AffixWashConfig{
		Version: CurrentVersion,
		Affix: AffixWashRule{
			EmptyPercent:            20,
			SixDimensionPercent:     5,
			SixDimensionFullPercent: 0.03,
			SixDimensionTiers: []AffixWashTier{
				{Quality: 1, Tier: 2},
				{Quality: 2, Tier: 2},
				{Quality: 3, Tier: 3},
				{Quality: 4, Tier: 3},
				{Quality: 5, Tier: 4},
				{Quality: 6, Tier: 4},
			},
		},
	}
}

// DecodeAffixWash 把 MySQL 的配置树正规化成 AffixWashConfig：先取默认值，
// 再用传入的字段覆盖，最后用 KnownFields 拒绝拼错的键。与 Gameplay 的 Decode
// 走同一套流程，避免漏填字段时拿到零值。
func DecodeAffixWash(value interface{}) (AffixWashConfig, error) {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return AffixWashConfig{}, fmt.Errorf("encode normalized config: %w", err)
	}
	var supplied map[string]interface{}
	if err := yaml.Unmarshal(raw, &supplied); err != nil {
		return AffixWashConfig{}, fmt.Errorf("decode normalized config tree: %w", err)
	}
	defaultRaw, err := yaml.Marshal(DefaultAffixWash())
	if err != nil {
		return AffixWashConfig{}, fmt.Errorf("encode default affix wash config: %w", err)
	}
	var merged map[string]interface{}
	if err := yaml.Unmarshal(defaultRaw, &merged); err != nil {
		return AffixWashConfig{}, fmt.Errorf("decode default affix wash config tree: %w", err)
	}
	mergeConfigMapping(merged, supplied)
	raw, err = yaml.Marshal(merged)
	if err != nil {
		return AffixWashConfig{}, fmt.Errorf("encode merged affix wash config: %w", err)
	}
	var config AffixWashConfig
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return AffixWashConfig{}, fmt.Errorf("decode affix wash config: %w", err)
	}
	if err := ValidateAffixWash(config); err != nil {
		return AffixWashConfig{}, err
	}
	return config, nil
}

func ValidateAffixWash(config AffixWashConfig) error {
	if config.Version != CurrentVersion {
		return fmt.Errorf("version=%d, want %d", config.Version, CurrentVersion)
	}
	rule := config.Affix
	if err := validatePercent("affix.empty_percent", rule.EmptyPercent); err != nil {
		return err
	}
	if err := validatePercent("affix.six_dimension_percent", rule.SixDimensionPercent); err != nil {
		return err
	}
	if err := validatePercent("affix.six_dimension_full_percent", rule.SixDimensionFullPercent); err != nil {
		return err
	}
	// 三个判定按顺序互斥，总概率不能超过 100，否则后面的分支永远抽不到。
	total := rule.EmptyPercent + rule.SixDimensionPercent + rule.SixDimensionFullPercent
	if total > 100 {
		return fmt.Errorf("affix percentages must not sum above 100, got %g", total)
	}
	if len(rule.SixDimensionTiers) == 0 {
		return fmt.Errorf("affix.six_dimension_tiers must not be empty")
	}
	seen := make(map[int32]bool, len(rule.SixDimensionTiers))
	for _, tier := range rule.SixDimensionTiers {
		if tier.Quality < 1 || tier.Quality > 6 {
			return fmt.Errorf("affix.six_dimension_tiers.quality must be 1..6, got %d", tier.Quality)
		}
		if seen[tier.Quality] {
			return fmt.Errorf("affix.six_dimension_tiers.quality %d appears twice", tier.Quality)
		}
		seen[tier.Quality] = true
		if tier.Tier < 1 || tier.Tier > affixMaxTier {
			return fmt.Errorf("affix.six_dimension_tiers.tier must be 1..%d, got %d", affixMaxTier, tier.Tier)
		}
	}
	for quality := int32(1); quality <= 6; quality++ {
		if !seen[quality] {
			return fmt.Errorf("affix.six_dimension_tiers must cover quality %d", quality)
		}
	}
	return nil
}

// affixMaxTier 是 EquipAffixConfig.AffixArr 的档位数量（线上数据固定 5 档）。
const affixMaxTier = 5

func validatePercent(name string, value float64) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("%s must be 0..100, got %g", name, value)
	}
	return nil
}

// SixDimensionTierForQuality 返回某品质色在六维抽取里使用的档位。
// 配置校验保证 1..6 全覆盖，因此这里总能命中；未命中时退回原版规则（品质-1）。
func (config AffixWashConfig) SixDimensionTierForQuality(quality int32) int32 {
	for _, tier := range config.Affix.SixDimensionTiers {
		if tier.Quality == quality {
			return tier.Tier
		}
	}
	tier := quality - 1
	if tier < 1 {
		tier = 1
	}
	if tier > affixMaxTier {
		tier = affixMaxTier
	}
	return tier
}
