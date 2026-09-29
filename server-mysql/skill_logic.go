package main

import (
	"fmt"
)

const skillLogicCollectionType = "SkillLogicConfigCollection"

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

type SkillValue struct {
	Values          [][2]float64 `json:"values"`
	SkillSourceType int32        `json:"skillSourcetype"`
	Value           float64      `json:"value"`
}

// skillValueSourcePerLevel is a values-table lookup keyed by skill level; every
// other source type is a single constant carried in Value.
const skillValueSourcePerLevel int32 = 1

func (value SkillValue) Resolve(level int32) (float64, bool) {
	if value.SkillSourceType == skillValueSourcePerLevel {
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

// A handful of shipped SkillLogicConfig modifiers have a levelList copied
// from the wrong node, or a number the node left blank.  Their SkillConfig
// descriptions still state the level and the value that apply.  Keep this
// compatibility table small and explicit: normal modifiers continue to use
// their own levelList and their own numbers.
type modifierLevelOverride struct {
	levels          []int32
	durationArg     string
	durationDefault float64
	// durationArg/durationDefault repair a duration; this repairs the tick
	// amount of an event-8 伤害 node whose own per-level table is empty, and it
	// is read as a percentage of the target's maximum HP (calculateType 2).
	tickDamagePercent float64
}

var onlineModifierLevelOverrides = map[int64]modifierLevelOverride{
	// Anatomy: the level-3 death protection lasts Args3 (7 seconds).
	31060113: {levels: []int32{3}, durationArg: "Args3", durationDefault: 7},
	// Demolition Bomb: the level-4/5 spirit-defense debuff writes -7 in the
	// client's continueTime table while its own description says "持续7秒"
	// (-25%/-28% spirit defense for 7 seconds). The sign leaked from the
	// attribute value, so the duration is the magnitude; reading it verbatim
	// made the debuff permanent, because the runtime treats a non-positive
	// duration as "never expires".
	11030216: {levels: []int32{4, 5}, durationArg: "Args2", durationDefault: 7},
	// Hook/attribute nodes for the level-gated profession skills.
	21040313: {levels: []int32{4, 5}, durationArg: "Args6", durationDefault: 14},
	21040412: {levels: []int32{1, 2, 3, 4, 5}, durationArg: "Args3", durationDefault: 14},
	41040213: {levels: []int32{5}, durationArg: "Args4", durationDefault: 7},
	41060412: {levels: []int32{3}, durationArg: "Args4", durationDefault: 14},
	// Open Volley (开大脚) level 3: its 流血 tick node is the only event-8 伤害
	// node in the whole table that declares a per-level param table
	// (skillSourcetype 1) and then leaves it empty. The node therefore resolves
	// to no formula at all, the cast-time tick amount comes out 0, and because
	// the runtime only installs a periodic effect with a positive amount the
	// 流血 never even lands on the target. The description for this level says
	// "20%的几率给敌方添加4%敌方最大生命值的【流血】，持续14秒效果", and every
	// other 流血 node writes the described number as [[level, percent]] on
	// calculateType 2 (max-HP percent), so 4 is the missing value. 210603 has
	// no other level carrying this modifier.
	21060317: {levels: []int32{3}, tickDamagePercent: 4},
}

// 两个已发布技能的 teamType 与同职业其余技能、以及它们自己的文案都相反，
// 导致"点队友 → 效果落到敌人身上"。这是技能级（不是 modifier 级）的定点覆盖，
// 保持最小、显式：只有这两个技能受影响，其余 128 个技能照旧读自己的 teamType。
//
// 判据（详见 文档/31-技能偏差清单-客户端-服务端对照-2026-09-15.md §七）：
//   - 两者的数值、几率、持续与文案**全部一致**，唯一反的就是阵营；
//   - 同职业的友方技能阵营全是 2 —— 护士 8 个（急救/兴奋剂/高级急救/强心针/
//     护心决/连锁注射/心灵启示/人体学）、超能力 4 个（正义联盟/烽火连城/无敌/
//     魔法护盾），只有它们两个是 1。
//
// 客户端支持给友方目标施法：反汇编 ET.ClickBattleTargetEvent 可见点选按
// UnitType 分派（4 → C2M_SelectEnermy，5 → C2M_SelectTeamMember），客户端
// 从头到尾不读 teamType。所以改成 2 之后玩家点队友会正常生效；没点队友时
// 退回 playerSkillPrimaryTarget 的兜底（血量百分比最低的存活队友）。
var onlineSkillTeamTypeOverrides = map[int32]int32{
	310604: 2, // 双面镜：文案"使友方单体攻击力增加…防御力减少"
	410404: 2, // 柳暗花明：文案"给友方单体添加【隐身】"
}

type SkillModifierPair struct {
	ID       int64
	Modifier *SkillModifier
}

type SkillModifierPairs []SkillModifierPair

type skillLogicCollection struct {
	Type   string          `json:"_t"`
	Skills skillLogicPairs `json:"skillDic"`
}

type skillLogicPair struct {
	ID    int32
	Skill *SkillLogic
}

type skillLogicPairs []skillLogicPair

func buildSkillLogicCatalog(collection skillLogicCollection) (*SkillLogicCatalog, error) {
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
	SkillID          int32
	SkillLevel       int32
	ModifierID       int64
	EffectID         int32
	EffectAttachType int32
	Tag              int32
	ImmuneTag        int32
	ImmuneTags       []int32
	Attribute        int32
	OverlayType      int32
	PerOverlay       float64
	DurationSeconds  float64
	TickSeconds      float64
	BuffType         int32
	CanBeCleared     bool
	ThinkerType      int32
	ValueKey         int32
	Value            float64
	StateKey         int32
	StateValue       int32
	IsBuff           bool
	IsDebuff         bool
	IsControl        bool
	HasDOT           bool
	HasHOT           bool
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
	ImpactEffectID   int32
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
	teamType := skill.TeamType
	if override, ok := onlineSkillTeamTypeOverrides[skillID]; ok {
		teamType = override
	}
	plan := SkillPlan{
		SkillID: skillID, Level: level, SkillType: skill.SkillType,
		CooldownMS: skill.CooldownMS, DurationMS: skill.DurationMS,
		IntervalMS: skill.IntervalMS, TeamType: teamType, Cast: skill.Cast,
	}
	builder := skillPlanBuilder{skill: skill, level: level, modifierStack: make(map[int64]bool), teamType: teamType}
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
	// teamType 是本技能的生效阵营，已经过 onlineSkillTeamTypeOverrides 修正。
	// 选择器里 teamType=0 表示"继承技能自己的阵营"，必须读这个值而不是
	// skill.TeamType，否则被覆盖的技能其零值选择器会走回错误的阵营。
	teamType int32
}

func (builder *skillPlanBuilder) modifierOverride(id int64) (modifierLevelOverride, bool) {
	override, ok := onlineModifierLevelOverrides[id]
	return override, ok
}

func (builder *skillPlanBuilder) modifierActiveAt(id int64, modifier *SkillModifier) bool {
	if modifier == nil {
		return false
	}
	if override, ok := builder.modifierOverride(id); ok {
		for _, level := range override.levels {
			if level == builder.level {
				return true
			}
		}
		return false
	}
	return modifier.ActiveAt(builder.level)
}

func (builder *skillPlanBuilder) modifierDuration(id int64, modifier *SkillModifier) float64 {
	if modifier == nil {
		return 0
	}
	if override, ok := builder.modifierOverride(id); ok {
		if value := skillLevelArgument(builder.skill.SkillID, builder.level, override.durationArg); value > 0 {
			return value
		}
		if override.durationDefault > 0 {
			return override.durationDefault
		}
	}
	duration, _ := modifier.ContinueTime.Resolve(builder.level)
	return duration
}

// repairTickDamage supplies the per-tick amount of an event-8 伤害 node the
// shipped table left empty. An empty param table resolves to no formula, and
// the runtime reads that as a zero-damage tick it then refuses to install.
// Only an unresolved target formula is repaired, and only from an override
// that states the number: a node carrying its own value keeps it, and a node
// with nothing to say stays untouched.
func (builder *skillPlanBuilder) repairTickDamage(id int64, damage *SkillDamagePlan) {
	if damage == nil || damage.Target.Stat != "" || damage.Target.Percent != 0 {
		return
	}
	override, ok := builder.modifierOverride(id)
	if !ok || override.tickDamagePercent <= 0 {
		return
	}
	damage.Target = SkillFormula{Stat: SkillStatMaxHP, Percent: override.tickDamagePercent}
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
				if effect.ImpactEffectID == 0 {
					if modifier := builder.skill.Modifiers[ref.Value]; modifier != nil && builder.modifierActiveAt(ref.Value, modifier) {
						effect.ImpactEffectID = modifier.EffectID
					}
				}
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
			// The client stores the reflected percentage in the option param
			// (e.g. 110602 writes 20/23/28 for its "20/23/28% 反伤"). Without
			// this the plan leaves ParamPercent at 0 and the runtime falls back
			// to a full 100% reflection.
			effect.ParamPercent, err = builder.param(option)
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
	if !builder.modifierActiveAt(id, modifier) {
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
				builder.repairTickDamage(id, children[index].Damage)
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
	if !builder.modifierActiveAt(id, modifier) {
		return nil, nil
	}
	value, _ := modifier.Value.Resolve(builder.level)
	perOverlay, _ := modifier.PerOverlay.Resolve(builder.level)
	duration := builder.modifierDuration(id, modifier)
	interval, _ := modifier.ThinkInterval.Resolve(builder.level)
	status := &SkillStatusPlan{
		SkillID: builder.skill.SkillID, SkillLevel: builder.level,
		ModifierID: id, Tag: modifier.Tag, ImmuneTag: modifier.ImmuneTag,
		EffectID: modifier.EffectID, EffectAttachType: modifier.EffectAttachType,
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
		switch builder.teamType {
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
