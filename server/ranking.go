package main

import (
	"sort"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

type rankingEntry struct {
	id    int64
	info  *protocol.RankingInfo
	value float32
}

func rankingJobName(jobID int32) string {
	switch jobID {
	case 1:
		return "军官"
	case 2:
		return "运动员"
	case 3:
		return "护士"
	case 4:
		return "超人"
	default:
		return "未知"
	}
}

func rankingExtraValue(ss *session, numericType int32) float32 {
	if ss == nil {
		return 0
	}
	return equipBonus(ss)[numericType] + float32(starSoulBonus(ss)[numericType]) + ss.transBonus[numericType]
}

func rankingValue(ss *session, index int32) (float32, bool) {
	if ss == nil {
		return 0, false
	}
	switch index {
	case 0:
		return float32(ss.level), true
	case 1:
		return float32(ss.coin), true
	case 2:
		return float32(ss.playerMaxHp()), true
	case 3:
		return float32(ss.playerMaxMp()), true
	case 4:
		if ss.pet == nil {
			return 0, true
		}
		return float32(ss.pet.Level), true
	case 5:
		return rankingExtraValue(ss, 1017), true
	case 6:
		return float32(ss.playerPhyAtk()), true
	case 7:
		return float32(ss.playerSpiAtk()), true
	case 8:
		return rankingExtraValue(ss, 1013), true
	case 9:
		return rankingExtraValue(ss, 1014), true
	case 10:
		return rankingExtraValue(ss, 1015), true
	case 11:
		return rankingExtraValue(ss, 1016), true
	case 12:
		return rankingExtraValue(ss, 1022), true
	case 13:
		return rankingExtraValue(ss, 1023), true
	case 14:
		return float32(ss.playerPhyDef()), true
	case 15:
		return float32(ss.playerSpiDef()), true
	default:
		return 0, false
	}
}

func (s *Server) onGetRanking(ch *channel, req *protocol.C2M_GetRanking) proto.Message {
	resp := &protocol.M2C_GetRanking{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	if _, ok := rankingValue(ch.session, req.Index); !ok {
		resp.Error, resp.Message = errBadParam, "排行榜类型无效"
		return resp
	}
	players, err := s.store.ListPlayers(0)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "排行榜读取失败"
		return resp
	}
	entries := make([]rankingEntry, 0, len(players))
	for _, player := range players {
		if player == nil {
			continue
		}
		ss := newSession()
		ss.playerID = player.ID
		ss.jobID = player.JobID
		ss.skinID = player.SkinID
		ss.loadData(player)
		value, ok := rankingValue(ss, req.Index)
		if !ok {
			continue
		}
		entries = append(entries, rankingEntry{
			id:    player.ID,
			value: value,
			info: &protocol.RankingInfo{
				Name:  player.Name,
				Job:   rankingJobName(player.JobID),
				Value: value,
			},
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].value == entries[j].value {
			return entries[i].id < entries[j].id
		}
		return entries[i].value > entries[j].value
	})
	if len(entries) > 100 {
		entries = entries[:100]
	}
	resp.RankingInfoList = make([]*protocol.RankingInfo, 0, len(entries))
	for _, entry := range entries {
		resp.RankingInfoList = append(resp.RankingInfoList, entry.info)
	}
	return resp
}
