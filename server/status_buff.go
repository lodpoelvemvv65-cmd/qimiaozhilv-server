package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"mhqserver/protocol"
)

// The top-left status list always resolves icons from the FairyGUI "Skill"
// package. These are the exact exported resource names in Skill_fui.
var clientSkillBuffIcons = map[string]string{
	"410602":              "410602",
	"bufficon_atkAdd":     "bufficon_atkAdd",
	"bufficon_atkRes":     "bufficon_atkRes",
	"bufficon_Bearing":    "bufficon_Bearing",
	"bufficon_beatback":   "bufficon_beatback",
	"bufficon_bleed":      "bufficon_bleed",
	"bufficon_constrea":   "bufficon_constrea",
	"bufficon_critAdd":    "bufficon_critAdd",
	"bufficon_critRes":    "bufficon_critRes",
	"bufficon_dfeAdd":     "bufficon_dfeAdd",
	"bufficon_dfeRes":     "bufficon_dfeRes",
	"bufficon_frost":      "bufficon_frost",
	"bufficon_hpdown":     "bufficon_hpdown",
	"bufficon_hpup":       "bufficon_hpup",
	"bufficon_light":      "bufficon_light",
	"bufficon_nodie ":     "bufficon_nodie ", // The package name has a trailing space.
	"bufficon_poisoning":  "bufficon_poisoning",
	"bufficon_resurgence": "bufficon_resurgence",
	"bufficon_shield":     "bufficon_shield",
	"bufficon_shihua":     "bufficon_shihua",
	"bufficon_silence":    "bufficon_silence",
	"bufficon_Thorns":     "bufficon_Thorns",
	"bufficon_vertigo":    "bufficon_vertigo",
}

// SkillLogicConfig and Skill_fui have a few naming mismatches. Keep the server
// wire value aligned with the resources the client can actually load.
func normalizeClientBuffIcon(icon string) (string, bool) {
	icon = strings.TrimSpace(icon)
	switch icon {
	case "41060200":
		// SkillLogicConfig uses a modifier icon suffix, while Skill_fui only
		// exports the owning skill icon.
		icon = "410602"
	case "bufficon_nodie":
		icon = "bufficon_nodie "
	case "bufficon_yinshen":
		// Skill_fui exports the matching blue silhouette as bufficon_Bearing.
		icon = "bufficon_Bearing"
	}
	resolved, ok := clientSkillBuffIcons[icon]
	return resolved, ok
}

const (
	itemBuffBattleExp int32 = 1
	itemBuffIdleExp   int32 = 2
	itemBuffPetExp    int32 = 3
	itemBuffHPBall    int32 = 4
	itemBuffMPBall    int32 = 5
	itemBuffNormalRun int32 = 6
	itemBuffFastRun   int32 = 7

	indefiniteClientBuffMS int32 = math.MaxInt32
)

type activeItemBuff struct {
	ItemID     int32   `json:"item_id"`
	EffectType int32   `json:"effect_type"`
	ExpiresAt  int64   `json:"expires_at,omitempty"` // Unix milliseconds; zero means until consumed.
	Multiplier float64 `json:"multiplier,omitempty"`
	Capacity   int32   `json:"capacity,omitempty"`
}

func itemBuffsToJSON(buffs map[int32]*activeItemBuff) string {
	if len(buffs) == 0 {
		return ""
	}
	b, err := json.Marshal(buffs)
	if err != nil {
		return ""
	}
	return string(b)
}

func itemBuffsFromJSON(raw string) map[int32]*activeItemBuff {
	buffs := make(map[int32]*activeItemBuff)
	if strings.TrimSpace(raw) == "" {
		return buffs
	}
	if err := json.Unmarshal([]byte(raw), &buffs); err != nil {
		return make(map[int32]*activeItemBuff)
	}
	pruneExpiredItemBuffs(buffs, time.Now().UnixMilli())
	return buffs
}

func pruneExpiredItemBuffs(buffs map[int32]*activeItemBuff, now int64) {
	for category, buff := range buffs {
		if buff == nil || buff.ItemID <= 0 || (buff.ExpiresAt > 0 && buff.ExpiresAt <= now) {
			delete(buffs, category)
		}
	}
}

func (ss *session) ensureItemBuffsLocked() {
	if ss.itemBuffs == nil {
		ss.itemBuffs = make(map[int32]*activeItemBuff)
	}
}

func (ss *session) ensureItemBuffs() {
	ss.itemBuffMu.Lock()
	ss.ensureItemBuffsLocked()
	ss.itemBuffMu.Unlock()
}

func (ss *session) syncLegacyItemBuffFieldsLocked(now int64) {
	ss.ensureItemBuffsLocked()
	pruneExpiredItemBuffs(ss.itemBuffs, now)
	ss.expMult, ss.expMultUntil = 1, 0
	ss.hpBallUntil, ss.hpBallCap = 0, 0
	ss.mpBallUntil, ss.mpBallCap = 0, 0
	if buff := ss.itemBuffs[itemBuffBattleExp]; buff != nil {
		ss.expMult, ss.expMultUntil = buff.Multiplier, buff.ExpiresAt
	}
	if buff := ss.itemBuffs[itemBuffHPBall]; buff != nil {
		ss.hpBallUntil, ss.hpBallCap = math.MaxInt64, buff.Capacity
	}
	if buff := ss.itemBuffs[itemBuffMPBall]; buff != nil {
		ss.mpBallUntil, ss.mpBallCap = math.MaxInt64, buff.Capacity
	}
}

func (ss *session) syncLegacyItemBuffFields() {
	ss.itemBuffMu.Lock()
	ss.syncLegacyItemBuffFieldsLocked(time.Now().UnixMilli())
	ss.itemBuffMu.Unlock()
}

func (ss *session) itemBuffsJSON() string {
	if ss == nil {
		return ""
	}
	ss.itemBuffMu.Lock()
	defer ss.itemBuffMu.Unlock()
	ss.ensureItemBuffsLocked()
	pruneExpiredItemBuffs(ss.itemBuffs, time.Now().UnixMilli())
	return itemBuffsToJSON(ss.itemBuffs)
}

func itemBuffCategory(effectType int32) int32 {
	switch effectType {
	case 6:
		return itemBuffBattleExp
	case 7:
		return itemBuffIdleExp
	case 16:
		return itemBuffPetExp
	case 2:
		return itemBuffHPBall
	case 3:
		return itemBuffMPBall
	case 20:
		return itemBuffNormalRun
	case 21:
		return itemBuffFastRun
	default:
		return 0
	}
}

func itemBuffIcon(effectType int32) string {
	switch effectType {
	case 2, 3:
		// SkillLogicConfig itself uses hpup for both max HP and max MP states.
		return "bufficon_hpup"
	case 20:
		return "bufficon_Bearing"
	case 21:
		return "bufficon_light"
	case 6, 7, 16:
		return "bufficon_atkAdd"
	default:
		return ""
	}
}

func itemBuffDescription(gb map[string]interface{}, itemID int32) string {
	if desc, _ := gb["Description"].(string); strings.TrimSpace(desc) != "" {
		return desc
	}
	if name, _ := gb["Name"].(string); strings.TrimSpace(name) != "" {
		return name
	}
	return fmt.Sprintf("物品%d", itemID)
}

func itemBuffDurationMS(gb map[string]interface{}, effectType int32) int64 {
	if duration := int64(num(gb["ContinuedSeconds"])); duration > 0 {
		return duration
	}
	if effectType == 20 || effectType == 21 {
		return int64(time.Hour / time.Millisecond)
	}
	return 0
}

func remainingItemBuffMS(buff *activeItemBuff, now int64) int32 {
	if buff == nil {
		return 0
	}
	if buff.ExpiresAt == 0 {
		return indefiniteClientBuffMS
	}
	remaining := buff.ExpiresAt - now
	if remaining <= 0 {
		return 0
	}
	if remaining > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(remaining)
}

func (s *Server) pushItemBuffMessage(ch *channel, buff *activeItemBuff, change protocol.ChangeType, duration int32) {
	if ch == nil || ch.session == nil || buff == nil {
		return
	}
	if tables == nil {
		return
	}
	gb := tables.goodsBase[int64(buff.ItemID)]
	icon, ok := normalizeClientBuffIcon(itemBuffIcon(buff.EffectType))
	if gb == nil || !ok {
		return
	}
	s.sendPush(ch, protocol.OpM2C_BattleChangeState, &protocol.M2C_BattleChangeState{
		Id:           int64(buff.ItemID),
		TargetUnitId: ch.session.playerID,
		IconId:       icon,
		IconDesc:     itemBuffDescription(gb, buff.ItemID),
		Type:         change,
		Time:         duration,
		IsBuff:       true,
		ActorId:      ch.session.playerID,
	})
}

func (s *Server) activateItemBuff(ch *channel, gb map[string]interface{}) {
	if ch == nil || ch.session == nil || gb == nil {
		return
	}
	effectType := int32(num(gb["EffectType"]))
	category := itemBuffCategory(effectType)
	if category == 0 {
		return
	}
	itemID := int32(num(gb["_id"]))
	if itemID <= 0 {
		return
	}
	ss := ch.session
	now := time.Now().UnixMilli()
	ss.itemBuffMu.Lock()
	ss.ensureItemBuffsLocked()
	pruneExpiredItemBuffs(ss.itemBuffs, now)
	old := ss.itemBuffs[category]
	var removed *activeItemBuff
	if old != nil && old.ItemID != itemID {
		copy := *old
		removed = &copy
	}
	duration := itemBuffDurationMS(gb, effectType)
	expiresAt := int64(0)
	if duration > 0 {
		expiresAt = now + duration
		// Run time is a reserve and accumulates when another card is used.
		if (effectType == 20 || effectType == 21) && old != nil && old.ExpiresAt > now {
			expiresAt = old.ExpiresAt + duration
		}
	}
	buff := &activeItemBuff{
		ItemID: itemID, EffectType: effectType, ExpiresAt: expiresAt,
		Multiplier: numf(gb["ExpRange"]), Capacity: int32(num(gb["Capacity"])),
	}
	ss.itemBuffs[category] = buff
	ss.syncLegacyItemBuffFieldsLocked(now)
	wireBuff := *buff
	ss.itemBuffMu.Unlock()
	if removed != nil {
		s.pushItemBuffMessage(ch, removed, protocol.ChangeType_Reduce, 0)
	}
	s.pushItemBuffMessage(ch, &wireBuff, protocol.ChangeType_Add, remainingItemBuffMS(&wireBuff, now))
}

func (s *Server) clearItemBuff(ch *channel, category int32) {
	if ch == nil || ch.session == nil {
		return
	}
	ss := ch.session
	ss.itemBuffMu.Lock()
	ss.ensureItemBuffsLocked()
	var removed *activeItemBuff
	if old := ss.itemBuffs[category]; old != nil {
		copy := *old
		removed = &copy
		delete(ss.itemBuffs, category)
	}
	ss.syncLegacyItemBuffFieldsLocked(time.Now().UnixMilli())
	ss.itemBuffMu.Unlock()
	if removed != nil {
		s.pushItemBuffMessage(ch, removed, protocol.ChangeType_Reduce, 0)
	}
}

// C2M_GetBuffTime is the client's ready signal for rebuilding the top-left
// status list after login or a scene recreation.
func (s *Server) pushActiveItemBuffs(ch *channel) {
	if ch == nil || ch.session == nil {
		return
	}
	ss := ch.session
	now := time.Now().UnixMilli()
	ss.itemBuffMu.Lock()
	ss.ensureItemBuffsLocked()
	pruneExpiredItemBuffs(ss.itemBuffs, now)
	ss.syncLegacyItemBuffFieldsLocked(now)
	var active []activeItemBuff
	for _, category := range []int32{
		itemBuffBattleExp, itemBuffIdleExp, itemBuffPetExp, itemBuffHPBall,
		itemBuffMPBall, itemBuffNormalRun, itemBuffFastRun,
	} {
		if buff := ss.itemBuffs[category]; buff != nil {
			active = append(active, *buff)
		}
	}
	ss.itemBuffMu.Unlock()
	for i := range active {
		s.pushItemBuffMessage(ch, &active[i], protocol.ChangeType_Add, remainingItemBuffMS(&active[i], now))
	}
}

func (ss *session) petExperienceMultiplier() float64 {
	if ss == nil {
		return 1
	}
	ss.itemBuffMu.Lock()
	defer ss.itemBuffMu.Unlock()
	ss.ensureItemBuffsLocked()
	buff := ss.itemBuffs[itemBuffPetExp]
	if buff == nil || buff.ExpiresAt <= time.Now().UnixMilli() || buff.Multiplier <= 1 {
		return 1
	}
	return buff.Multiplier
}
