package main

import (
	"database/sql"
	"fmt"
	"math"
)

// LoadSkillLogicCatalogFromDB decodes the relational MySQL configuration tree
// directly into the combat model. No file or serialization adapter is used.
func LoadSkillLogicCatalogFromDB(db *sql.DB) (*SkillLogicCatalog, error) {
	root, err := buildConfigRootFromDB(db, "SkillLogicConfig")
	if err != nil {
		return nil, err
	}
	collection, err := decodeSkillLogicCollection(root, "SkillLogicConfig")
	if err != nil {
		return nil, err
	}
	return buildSkillLogicCatalog(collection)
}

func decodeSkillLogicCollection(value interface{}, path string) (skillLogicCollection, error) {
	object, err := configObject(value, path)
	if err != nil {
		return skillLogicCollection{}, err
	}
	typeName, err := configString(object["_t"], path+"._t")
	if err != nil {
		return skillLogicCollection{}, err
	}
	rawPairs, err := configArray(object["skillDic"], path+".skillDic")
	if err != nil {
		return skillLogicCollection{}, err
	}
	pairs := make(skillLogicPairs, 0, len(rawPairs))
	for index, rawPair := range rawPairs {
		pairPath := fmt.Sprintf("%s.skillDic[%d]", path, index)
		values, err := configPair(rawPair, pairPath)
		if err != nil {
			return skillLogicCollection{}, err
		}
		id, err := configInt32(values[0], pairPath+"[0]")
		if err != nil {
			return skillLogicCollection{}, err
		}
		skill, err := decodeSkillLogic(values[1], pairPath+"[1]")
		if err != nil {
			return skillLogicCollection{}, err
		}
		pairs = append(pairs, skillLogicPair{ID: id, Skill: skill})
	}
	return skillLogicCollection{Type: typeName, Skills: pairs}, nil
}

func decodeSkillLogic(value interface{}, path string) (*SkillLogic, error) {
	object, err := configObject(value, path)
	if err != nil {
		return nil, err
	}
	skill := &SkillLogic{}
	if skill.SkillID, err = configInt32Default(object["skillId"], path+".skillId"); err != nil {
		return nil, err
	}
	if skill.SkillName, err = configStringDefault(object["skillName"], path+".skillName"); err != nil {
		return nil, err
	}
	if skill.MaxLevel, err = configInt32Default(object["maxLevel"], path+".maxLevel"); err != nil {
		return nil, err
	}
	if object["cast"] != nil {
		if skill.Cast, err = decodeSkillCast(object["cast"], path+".cast"); err != nil {
			return nil, err
		}
	}
	for key, target := range map[string]*int32{
		"skillType": &skill.SkillType, "CD": &skill.CooldownMS, "duration": &skill.DurationMS,
		"interval": &skill.IntervalMS, "teamType": &skill.TeamType, "flagType": &skill.FlagType,
	} {
		if *target, err = configInt32Default(object[key], path+"."+key); err != nil {
			return nil, err
		}
	}
	if skill.Description, err = configStringDefault(object["desc"], path+".desc"); err != nil {
		return nil, err
	}
	if skill.SkillEvents, err = decodeSkillEvents(object["skillEventDic"], path+".skillEventDic"); err != nil {
		return nil, err
	}
	if skill.ModifierPairs, err = decodeSkillModifiers(object["modifierDic"], path+".modifierDic"); err != nil {
		return nil, err
	}
	return skill, nil
}

func decodeSkillCast(value interface{}, path string) (SkillCast, error) {
	object, err := configObject(value, path)
	if err != nil {
		return SkillCast{}, err
	}
	var cast SkillCast
	if cast.SkillCastType, err = configInt32Default(object["skillCastType"], path+".skillCastType"); err != nil {
		return SkillCast{}, err
	}
	if cast.CastBaseType, err = configInt32Default(object["castBaseType"], path+".castBaseType"); err != nil {
		return SkillCast{}, err
	}
	if cast.SkillCast, err = configFloatDefault(object["skillCast"], path+".skillCast"); err != nil {
		return SkillCast{}, err
	}
	if cast.AttributeType, err = configInt32Default(object["attributeType"], path+".attributeType"); err != nil {
		return SkillCast{}, err
	}
	return cast, nil
}

func decodeSkillEvents(value interface{}, path string) (SkillEventList, error) {
	if value == nil {
		return nil, nil
	}
	pairs, err := configArray(value, path)
	if err != nil {
		return nil, err
	}
	events := make(SkillEventList, 0, len(pairs))
	for index, rawPair := range pairs {
		pairPath := fmt.Sprintf("%s[%d]", path, index)
		values, err := configPair(rawPair, pairPath)
		if err != nil {
			return nil, err
		}
		code, err := configInt32(values[0], pairPath+"[0]")
		if err != nil {
			return nil, err
		}
		options, err := decodeSkillOptions(values[1], pairPath+"[1]")
		if err != nil {
			return nil, err
		}
		events = append(events, SkillEvent{Code: code, Options: options})
	}
	return events, nil
}

func decodeSkillOptions(value interface{}, path string) (SkillOptionList, error) {
	if value == nil {
		return nil, nil
	}
	values, err := configArray(value, path)
	if err != nil {
		return nil, err
	}
	options := make(SkillOptionList, 0, len(values))
	for index, raw := range values {
		option, err := decodeSkillOption(raw, fmt.Sprintf("%s[%d]", path, index))
		if err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, nil
}

func decodeSkillOption(value interface{}, path string) (SkillOption, error) {
	object, err := configObject(value, path)
	if err != nil {
		return SkillOption{}, err
	}
	kindName, err := configString(object["_t"], path+"._t")
	if err != nil {
		return SkillOption{}, err
	}
	kind := SkillOptionKind(kindName)
	if _, ok := knownSkillOptionKinds[kind]; !ok {
		return SkillOption{}, fmt.Errorf("%s: unsupported skill option %q", path, kind)
	}
	option := SkillOption{Kind: kind}
	if option.Name, err = configStringDefault(object["name"], path+".name"); err != nil {
		return SkillOption{}, err
	}
	for key, target := range map[string]*int32{
		"hideFlags": &option.HideFlags, "uniqeId": &option.UniqueID, "effectId": &option.EffectID,
		"effectType": &option.EffectType, "effectPosType": &option.EffectPositionType,
		"effectAttachType": &option.EffectAttachType, "damageType": &option.DamageType,
	} {
		if *target, err = configInt32Default(object[key], path+"."+key); err != nil {
			return SkillOption{}, err
		}
	}
	if object["position"] != nil {
		if option.Position, err = decodeSkillPosition(object["position"], path+".position"); err != nil {
			return SkillOption{}, err
		}
	}
	if option.Next, err = decodeSkillLinks(object["nextIds"], path+".nextIds"); err != nil {
		return SkillOption{}, err
	}
	if object["selectTarget"] != nil {
		if option.Target, err = decodeSkillTarget(object["selectTarget"], path+".selectTarget"); err != nil {
			return SkillOption{}, err
		}
	}
	if object["modifierId"] != nil {
		if option.ModifierID, err = decodeSkillIDRef(object["modifierId"], path+".modifierId"); err != nil {
			return SkillOption{}, err
		}
	}
	if option.ModifierIDs, err = decodeSkillIDRefs(object["modifierIds"], path+".modifierIds"); err != nil {
		return SkillOption{}, err
	}
	if option.Options, err = decodeSkillOptions(object["value"], path+".value"); err != nil {
		return SkillOption{}, err
	}
	if option.Param, err = decodeSkillValue(object["param"], path+".param"); err != nil {
		return SkillOption{}, err
	}
	if option.Success, err = decodeSkillOptions(object["succeedOptionList"], path+".succeedOptionList"); err != nil {
		return SkillOption{}, err
	}
	if option.Failure, err = decodeSkillOptions(object["failOptionList"], path+".failOptionList"); err != nil {
		return SkillOption{}, err
	}
	if option.IsCritEvent, err = configBoolDefault(object["isCritEvent"], path+".isCritEvent"); err != nil {
		return SkillOption{}, err
	}
	if option.IsDamageLimited, err = configBoolDefault(object["isDamageLimited"], path+".isDamageLimited"); err != nil {
		return SkillOption{}, err
	}
	for key, target := range map[string]**SkillCalculation{
		"damageCalculate_Self": &option.DamageSelf, "damageCalculate_Target": &option.DamageTarget,
		"damageCalculate_Limit": &option.DamageLimit, "treatCalculate_Self": &option.TreatSelf,
		"treatCalculate_Target": &option.TreatTarget,
	} {
		if *target, err = decodeSkillCalculation(object[key], path+"."+key); err != nil {
			return SkillOption{}, err
		}
	}
	if rawTime, exists := object["time"]; exists && rawTime != nil {
		switch kind {
		case SkillOptionProjectile:
			if option.ProjectileTimeMS, err = configInt32(rawTime, path+".time"); err != nil {
				return SkillOption{}, err
			}
		case SkillOptionDelay:
			if option.Delay, err = decodeSkillValue(rawTime, path+".time"); err != nil {
				return SkillOption{}, err
			}
		default:
			return SkillOption{}, fmt.Errorf("%s: option %q unexpectedly contains time", path, kind)
		}
	}
	return option, nil
}

func decodeSkillTarget(value interface{}, path string) (*SkillTargetSelector, error) {
	object, err := configObject(value, path)
	if err != nil {
		return nil, err
	}
	typeName, err := configString(object["_t"], path+"._t")
	if err != nil {
		return nil, err
	}
	target := &SkillTargetSelector{}
	switch typeName {
	case "SelectSingleTarget":
		target.Kind = SkillTargetSingle
		if object["targetType"] == nil {
			return nil, fmt.Errorf("%s: single target lacks targetType", path)
		}
		target.TargetType, err = configInt32(object["targetType"], path+".targetType")
	case "SelectMultiTarget":
		target.Kind = SkillTargetMulti
		for _, key := range []string{"isRndom", "teamType", "flags"} {
			if object[key] == nil {
				return nil, fmt.Errorf("%s: multi target lacks %s", path, key)
			}
		}
		if target.Random, err = configBool(object["isRndom"], path+".isRndom"); err != nil {
			return nil, err
		}
		if target.TeamType, err = configInt32(object["teamType"], path+".teamType"); err != nil {
			return nil, err
		}
		if target.Flags, err = configInt32(object["flags"], path+".flags"); err != nil {
			return nil, err
		}
		if target.MinCount, err = decodeSkillValue(object["minCount"], path+".minCount"); err != nil {
			return nil, err
		}
		if target.MaxCount, err = decodeSkillValue(object["maxCount"], path+".maxCount"); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%s: unsupported target selector %q", path, typeName)
	}
	if err != nil {
		return nil, err
	}
	return target, nil
}

func decodeSkillLinks(value interface{}, path string) (SkillOptionLinks, error) {
	if value == nil {
		return nil, nil
	}
	pairs, err := configArray(value, path)
	if err != nil {
		return nil, err
	}
	links := make(SkillOptionLinks, 0, len(pairs))
	for index, rawPair := range pairs {
		pairPath := fmt.Sprintf("%s[%d]", path, index)
		values, err := configPair(rawPair, pairPath)
		if err != nil {
			return nil, err
		}
		name, err := configString(values[0], pairPath+"[0]")
		if err != nil {
			return nil, err
		}
		ids, err := decodeInt32List(values[1], pairPath+"[1]")
		if err != nil {
			return nil, err
		}
		links = append(links, SkillOptionLink{Name: name, IDs: ids})
	}
	return links, nil
}

func decodeSkillPosition(value interface{}, path string) (SkillPosition, error) {
	object, err := configObject(value, path)
	if err != nil {
		return SkillPosition{}, err
	}
	x, err := configFloatDefault(object["x"], path+".x")
	if err != nil {
		return SkillPosition{}, err
	}
	y, err := configFloatDefault(object["y"], path+".y")
	return SkillPosition{X: x, Y: y}, err
}

func decodeSkillValue(value interface{}, path string) (*SkillValue, error) {
	if value == nil {
		return nil, nil
	}
	object, err := configObject(value, path)
	if err != nil {
		return nil, err
	}
	out := &SkillValue{}
	if out.SkillSourceType, err = configInt32Default(object["skillSourcetype"], path+".skillSourcetype"); err != nil {
		return nil, err
	}
	if out.Value, err = configFloatDefault(object["value"], path+".value"); err != nil {
		return nil, err
	}
	if object["values"] != nil {
		pairs, err := configArray(object["values"], path+".values")
		if err != nil {
			return nil, err
		}
		out.Values = make([][2]float64, 0, len(pairs))
		for index, rawPair := range pairs {
			pairPath := fmt.Sprintf("%s.values[%d]", path, index)
			values, err := configPair(rawPair, pairPath)
			if err != nil {
				return nil, err
			}
			first, err := configFloat(values[0], pairPath+"[0]")
			if err != nil {
				return nil, err
			}
			second, err := configFloat(values[1], pairPath+"[1]")
			if err != nil {
				return nil, err
			}
			out.Values = append(out.Values, [2]float64{first, second})
		}
	}
	return out, nil
}

func decodeSkillCalculation(value interface{}, path string) (*SkillCalculation, error) {
	if value == nil {
		return nil, nil
	}
	object, err := configObject(value, path)
	if err != nil {
		return nil, err
	}
	calculation := &SkillCalculation{}
	if calculation.CalculateType, err = configInt32Default(object["calculateType"], path+".calculateType"); err != nil {
		return nil, err
	}
	param, err := decodeSkillValue(object["param"], path+".param")
	if err != nil {
		return nil, err
	}
	if param != nil {
		calculation.Param = *param
	}
	return calculation, nil
}

func decodeSkillIDRef(value interface{}, path string) (SkillIDRef, error) {
	object, err := configObject(value, path)
	if err != nil {
		return SkillIDRef{}, err
	}
	id, err := configInt64Default(object["Value"], path+".Value")
	return SkillIDRef{Value: id}, err
}

func decodeSkillIDRefs(value interface{}, path string) ([]SkillIDRef, error) {
	if value == nil {
		return nil, nil
	}
	values, err := configArray(value, path)
	if err != nil {
		return nil, err
	}
	out := make([]SkillIDRef, 0, len(values))
	for index, raw := range values {
		ref, err := decodeSkillIDRef(raw, fmt.Sprintf("%s[%d]", path, index))
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

func decodeSkillModifiers(value interface{}, path string) (SkillModifierPairs, error) {
	if value == nil {
		return nil, nil
	}
	pairs, err := configArray(value, path)
	if err != nil {
		return nil, err
	}
	out := make(SkillModifierPairs, 0, len(pairs))
	for index, rawPair := range pairs {
		pairPath := fmt.Sprintf("%s[%d]", path, index)
		values, err := configPair(rawPair, pairPath)
		if err != nil {
			return nil, err
		}
		id, err := decodeSkillIDRef(values[0], pairPath+"[0]")
		if err != nil {
			return nil, err
		}
		modifier, err := decodeSkillModifier(values[1], pairPath+"[1]")
		if err != nil {
			return nil, err
		}
		if modifier.ID.Value != id.Value {
			return nil, fmt.Errorf("%s: modifier key %d does not match body id %d", pairPath, id.Value, modifier.ID.Value)
		}
		out = append(out, SkillModifierPair{ID: id.Value, Modifier: modifier})
	}
	return out, nil
}

func decodeSkillModifier(value interface{}, path string) (*SkillModifier, error) {
	object, err := configObject(value, path)
	if err != nil {
		return nil, err
	}
	modifier := &SkillModifier{}
	if modifier.ID, err = decodeSkillIDRef(object["_id"], path+"._id"); err != nil {
		return nil, err
	}
	if modifier.Name, err = configStringDefault(object["name"], path+".name"); err != nil {
		return nil, err
	}
	if modifier.LevelList, err = decodeInt32List(object["levelList"], path+".levelList"); err != nil {
		return nil, err
	}
	for key, target := range map[string]*int32{
		"tag": &modifier.Tag, "immuneTag": &modifier.ImmuneTag, "attribute": &modifier.Attribute,
		"overlayType": &modifier.OverlayType, "buffType": &modifier.BuffType, "thinkerType": &modifier.ThinkerType,
		"effectId": &modifier.EffectID, "effectAttachType": &modifier.EffectAttachType,
		"valueK": &modifier.ValueKey, "stateK": &modifier.StateKey, "stateV": &modifier.StateValue,
	} {
		if *target, err = configInt32Default(object[key], path+"."+key); err != nil {
			return nil, err
		}
	}
	if modifier.ImmuneTagArray, err = decodeInt32List(object["immuneTagArr"], path+".immuneTagArr"); err != nil {
		return nil, err
	}
	for key, target := range map[string]*SkillValue{
		"perOverlay": &modifier.PerOverlay, "continueTime": &modifier.ContinueTime,
		"thinkInterval": &modifier.ThinkInterval, "valueV": &modifier.Value,
	} {
		decoded, err := decodeSkillValue(object[key], path+"."+key)
		if err != nil {
			return nil, err
		}
		if decoded != nil {
			*target = *decoded
		}
	}
	if modifier.CanBeCleared, err = configBoolDefault(object["canBeClear"], path+".canBeClear"); err != nil {
		return nil, err
	}
	if modifier.IconID, err = configStringDefault(object["iconId"], path+".iconId"); err != nil {
		return nil, err
	}
	if modifier.IconDescription, err = configStringDefault(object["iconDesc"], path+".iconDesc"); err != nil {
		return nil, err
	}
	if modifier.Events, err = decodeSkillEvents(object["modifierEventDic"], path+".modifierEventDic"); err != nil {
		return nil, err
	}
	return modifier, nil
}

func decodeInt32List(value interface{}, path string) ([]int32, error) {
	if value == nil {
		return nil, nil
	}
	values, err := configArray(value, path)
	if err != nil {
		return nil, err
	}
	out := make([]int32, 0, len(values))
	for index, raw := range values {
		item, err := configInt32(raw, fmt.Sprintf("%s[%d]", path, index))
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func configObject(value interface{}, path string) (map[string]interface{}, error) {
	object, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s: expected object, got %T", path, value)
	}
	return object, nil
}

func configArray(value interface{}, path string) ([]interface{}, error) {
	values, ok := value.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%s: expected array, got %T", path, value)
	}
	return values, nil
}

func configPair(value interface{}, path string) ([]interface{}, error) {
	values, err := configArray(value, path)
	if err != nil {
		return nil, err
	}
	if len(values) != 2 {
		return nil, fmt.Errorf("%s: expected pair, got %d values", path, len(values))
	}
	return values, nil
}

func configString(value interface{}, path string) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s: expected string, got %T", path, value)
	}
	return text, nil
}

func configStringDefault(value interface{}, path string) (string, error) {
	if value == nil {
		return "", nil
	}
	return configString(value, path)
}

func configBool(value interface{}, path string) (bool, error) {
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s: expected bool, got %T", path, value)
	}
	return result, nil
}

func configBoolDefault(value interface{}, path string) (bool, error) {
	if value == nil {
		return false, nil
	}
	return configBool(value, path)
}

func configInt32(value interface{}, path string) (int32, error) {
	result, err := configInt64(value, path)
	if err != nil {
		return 0, err
	}
	if result < math.MinInt32 || result > math.MaxInt32 {
		return 0, fmt.Errorf("%s: integer %d exceeds int32", path, result)
	}
	return int32(result), nil
}

func configInt32Default(value interface{}, path string) (int32, error) {
	if value == nil {
		return 0, nil
	}
	return configInt32(value, path)
}

func configInt64Default(value interface{}, path string) (int64, error) {
	if value == nil {
		return 0, nil
	}
	return configInt64(value, path)
}

func configInt64(value interface{}, path string) (int64, error) {
	switch number := value.(type) {
	case int:
		return int64(number), nil
	case int32:
		return int64(number), nil
	case int64:
		return number, nil
	case uint:
		if uint64(number) <= math.MaxInt64 {
			return int64(number), nil
		}
	case uint32:
		return int64(number), nil
	case uint64:
		if number <= math.MaxInt64 {
			return int64(number), nil
		}
	case float64:
		if math.Trunc(number) == number && number >= math.MinInt64 && number <= math.MaxInt64 {
			return int64(number), nil
		}
	case float32:
		converted := float64(number)
		if math.Trunc(converted) == converted {
			return int64(number), nil
		}
	}
	return 0, fmt.Errorf("%s: expected integer, got %T(%v)", path, value, value)
}

func configFloat(value interface{}, path string) (float64, error) {
	switch number := value.(type) {
	case float64:
		return number, nil
	case float32:
		return float64(number), nil
	case int:
		return float64(number), nil
	case int32:
		return float64(number), nil
	case int64:
		return float64(number), nil
	case uint:
		return float64(number), nil
	case uint32:
		return float64(number), nil
	case uint64:
		return float64(number), nil
	default:
		return 0, fmt.Errorf("%s: expected number, got %T", path, value)
	}
}

func configFloatDefault(value interface{}, path string) (float64, error) {
	if value == nil {
		return 0, nil
	}
	return configFloat(value, path)
}
