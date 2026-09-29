package main

import "log"

// These keys live in player_kill_counts so the beach story can be persisted
// without overloading task state. They are outside every online MonsterBase
// id range but remain valid signed MySQL INT values.
const (
	beachHighestLayerKey = int32(2147400001)
	beachReturnRewardKey = int32(2147400002)
	beachReturnLevel     = int32(131)
	// Main-story completion keys are kept in the existing persisted kill-count
	// relation.  The range is outside all online MonsterBase ids and leaves
	// the beach compatibility key untouched.
	mainStoryCompletionKeyBase = int32(2147000000)
	// One key per non-beach scene stores the current layer of that chapter's
	// repeatable run. The value survives a voluntary return to town.
	mainStoryResumeKeyBase = int32(2146000000)
)

func mainStoryCompletionKey(region int32) int32 {
	if region <= 0 || region > 1000000 {
		return 0
	}
	return mainStoryCompletionKeyBase + region
}

func mainStoryResumeKey(sceneID int32) int32 {
	if sceneID <= 0 || sceneID > 1000000 {
		return 0
	}
	return mainStoryResumeKeyBase + sceneID
}

func mainStoryCompleted(ss *session, region int32) bool {
	if ss == nil || region <= 0 {
		return false
	}
	if ss.mapID/100 == 10006 && region == 1000+ss.mapID%100 {
		if beachHighestCompletedLayer(ss) >= ss.mapID%100 {
			return true
		}
	}
	key := mainStoryCompletionKey(region)
	return key != 0 && ss.killCount != nil && ss.killCount[key] > 0
}

func mainStoryRegionForMapID(mapID int32) (int32, bool) {
	region, _, _, _, ok := mainStoryRosterForMap(mapID)
	return region, ok
}

func markMainStoryVictory(ss *session, region int32) bool {
	if ss == nil || region <= 0 {
		return false
	}
	key := mainStoryCompletionKey(region)
	if key == 0 {
		return false
	}
	if ss.killCount == nil {
		ss.killCount = make(map[int32]int32)
	}
	if ss.killCount[key] > 0 {
		return false
	}
	ss.killCount[key] = 1
	return true
}

func beachHighestCompletedLayer(ss *session) int32 {
	if ss == nil || ss.killCount == nil {
		return 0
	}
	return ss.killCount[beachHighestLayerKey]
}

func markBeachLayerVictory(ss *session, region int32) bool {
	if ss == nil || ss.mapID/100 != 10006 {
		return false
	}
	layer := ss.mapID % 100
	if layer < 1 || layer > 10 || region != 1000+layer {
		return false
	}
	if ss.killCount == nil {
		ss.killCount = make(map[int32]int32)
	}
	if ss.killCount[beachHighestLayerKey] >= layer {
		return false
	}
	ss.killCount[beachHighestLayerKey] = layer
	return true
}

func experienceNeededForLevel(ss *session, target int32) int64 {
	if ss == nil || target <= ss.level {
		return 0
	}
	needed := -ss.exp
	for level := ss.level; level < target; level++ {
		needed += expNeed(level)
	}
	if needed < 0 {
		return 0
	}
	return needed
}

// grantBeachReturnProgress reproduces the tutorial transition: accept task
// 10016 from NPC 1015 on beach layer 4, then Ctrl+G back to town at level 131.
// MainStoryExp[10016] is the online milestone reward; the level floor also
// covers the original server's rounding/off-by-one behavior at this boundary.
func (s *Server) grantBeachReturnProgress(ch *channel) bool {
	if ch == nil || ch.session == nil || ch.session.mapID != 1000604 {
		return false
	}
	ss := ch.session
	if ss.tasks[10016] != taskStateRunning {
		return false
	}
	if ss.killCount == nil {
		ss.killCount = make(map[int32]int32)
	}
	if ss.killCount[beachReturnRewardKey] != 0 {
		return false
	}
	if ss.level >= beachReturnLevel {
		// Compatibility for characters created before this milestone was tracked.
		ss.killCount[beachReturnRewardKey] = 1
		return false
	}

	reward := int64(0)
	if tables != nil && tables.mainStoryExp != nil {
		if row := tables.mainStoryExp[10016]; row != nil {
			reward = num(row["Exp"])
		}
	}
	if floor := experienceNeededForLevel(ss, beachReturnLevel); reward < floor {
		reward = floor
	}
	if reward <= 0 {
		return false
	}
	ss.killCount[beachReturnRewardKey] = 1
	s.gainExp(ch, reward)
	log.Printf("[S=%d] beach return milestone exp=%d level=%d", ch.id, reward, ss.level)
	return true
}
