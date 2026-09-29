package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const skillLogicCollectionType = "SkillLogicConfigCollection"

// skillLogicFixturePath 依次尝试仓库自带的 testdata 夹具与本地客户端解包目录，
// 保证不依赖游戏客户端也能运行 `go test` 与本地启动。
func skillLogicFixturePath() string {
	for _, path := range []string{
		filepath.Join("testdata", "SkillLogicConfig"),
		filepath.Join("..", "client-test", "梦幻奇遇记_Data", "StreamingAssets", "yoo", "_extracted", "SkillLogicConfig"),
		filepath.Join("..", "client-127.0.0.1", "梦幻奇遇记_Data", "StreamingAssets", "yoo", "_extracted", "SkillLogicConfig"),
	} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join("testdata", "SkillLogicConfig")
}

type SkillOptionKind string

const (
	SkillOptionPlayEffect      SkillOptionKind = "SkillOption_播放特效"
	SkillOptionProjectile      SkillOptionKind = "SkillOption_创建携带Modifier的子弹"
	SkillOptionCounter         SkillOptionKind = "SkillOption_反击"
	SkillOptionReflect         SkillOptionKind = "SkillOption_反伤"
	SkillOptionChangeGlobalCD  SkillOptionKind = "SkillOption_改变公共CD"
	SkillOptionChangeDamage    SkillOptionKind = "SkillOption_改变伤害倍率"
	SkillOptionChangeCastCount SkillOptionKind = "SkillOption_改变释放次数"
	SkillOptionShield          SkillOptionKind = "SkillOption_护盾"
	SkillOptionChance          SkillOptionKind = "SkillOption_几率"
	SkillOptionDamage          SkillOptionKind = "SkillOption_伤害"
	SkillOptionLifeSteal       SkillOptionKind = "SkillOption_吸血"
	SkillOptionModifyModifier  SkillOptionKind = "SkillOption_修改Modifier数据"
	SkillOptionDelay           SkillOptionKind = "SkillOption_延迟操作"
	SkillOptionApplyModifier   SkillOptionKind = "SkillOption_应用Modifier"
	SkillOptionHeal            SkillOptionKind = "SkillOption_治愈"
)

var knownSkillOptionKinds = map[SkillOptionKind]struct{}{
	SkillOptionPlayEffect: {}, SkillOptionProjectile: {}, SkillOptionCounter: {},
	SkillOptionReflect: {}, SkillOptionChangeGlobalCD: {}, SkillOptionChangeDamage: {},
	SkillOptionChangeCastCount: {}, SkillOptionShield: {}, SkillOptionChance: {},
	SkillOptionDamage: {}, SkillOptionLifeSteal: {}, SkillOptionModifyModifier: {},
	SkillOptionDelay: {}, SkillOptionApplyModifier: {}, SkillOptionHeal: {},
}

type SkillLogicCatalog struct {
	Skills    map[int32]*SkillLogic
	Modifiers map[int64]*SkillModifier
}

type SkillLogic struct {
	SkillID       int32                    `json:"skillId"`
	SkillName     string                   `json:"skillName"`
	MaxLevel      int32                    `json:"maxLevel"`
	Cast          SkillCast                `json:"cast"`
	SkillType     int32                    `json:"skillType"`
	CooldownMS    int32                    `json:"CD"`
	DurationMS    int32                    `json:"duration"`
	IntervalMS    int32                    `json:"interval"`
	TeamType      int32                    `json:"teamType"`
	FlagType      int32                    `json:"flagType"`
	Description   string                   `json:"desc"`
	SkillEvents   SkillEventList           `json:"skillEventDic"`
	ModifierPairs SkillModifierPairs       `json:"modifierDic"`
	Modifiers     map[int64]*SkillModifier `json:"-"`
}

type SkillCast struct {
	SkillCastType int32   `json:"skillCastType"`
	CastBaseType  int32   `json:"castBaseType"`
	SkillCast     float64 `json:"skillCast"`
	AttributeType int32   `json:"attributeType"`
}

type SkillEvent struct {
	Code    int32
	Options SkillOptionList
}

type SkillEventList []SkillEvent

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

type SkillValue struct {
	Values          [][2]float64 `json:"values"`
	SkillSourceType int32        `json:"skillSourcetype"`
	Value           float64      `json:"value"`
}

func (value SkillValue) Resolve(level int32) (float64, bool) {
	if value.SkillSourceType == 1 {
		for _, item := range value.Values {
			if int32(item[0]) == level {
				return item[1], true
			}
		}
		return 0, false
	}
	return value.Value, true
}

type SkillCalculation struct {
	CalculateType int32      `json:"calculateType"`
	Param         SkillValue `json:"param"`
}

type SkillIDRef struct {
	Value int64 `json:"Value"`
}

type SkillPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type SkillOptionLink struct {
	Name string
	IDs  []int32
}

type SkillOptionLinks []SkillOptionLink

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

type SkillTargetKind string

const (
	SkillTargetSingle SkillTargetKind = "single"
	SkillTargetMulti  SkillTargetKind = "multi"
)

type SkillTargetSelector struct {
	Kind       SkillTargetKind
	TargetType int32
	Random     bool
	MinCount   *SkillValue
	MaxCount   *SkillValue
	TeamType   int32
	Flags      int32
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

type SkillOption struct {
	Kind               SkillOptionKind
	Name               string
	HideFlags          int32
	Position           SkillPosition
	UniqueID           int32
	Next               SkillOptionLinks
	Target             *SkillTargetSelector
	EffectID           int32
	EffectType         int32
	EffectPositionType int32
	EffectAttachType   int32
	ProjectileTimeMS   int32
	ModifierID         SkillIDRef
	ModifierIDs        []SkillIDRef
	Delay              *SkillValue
	Options            SkillOptionList
	Param              *SkillValue
	Success            SkillOptionList
	Failure            SkillOptionList
	IsCritEvent        bool
	DamageType         int32
	DamageSelf         *SkillCalculation
	DamageTarget       *SkillCalculation
	IsDamageLimited    bool
	DamageLimit        *SkillCalculation
	TreatSelf          *SkillCalculation
	TreatTarget        *SkillCalculation
}

type SkillOptionList []SkillOption

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
	if len(wire.Time) > 0 && !bytes.Equal(wire.Time, []byte("null")) {
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
	}
	return nil
}

type SkillModifier struct {
	ID               SkillIDRef     `json:"_id"`
	Name             string         `json:"name"`
	LevelList        []int32        `json:"levelList"`
	Tag              int32          `json:"tag"`
	ImmuneTag        int32          `json:"immuneTag"`
	ImmuneTagArray   []int32        `json:"immuneTagArr"`
	Attribute        int32          `json:"attribute"`
	OverlayType      int32          `json:"overlayType"`
	PerOverlay       SkillValue     `json:"perOverlay"`
	ContinueTime     SkillValue     `json:"continueTime"`
	BuffType         int32          `json:"buffType"`
	CanBeCleared     bool           `json:"canBeClear"`
	ThinkerType      int32          `json:"thinkerType"`
	ThinkInterval    SkillValue     `json:"thinkInterval"`
	IconID           string         `json:"iconId"`
	IconDescription  string         `json:"iconDesc"`
	EffectID         int32          `json:"effectId"`
	EffectAttachType int32          `json:"effectAttachType"`
	ValueKey         int32          `json:"valueK"`
	Value            SkillValue     `json:"valueV"`
	StateKey         int32          `json:"stateK"`
	StateValue       int32          `json:"stateV"`
	Events           SkillEventList `json:"modifierEventDic"`
}

func (modifier SkillModifier) ActiveAt(level int32) bool {
	if len(modifier.LevelList) == 0 {
		return true
	}
	for _, candidate := range modifier.LevelList {
		if candidate == level {
			return true
		}
	}
	return false
}

type SkillModifierPair struct {
	ID       int64
	Modifier *SkillModifier
}

type SkillModifierPairs []SkillModifierPair

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

type skillLogicCollection struct {
	Type   string          `json:"_t"`
	Skills skillLogicPairs `json:"skillDic"`
}

type skillLogicPair struct {
	ID    int32
	Skill *SkillLogic
}

type skillLogicPairs []skillLogicPair

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
	if collection.Type != skillLogicCollectionType {
		return nil, fmt.Errorf("unexpected collection type %q", collection.Type)
	}
	catalog := &SkillLogicCatalog{
		Skills:    make(map[int32]*SkillLogic, len(collection.Skills)),
		Modifiers: make(map[int64]*SkillModifier),
	}
	for _, pair := range collection.Skills {
		if pair.Skill == nil || pair.Skill.SkillID != pair.ID {
			return nil, fmt.Errorf("skill key %d does not match body", pair.ID)
		}
		if _, duplicate := catalog.Skills[pair.ID]; duplicate {
			return nil, fmt.Errorf("duplicate skill %d", pair.ID)
		}
		pair.Skill.Modifiers = make(map[int64]*SkillModifier, len(pair.Skill.ModifierPairs))
		for _, modifierPair := range pair.Skill.ModifierPairs {
			if _, duplicate := pair.Skill.Modifiers[modifierPair.ID]; duplicate {
				return nil, fmt.Errorf("skill %d has duplicate modifier %d", pair.ID, modifierPair.ID)
			}
			pair.Skill.Modifiers[modifierPair.ID] = modifierPair.Modifier
			if existing, duplicate := catalog.Modifiers[modifierPair.ID]; duplicate && existing != modifierPair.Modifier {
				return nil, fmt.Errorf("duplicate global modifier %d", modifierPair.ID)
			}
			catalog.Modifiers[modifierPair.ID] = modifierPair.Modifier
		}
		catalog.Skills[pair.ID] = pair.Skill
	}
	return catalog, nil
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

type SkillEffectKind string

const (
	SkillEffectDamage          SkillEffectKind = "damage"
	SkillEffectHeal            SkillEffectKind = "heal"
	SkillEffectShield          SkillEffectKind = "shield"
	SkillEffectStatus          SkillEffectKind = "status"
	SkillEffectChance          SkillEffectKind = "chance"
	SkillEffectDelay           SkillEffectKind = "delay"
	SkillEffectProjectile      SkillEffectKind = "projectile"
	SkillEffectVisual          SkillEffectKind = "visual"
	SkillEffectCounter         SkillEffectKind = "counter"
	SkillEffectReflect         SkillEffectKind = "reflect"
	SkillEffectLifeSteal       SkillEffectKind = "life_steal"
	SkillEffectChangeGlobalCD  SkillEffectKind = "change_global_cd"
	SkillEffectChangeDamage    SkillEffectKind = "change_damage"
	SkillEffectChangeCastCount SkillEffectKind = "change_cast_count"
	SkillEffectModifyStatus    SkillEffectKind = "modify_status"
)

type SkillTargetSide string

const (
	SkillTargetSelf        SkillTargetSide = "self"
	SkillTargetCastTarget  SkillTargetSide = "cast_target"
	SkillTargetEventSource SkillTargetSide = "event_source"
	SkillTargetHolder      SkillTargetSide = "modifier_holder"
	SkillTargetEnemy       SkillTargetSide = "enemy"
	SkillTargetAlly        SkillTargetSide = "ally"
	SkillTargetAny         SkillTargetSide = "any"
)

type SkillTargetPlan struct {
	Kind     SkillTargetKind
	Side     SkillTargetSide
	MinCount int32
	MaxCount int32
	Random   bool
	Flags    int32
}

type SkillFormulaStat string

const (
	SkillStatPhysicalAttack  SkillFormulaStat = "physical_attack"
	SkillStatSpiritualAttack SkillFormulaStat = "spiritual_attack"
	SkillStatMaxHP           SkillFormulaStat = "max_hp"
	SkillStatCurrentHP       SkillFormulaStat = "current_hp"
)

type SkillFormula struct {
	Stat    SkillFormulaStat
	Percent float64
}

type SkillDamageType string

const (
	SkillDamagePhysical  SkillDamageType = "physical"
	SkillDamageSpiritual SkillDamageType = "spiritual"
	SkillDamageTrue      SkillDamageType = "true"
)

type SkillDamagePlan struct {
	Type     SkillDamageType
	Self     SkillFormula
	Target   SkillFormula
	CanCrit  bool
	Limited  bool
	Limit    SkillFormula
	Periodic bool
}

type SkillTreatmentPlan struct {
	Self     SkillFormula
	Target   SkillFormula
	CanCrit  bool
	Periodic bool
}

type SkillStatusPlan struct {
	SkillID         int32
	SkillLevel      int32
	ModifierID      int64
	Tag             int32
	ImmuneTag       int32
	ImmuneTags      []int32
	Attribute       int32
	OverlayType     int32
	PerOverlay      float64
	DurationSeconds float64
	TickSeconds     float64
	BuffType        int32
	CanBeCleared    bool
	ThinkerType     int32
	ValueKey        int32
	Value           float64
	StateKey        int32
	StateValue      int32
	IsBuff          bool
	IsDebuff        bool
	IsControl       bool
	HasDOT          bool
	HasHOT          bool
}

type SkillTrigger struct {
	Scope      string
	Event      int32
	ModifierID int64
}

type SkillEffect struct {
	Kind             SkillEffectKind
	Trigger          SkillTrigger
	Target           SkillTargetPlan
	ChancePercent    float64
	DelayMS          int32
	ParamPercent     float64
	ModifierID       int64
	EffectID         int32
	EffectType       int32
	EffectPosition   int32
	EffectAttachType int32
	Damage           *SkillDamagePlan
	Treatment        *SkillTreatmentPlan
	Status           *SkillStatusPlan
	Success          []SkillEffect
	Failure          []SkillEffect
	Children         []SkillEffect
}

type SkillPlan struct {
	SkillID    int32
	Level      int32
	SkillType  int32
	CooldownMS int32
	DurationMS int32
	IntervalMS int32
	TeamType   int32
	Cast       SkillCast
	Effects    []SkillEffect
}

func (catalog *SkillLogicCatalog) Plan(skillID, level int32) (SkillPlan, error) {
	skill, ok := catalog.Skills[skillID]
	if !ok {
		return SkillPlan{}, fmt.Errorf("skill %d not found", skillID)
	}
	if level < 1 || level > skill.MaxLevel {
		return SkillPlan{}, fmt.Errorf("skill %d level %d outside 1..%d", skillID, level, skill.MaxLevel)
	}
	plan := SkillPlan{
		SkillID: skillID, Level: level, SkillType: skill.SkillType,
		CooldownMS: skill.CooldownMS, DurationMS: skill.DurationMS,
		IntervalMS: skill.IntervalMS, TeamType: skill.TeamType, Cast: skill.Cast,
	}
	builder := skillPlanBuilder{skill: skill, level: level, modifierStack: make(map[int64]bool)}
	for _, event := range skill.SkillEvents {
		effects, err := builder.options(event.Options, SkillTrigger{Scope: "skill", Event: event.Code})
		if err != nil {
			return SkillPlan{}, fmt.Errorf("skill %d level %d event %d: %w", skillID, level, event.Code, err)
		}
		plan.Effects = append(plan.Effects, effects...)
	}
	return plan, nil
}

type skillPlanBuilder struct {
	skill         *SkillLogic
	level         int32
	modifierStack map[int64]bool
}

func (builder *skillPlanBuilder) options(options SkillOptionList, trigger SkillTrigger) ([]SkillEffect, error) {
	effects := make([]SkillEffect, 0, len(options))
	for _, option := range options {
		effect := SkillEffect{Trigger: trigger, Target: builder.target(option.Target)}
		var err error
		switch option.Kind {
		case SkillOptionDamage:
			effect.Kind = SkillEffectDamage
			effect.Damage, err = builder.damage(option, trigger.Event == 8)
		case SkillOptionHeal:
			effect.Kind = SkillEffectHeal
			effect.Treatment, err = builder.treatment(option, trigger.Event == 8)
		case SkillOptionShield:
			effect.Kind = SkillEffectShield
			effect.Treatment, err = builder.treatment(option, false)
		case SkillOptionApplyModifier:
			effect, err = builder.modifierEffect(option.ModifierID.Value, effect.Target, trigger)
		case SkillOptionProjectile:
			effect.Kind, effect.DelayMS, effect.EffectID = SkillEffectProjectile, option.ProjectileTimeMS, option.EffectID
			for _, ref := range option.ModifierIDs {
				child, childErr := builder.modifierEffect(ref.Value, effect.Target, trigger)
				if childErr != nil {
					err = childErr
					break
				}
				effect.Children = append(effect.Children, child)
			}
		case SkillOptionChance:
			effect.Kind = SkillEffectChance
			if option.Param == nil {
				err = fmt.Errorf("chance option lacks param")
				break
			}
			effect.ChancePercent, _ = option.Param.Resolve(builder.level)
			effect.Success, err = builder.options(option.Success, trigger)
			if err == nil {
				effect.Failure, err = builder.options(option.Failure, trigger)
			}
		case SkillOptionDelay:
			effect.Kind = SkillEffectDelay
			if option.Delay == nil {
				err = fmt.Errorf("delay option lacks time")
				break
			}
			delay, ok := option.Delay.Resolve(builder.level)
			if !ok {
				err = fmt.Errorf("delay has no value for level %d", builder.level)
				break
			}
			effect.DelayMS = int32(delay)
			effect.Children, err = builder.options(option.Options, trigger)
		case SkillOptionPlayEffect:
			effect.Kind, effect.EffectID = SkillEffectVisual, option.EffectID
			effect.EffectType, effect.EffectPosition = option.EffectType, option.EffectPositionType
			effect.EffectAttachType = option.EffectAttachType
		case SkillOptionCounter:
			effect.Kind = SkillEffectCounter
		case SkillOptionReflect:
			effect.Kind = SkillEffectReflect
		case SkillOptionLifeSteal:
			effect.Kind = SkillEffectLifeSteal
			effect.ParamPercent, err = builder.param(option)
		case SkillOptionChangeGlobalCD:
			effect.Kind = SkillEffectChangeGlobalCD
			effect.ParamPercent, err = builder.param(option)
		case SkillOptionChangeDamage:
			effect.Kind = SkillEffectChangeDamage
			effect.ParamPercent, err = builder.param(option)
		case SkillOptionChangeCastCount:
			effect.Kind = SkillEffectChangeCastCount
			effect.ParamPercent, err = builder.param(option)
		case SkillOptionModifyModifier:
			effect.Kind, effect.ModifierID = SkillEffectModifyStatus, option.ModifierID.Value
			effect.Status, err = builder.statusPlan(option.ModifierID.Value)
		default:
			err = fmt.Errorf("unsupported option %q", option.Kind)
		}
		if err != nil {
			return nil, fmt.Errorf("option %q id %d: %w", option.Kind, option.UniqueID, err)
		}
		effects = append(effects, effect)
	}
	return effects, nil
}

func (builder *skillPlanBuilder) modifierEffect(id int64, target SkillTargetPlan, trigger SkillTrigger) (SkillEffect, error) {
	modifier, ok := builder.skill.Modifiers[id]
	if !ok {
		return SkillEffect{}, fmt.Errorf("modifier %d not found", id)
	}
	effect := SkillEffect{Kind: SkillEffectStatus, Trigger: trigger, Target: target, ModifierID: id}
	if !modifier.ActiveAt(builder.level) {
		return effect, nil
	}
	status, err := builder.statusPlan(id)
	if err != nil {
		return SkillEffect{}, err
	}
	effect.Status = status
	if builder.modifierStack[id] {
		return effect, nil
	}
	builder.modifierStack[id] = true
	defer delete(builder.modifierStack, id)
	for _, event := range modifier.Events {
		children, childErr := builder.options(event.Options, SkillTrigger{Scope: "modifier", Event: event.Code, ModifierID: id})
		if childErr != nil {
			return SkillEffect{}, childErr
		}
		for index := range children {
			if event.Code == 8 && children[index].Damage != nil {
				children[index].Damage.Periodic = true
				status.HasDOT = true
			}
			if event.Code == 8 && children[index].Treatment != nil {
				children[index].Treatment.Periodic = true
				status.HasHOT = true
			}
		}
		effect.Children = append(effect.Children, children...)
	}
	return effect, nil
}

func (builder *skillPlanBuilder) statusPlan(id int64) (*SkillStatusPlan, error) {
	modifier, ok := builder.skill.Modifiers[id]
	if !ok {
		return nil, fmt.Errorf("modifier %d not found", id)
	}
	if !modifier.ActiveAt(builder.level) {
		return nil, nil
	}
	value, _ := modifier.Value.Resolve(builder.level)
	perOverlay, _ := modifier.PerOverlay.Resolve(builder.level)
	duration, _ := modifier.ContinueTime.Resolve(builder.level)
	interval, _ := modifier.ThinkInterval.Resolve(builder.level)
	status := &SkillStatusPlan{
		SkillID: builder.skill.SkillID, SkillLevel: builder.level,
		ModifierID: id, Tag: modifier.Tag, ImmuneTag: modifier.ImmuneTag,
		ImmuneTags: append([]int32(nil), modifier.ImmuneTagArray...), Attribute: modifier.Attribute,
		OverlayType: modifier.OverlayType, PerOverlay: perOverlay, DurationSeconds: duration,
		TickSeconds: interval, BuffType: modifier.BuffType, CanBeCleared: modifier.CanBeCleared,
		ThinkerType: modifier.ThinkerType, ValueKey: modifier.ValueKey, Value: value,
		StateKey: modifier.StateKey, StateValue: modifier.StateValue,
	}
	status.IsBuff = modifier.BuffType == 0 && modifier.ValueKey != 0 && value >= 0
	status.IsDebuff = modifier.BuffType == 1 || (modifier.ValueKey != 0 && value < 0)
	status.IsControl = modifier.StateKey != 0
	return status, nil
}

func (builder *skillPlanBuilder) param(option SkillOption) (float64, error) {
	if option.Param == nil {
		return 0, fmt.Errorf("param missing")
	}
	value, ok := option.Param.Resolve(builder.level)
	if !ok {
		return 0, fmt.Errorf("param has no value for level %d", builder.level)
	}
	return value, nil
}

func (builder *skillPlanBuilder) damage(option SkillOption, periodic bool) (*SkillDamagePlan, error) {
	damageType, err := damageType(option.DamageType)
	if err != nil {
		return nil, err
	}
	self, err := builder.formula(option.DamageSelf)
	if err != nil {
		return nil, fmt.Errorf("self damage: %w", err)
	}
	target, err := builder.formula(option.DamageTarget)
	if err != nil {
		return nil, fmt.Errorf("target damage: %w", err)
	}
	limit, err := builder.formula(option.DamageLimit)
	if err != nil {
		return nil, fmt.Errorf("damage limit: %w", err)
	}
	return &SkillDamagePlan{Type: damageType, Self: self, Target: target, CanCrit: option.IsCritEvent, Limited: option.IsDamageLimited, Limit: limit, Periodic: periodic}, nil
}

func (builder *skillPlanBuilder) treatment(option SkillOption, periodic bool) (*SkillTreatmentPlan, error) {
	self, err := builder.formula(option.TreatSelf)
	if err != nil {
		return nil, fmt.Errorf("self treatment: %w", err)
	}
	target, err := builder.formula(option.TreatTarget)
	if err != nil {
		return nil, fmt.Errorf("target treatment: %w", err)
	}
	return &SkillTreatmentPlan{Self: self, Target: target, CanCrit: option.IsCritEvent, Periodic: periodic}, nil
}

func (builder *skillPlanBuilder) formula(calculation *SkillCalculation) (SkillFormula, error) {
	if calculation == nil {
		return SkillFormula{}, nil
	}
	stat, err := formulaStat(calculation.CalculateType)
	if err != nil {
		return SkillFormula{}, err
	}
	value, ok := calculation.Param.Resolve(builder.level)
	if !ok {
		return SkillFormula{}, nil
	}
	return SkillFormula{Stat: stat, Percent: value}, nil
}

func formulaStat(code int32) (SkillFormulaStat, error) {
	switch code {
	case 0:
		return SkillStatPhysicalAttack, nil
	case 1:
		return SkillStatSpiritualAttack, nil
	case 2:
		return SkillStatMaxHP, nil
	case 3:
		return SkillStatCurrentHP, nil
	default:
		return "", fmt.Errorf("unsupported calculation type %d", code)
	}
}

func damageType(code int32) (SkillDamageType, error) {
	switch code {
	case 1:
		return SkillDamagePhysical, nil
	case 2:
		return SkillDamageSpiritual, nil
	case 3:
		return SkillDamageTrue, nil
	default:
		return "", fmt.Errorf("unsupported damage type %d", code)
	}
}

func (builder *skillPlanBuilder) target(selector *SkillTargetSelector) SkillTargetPlan {
	if selector == nil {
		return SkillTargetPlan{}
	}
	plan := SkillTargetPlan{Kind: selector.Kind, Random: selector.Random, Flags: selector.Flags}
	if selector.Kind == SkillTargetSingle {
		switch selector.TargetType {
		case 0:
			plan.Side = SkillTargetSelf
		case 1:
			plan.Side = SkillTargetCastTarget
		case 2:
			plan.Side = SkillTargetEventSource
		case 3:
			plan.Side = SkillTargetHolder
		}
		plan.MinCount, plan.MaxCount = 1, 1
		return plan
	}
	switch selector.TeamType {
	case 0:
		// Zero inherits the skill's own team type. Online friendly skills such
		// as 110301 and 310404 use this encoding for their group effects.
		switch builder.skill.TeamType {
		case 1:
			plan.Side = SkillTargetEnemy
		case 2:
			plan.Side = SkillTargetAlly
		default:
			plan.Side = SkillTargetAny
		}
	case 1:
		plan.Side = SkillTargetEnemy
	case 2:
		plan.Side = SkillTargetAlly
	default:
		plan.Side = SkillTargetAny
	}
	if selector.MinCount != nil {
		value, _ := selector.MinCount.Resolve(builder.level)
		plan.MinCount = int32(value)
	}
	if selector.MaxCount != nil {
		value, _ := selector.MaxCount.Resolve(builder.level)
		plan.MaxCount = int32(value)
	}
	return plan
}

func (catalog *SkillLogicCatalog) OptionKindCounts() map[SkillOptionKind]int {
	counts := make(map[SkillOptionKind]int)
	var walk func(SkillOptionList)
	walk = func(options SkillOptionList) {
		for _, option := range options {
			counts[option.Kind]++
			walk(option.Options)
			walk(option.Success)
			walk(option.Failure)
		}
	}
	for _, skill := range catalog.Skills {
		for _, event := range skill.SkillEvents {
			walk(event.Options)
		}
		for _, modifier := range skill.Modifiers {
			for _, event := range modifier.Events {
				walk(event.Options)
			}
		}
	}
	return counts
}
