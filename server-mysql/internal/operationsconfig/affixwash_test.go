package operationsconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAffixWashDecodeKeepsDefaultsForMissingFields(t *testing.T) {
	config, err := DecodeAffixWash(map[string]interface{}{
		"version": 1,
		"affix":   map[string]interface{}{"empty_percent": 35},
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Affix.EmptyPercent != 35 {
		t.Fatalf("empty_percent = %v, want the supplied 35", config.Affix.EmptyPercent)
	}
	defaults := DefaultAffixWash()
	if config.Affix.SixDimensionPercent != defaults.Affix.SixDimensionPercent ||
		config.Affix.SixDimensionFullPercent != defaults.Affix.SixDimensionFullPercent {
		t.Fatalf("missing percentages did not inherit defaults: %+v", config.Affix)
	}
	if len(config.Affix.SixDimensionTiers) != len(defaults.Affix.SixDimensionTiers) {
		t.Fatalf("missing six_dimension_tiers did not inherit defaults: %+v", config.Affix.SixDimensionTiers)
	}
}

func TestAffixWashEncodeDecodeRoundTripsThroughMySQLTree(t *testing.T) {
	// 配置在 MySQL 里按标量节点存储，读取后是 int64/float64 混排的 map，
	// 这里用一次 YAML 往返模拟同样的形状，确认 0.03 这种小数不会退化成 0。
	raw, err := yaml.Marshal(DefaultAffixWash())
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	config, err := DecodeAffixWash(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.Affix.SixDimensionFullPercent != 0.03 {
		t.Fatalf("six_dimension_full_percent = %v, want 0.03", config.Affix.SixDimensionFullPercent)
	}
	if config.SixDimensionTierForQuality(6) != 4 {
		t.Fatalf("quality 6 tier = %d, want 4", config.SixDimensionTierForQuality(6))
	}
	if config.SixDimensionTierForQuality(1) != 2 {
		t.Fatalf("quality 1 tier = %d, want 2", config.SixDimensionTierForQuality(1))
	}
}

func TestShippedAffixWashYAMLMatchesDefaults(t *testing.T) {
	path := filepath.Join("..", "..", "config", "operations", "AffixWash.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	config, err := DecodeAffixWash(root)
	if err != nil {
		t.Fatalf("shipped AffixWash.yaml is invalid: %v", err)
	}
	if !reflect.DeepEqual(config, DefaultAffixWash()) {
		t.Fatalf("shipped AffixWash.yaml = %+v, want the code defaults %+v", config, DefaultAffixWash())
	}
}

func TestValidateAffixWashRejectsOutOfRangeValues(t *testing.T) {
	mutate := func(apply func(*AffixWashConfig)) AffixWashConfig {
		config := DefaultAffixWash()
		apply(&config)
		return config
	}
	tests := []struct {
		name   string
		config AffixWashConfig
	}{
		{name: "empty above 100", config: mutate(func(c *AffixWashConfig) { c.Affix.EmptyPercent = 101 })},
		{name: "empty below 0", config: mutate(func(c *AffixWashConfig) { c.Affix.EmptyPercent = -1 })},
		{name: "six dimension above 100", config: mutate(func(c *AffixWashConfig) { c.Affix.SixDimensionPercent = 100.5 })},
		{name: "full above 100", config: mutate(func(c *AffixWashConfig) { c.Affix.SixDimensionFullPercent = 120 })},
		{name: "percentages sum above 100", config: mutate(func(c *AffixWashConfig) {
			c.Affix.EmptyPercent, c.Affix.SixDimensionPercent, c.Affix.SixDimensionFullPercent = 60, 30, 20
		})},
		{name: "wrong version", config: mutate(func(c *AffixWashConfig) { c.Version = 99 })},
		{name: "no tiers", config: mutate(func(c *AffixWashConfig) { c.Affix.SixDimensionTiers = nil })},
		{name: "quality out of range", config: mutate(func(c *AffixWashConfig) { c.Affix.SixDimensionTiers[0].Quality = 7 })},
		{name: "duplicate quality", config: mutate(func(c *AffixWashConfig) { c.Affix.SixDimensionTiers[1].Quality = 1 })},
		{name: "missing quality", config: mutate(func(c *AffixWashConfig) { c.Affix.SixDimensionTiers = c.Affix.SixDimensionTiers[1:] })},
		{name: "tier above 5", config: mutate(func(c *AffixWashConfig) { c.Affix.SixDimensionTiers[0].Tier = 6 })},
		{name: "tier below 1", config: mutate(func(c *AffixWashConfig) { c.Affix.SixDimensionTiers[0].Tier = 0 })},
	}
	for _, tt := range tests {
		if err := ValidateAffixWash(tt.config); err == nil {
			t.Errorf("%s: invalid config was accepted", tt.name)
		}
	}
}

func TestValidateAffixWashAcceptsShippedDefaults(t *testing.T) {
	if err := ValidateAffixWash(DefaultAffixWash()); err != nil {
		t.Fatalf("default AffixWash config rejected: %v", err)
	}
}

func TestDecodeAffixWashRejectsUnknownField(t *testing.T) {
	_, err := DecodeAffixWash(map[string]interface{}{
		"version": 1,
		"affix":   map[string]interface{}{"empty_percnet": 20},
	})
	if err == nil {
		t.Fatal("misspelled affix field was accepted")
	}
}
