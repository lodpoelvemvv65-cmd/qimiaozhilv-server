package main

import "sort"

const (
	// The client exposes ordinary and hard main-story chapters as two separate
	// lists.  The ordinary list starts at SceneId 10011; beach (10006) is a
	// tutorial/transfer scene and is not the prerequisite for that list's first
	// entry.  Hard chapters start at SceneId 10052 and remain gated by the last
	// ordinary chapter.
	mainStoryNormalEntryScene int32 = 10011
	mainStoryHardEntryScene   int32 = 10052
)

// mainStoryStage is the server-side progression identity of one configured
// MainStory layer. The map id is authoritative for ordering; region is only
// the persistence key used by the existing battle state.
type mainStoryStage struct {
	mapID   int32
	sceneID int32
	layer   int32
	region  int32
}

func mainStoryStageForMap(mapID int32) (mainStoryStage, int, bool) {
	if tables == nil || tables.mainStory == nil {
		return mainStoryStage{}, 0, false
	}
	stages := allMainStoryStages()
	for index, stage := range stages {
		if stage.mapID == mapID {
			return stage, index, true
		}
	}
	return mainStoryStage{}, 0, false
}

func allMainStoryStages() []mainStoryStage {
	if tables == nil || tables.mainStory == nil {
		return nil
	}
	stages := make([]mainStoryStage, 0, len(tables.mainStory))
	for region, row := range tables.mainStory {
		if row == nil {
			continue
		}
		sceneID := int32(num(row["SceneId"]))
		layer := int32(num(row["Layer"]))
		if sceneID <= 0 || layer <= 0 {
			continue
		}
		stages = append(stages, mainStoryStage{
			mapID: sceneID*100 + layer, sceneID: sceneID, layer: layer,
			region: int32(region),
		})
	}
	sort.Slice(stages, func(i, j int) bool {
		if stages[i].sceneID != stages[j].sceneID {
			return stages[i].sceneID < stages[j].sceneID
		}
		if stages[i].layer != stages[j].layer {
			return stages[i].layer < stages[j].layer
		}
		return stages[i].region < stages[j].region
	})
	return stages
}

func isMainStoryScene(sceneID int32) bool {
	if tables == nil || tables.mainStory == nil {
		return false
	}
	for _, row := range tables.mainStory {
		if row != nil && int32(num(row["SceneId"])) == sceneID {
			return true
		}
	}
	return false
}

func isMainStoryCityMap(mapID int32) bool {
	return mapID == 10004 || mapID == 1000401
}

func isMainStoryNormalEntry(stage mainStoryStage) bool {
	return stage.sceneID == mainStoryNormalEntryScene && stage.layer == 1
}

func isMainStoryHardEntry(stage mainStoryStage) bool {
	return stage.sceneID == mainStoryHardEntryScene && stage.layer == 1
}

// isMainStoryFinalStageMap identifies the last configured layer of a
// non-beach chapter.  MainStory chapters are not guaranteed to be contiguous
// in their region keys, so the next stage's scene (rather than region+1) is
// the authoritative end-of-chapter check.
func isMainStoryFinalStageMap(mapID int32) bool {
	stage, index, ok := mainStoryStageForMap(mapID)
	if !ok || stage.sceneID == 10006 {
		return false
	}
	stages := allMainStoryStages()
	return index+1 >= len(stages) || stages[index+1].sceneID != stage.sceneID
}

// nextMainStoryStageMap returns only the next layer inside the current
// chapter. Chapter completion is handled separately (the player returns to
// town); automatic running must never jump directly into the next chapter.
func nextMainStoryStageMap(mapID int32) (mainStoryStage, bool) {
	current, index, ok := mainStoryStageForMap(mapID)
	if !ok {
		return mainStoryStage{}, false
	}
	stages := allMainStoryStages()
	if index+1 >= len(stages) || stages[index+1].sceneID != current.sceneID {
		return mainStoryStage{}, false
	}
	return stages[index+1], true
}

func setMainStoryResumeMap(ss *session, mapID int32) bool {
	stage, _, ok := mainStoryStageForMap(mapID)
	if ss == nil || !ok || stage.sceneID == 10006 {
		return false
	}
	key := mainStoryResumeKey(stage.sceneID)
	if key == 0 {
		return false
	}
	if ss.killCount == nil {
		ss.killCount = make(map[int32]int32)
	}
	if ss.killCount[key] == stage.layer {
		return false
	}
	ss.killCount[key] = stage.layer
	return true
}

func resetMainStoryResumeAfterFinalVictory(ss *session, mapID int32) bool {
	stage, _, ok := mainStoryStageForMap(mapID)
	if ss == nil || !ok || stage.sceneID == 10006 || !isMainStoryFinalStageMap(mapID) {
		return false
	}
	first, found := firstMainStoryStageForScene(stage.sceneID)
	return found && setMainStoryResumeMap(ss, first.mapID)
}

// mainStoryEntryResumeMap translates the chapter button's fixed layer-one
// request into the saved layer of the current run. It applies only to a city
// entry, so automatic and portal progression keep their exact 1..10 order.
func mainStoryEntryResumeMap(ss *session, requestedMap int32) int32 {
	requested, requestedIndex, ok := mainStoryStageForMap(requestedMap)
	if ss == nil || !ok || requested.sceneID == 10006 || requested.layer != 1 || !isMainStoryCityMap(ss.mapID) {
		return requestedMap
	}
	key := mainStoryResumeKey(requested.sceneID)
	if key != 0 && ss.killCount != nil {
		layer := ss.killCount[key]
		if layer > 0 {
			candidate := requested.sceneID*100 + layer
			if stage, _, found := mainStoryStageForMap(candidate); found && stage.sceneID == requested.sceneID {
				return candidate
			}
		}
	}

	// Compatibility for characters saved before resume markers existed.
	// Completed predecessors identify the first still-pending layer.
	stages := allMainStoryStages()
	lastIndex := requestedIndex
	for lastIndex+1 < len(stages) && stages[lastIndex+1].sceneID == requested.sceneID {
		lastIndex++
	}
	if mainStoryStageCompleted(ss, stages[lastIndex]) {
		return requestedMap
	}
	resume := requestedMap
	for index := requestedIndex + 1; index < len(stages) && stages[index].sceneID == requested.sceneID; index++ {
		if !mainStoryStageCompleted(ss, stages[index-1]) {
			break
		}
		resume = stages[index].mapID
	}
	return resume
}

// mainStoryStageCompleted also understands the persisted beach layer marker.
// This keeps characters created before the generic completion key was added
// from being locked out of the first non-beach chapter.
func mainStoryStageCompleted(ss *session, stage mainStoryStage) bool {
	if ss == nil {
		return false
	}
	if stage.sceneID == 10006 && beachHighestCompletedLayer(ss) >= stage.layer {
		return true
	}
	return mainStoryCompleted(ss, stage.region)
}

// nonBeachMainStoryTransitionMessage returns a user-facing failure for a
// non-beach MainStory target. An empty string means the transition is valid or
// the target is not a non-beach MainStory map.
func nonBeachMainStoryTransitionMessage(ss *session, targetMap int32) string {
	target, targetIndex, targetOK := mainStoryStageForMap(targetMap)
	if !targetOK || target.sceneID == 10006 {
		return ""
	}
	current, currentIndex, currentOK := mainStoryStageForMap(ss.mapID)
	if currentOK {
		if targetIndex == currentIndex {
			return ""
		}
		if targetIndex != currentIndex+1 {
			return "无法进入该层"
		}
		if !mainStoryStageCompleted(ss, current) {
			return "请先完成本层战斗"
		}
		return ""
	}

	// The first ordinary chapter is a separate entry from the beach tutorial.
	// RightTransPointUI sends 1001101 directly from the main city, so requiring
	// the beach's final layer here makes a fresh character unable to enter the
	// very first "初心勃岛" chapter.  Once inside, layer progression is still
	// enforced by the current-map branch above.
	if isMainStoryNormalEntry(target) && isMainStoryCityMap(ss.mapID) {
		return ""
	}

	// A character entering from town (or another non-main-story scene) may
	// enter any already-unlocked layer, but the immediately preceding online
	// layer must have been completed first. This covers both a new chapter's
	// first layer and a resumed layer after returning to town.
	if targetIndex == 0 {
		return ""
	}
	stages := allMainStoryStages()
	if targetIndex <= 0 || targetIndex > len(stages)-1 {
		return "无法进入该层"
	}
	if !mainStoryStageCompleted(ss, stages[targetIndex-1]) {
		return "请先完成上一层战斗"
	}
	return ""
}

// mainStoryTeamEntryFailure applies the same progression check to every
// online, non-battling member that changeMapForTeamLeader would move. A party
// must not be able to bypass a chapter lock merely because its leader has
// already cleared that chapter.
func (s *Server) mainStoryTeamEntryFailure(ch *channel, targetMap int32) string {
	target, _, ok := mainStoryStageForMap(targetMap)
	if !ok || target.sceneID == 10006 || ch == nil || ch.session == nil {
		return ""
	}
	teamMu.Lock()
	_, team := s.teamEntryOf(ch.session.playerID)
	if team == nil || team.LeaderId != ch.session.playerID {
		teamMu.Unlock()
		return ""
	}
	memberIDs := append([]int64(nil), team.Members...)
	teamMu.Unlock()

	for _, memberID := range memberIDs {
		member := s.findChannelByPlayerID(memberID)
		if member == nil || member.session == nil {
			continue
		}
		member.session.battleMu.Lock()
		inBattle := member.session.battle != nil
		member.session.battleMu.Unlock()
		if inBattle {
			// changeMapForTeamLeader deliberately leaves active battles in place.
			continue
		}
		if message := nonBeachMainStoryTransitionMessage(member.session, targetMap); message != "" {
			return "队伍中有成员尚未解锁该主线"
		}
	}
	return ""
}
