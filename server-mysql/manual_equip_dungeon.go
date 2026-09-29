package main

import "fmt"

const (
	manualEquipFirstScene = int32(10033)
	manualEquipLastScene  = int32(10035)
	manualEquipLayerCount = int32(4)
	manualEquipCopyID     = int64(10007)
)

func manualEquipMapInfo(mapID int32) (tier, layer int32, ok bool) {
	sceneID := mapID / 100
	layer = mapID % 100
	if sceneID < manualEquipFirstScene || sceneID > manualEquipLastScene || layer < 1 || layer > manualEquipLayerCount {
		return 0, 0, false
	}
	return sceneID - manualEquipFirstScene + 1, layer, true
}

func isManualEquipSceneID(sceneID int32) bool {
	return sceneID >= manualEquipFirstScene && sceneID <= manualEquipLastScene
}

func isMainCityMapID(mapID int32) bool {
	return mapID == 10004 || mapID == 1000401
}

func isManualEquipBattle(battle *battleState) bool {
	if battle == nil || battle.copyID != manualEquipCopyID {
		return false
	}
	_, _, ok := manualEquipMapInfo(battle.mapID)
	return ok
}

func manualEquipProgress(ss *session, tier int32) int32 {
	if ss == nil || ss.signin == nil {
		return 0
	}
	switch tier {
	case 1:
		return ss.signin.ManualEquipTier1
	case 2:
		return ss.signin.ManualEquipTier2
	case 3:
		return ss.signin.ManualEquipTier3
	default:
		return 0
	}
}

func setManualEquipProgress(ss *session, tier, layer int32) bool {
	if ss == nil || tier < 1 || tier > 3 || layer < 0 || layer > manualEquipLayerCount {
		return false
	}
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	var target *int32
	switch tier {
	case 1:
		target = &ss.signin.ManualEquipTier1
	case 2:
		target = &ss.signin.ManualEquipTier2
	case 3:
		target = &ss.signin.ManualEquipTier3
	}
	if target == nil || *target == layer {
		return false
	}
	*target = layer
	return true
}

// The original right-side transfer UI always requests layer 1. Unlike trial
// copies, manual-equipment victories are not IsForceNextLayer; the server-side
// record selects the next pending layer both when the player clicks the exit
// portal and when they later re-enter from the city.
func manualEquipEntryResumeMap(ss *session, requestedMap int32) int32 {
	tier, layer, ok := manualEquipMapInfo(requestedMap)
	if !ok || layer != 1 {
		return requestedMap
	}
	completed := manualEquipProgress(ss, tier)
	next := completed + 1
	if next < 1 || next > manualEquipLayerCount {
		next = 1
	}
	sceneID := manualEquipFirstScene + tier - 1
	return sceneID*100 + next
}

// manualEquipPortalTargetMap resolves the exit portal request emitted by a
// manual-equipment scene. The client always sends the configured city target;
// only a recorded victory for the current layer may advance to the next map.
// An unfinished layer (or the fourth layer) remains a normal return-to-city
// request, so players cannot skip a fight by walking into the portal.
func manualEquipPortalTargetMap(ss *session, requestedMap int32) int32 {
	if ss == nil || !isMainCityMapID(requestedMap) {
		return requestedMap
	}
	tier, layer, ok := manualEquipMapInfo(ss.mapID)
	// Progress records the last completed layer. Equality is intentional: after
	// layer four the next cycle starts at layer one while the record is still 4,
	// so a player must win layer one before the portal can advance to layer two.
	if !ok || layer >= manualEquipLayerCount || manualEquipProgress(ss, tier) != layer {
		return requestedMap
	}
	sceneID := manualEquipFirstScene + tier - 1
	return sceneID*100 + layer + 1
}

func recordManualEquipVictory(ss *session, mapID int32) bool {
	tier, layer, ok := manualEquipMapInfo(mapID)
	if !ok {
		return false
	}
	completed := manualEquipProgress(ss, tier)
	// Completing layer four closes one cycle. A later layer-one victory starts
	// the next cycle instead of leaving the record permanently capped at four.
	if completed >= manualEquipLayerCount && layer == 1 {
		return setManualEquipProgress(ss, tier, 1)
	}
	if layer <= completed {
		return false
	}
	return setManualEquipProgress(ss, tier, layer)
}

func (s *Server) manualEquipBattleFailureMessage(ch *channel) string {
	if ch == nil || ch.session == nil {
		return "开始手工副本战斗失败"
	}
	presentation := battlePresentation{kind: presentationManualEquip}
	participants := s.battleParticipantsForPresentation(ch, manualEquipCopyID, presentation)
	minimum := 3
	if tables != nil && tables.copyConfig != nil {
		if row := tables.copyConfig[manualEquipCopyID]; row != nil {
			if configured := int(num(row["MinTeamMember"])); configured > 0 {
				minimum = configured
			}
		}
	}
	if len(participants) < minimum {
		return fmt.Sprintf("手工副本需要%d人组队，且队员必须在同一层", minimum)
	}
	if message := battleEntryHealthFailure(ch, participants); message != "" {
		return message
	}
	return "开始手工副本战斗失败"
}
