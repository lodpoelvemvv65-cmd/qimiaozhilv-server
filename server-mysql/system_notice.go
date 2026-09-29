package main

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

const systemNoticeSender = "系统"

// sendSystemNotice stores each line in the client's native System channel.
// Center broadcasts additionally use MainUI.m_txtGlobal; routine rewards must
// leave that disabled so repeated battle gains do not cover the play field.
func (s *Server) sendSystemNotice(ch *channel, lines []string, centerBroadcast bool) bool {
	if ch == nil || ch.session == nil {
		return false
	}
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			filtered = append(filtered, line)
		}
	}
	if len(filtered) == 0 {
		return false
	}
	message := &protocol.M2C_SendSystemChat{
		Type:            protocol.ChatType_System,
		Name:            systemNoticeSender,
		IsSystemBrocast: centerBroadcast,
		ActorId:         ch.session.playerID,
	}
	body, err := proto.Marshal(message)
	if err != nil {
		return false
	}
	for _, line := range filtered {
		body = pbAppendBytes(body, 1, []byte(line))
	}
	s.sendRawPush(ch, protocol.OpM2C_SendSystemChat, body)
	return true
}

func rewardItemName(item *protocol.RewardItem) string {
	if item == nil {
		return "物品"
	}
	var table map[int64]map[string]interface{}
	if tables != nil {
		switch item.ItemType {
		case protocol.ItemType_EquipItem:
			table = tables.equipBase
		case protocol.ItemType_GoodsItem:
			table = tables.goodsBase
		case protocol.ItemType_MaterialsItem:
			table = tables.materialBase
		}
		if row := table[int64(item.Id)]; row != nil {
			if name, ok := row["Name"].(string); ok && name != "" {
				return name
			}
		}
	}
	return fmt.Sprintf("物品%d", item.Id)
}

func battleRewardSystemLines(reward *protocol.M2C_SendReward) []string {
	if reward == nil {
		return nil
	}
	lines := make([]string, 0, len(reward.ItemList)+2)
	if reward.Exp > 0 {
		lines = append(lines, fmt.Sprintf("获得经验：%d", reward.Exp))
	}
	if reward.Coin > 0 {
		lines = append(lines, fmt.Sprintf("获得铜币：%d", reward.Coin))
	}
	for _, item := range reward.ItemList {
		if item == nil || item.Count <= 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("获得%s x%d", rewardItemName(item), item.Count))
	}
	return lines
}

// sendReward keeps the native reward popup and System-channel history in the
// same server operation. M2C_SendReward itself never writes chat messages.
func (s *Server) sendReward(ch *channel, reward *protocol.M2C_SendReward) {
	if ch == nil || reward == nil {
		return
	}
	s.sendPush(ch, protocol.OpM2C_SendReward, reward)
	s.sendSystemNotice(ch, battleRewardSystemLines(reward), false)
}
