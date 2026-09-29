package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func (events *SkillEventList) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*events = nil
		return nil
	}
	var pairs [][]json.RawMessage
	if err := strictUnmarshal(data, &pairs); err != nil {
		return fmt.Errorf("event dictionary: %w", err)
	}
	out := make(SkillEventList, 0, len(pairs))
	for _, pair := range pairs {
		if len(pair) != 2 {
			return fmt.Errorf("event dictionary pair has %d elements", len(pair))
		}
		var event SkillEvent
		if err := strictUnmarshal(pair[0], &event.Code); err != nil {
			return fmt.Errorf("event code: %w", err)
		}
		if err := strictUnmarshal(pair[1], &event.Options); err != nil {
			return fmt.Errorf("event %d: %w", event.Code, err)
		}
		out = append(out, event)
	}
	*events = out
	return nil
}

func (links *SkillOptionLinks) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*links = nil
		return nil
	}
	var pairs [][]json.RawMessage
	if err := strictUnmarshal(data, &pairs); err != nil {
		return err
	}
	out := make(SkillOptionLinks, 0, len(pairs))
	for _, pair := range pairs {
		if len(pair) != 2 {
			return fmt.Errorf("option link pair has %d elements", len(pair))
		}
		var link SkillOptionLink
		if err := strictUnmarshal(pair[0], &link.Name); err != nil {
			return err
		}
		if err := strictUnmarshal(pair[1], &link.IDs); err != nil {
			return err
		}
		out = append(out, link)
	}
	*links = out
	return nil
}

func (target *SkillTargetSelector) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type       string      `json:"_t"`
		TargetType *int32      `json:"targetType"`
		Random     *bool       `json:"isRndom"`
		MinCount   *SkillValue `json:"minCount"`
		MaxCount   *SkillValue `json:"maxCount"`
		TeamType   *int32      `json:"teamType"`
		Flags      *int32      `json:"flags"`
	}
	if err := strictUnmarshal(data, &wire); err != nil {
		return err
	}
	switch wire.Type {
	case "SelectSingleTarget":
		if wire.TargetType == nil {
			return fmt.Errorf("single target lacks targetType")
		}
		target.Kind, target.TargetType = SkillTargetSingle, *wire.TargetType
	case "SelectMultiTarget":
		if wire.Random == nil || wire.TeamType == nil || wire.Flags == nil {
			return fmt.Errorf("multi target metadata incomplete")
		}
		target.Kind, target.Random = SkillTargetMulti, *wire.Random
		target.MinCount, target.MaxCount = wire.MinCount, wire.MaxCount
		target.TeamType, target.Flags = *wire.TeamType, *wire.Flags
	default:
		return fmt.Errorf("unsupported target selector %q", wire.Type)
	}
	return nil
}

func (list *SkillOptionList) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*list = nil
		return nil
	}
	var raws []json.RawMessage
	if err := strictUnmarshal(data, &raws); err != nil {
		return err
	}
	out := make(SkillOptionList, 0, len(raws))
	for i, raw := range raws {
		var option SkillOption
		if err := strictUnmarshal(raw, &option); err != nil {
			return fmt.Errorf("option %d: %w", i, err)
		}
		out = append(out, option)
	}
	*list = out
	return nil
}

func (option *SkillOption) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type               SkillOptionKind      `json:"_t"`
		Name               string               `json:"name"`
		HideFlags          int32                `json:"hideFlags"`
		Position           SkillPosition        `json:"position"`
		UniqueID           int32                `json:"uniqeId"`
		Next               SkillOptionLinks     `json:"nextIds"`
		Target             *SkillTargetSelector `json:"selectTarget"`
		EffectID           int32                `json:"effectId"`
		EffectType         int32                `json:"effectType"`
		EffectPositionType int32                `json:"effectPosType"`
		EffectAttachType   int32                `json:"effectAttachType"`
		Time               json.RawMessage      `json:"time"`
		ModifierID         SkillIDRef           `json:"modifierId"`
		ModifierIDs        []SkillIDRef         `json:"modifierIds"`
		Options            SkillOptionList      `json:"value"`
		Param              *SkillValue          `json:"param"`
		Success            SkillOptionList      `json:"succeedOptionList"`
		Failure            SkillOptionList      `json:"failOptionList"`
		IsCritEvent        bool                 `json:"isCritEvent"`
		DamageType         int32                `json:"damageType"`
		DamageSelf         *SkillCalculation    `json:"damageCalculate_Self"`
		DamageTarget       *SkillCalculation    `json:"damageCalculate_Target"`
		IsDamageLimited    bool                 `json:"isDamageLimited"`
		DamageLimit        *SkillCalculation    `json:"damageCalculate_Limit"`
		TreatSelf          *SkillCalculation    `json:"treatCalculate_Self"`
		TreatTarget        *SkillCalculation    `json:"treatCalculate_Target"`
	}
	if err := strictUnmarshal(data, &wire); err != nil {
		return err
	}
	if _, ok := knownSkillOptionKinds[wire.Type]; !ok {
		return fmt.Errorf("unsupported skill option %q", wire.Type)
	}
	*option = SkillOption{
		Kind: wire.Type, Name: wire.Name, HideFlags: wire.HideFlags, Position: wire.Position,
		UniqueID: wire.UniqueID, Next: wire.Next, Target: wire.Target, EffectID: wire.EffectID,
		EffectType: wire.EffectType, EffectPositionType: wire.EffectPositionType,
		EffectAttachType: wire.EffectAttachType, ModifierID: wire.ModifierID,
		ModifierIDs: wire.ModifierIDs, Options: wire.Options, Param: wire.Param,
		Success: wire.Success, Failure: wire.Failure, IsCritEvent: wire.IsCritEvent,
		DamageType: wire.DamageType, DamageSelf: wire.DamageSelf,
		DamageTarget: wire.DamageTarget, IsDamageLimited: wire.IsDamageLimited,
		DamageLimit: wire.DamageLimit, TreatSelf: wire.TreatSelf, TreatTarget: wire.TreatTarget,
	}
	if len(wire.Time) == 0 || bytes.Equal(wire.Time, []byte("null")) {
		return nil
	}
	switch wire.Type {
	case SkillOptionProjectile:
		if err := strictUnmarshal(wire.Time, &option.ProjectileTimeMS); err != nil {
			return fmt.Errorf("projectile time: %w", err)
		}
	case SkillOptionDelay:
		var delay SkillValue
		if err := strictUnmarshal(wire.Time, &delay); err != nil {
			return fmt.Errorf("delay time: %w", err)
		}
		option.Delay = &delay
	default:
		return fmt.Errorf("option %q unexpectedly contains time", wire.Type)
	}
	return nil
}

func (pairs *SkillModifierPairs) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*pairs = nil
		return nil
	}
	var rawPairs [][]json.RawMessage
	if err := strictUnmarshal(data, &rawPairs); err != nil {
		return err
	}
	out := make(SkillModifierPairs, 0, len(rawPairs))
	for _, rawPair := range rawPairs {
		if len(rawPair) != 2 {
			return fmt.Errorf("modifier dictionary pair has %d elements", len(rawPair))
		}
		var id SkillIDRef
		var modifier SkillModifier
		if err := strictUnmarshal(rawPair[0], &id); err != nil {
			return err
		}
		if err := strictUnmarshal(rawPair[1], &modifier); err != nil {
			return fmt.Errorf("modifier %d: %w", id.Value, err)
		}
		if modifier.ID.Value != id.Value {
			return fmt.Errorf("modifier key %d does not match body id %d", id.Value, modifier.ID.Value)
		}
		out = append(out, SkillModifierPair{ID: id.Value, Modifier: &modifier})
	}
	*pairs = out
	return nil
}

func (pairs *skillLogicPairs) UnmarshalJSON(data []byte) error {
	var rawPairs [][]json.RawMessage
	if err := strictUnmarshal(data, &rawPairs); err != nil {
		return err
	}
	out := make(skillLogicPairs, 0, len(rawPairs))
	for _, rawPair := range rawPairs {
		if len(rawPair) != 2 {
			return fmt.Errorf("skill dictionary pair has %d elements", len(rawPair))
		}
		var pair skillLogicPair
		if err := strictUnmarshal(rawPair[0], &pair.ID); err != nil {
			return err
		}
		if err := strictUnmarshal(rawPair[1], &pair.Skill); err != nil {
			return fmt.Errorf("skill %d: %w", pair.ID, err)
		}
		out = append(out, pair)
	}
	*pairs = out
	return nil
}

func DecodeSkillLogicCatalog(reader io.Reader) (*SkillLogicCatalog, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var collection skillLogicCollection
	if err := decoder.Decode(&collection); err != nil {
		return nil, fmt.Errorf("decode SkillLogicConfig: %w", err)
	}
	return buildSkillLogicCatalog(collection)
}

func LoadSkillLogicCatalog(path string) (*SkillLogicCatalog, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return DecodeSkillLogicCatalog(file)
}

func strictUnmarshal(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
