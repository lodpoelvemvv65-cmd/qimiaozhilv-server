package main

// gm_control_family.go：GM 家族归属重置（player.family_reset）。
//
// 为什么需要这个命令：players.family_id 是家族归属的唯一权威，但服务端从不校验
// 它指向的 families 行是否还在。运维直接执行 SQL、或测试脚本 DELETE FROM families
// 绕过 onDeleteFamily 之后，角色就被自己的脏数据锁死了：
//
//	20125 开面板  -> 「家族不存在」
//	20123 建家族  -> 「已有家族」
//	20131 申请入族 -> 「已有家族」
//
// family_selfheal.go 的 healDanglingFamily 能让这种情况在玩家点这几个按钮时自愈，
// 但那只覆盖「角色在线并且点了按钮」。角色离线、或家族还在而角色被错误地关在里面
// （例如族长号被删、家族只剩一个挂着不动的角色），自愈都碰不到，需要 GM 主动干预。
//
// 两种模式：
//
//	mode=detach   （默认）把角色从家族摘除。他是族长且还有别人时，族长让给剩余
//	              成员里 ID 最小的那个；摘完没人了，家族整个解散。
//	mode=dissolve 解散角色所在的那个家族，全体成员清归属。
//
// 命令是幂等的：角色本来就没有家族时返回 completed + familyId=0，不报错，这样
// 带 Idempotency-Key 的重试不会因为「第一次已经修好了」而变成失败。

import (
	"log"
	"strings"

	json "github.com/goccy/go-json"
)

func init() {
	registerGMAction("player.family_reset", func(s *Server, playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
		return s.gmResetFamily(playerID, raw)
	})
}

const (
	gmFamilyResetDetach   = "detach"
	gmFamilyResetDissolve = "dissolve"
)

type gmFamilyResetPayload struct {
	Mode string `json:"mode"`
}

// gmResetFamily 是 player.family_reset 的实现。
//
// 会话里的 familyID 和库里的 players.family_id 都可能比对方新，两种方向都出现过：
//
//	会话旧于库：库被直接清过，在线会话还留着旧 ID，下次 saveData 会把它写回来
//	会话新于库：正常路径下的家族操作，库里已提交而会话还没刷新
//
// 所以这里不走「只信库」或「只信会话」的任何一边，而是两边都收尾：先按并集做库上
// 的动作，最后无条件把在线会话的归属清成 0。会话收尾一定不能省——只做库的话，
// 一次 saveData 就能把刚修好的引用写回去。
func (s *Server) gmResetFamily(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	if playerID <= 0 {
		return "rejected", nil, "invalid_target", "角色 ID 无效"
	}
	mode := gmFamilyResetDetach
	if len(raw) > 0 {
		var payload gmFamilyResetPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return "rejected", nil, "invalid_payload", "家族重置参数错误"
		}
		if trimmed := strings.ToLower(strings.TrimSpace(payload.Mode)); trimmed != "" {
			mode = trimmed
		}
	}
	if mode != gmFamilyResetDetach && mode != gmFamilyResetDissolve {
		return "rejected", nil, "invalid_payload", "模式只能是 detach 或 dissolve"
	}

	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	sessionFamilyID := ch.session.familyID

	// dangling 表示「库里已经没有归属，只有会话还记着一个家族 ID」。这一路没删任何
	// families 行，所以 dissolved 是 false；单列出来，运营才不会以为命令没生效。
	dangling := false
	var result *familyDetachResult
	if mode == gmFamilyResetDissolve {
		familyID := sessionFamilyID
		if familyID == 0 {
			familyID, _ = s.playerFamilyIDFromDB(playerID)
		}
		if familyID == 0 {
			// 没有家族可解散。仍然走后面的统一收尾：会话可能还留着旧 ID，得清掉。
			result = &familyDetachResult{}
		} else {
			result, err = s.dissolveFamily(familyID)
		}
	} else {
		result, err = s.detachPlayerFromFamily(playerID)
		if err == nil && result.FamilyID == 0 && sessionFamilyID != 0 {
			// 库里已经没有归属了，只剩会话是旧的。把那个家族 ID 的遗留数据一并收拾掉，
			// 否则孤儿 family_boss_* 行会一直留着（不是外键，级联删不掉）。
			result.FamilyID = sessionFamilyID
			result.RemovedMembers = []int64{playerID}
			dangling = true
			s.purgeOrphanFamilyData(sessionFamilyID)
		}
	}
	if err != nil {
		log.Printf("[GM] family reset player=%d mode=%s: %v", playerID, mode, err)
		return "rejected", nil, "family_reset_failed", "家族重置失败"
	}
	s.syncGMFamilyReset(playerID, result)
	// familyId 为 0 表示这次什么都没改（库里和会话里都没有归属）。仍然是 completed
	// ——幂等重试不该因为「第一次就修好了」而失败——但要说清楚，否则运营会以为
	// 命令静默失效了。
	message := ""
	if result.FamilyID == 0 {
		message = "该角色当前没有家族"
	}
	log.Printf("[GM] family reset player=%d mode=%s family=%d dissolved=%v dangling=%v promoted=%d removed=%v",
		playerID, mode, result.FamilyID, result.Dissolved, dangling, result.PromotedLeader, result.RemovedMembers)
	return "completed", gmFamilyResetData(result, dangling, message), "", ""
}

// syncGMFamilyReset 把库上的结果同步给在线会话。
//
// dissolveFamily 已经同步过它自己那份成员名单，这里只补目标角色一个：目标可能不在
// 那份名单里（会话与库不一致时就是这种情况），但它的会话无论如何都得清成 0。
// syncFamilyDetachToOnline 内部按在线频道查，离线角色自然跳过。
func (s *Server) syncGMFamilyReset(playerID int64, result *familyDetachResult) {
	ids := []int64{}
	if result != nil {
		ids = append(ids, result.RemovedMembers...)
	}
	found := false
	for _, id := range ids {
		if id == playerID {
			found = true
			break
		}
	}
	if !found {
		ids = append(ids, playerID)
	}
	familyID := int64(0)
	if result != nil {
		familyID = result.FamilyID
	}
	s.syncFamilyDetachToOnline(&familyDetachResult{FamilyID: familyID, RemovedMembers: ids})
}

// gmFamilyResetData 组装回给 GM 后台的审计数据。
//
// dangling 为 true 时 families 行早就不在了，dissolved 会是 false（这次没删任何家族行）。
// 后端把它单独回显，以免运营把「本来就悬空」读成「命令没生效」。
func gmFamilyResetData(result *familyDetachResult, dangling bool, message string) map[string]any {
	data := map[string]any{
		"familyId":       int64(0),
		"familyName":     "",
		"removedMembers": []int64{},
		"dissolved":      false,
		"dangling":       dangling,
	}
	if result != nil {
		removed := result.RemovedMembers
		if removed == nil {
			removed = []int64{}
		}
		data["familyId"] = result.FamilyID
		data["familyName"] = result.FamilyName
		data["removedMembers"] = removed
		data["dissolved"] = result.Dissolved
		if result.PromotedLeader != 0 {
			data["promotedLeader"] = result.PromotedLeader
		}
	}
	if message != "" {
		data["message"] = message
	}
	return data
}
