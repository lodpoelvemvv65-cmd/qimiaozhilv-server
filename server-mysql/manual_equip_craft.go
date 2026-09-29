package main

import (
	"math/rand"

	"mhqserver/internal/operationsconfig"
	"mhqserver/protocol"
)

const (
	manualQualityWhite  = 1
	manualQualityGreen  = 2
	manualQualityBlue   = 3
	manualQualityPurple = 4
	manualQualityOrange = 5
	manualQualityRed    = 6
)

func applyManualCraftOutcome(ss *session, rarity int32, it *bagItem) {
	applyManualCraftOutcomeWithIntn(ss, rarity, it, rand.Intn)
}

func applyManualCraftOutcomeWithIntn(ss *session, rarity int32, it *bagItem, intn func(int) int) {
	if it == nil {
		return
	}
	if intn == nil {
		intn = rand.Intn
	}
	recipe := gameplayManualEquipRecipe(rarity)
	count, pityAt := manualCraftPity(ss, rarity)
	quality, star, count, pityAt := rollManualCraft(recipe, count, pityAt, intn)
	setManualCraftPity(ss, rarity, count, pityAt)
	it.Quality = quality
	it.Star = star
	it.IsLock = recipe.Bound
	// 出厂不带洗练词缀：原版客户端只在玩家主动洗练（20347）时才产生这些行，
	// 与 newBagItem/ensureEquipmentVariations 的「空列表保持空」一致。
	// 随机词条数就是星级本身（几星几条），装备模板自带的固定属性行
	// （SpecialKey 那条，例如时之皇冠的「物理防御：8000」）不占名额，
	// 所以 6 星 = 1 条固定行 + 6 条随机词条。品质只决定从哪一档属性池里取
	// （ManulEquipAttribute 每 100 一档，101..626 共 6 档）。
	it.RandomAttrs = manualAttributeIDs(quality, int(star))
}

func manualCraftPity(ss *session, rarity int32) (count, pityAt int32) {
	if ss == nil || ss.signin == nil {
		return 0, 0
	}
	switch protocol.StoneType(rarity) {
	case protocol.StoneType_Rare, protocol.StoneType_UpgradeRare:
		return ss.signin.ManualRareCraftCount, ss.signin.ManualRarePityAt
	case protocol.StoneType_Epic:
		return ss.signin.ManualEpicCraftCount, ss.signin.ManualEpicPityAt
	default:
		return 0, 0
	}
}

func setManualCraftPity(ss *session, rarity int32, count, pityAt int32) {
	if ss == nil {
		return
	}
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	switch protocol.StoneType(rarity) {
	case protocol.StoneType_Rare, protocol.StoneType_UpgradeRare:
		ss.signin.ManualRareCraftCount = count
		ss.signin.ManualRarePityAt = pityAt
	case protocol.StoneType_Epic:
		ss.signin.ManualEpicCraftCount = count
		ss.signin.ManualEpicPityAt = pityAt
	}
}

func rollManualCraft(recipe operationsconfig.ManualEquipRecipe, count, pityAt int32, intn func(int) int) (quality, star, newCount, newPityAt int32) {
	if intn == nil {
		intn = rand.Intn
	}
	star = rollInclusive(recipe.MinStar, recipe.MaxStar, intn)
	quality = rollManualQuality(recipe, intn)
	if recipe.PityMaxCount > 0 && recipe.PityMinQuality > 0 {
		if pityAt <= 0 {
			pityAt = rollInclusive(recipe.PityMinCount, recipe.PityMaxCount, intn)
		}
		count++
		if count >= pityAt && quality < recipe.PityMinQuality {
			quality = recipe.PityMinQuality
			if recipe.RedPercent > 0 && quality < manualQualityRed && rollPercent(recipe.RedPercent, intn) {
				quality = manualQualityRed
			}
		}
		if quality >= recipe.PityMinQuality {
			count = 0
			pityAt = 0
		}
	}
	return quality, star, count, pityAt
}

func rollManualQuality(recipe operationsconfig.ManualEquipRecipe, intn func(int) int) int32 {
	if recipe.RedPercent > 0 && rollPercent(recipe.RedPercent, intn) {
		return manualQualityRed
	}
	quality := rollWeightedQuality(recipe.QualityWeights, intn)
	if recipe.MinQuality > 0 && quality < recipe.MinQuality {
		quality = recipe.MinQuality
	}
	if quality < manualQualityWhite {
		quality = manualQualityWhite
	}
	if quality > manualQualityRed {
		quality = manualQualityRed
	}
	return quality
}

func rollInclusive(min, max int32, intn func(int) int) int32 {
	if max <= min {
		return min
	}
	return min + int32(intn(int(max-min+1)))
}

func rollPercent(percent float64, intn func(int) int) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	threshold := int(percent*100 + 0.5)
	if threshold <= 0 {
		return false
	}
	return intn(10000) < threshold
}

func rollWeightedQuality(weights []operationsconfig.ManualQualityWeight, intn func(int) int) int32 {
	total := 0
	var last int32
	for _, item := range weights {
		if item.Weight > 0 && item.Quality >= manualQualityWhite && item.Quality <= manualQualityRed {
			total += int(item.Weight)
			last = item.Quality
		}
	}
	if total <= 0 {
		return manualQualityWhite
	}
	pick := intn(total)
	for _, item := range weights {
		if item.Weight <= 0 || item.Quality < manualQualityWhite || item.Quality > manualQualityRed {
			continue
		}
		if pick < int(item.Weight) {
			return item.Quality
		}
		pick -= int(item.Weight)
	}
	if last == 0 {
		return manualQualityWhite
	}
	return last
}
