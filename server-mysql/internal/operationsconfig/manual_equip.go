package operationsconfig

import "fmt"

const (
	manualQualityMin = 1
	manualQualityMax = 6
	manualStarMin    = 1
	manualStarMax    = 20
)

// ManualQualityWeight is one quality color and its relative chance.
type ManualQualityWeight struct {
	Quality int32 `yaml:"quality"`
	Weight  int32 `yaml:"weight"`
}

// ManualEquipRecipe is the online colored-stone crafting rule for one
// material grade. Star range, bind, red rate and pity come from MaterialBase
// stone descriptions; quality weights are operational because the client table
// does not include a probability row.
type ManualEquipRecipe struct {
	MinStar        int32                 `yaml:"min_star"`
	MaxStar        int32                 `yaml:"max_star"`
	MinQuality     int32                 `yaml:"min_quality"`
	Bound          bool                  `yaml:"bound"`
	RedPercent     float64               `yaml:"red_percent"`
	PityMinCount   int32                 `yaml:"pity_min_count"`
	PityMaxCount   int32                 `yaml:"pity_max_count"`
	PityMinQuality int32                 `yaml:"pity_min_quality"`
	QualityWeights []ManualQualityWeight `yaml:"quality_weights"`
}

// ManualEquipConfig holds the four online handmade stone grades.
type ManualEquipConfig struct {
	Normal      ManualEquipRecipe `yaml:"normal"`
	Rare        ManualEquipRecipe `yaml:"rare"`
	Epic        ManualEquipRecipe `yaml:"epic"`
	UpgradeRare ManualEquipRecipe `yaml:"upgrade_rare"`
}

func defaultManualEquip() ManualEquipConfig {
	rareWeights := []ManualQualityWeight{
		{Quality: 3, Weight: 45},
		{Quality: 4, Weight: 35},
		{Quality: 5, Weight: 15},
	}
	return ManualEquipConfig{
		Normal: ManualEquipRecipe{
			MinStar: 3,
			MaxStar: 10,
			Bound:   true,
			QualityWeights: []ManualQualityWeight{
				{Quality: 1, Weight: 40},
				{Quality: 2, Weight: 30},
				{Quality: 3, Weight: 20},
				{Quality: 4, Weight: 10},
			},
		},
		Rare: ManualEquipRecipe{
			MinStar:        6,
			MaxStar:        10,
			RedPercent:     5,
			PityMinCount:   80,
			PityMaxCount:   100,
			PityMinQuality: 5,
			QualityWeights: append([]ManualQualityWeight(nil), rareWeights...),
		},
		Epic: ManualEquipRecipe{
			MinStar:        6,
			MaxStar:        10,
			MinQuality:     3,
			PityMinCount:   80,
			PityMaxCount:   100,
			PityMinQuality: 6,
			QualityWeights: []ManualQualityWeight{
				{Quality: 3, Weight: 40},
				{Quality: 4, Weight: 30},
				{Quality: 5, Weight: 25},
				{Quality: 6, Weight: 5},
			},
		},
		UpgradeRare: ManualEquipRecipe{
			MinStar:        6,
			MaxStar:        10,
			Bound:          true,
			RedPercent:     5,
			PityMinCount:   80,
			PityMaxCount:   100,
			PityMinQuality: 5,
			QualityWeights: append([]ManualQualityWeight(nil), rareWeights...),
		},
	}
}

func validateManualEquip(config ManualEquipConfig) error {
	for _, item := range []struct {
		name   string
		recipe ManualEquipRecipe
	}{
		{"normal", config.Normal},
		{"rare", config.Rare},
		{"epic", config.Epic},
		{"upgrade_rare", config.UpgradeRare},
	} {
		if err := validateManualEquipRecipe("manual_equip."+item.name, item.recipe); err != nil {
			return err
		}
	}
	return nil
}

func validateManualEquipRecipe(name string, recipe ManualEquipRecipe) error {
	if recipe.MinStar < manualStarMin || recipe.MaxStar > manualStarMax || recipe.MaxStar < recipe.MinStar {
		return fmt.Errorf("%s star range must be ordered within %d..%d", name, manualStarMin, manualStarMax)
	}
	if recipe.MinQuality < 0 || recipe.MinQuality > manualQualityMax {
		return fmt.Errorf("%s.min_quality must be 0..%d", name, manualQualityMax)
	}
	if recipe.RedPercent < 0 || recipe.RedPercent > 100 {
		return fmt.Errorf("%s.red_percent must be 0..100", name)
	}
	if recipe.PityMaxCount == 0 && recipe.PityMinCount == 0 && recipe.PityMinQuality == 0 {
		return validateManualQualityWeights(name, recipe)
	}
	if recipe.PityMinCount < 1 || recipe.PityMaxCount > 10_000 || recipe.PityMaxCount < recipe.PityMinCount {
		return fmt.Errorf("%s pity count range must be ordered within 1..10000", name)
	}
	if recipe.PityMinQuality < manualQualityMin || recipe.PityMinQuality > manualQualityMax {
		return fmt.Errorf("%s.pity_min_quality must be %d..%d", name, manualQualityMin, manualQualityMax)
	}
	return validateManualQualityWeights(name, recipe)
}

func validateManualQualityWeights(name string, recipe ManualEquipRecipe) error {
	if len(recipe.QualityWeights) == 0 {
		return fmt.Errorf("%s.quality_weights must not be empty", name)
	}
	seen := make(map[int32]struct{}, len(recipe.QualityWeights))
	for index, item := range recipe.QualityWeights {
		if item.Quality < manualQualityMin || item.Quality > manualQualityMax {
			return fmt.Errorf("%s.quality_weights[%d].quality must be %d..%d", name, index, manualQualityMin, manualQualityMax)
		}
		if item.Weight < 1 || item.Weight > 1_000_000 {
			return fmt.Errorf("%s.quality_weights[%d].weight must be 1..1000000", name, index)
		}
		if _, duplicate := seen[item.Quality]; duplicate {
			return fmt.Errorf("%s.quality_weights contains duplicate quality %d", name, item.Quality)
		}
		seen[item.Quality] = struct{}{}
	}
	return nil
}
