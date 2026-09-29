package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// The wire protocol encodes a map as sceneId*100 + layer. Keep the legacy
// logical city id 10004 in player data, but send its valid layer-1 id so the
// client does not decode it as the nonexistent scene 100.
func wireMapID(mapID int32) int32 {
	if mapID == 10004 {
		return 1000401
	}
	return mapID
}

const mapStartupDelay = 750 * time.Millisecond

func (s *Server) scheduleStartupTransPoint(ch *channel, mapID int32, seq uint64) {
	if ch == nil || ch.session == nil {
		return
	}
	time.AfterFunc(mapStartupDelay, func() {
		if !ch.session.isCurrentMapChange(mapID, seq) {
			return
		}
		s.sendPush(ch, protocol.OpM2C_StartupTransPoint, &protocol.M2C_StartupTransPoint{
			MapId: wireMapID(mapID), ActorId: ch.session.playerID,
		})
		log.Printf("[S=%d] push M2C_StartupTransPoint mapId=%d", ch.id, wireMapID(mapID))
	})
}

// 消息分发入口（07 §7 / 08 §4）。
// 所有请求 RpcId（tag=90）原样回填到响应；错误走 Error + Message 字段。
// 登录/网关同址：Address 恒回 127.0.0.1:7756。

const (
	gateAddress = "127.0.0.1:7756"
	gateId      = 1
)

// 错误码（自建服自定义，客户端按 Message 提示）
const (
	errOK            = 0
	errBadParam      = 1    // 参数错误/通用
	errAccountExists = 1001 // 账号已存在
	errAccountNot    = 1002 // 账号不存在
	errPwdError      = 1003 // 密码错误
	errKeyInvalid    = 1004 // 登录凭证无效/过期
	errNotLogged     = 1005 // 未登录/未进入游戏
)

// handleOpcode：按 opcode 反序列化请求并分发，统一发送响应。
func (s *Server) handleOpcode(ch *channel, opcode uint16, body []byte) {
	var respOpcode uint16
	var resp proto.Message
	var err error

	switch opcode {
	case protocol.OpC2R_Login:
		var req protocol.C2R_Login
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpR2C_Login, s.onLogin(ch, &req)
		}
	case protocol.OpC2R_Regist:
		var req protocol.C2R_Regist
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpR2C_Regist, s.onRegist(ch, &req)
		}
	case protocol.OpC2R_CreateRole:
		var req protocol.C2R_CreateRole
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpR2C_CreateRole, s.onCreateRole(ch, &req)
		}
	case protocol.OpC2G_LoginGate:
		var req protocol.C2G_LoginGate
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpG2C_LoginGate, s.onLoginGate(ch, &req)
		}
	case protocol.OpC2G_EnterGame:
		var req protocol.C2G_EnterGame
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpG2C_EnterGame, s.onEnterGame(ch, &req)
		}
	case protocol.OpC2G_HeartBeat:
		var req protocol.C2G_HeartBeat
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpG2C_HeartBeat, s.onHeartBeat(ch, &req)
		}
	case protocol.OpC2G_Ping:
		var req protocol.C2G_Ping
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpG2C_Ping, s.onPing(ch, &req)
		}
	case protocol.OpC2M_GetMainUISetting:
		var req protocol.C2M_GetMainUISetting
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetMainUISetting, s.onGetMainUISetting(ch, &req)
		}
	case protocol.OpC2M_GetStateReback:
		// 无响应消息（fire-and-forget），只记日志，不发响应帧
		var req protocol.C2M_GetStateReback
		if err = proto.Unmarshal(body, &req); err == nil {
			s.onGetStateReback(ch, &req)
		}
		return
	case protocol.OpC2M_StartMainStoryFight:
		var req protocol.C2M_StartMainStoryFight
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartMainStoryFight, s.onStartMainStoryFight(ch, &req)
		}
	case protocol.OpC2M_StartTrialCopyFight:
		var req protocol.C2M_StartTrialCopyFight
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartTrialCopyFight, s.onStartTrialCopyFight(ch, &req)
		}
	case protocol.OpC2M_StartBossFightRequest:
		var req protocol.C2M_StartBossFightRequest
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartBossFightRequest, s.onStartBossFightRequest(ch, &req)
		}
	case protocol.OpC2M_StartBossFight:
		var req protocol.C2M_StartBossFight
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartBossFight, s.onStartBossFight(ch, &req)
		}
	case protocol.OpC2M_UseMainUISkill:
		var req protocol.C2M_UseMainUISkill
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_UseMainUISkill, s.onUseMainUISkill(ch, &req)
		}
	case protocol.OpC2M_UseMainUIGoods:
		var req protocol.C2M_UseMainUIGoods
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_UseMainUIGoods, s.onUseMainUIGoods(ch, &req)
		}
	case protocol.OpC2M_AutoBattle:
		var req protocol.C2M_AutoBattle
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_AutoBattle, s.onAutoBattle(ch, &req)
		}
	case protocol.OpC2M_SelectEnermy:
		var req protocol.C2M_SelectEnermy
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_SelectEnermy, s.onSelectEnermy(ch, &req)
		}
	case protocol.OpC2M_SelectTeamMember:
		var req protocol.C2M_SelectTeamMember
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_SelectTeamMember, s.onSelectTeamMember(ch, &req)
		}
	case protocol.OpC2M_GetTaskState:
		var req protocol.C2M_GetTaskState
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetTaskState, s.onGetTaskState(ch, &req)
		}
	case protocol.OpC2M_GetTask:
		var req protocol.C2M_GetTask
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetTask, s.onGetTask(ch, &req)
		}
	case protocol.OpC2M_CompleteTask:
		var req protocol.C2M_CompleteTask
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_CompleteTask, s.onCompleteTask(ch, &req)
		}
	case protocol.OpC2M_ClickNPC:
		var req protocol.C2M_ClickNPC
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ClickNPC, s.onClickNPC(ch, &req)
		}
	case protocol.OpC2M_AcceptTask:
		var req protocol.C2M_AcceptTask
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_AcceptTask, s.onAcceptTask(ch, &req)
		}
	case protocol.OpC2M_GetSkill:
		var req protocol.C2M_GetSkill
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetSkill, s.onGetSkill(ch, &req)
		}
	case protocol.OpC2M_SaveAutoSkill:
		var req protocol.C2M_SaveAutoSkill
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_SaveAutoSkill, s.onSaveAutoSkill(ch, &req)
		}
	case protocol.OpC2M_LearnSkill:
		var req protocol.C2M_LearnSkill
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_LearnSkill, s.onLearnSkill(ch, &req)
		}
	case protocol.OpC2M_DropSkill:
		var req protocol.C2M_DropSkill
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_DropSkill, s.onDropSkill(ch, &req)
		}
	case protocol.OpC2M_DropItem:
		// 20231 → 20232：拖物品到主界面槽（mainui.go 自行推送 20232 返回 nil）
		var req protocol.C2M_DropItem
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_DropItem, s.onDropItem(ch, &req)
		}
	case protocol.OpC2M_FreeReSetSkill:
		var req protocol.C2M_FreeReSetSkill
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_FreeReSetSkill, s.onFreeReSetSkill(ch, &req)
		}
	case protocol.OpC2M_GetReSetSkillPrice:
		var req protocol.C2M_GetReSetSkillPrice
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetReSetSkillPrice, s.onGetReSetSkillPrice(ch, &req)
		}
	case protocol.OpC2M_ReSetSkill:
		var req protocol.C2M_ReSetSkill
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ReSetSkill, s.onReSetSkill(ch, &req)
		}
	case protocol.OpC2M_RequestEnterMap:
		var req protocol.C2M_RequestEnterMap
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_RequestEnterMap, s.onRequestEnterMap(ch, &req)
		}
	case protocol.OpC2M_AddPoint:
		// 20253 → 20254：属性点分配（Trans：1=Str 2=Quk 3=Spi 4=Wim）
		var req protocol.C2M_AddPoint
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_AddPoint, s.onAddPoint(ch, &req)
		}
	case protocol.OpC2M_ResetPoint:
		// 20255 → 20256：洗点
		var req protocol.C2M_ResetPoint
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ResetPoint, s.onResetPoint(ch, &req)
		}
	case protocol.OpC2M_GetStarSoulBag:
		var req protocol.C2M_GetStarSoulBag
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetStarSoulBag, s.onGetStarSoulBag(ch, &req)
		}
	case protocol.OpFrame_ClickMap:
		// 20022 点击地图寻路：无独立响应帧，处理器自行推送 M2C_PathfindingResult(20024) 后返回 nil
		var req protocol.Frame_ClickMap
		if err = proto.Unmarshal(body, &req); err == nil {
			resp = s.onFrameClickMap(ch, &req)
		}
	case protocol.OpC2M_GetCharacter:
		var req protocol.C2M_GetCharacter
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetCharacter, s.onGetCharacter(ch, &req)
		}
	case protocol.OpC2M_ClickMapUnit:
		// 20391 → 20392：点击地图单位（字段怪），实现在 mapmonster.go
		var req protocol.C2M_ClickMapUnit
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ClickMapUnit, s.onClickMapUnit(ch, &req)
		}
	case protocol.OpC2M_GetBag:
		// 20258 → 20259：查询背包（equip.go 自行推送 20259 + 20260，返回 nil）
		var req protocol.C2M_GetBag
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetBag, s.onGetBag(ch, &req)
		}
	case protocol.OpC2M_DeleteItem:
		// 20263 → 20264：背包拖出丢弃物品（bagtools.go）。
		var req protocol.C2M_DeleteItem
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_DeleteItem, s.onDeleteItem(ch, &req)
		}
	case protocol.OpC2M_ChangeItemPos:
		// 20265 → 20266：交换背包两格（bagtools.go）
		var req protocol.C2M_ChangeItemPos
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ChangeItemPos, s.onChangeItemPos(ch, &req)
		}
	case protocol.OpC2M_ChargeVoucher:
		// 20312 → 20313：元宝兑换代金券（bagtools.go）
		var req protocol.C2M_ChargeVoucher
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ChargeVoucher, s.onChargeVoucher(ch, &req)
		}
	case protocol.OpC2M_ChargeCoin:
		// 20314 → 20315：兑换星币（bagtools.go）
		var req protocol.C2M_ChargeCoin
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ChargeCoin, s.onChargeCoin(ch, &req)
		}
	case protocol.OpC2M_GetMapCoin:
		// 20351 → 20352：点击金币怪领取铜币（bagtools.go）
		var req protocol.C2M_GetMapCoin
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetMapCoin, s.onGetMapCoin(ch, &req)
		}
	case protocol.OpC2M_AddMapCoinCount:
		// 20353 → 20354：花费代金券增加当天金币怪拾取次数。
		var req protocol.C2M_AddMapCoinCount
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_AddMapCoinCount, s.onAddMapCoinCount(ch, &req)
		}
	case protocol.OpC2M_MakeMunalEquip:
		// 20324 → 20325：万能管家手工装备。
		var req protocol.C2M_MakeMunalEquip
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_MakeMunalEquip, s.onMakeMunalEquip(ch, &req)
		}
	case protocol.OpC2M_StrengthEquip:
		// 20340 → 20341：万能管家强化。
		var req protocol.C2M_StrengthEquip
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StrengthEquip, s.onStrengthEquip(ch, &req)
		}
	case protocol.OpC2M_RefreshEquipMainAttribute:
		// 20345 → 20346：万能管家主属性洗练。
		var req protocol.C2M_RefreshEquipMainAttribute
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_RefreshEquipMainAttribute, s.onRefreshEquipMainAttribute(ch, &req)
		}
	case protocol.OpC2M_RefreshEquipAffix:
		// 20347 → 20348：万能管家词缀洗练。
		var req protocol.C2M_RefreshEquipAffix
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_RefreshEquipAffix, s.onRefreshEquipAffix(ch, &req)
		}
	case protocol.OpC2M_MeltEquip:
		// 20212 → 20213：宝石镶嵌（gem.go）
		var req protocol.C2M_MeltEquip
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_MeltEquip, s.onMeltEquip(ch, &req)
		}
	case protocol.OpC2M_DismountGem:
		// 20349 → 20350：宝石拆卸（gem.go）
		var req protocol.C2M_DismountGem
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_DismountGem, s.onDismountGem(ch, &req)
		}
	case protocol.OpC2M_PutOn:
		// 20271 → 20272：穿戴装备（equip.go 自行推送响应）
		var req protocol.C2M_PutOn
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_PutOn, s.onPutOn(ch, &req)
		}
	case protocol.OpC2M_Takeoff:
		// 20273 → 20274：脱下装备
		var req protocol.C2M_Takeoff
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_Takeoff, s.onTakeoff(ch, &req)
		}
	case protocol.OpC2M_UseGoods:
		// 20275 → 20276：使用物品
		var req protocol.C2M_UseGoods
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_UseGoods, s.onUseGoods(ch, &req)
		}
	case protocol.OpC2M_SplitItem:
		// 20267 → 20268：拆分物品（bagtools.go）
		var req protocol.C2M_SplitItem
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_SplitItem, s.onSplitItem(ch, &req)
		}
	case protocol.OpC2M_SortBag:
		// 20269 → 20270：整理背包（bagtools.go）
		var req protocol.C2M_SortBag
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_SortBag, s.onSortBag(ch, &req)
		}
	case protocol.OpC2M_Upgrade:
		// 20277 → 20278：装备升级/进化（bagtools.go）
		var req protocol.C2M_Upgrade
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_Upgrade, s.onUpgradeEquip(ch, &req)
		}
	case protocol.OpC2M_ForgeEquip:
		// 20204 → 20205：锻造/进化装备（bagtools.go）
		var req protocol.C2M_ForgeEquip
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ForgeEquip, s.onForgeEquip(ch, &req)
		}
	// ===================== 商店 / 市场 / 多商店 / 仓库 / 寄售 / 远程商店（shop.go） =====================
	case protocol.OpC2M_GetMarket:
		// 20172 → 20173：元宝市场查询
		var req protocol.C2M_GetMarket
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetMarket, s.onGetMarket(ch, &req)
		}
	case protocol.OpC2M_BuyInMarket:
		// 20176 → 20177：市场购买
		var req protocol.C2M_BuyInMarket
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_BuyInMarket, s.onBuyInMarket(ch, &req)
		}
	case protocol.OpC2M_BuyInShop:
		// 20174 → 20175：普通商店购买
		var req protocol.C2M_BuyInShop
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_BuyInShop, s.onBuyInShop(ch, &req)
		}
	case protocol.OpC2M_SellItem:
		// 20178 → 20179：卖出物品
		var req protocol.C2M_SellItem
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_SellItem, s.onSellItem(ch, &req)
		}
	case protocol.OpC2M_BuyInMultiShop:
		// 20417 → 20418：多商店购买
		var req protocol.C2M_BuyInMultiShop
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_BuyInMultiShop, s.onBuyInMultiShop(ch, &req)
		}
	case protocol.OpC2M_GetStore:
		// 20188 → 20189：仓库查询
		var req protocol.C2M_GetStore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetStore, s.onGetStore(ch, &req)
		}
	case protocol.OpC2M_SortStore:
		// 20190 → 20191：仓库整理
		var req protocol.C2M_SortStore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_SortStore, s.onSortStore(ch, &req)
		}
	case protocol.OpC2M_PutInStore:
		// 20192 → 20193：存入仓库
		var req protocol.C2M_PutInStore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_PutInStore, s.onPutInStore(ch, &req)
		}
	case protocol.OpC2M_TakeOffStore:
		// 20194 → 20195：取出仓库
		var req protocol.C2M_TakeOffStore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TakeOffStore, s.onTakeOffStore(ch, &req)
		}
	case protocol.OpC2M_PutCoinInStore:
		// 20196 → 20197：存钱
		var req protocol.C2M_PutCoinInStore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_PutCoinInStore, s.onPutCoinInStore(ch, &req)
		}
	case protocol.OpC2M_TakeCoinOutStore:
		// 20198 → 20199：取钱
		var req protocol.C2M_TakeCoinOutStore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TakeCoinOutStore, s.onTakeCoinOutStore(ch, &req)
		}
	case protocol.OpC2M_QueryExtandPrice:
		// 20200 → 20201：仓库扩容询价
		var req protocol.C2M_QueryExtandPrice
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_QueryExtandPrice, s.onQueryExtandPrice(ch, &req)
		}
	case protocol.OpC2M_ExtandStore:
		// 20202 → 20203：仓库扩容
		var req protocol.C2M_ExtandStore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ExtandStore, s.onExtandStore(ch, &req)
		}
	case protocol.OpC2M_GetConsignment:
		// 20181 → 20182：寄售行查询
		var req protocol.C2M_GetConsignment
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetConsignment, s.onGetConsignment(ch, &req)
		}
	case protocol.OpC2M_PutInConsignment:
		// 20183 → 20184：寄售上架
		var req protocol.C2M_PutInConsignment
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_PutInConsignment, s.onPutInConsignment(ch, &req)
		}
	case protocol.OpC2M_BuyInConsignment:
		// 20185 → 20186：寄售购买
		var req protocol.C2M_BuyInConsignment
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_BuyInConsignment, s.onBuyInConsignment(ch, &req)
		}
	case protocol.OpC2M_GetOpenRemoteStore:
		// 20294 → 20295：远程商店询价
		var req protocol.C2M_GetOpenRemoteStore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetOpenRemoteStore, s.onGetOpenRemoteStore(ch, &req)
		}
	case protocol.OpC2M_OpenRemoteStore:
		// 20296 → 20297：开启远程商店
		var req protocol.C2M_OpenRemoteStore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_OpenRemoteStore, s.onOpenRemoteStore(ch, &req)
		}
	// ===================== 邮件（mail.go） =====================
	case protocol.OpC2M_GetMail:
		// 20281 → 20282：拉取邮件
		var req protocol.C2M_GetMail
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetMail, s.onGetMail(ch, &req)
		}
	case protocol.OpC2M_ReceiveMail:
		// 20283 → 20284：领取邮件附件
		var req protocol.C2M_ReceiveMail
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ReceiveMail, s.onReceiveMail(ch, &req)
		}
	case protocol.OpC2M_DeleteMail:
		// 20285 → 20286：删除邮件
		var req protocol.C2M_DeleteMail
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_DeleteMail, s.onDeleteMail(ch, &req)
		}
	case protocol.OpC2M_ReceiveAllMail:
		// 20287 → 20288：一键领取
		var req protocol.C2M_ReceiveAllMail
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ReceiveAllMail, s.onReceiveAllMail(ch, &req)
		}
	case protocol.OpC2M_DeleteAllMail:
		// 20289 → 20290：一键删除
		var req protocol.C2M_DeleteAllMail
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_DeleteAllMail, s.onDeleteAllMail(ch, &req)
		}
	// ===================== 聊天（chat.go） =====================
	case protocol.OpC2M_RequestChat:
		// 20298 → 20299：发送聊天（处理器自行广播 20300，返回空响应）
		var req protocol.C2M_RequestChat
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_RequestChat, s.onRequestChat(ch, &req)
		}
	case protocol.OpC2M_GetRanking:
		var req protocol.C2M_GetRanking
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetRanking, s.onGetRanking(ch, &req)
		}
	case protocol.OpC2M_StartAnswerQuest:
		var req protocol.C2M_StartAnswerQuest
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartAnswerQuest, s.onStartAnswerQuest(ch, &req)
		}
	case protocol.OpC2M_NextAnswerQuest:
		var req protocol.C2M_NextAnswerQuest
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_NextAnswerQuest, s.onNextAnswerQuest(ch, &req)
		}
	case protocol.OpC2M_AnswerQuest:
		var req protocol.C2M_AnswerQuest
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_AnswerQuest, s.onAnswerQuest(ch, &req)
		}
	case protocol.OpC2M_GetQuestScordInfo:
		var req protocol.C2M_GetQuestScordInfo
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetQuestScordInfo, s.onGetQuestScordInfo(ch, &req)
		}
	case protocol.OpC2M_RequestPersonalPvp:
		var req protocol.C2M_RequestPersonalPvp
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_RequestPersonalPvp, s.onRequestPersonalPvp(ch, &req)
		}
	case protocol.OpC2M_GetPvpBoardInfo:
		var req protocol.C2M_GetPvpBoardInfo
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetPvpBoardInfo, s.onGetPvpBoardInfo(ch, &req)
		}
	// ===================== 宠物（pet.go） =====================
	case protocol.OpC2M_ShowPet:
		// 20371：展示/隐藏宠物（fire-and-forget，无响应帧）
		var req protocol.C2M_ShowPet
		if err = proto.Unmarshal(body, &req); err == nil {
			s.onShowPet(ch, &req)
		}
		return
	case protocol.OpC2M_GetPetInfo:
		// 20375 → 20376：宠物信息
		var req protocol.C2M_GetPetInfo
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetPetInfo, s.onGetPetInfo(ch, &req)
		}
	case protocol.OpC2M_StartPetPlay:
		var req protocol.C2M_StartPetPlay
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartPetPlay, s.onStartPetPlay(ch, &req)
		}
	case protocol.OpC2M_StartPetExplore:
		var req protocol.C2M_StartPetExplore
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartPetExplore, s.onStartPetExplore(ch, &req)
		}
	case protocol.OpC2M_StartPetExperience:
		var req protocol.C2M_StartPetExperience
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartPetExperience, s.onStartPetExperience(ch, &req)
		}
	case protocol.OpC2M_EndPetAction:
		var req protocol.C2M_EndPetAction
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_EndPetAction, s.onEndPetAction(ch, &req)
		}
	case protocol.OpC2M_GetPetActionReword:
		var req protocol.C2M_GetPetActionReword
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetPetActionReword, s.onGetPetActionReword(ch, &req)
		}
	case protocol.OpC2M_GetPetQuickEndPrice:
		var req protocol.C2M_GetPetQuickEndPrice
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetPetQuickEndPrice, s.onGetPetQuickEndPrice(ch, &req)
		}
	case protocol.OpC2M_UpgradePet:
		var req protocol.C2M_UpgradePet
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_UpgradePet, s.onUpgradePet(ch, &req)
		}
	// ===================== 好友（friend.go） =====================
	case protocol.OpC2M_GetFriend:
		// 20107 → 20108：好友列表
		var req protocol.C2M_GetFriend
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetFriend, s.onGetFriend(ch, &req)
		}
	case protocol.OpC2M_FindFriend:
		// 20109 → 20110：按名字查找玩家
		var req protocol.C2M_FindFriend
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_FindFriend, s.onFindFriend(ch, &req)
		}
	case protocol.OpC2M_AddFriend:
		// 20111 → 20112：添加好友（推 20113 给对方）
		var req protocol.C2M_AddFriend
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_AddFriend, s.onAddFriend(ch, &req)
		}
	case protocol.OpC2M_HandleAddFriend:
		// 20114 → 20115：处理好友申请
		var req protocol.C2M_HandleAddFriend
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_HandleAddFriend, s.onHandleAddFriend(ch, &req)
		}
	case protocol.OpC2M_DeleteFriend:
		// 20117 → 20118：删除好友
		var req protocol.C2M_DeleteFriend
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_DeleteFriend, s.onDeleteFriend(ch, &req)
		}
	// ===================== 组队（team.go） =====================
	case protocol.OpC2M_RequestTeam:
		// 20154 → 20155：申请入队（推 20158）
		var req protocol.C2M_RequestTeam
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_RequestTeam, s.onRequestTeam(ch, &req)
		}
	case protocol.OpC2M_InviteTeam:
		// 20156 → 20157：邀请组队（推 20159）
		var req protocol.C2M_InviteTeam
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_InviteTeam, s.onInviteTeam(ch, &req)
		}
	case protocol.OpC2M_HandleTeam:
		// 20160 → 20161：处理邀请/申请
		var req protocol.C2M_HandleTeam
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_HandleTeam, s.onHandleTeam(ch, &req)
		}
	case protocol.OpC2M_TransferTeamLeader:
		// 20163 → 20164：转让队长
		var req protocol.C2M_TransferTeamLeader
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TransferTeamLeader, s.onTransferTeamLeader(ch, &req)
		}
	case protocol.OpC2M_QuitTeam:
		// 20165 → 20166：退出队伍
		var req protocol.C2M_QuitTeam
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_QuitTeam, s.onQuitTeam(ch, &req)
		}
	case protocol.OpC2M_KickoutTeam:
		// 20167 → 20168：踢出队员
		var req protocol.C2M_KickoutTeam
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_KickoutTeam, s.onKickoutTeam(ch, &req)
		}
	// ===================== 家族（family.go） =====================
	case protocol.OpC2M_CrateFamily:
		// 20123 → 20124：创建家族
		var req protocol.C2M_CrateFamily
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_CrateFamily, s.onCrateFamily(ch, &req)
		}
	case protocol.OpC2M_GetFamily:
		// 20125 → 20126：获取我的家族（raw 20126 手工编码）
		var req protocol.C2M_GetFamily
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetFamily, s.onGetFamily(ch, &req)
		}
	case protocol.OpC2M_FindFamily:
		// 20127 → 20128：查找家族
		var req protocol.C2M_FindFamily
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_FindFamily, s.onFindFamily(ch, &req)
		}
	case protocol.OpC2M_DeleteFamily:
		// 20129 → 20130：解散家族
		var req protocol.C2M_DeleteFamily
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_DeleteFamily, s.onDeleteFamily(ch, &req)
		}
	case protocol.OpC2M_RequestEnterFamily:
		// 20131 → 20132：申请加入家族
		var req protocol.C2M_RequestEnterFamily
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_RequestEnterFamily, s.onRequestEnterFamily(ch, &req)
		}
	case protocol.OpC2M_LeaveFamily:
		// 20133 → 20134：退出家族
		var req protocol.C2M_LeaveFamily
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_LeaveFamily, s.onLeaveFamily(ch, &req)
		}
	case protocol.OpC2M_HandleEnterFamiy:
		// 20135 → 20136：族长处理入族申请
		var req protocol.C2M_HandleEnterFamiy
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_HandleEnterFamiy, s.onHandleEnterFamiy(ch, &req)
		}
	case protocol.OpC2M_DeleteFamilyMember:
		// 20137 → 20138：踢出成员
		var req protocol.C2M_DeleteFamilyMember
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_DeleteFamilyMember, s.onDeleteFamilyMember(ch, &req)
		}
	case protocol.OpC2M_GetFamilyBossInfo:
		// 20139 → 20141：家族 BOSS 信息（raw 20141）
		var req protocol.C2M_GetFamilyBossInfo
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetFamilyBossInfo, s.onGetFamilyBossInfo(ch, &req)
		}
	case protocol.OpC2M_StartFamilyBossFight:
		// 20142 → 20143：开家族 BOSS 战
		var req protocol.C2M_StartFamilyBossFight
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartFamilyBossFight, s.onStartFamilyBossFight(ch, &req)
		}
	case protocol.OpC2M_GetBossDamageMap:
		// 20148 → 20149：BOSS 伤害统计（raw 20149）
		var req protocol.C2M_GetBossDamageMap
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetBossDamageMap, s.onGetBossDamageMap(ch, &req)
		}
	case protocol.OpC2M_GetFamilyBossReward:
		// 20150 → 20152：领取家族 BOSS 奖励（raw 20152）
		var req protocol.C2M_GetFamilyBossReward
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetFamilyBossReward, s.onGetFamilyBossReward(ch, &req)
		}
	// ===================== 传送门 / 星空旅行 =====================
	case protocol.OpC2M_BackMainCity:
		// 20043 → 20044：回主城
		var req protocol.C2M_BackMainCity
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_BackMainCity, s.onBackMainCity(ch, &req)
		}
	case protocol.OpC2M_StartPKFight:
		// 20094 → 20095：挑战/窥探其他玩家（PK 切磋）→ 响应成功（pvp.go）
		var req protocol.C2M_StartPKFight
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartPKFight, s.onStartPKFight(ch, &req)
		}
	case protocol.OpC2M_StartSpaceTravel:
		// 20393 → 20394：开始星空旅行
		var req protocol.C2M_StartSpaceTravel
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartSpaceTravel, s.onStartSpaceTravel(ch, &req)
		}
	case protocol.OpC2M_StartManulEquipBattle:
		var req protocol.C2M_StartManulEquipBattle
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartManulEquipBattle, s.onStartManulEquipBattle(ch, &req)
		}
	// ===================== 人物 / 体力 / 签到 / 传送（misc.go） =====================
	case protocol.OpC2M_GetUserInfo:
		// 20357 → 20358：查询玩家信息
		var req protocol.C2M_GetUserInfo
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetUserInfo, s.onGetUserInfo(ch, &req)
		}
	case protocol.OpC2M_ChangeNickName:
		// 20363 → 20364：修改昵称
		var req protocol.C2M_ChangeNickName
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ChangeNickName, s.onChangeNickName(ch, &req)
		}
	case protocol.OpC2M_GetUIDByName:
		// 20411 → 20412：按名字查 Id
		var req protocol.C2M_GetUIDByName
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetUIDByName, s.onGetUIDByName(ch, &req)
		}
	case protocol.OpC2M_GetOtherUserBag:
		// 20426 → 20427：查看他人背包
		var req protocol.C2M_GetOtherUserBag
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetOtherUserBag, s.onGetOtherUserBag(ch, &req)
		}
	case protocol.OpC2M_GetAddEnergyPrice:
		// 20316 → 20317：体力价格
		var req protocol.C2M_GetAddEnergyPrice
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetAddEnergyPrice, s.onGetAddEnergyPrice(ch, &req)
		}
	case protocol.OpC2M_AddEnergy:
		// 20318 → 20319：购买体力
		var req protocol.C2M_AddEnergy
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_AddEnergy, s.onAddEnergy(ch, &req)
		}
	case protocol.OpC2M_GetAllTrialCopyReword:
		// 20373 → 20374：万能管家领取挑战副本奖励。
		var req protocol.C2M_GetAllTrialCopyReword
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetAllTrialCopyReword, s.onGetAllTrialCopyReward(ch, &req)
		}
	case protocol.OpC2M_TransferRequest:
		// 20331 → 20332：地图传送
		var req protocol.C2M_TransferRequest
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TransferRequest, s.onTransferRequest(ch, &req)
		}
	case protocol.OpC2M_GetSigninReward:
		// 20419 → 20420：今日签到
		var req protocol.C2M_GetSigninReward
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetSigninReward, s.onGetSigninReward(ch, &req)
		}
	case protocol.OpC2M_GetSigninInMonthReward:
		// 20421 → 20422：本月累计签到
		var req protocol.C2M_GetSigninInMonthReward
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetSigninInMonthReward, s.onGetSigninInMonthReward(ch, &req)
		}
	case protocol.OpC2M_GetBasicalInfo:
		// 20029：基础信息（fire-and-forget，无响应消息）
		var req protocol.C2M_GetBasicalInfo
		if err = proto.Unmarshal(body, &req); err == nil {
			log.Printf("[S=%d] get basical info (no-op)", ch.id)
		}
		return
	case protocol.OpC2M_CheckSceneError:
		// 20023：客户端场景错误上报（fire-and-forget，IActorLocationMessage）。
		// 记录日志即可，不响应帧（客户端不 await）。
		var req protocol.C2M_CheckSceneError
		if err = proto.Unmarshal(body, &req); err == nil {
			log.Printf("[S=%d] check scene error info=%q id=%d", ch.id, req.Info, req.Id)
		}
		return
	case protocol.OpC2M_StopMove:
		// 20040：停止移动（fire-and-forget）
		var req protocol.C2M_StopMove
		if err = proto.Unmarshal(body, &req); err == nil {
			ch.session.moving = false
		}
		return
	case protocol.OpC2M_GetBuffTime:
		// 20045：客户端场景就绪后查询持续状态；协议本身无响应类型，
		// 服务端重新推送仍在生效的 20080 状态快照。
		var req protocol.C2M_GetBuffTime
		if err = proto.Unmarshal(body, &req); err == nil {
			s.pushActiveItemBuffs(ch)
			log.Printf("[S=%d] get buff time: active item states pushed", ch.id)
		}
		return
	case protocol.OpC2M_AddExp:
		// 20046：GM/调试加经验（fire-and-forget）
		var req protocol.C2M_AddExp
		if err = proto.Unmarshal(body, &req); err == nil {
			log.Printf("[S=%d] add exp (no-op)", ch.id)
		}
		return
	case protocol.OpC2M_QuitBattle:
		// 20171：退出战斗（fire-and-forget，无响应帧）
		var req protocol.C2M_QuitBattle
		if err = proto.Unmarshal(body, &req); err == nil {
			s.onQuitBattle(ch, &req)
		}
		return
	case protocol.OpC2M_StartMainStoryAI:
		// 20365 → 20366：主线 AI 托管开始（同 20048 主线战斗）
		var req protocol.C2M_StartMainStoryAI
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartMainStoryAI, s.onStartMainStoryAI(ch, &req)
		}
	case protocol.OpC2M_EndMainStoryAI:
		// 20367 → 20368：主线 AI 托管结束
		var req protocol.C2M_EndMainStoryAI
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_EndMainStoryAI, s.onEndMainStoryAI(ch, &req)
		}
	case protocol.OpC2M_StartBattleIdleFight:
		// 20087 → 20088：挂机战斗开始（等价主线战斗，region 由地图推）
		var req protocol.C2M_StartBattleIdleFight
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartBattleIdleFight, s.onStartBattleIdleFight(ch, &req)
		}
	case protocol.OpC2M_EndBattleIdleFight:
		// 20089 → 20090：挂机战斗结束
		var req protocol.C2M_EndBattleIdleFight
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_EndBattleIdleFight, s.onEndBattleIdleFight(ch, &req)
		}
	case protocol.OpC2G_GetNotice:
		// 20041 → 20042：公告查询。自建服无公告系统，返回空 ACK（避免客户端 await 卡死）。
		var req protocol.C2G_GetNotice
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpG2C_GetNotice, &protocol.G2C_GetNotice{RpcId: req.RpcId}
			log.Printf("[S=%d] get notice (empty ack)", ch.id)
		}
	case protocol.OpC2M_SendGMCMD:
		// 20359 → 20360：GM 指令（空 ACK）。
		var req protocol.C2M_SendGMCMD
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_SendGMCMD, &protocol.M2C_SendGMCMD{RpcId: req.RpcId}
			log.Printf("[S=%d] send gm cmd cmd=%q (no-op)", ch.id, req.Cmd)
		}
	case protocol.OpC2M_OpenGMUI:
		// 20361 → 20362：打开 GM 界面（空 ACK）。
		var req protocol.C2M_OpenGMUI
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_OpenGMUI, &protocol.M2C_OpenGMUI{RpcId: req.RpcId}
		}
	case protocol.OpC2M_UpgradeStarSoulItem:
		// 20404 → 20405：星魂升级。
		var req protocol.C2M_UpgradeStarSoulItem
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_UpgradeStarSoulItem, s.onUpgradeStarSoulItem(ch, &req)
		}
	case protocol.OpC2M_PutonStarSoulItem:
		// 20406 → 20407：星魂穿戴。
		var req protocol.C2M_PutonStarSoulItem
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_PutonStarSoulItem, s.onPutonStarSoulItem(ch, &req)
		}
	case protocol.OpC2M_LockStarSoulItem:
		// 20408：星魂锁定（fire-and-forget，无响应帧）。
		var req protocol.C2M_LockStarSoulItem
		if err = proto.Unmarshal(body, &req); err == nil {
			s.onLockStarSoulItem(ch, &req)
		}
		return
	case protocol.OpC2M_ResolveStarSoul:
		// 20413 → 20414：星魂分解。
		var req protocol.C2M_ResolveStarSoul
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ResolveStarSoul, s.onResolveStarSoul(ch, &req)
		}
	case protocol.OpC2M_ResolveAllStarSoul:
		// 20415 → 20416：一键分解未锁定、未穿戴的星魂。
		var req protocol.C2M_ResolveAllStarSoul
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_ResolveAllStarSoul, s.onResolveAllStarSoul(ch, &req)
		}
	case protocol.OpC2M_GetStarSoulItem:
		// 20424 → 20425：消耗星魂碎片兑换指定套装/部位。
		var req protocol.C2M_GetStarSoulItem
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetStarSoulItem, s.onGetStarSoulItem(ch, &req)
		}
	case protocol.OpC2M_TransLevel:
		// 20342 → 20343：转生（trans.go）。
		var req protocol.C2M_TransLevel
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TransLevel, s.onTransLevel(ch, &req)
		}
	case protocol.OpC2M_StartActive:
		// 20409 → 20410：开始活动（客户端"每日活动"点击任何一项都会发，
		// 等响应帧——不接会协程挂起）。自建服无活动数据，空 ACK 成功。
		var req protocol.C2M_StartActive
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartActive, s.onStartActive(ch, &req)
		}
	case protocol.OpC2R_DelRole:
		var req protocol.C2R_DelRole
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpR2C_DelRole, s.onDelRole(ch, &req)
		}
	case protocol.OpC2M_GetBattleStateBuff:
		var req protocol.C2M_GetBattleStateBuff
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GetBattleStateBuff, s.onGetBattleStateBuff(ch, &req)
		}
	case protocol.OpC2M_TransferJob:
		var req protocol.C2M_TransferJob
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TransferJob, s.onTransferJob(ch, &req)
		}
	case protocol.OpC2M_SendQMacro:
		// CheckCheatComponent sends this one-way report and then closes the game.
		var req protocol.C2M_SendQMacro
		if err = proto.Unmarshal(body, &req); err == nil {
			log.Printf("[S=%d] client anti-cheat QMacro report player=%d", ch.id, ch.session.playerID)
		}
		return
	case protocol.OpC2M_StartTestBattleFight:
		var req protocol.C2M_StartTestBattleFight
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_StartTestBattleFight, s.onStartTestBattleFight(ch, &req)
		}
	case protocol.OpC2M_EndTestBattleFight:
		var req protocol.C2M_EndTestBattleFight
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_EndTestBattleFight, s.onEndTestBattleFight(ch, &req)
		}
	case protocol.OpC2M_AddRandomItem:
		// Debug-only fire-and-forget message. Never grant arbitrary items on a
		// production account; return a visible tip without disconnecting.
		var req protocol.C2M_AddRandomItem
		if err = proto.Unmarshal(body, &req); err == nil {
			s.sendPush(ch, protocol.OpM2C_SendTip, &protocol.M2C_SendTip{
				Message: "随机物品调试入口未开放", ActorId: ch.session.playerID,
			})
		}
		return
	case protocol.OpC2M_TestRequest:
		var req protocol.C2M_TestRequest
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TestRequest, s.onTestRequest(&req)
		}
	case protocol.OpC2M_Reload:
		var req protocol.C2M_Reload
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_Reload, s.onReload(&req)
		}
	case protocol.OpC2M_GMDelOtherUserBag:
		var req protocol.C2M_GMDelOtherUserBag
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_GMDelOtherUserBag, s.onGMDelOtherUserBag(&req)
		}
	default:
		log.Printf("[S=%d] opcode %d 未处理", ch.id, opcode)
		return
	}
	if err != nil {
		log.Printf("[S=%d] unmarshal opcode=%d err=%v", ch.id, opcode, err)
		return
	}

	// 部分处理器（如 onEnterGame）自行发送响应 + 后续推送，返回 nil
	if resp == nil {
		return
	}
	out, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("[S=%d] marshal resp opcode=%d err=%v", ch.id, respOpcode, err)
		return
	}
	s.SendToChannel(ch, packOuter(respOpcode, out))
	log.Printf("[S=%d] resp opcode=%d len=%d", ch.id, respOpcode, len(out))
}

// 20008 → 20009：登录。校验账号密码，签发登录 Key。
// LoginType=3（Voucher）：客户端断线重连用 LoginVoucher("account|key") 免密重登——
// Account 字段带账号、Password 字段带 key。
// ⚠ key 在正常登录链里已被 LoginGate(20014) 一次性消耗：客户端缓存的旧 voucher
// 重连时 ConsumeKey 必然失败。因此 Voucher 分支：ConsumeKey 成功用它；失败则
// 回退 FindAccount(账号) 免密（voucher 本身=上次登录成功的授权凭证，私服安全）。
// 响应必须填 LoginVoucher（tag4），否则客户端断线重连时
// LoginHelper.ReLogin 里 loginVoucher 为 null → NRE（LoginHelper.cs:215 刷屏）。
func (s *Server) onLogin(ch *channel, req *protocol.C2R_Login) proto.Message {
	resp := &protocol.R2C_Login{RpcId: req.RpcId}

	var accountID int64
	if req.LoginType == protocol.LoginType_Voucher {
		// 免密重连：Password 字段携带此前签发的 Key。
		if aid, ok := s.store.ConsumeKey(parseVoucherKey(req.Password)); ok {
			accountID = aid
		} else {
			// key 已被消耗（正常登录链 LoginGate 消耗）：回退账号免密。
			aid, _, err := s.store.FindAccount(req.Account)
			if errors.Is(err, sql.ErrNoRows) {
				resp.Error, resp.Message = errAccountNot, "账号不存在"
				return resp
			}
			if err != nil {
				log.Printf("[S=%d] FindAccount(%q) err=%v", ch.id, req.Account, err)
				resp.Error, resp.Message = errBadParam, "服务器内部错误"
				return resp
			}
			log.Printf("[S=%d] voucher key consumed, fallback account %q", ch.id, req.Account)
			accountID = aid
		}
	} else {
		aid, pwd, err := s.store.FindAccount(req.Account)
		if errors.Is(err, sql.ErrNoRows) {
			resp.Error, resp.Message = errAccountNot, "账号不存在"
			return resp
		}
		if err != nil {
			log.Printf("[S=%d] FindAccount(%q) err=%v", ch.id, req.Account, err)
			resp.Error, resp.Message = errBadParam, "服务器内部错误"
			return resp
		}
		if pwd != req.Password {
			resp.Error, resp.Message = errPwdError, "密码错误"
			return resp
		}
		accountID = aid
	}

	// 登录成功：绑定会话 + 签发一次性 Key（LoginGate 时校验）
	key := s.store.IssueKey(accountID)
	ch.session.state = sessLogged
	ch.session.accountID = accountID
	ch.session.account = req.Account

	resp.Address = gateAddress
	resp.Key = key
	resp.GateId = gateId
	resp.LoginVoucher = fmt.Sprintf("%s|%d", req.Account, key) // 断线重连凭证
	log.Printf("[S=%d] login ok account=%q -> Key=%d", ch.id, req.Account, key)
	return resp
}

// parseVoucherKey：解析 LoginVoucher 里的 key（"account|key" 或裸 key）。
func parseVoucherKey(v string) int64 {
	if i := strings.IndexByte(v, '|'); i >= 0 {
		v = v[i+1:]
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n
}

// 20010 → 20011：注册。成功即视为登录，签发 Key。
func (s *Server) onRegist(ch *channel, req *protocol.C2R_Regist) proto.Message {
	resp := &protocol.R2C_Regist{RpcId: req.RpcId}

	if req.Account == "" || req.Password == "" {
		resp.Error, resp.Message = errBadParam, "账号和密码不能为空"
		return resp
	}
	accountID, err := s.store.CreateAccount(req.Account, req.Password)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			resp.Error, resp.Message = errAccountExists, "账号已存在"
		} else {
			log.Printf("[S=%d] CreateAccount(%q) err=%v", ch.id, req.Account, err)
			resp.Error, resp.Message = errBadParam, "注册失败"
		}
		return resp
	}

	key := s.store.IssueKey(accountID)
	ch.session.state = sessLogged
	ch.session.accountID = accountID
	ch.session.account = req.Account

	resp.Address = gateAddress
	resp.Key = key
	resp.GateId = gateId
	resp.LoginVoucher = fmt.Sprintf("%s|%d", req.Account, key)
	log.Printf("[S=%d] regist ok account=%q -> Key=%d", ch.id, req.Account, key)
	return resp
}

// 20016 → 20017：创建角色。需先登录（本连接登录过 或 已通过 LoginGate）。
func (s *Server) onCreateRole(ch *channel, req *protocol.C2R_CreateRole) proto.Message {
	resp := &protocol.R2C_CreateRole{RpcId: req.RpcId}

	if ch.session.accountID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	// 每账号限一个角色
	if p, err := s.store.FirstPlayer(ch.session.accountID); err == nil && p != nil {
		resp.Error, resp.Message = errBadParam, "已有角色"
		return resp
	}
	name := req.Name
	if name == "" {
		name = fmt.Sprintf("玩家%d", ch.session.accountID)
	}
	playerID, err := s.store.CreatePlayer(ch.session.accountID, name, req.JobId, 0)
	if err != nil {
		log.Printf("[S=%d] CreatePlayer err=%v", ch.id, err)
		resp.Error, resp.Message = errBadParam, "创建角色失败"
		return resp
	}
	ch.session.playerID = playerID
	ch.session.jobID = req.JobId
	ch.session.skinID = 0
	if ch.session.skinID <= 0 {
		ch.session.skinID = req.JobId // 皮肤 = 职业（12 文档 §2.3）
	}
	log.Printf("[S=%d] create role ok accountID=%d playerID=%d job=%d name=%q",
		ch.id, ch.session.accountID, playerID, req.JobId, name)
	return resp
}

// 20014 → 20015：登录网关。校验 Key，加载角色信息。
func (s *Server) onLoginGate(ch *channel, req *protocol.C2G_LoginGate) proto.Message {
	resp := &protocol.G2C_LoginGate{
		RpcId:      req.RpcId,
		ServerTime: time.Now().UnixMilli(),
	}

	accountID, ok := s.store.ConsumeKey(req.Key)
	if !ok {
		resp.Error, resp.Message = errKeyInvalid, "登录凭证无效或已过期"
		return resp
	}
	ch.session.state = sessLogged
	ch.session.accountID = accountID

	p, err := s.store.FirstPlayer(accountID)
	if errors.Is(err, sql.ErrNoRows) {
		// 无角色：客户端随后发 C2R_CreateRole
		resp.HasRole = false
		return resp
	}
	if err != nil {
		log.Printf("[S=%d] FirstPlayer(%d) err=%v", ch.id, accountID, err)
		resp.Error, resp.Message = errBadParam, "服务器内部错误"
		return resp
	}

	ch.session.playerID = p.ID
	ch.session.jobID = p.JobID
	ch.session.skinID = p.SkinID
	if ch.session.skinID <= 0 || !isSkinIDValid(ch.session.skinID) {
		ch.session.skinID = p.JobID // 旧角色兜底（12 文档 §2.3）
	}
	resp.PlayerId = p.ID
	resp.HasRole = true
	resp.JobId = p.JobID
	resp.SkinId = ch.session.skinID
	log.Printf("[S=%d] logingate ok accountID=%d playerID=%d", ch.id, accountID, p.ID)
	return resp
}

// 20027 → 20028：进入游戏。标记在线并返回角色信息。
// 注意：本处理器自行发送 G2C_EnterGame 响应 + M2C_ChangeMap 推送，返回 nil 由 handleOpcode 跳过。
// M2C_ChangeMap 触发客户端 ChangeMapEvent → 创建 MyUnit → 发布 TranslateSceneEnd（解除"解密99%"卡条）。
func (s *Server) onEnterGame(ch *channel, req *protocol.C2G_EnterGame) proto.Message {
	resp := &protocol.G2C_EnterGame{RpcId: req.RpcId}

	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	p, err := s.store.FirstPlayer(ch.session.accountID)
	if err != nil {
		log.Printf("[S=%d] FirstPlayer err=%v", ch.id, err)
		resp.Error, resp.Message = errBadParam, "角色不存在"
		return resp
	}

	ch.session.state = sessInGame
	s.store.SetPlayerLastLogin(p.ID)
	s.store.SetPlayerOnline(p.ID, true)

	// 从 DB 加载技能/任务到 session 运行时，新号自动接初始任务链
	if ch.session.loadData(p) {
		// Persist the migration before returning character data so reconnecting
		// cannot recreate a negative trans-stage level.
		s.saveData(ch)
	}
	s.autoAcceptInitialTasks(ch)

	resp.Id = p.ID
	resp.SkinId = ch.session.skinID
	resp.JobId = ch.session.jobID
	resp.IsOnline = true

	// 先回 G2C_EnterGame 响应
	out, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("[S=%d] marshal enter game resp err=%v", ch.id, err)
		return nil
	}
	s.SendToChannel(ch, packOuter(protocol.OpG2C_EnterGame, out))
	log.Printf("[S=%d] resp opcode=%d len=%d", ch.id, protocol.OpG2C_EnterGame, len(out))

	// 推送本人角色信息 M2C_SendUnitInfo：客户端 M2C_SendUnitInfoHandler
	// → ClientUnitCharacterComponent.Update(UnitCharacter) 填充 _clientUnitCharacterDic。
	// 必须早于 M2C_ChangeMap：ChangeMapEvent 的 MyUnit==null 兜底分支会读字典[MyId]
	// 的 SkinId/JobId 创建单位，字典为空时返回 null → NRE → 卡"解密99%"。
	su := &protocol.M2C_SendUnitInfo{
		UnitCharacter: buildUnitCharacter(ch.session),
		ActorId:       p.ID,
	}
	suOut, err := proto.Marshal(su)
	if err != nil {
		log.Printf("[S=%d] marshal M2C_SendUnitInfo err=%v", ch.id, err)
		return nil
	}
	s.SendToChannel(ch, packOuter(protocol.OpM2C_SendUnitInfo, suOut))
	log.Printf("[S=%d] push M2C_SendUnitInfo id=%d name=%q job=%d skin=%d",
		ch.id, su.UnitCharacter.Id, su.UnitCharacter.NickName, su.UnitCharacter.JobId, su.UnitCharacter.SkinId)

	// 推送 M2C_SendCharacter(20257)：线上进游戏时主动推角色信息
	// （客户端 Handler → ClientUnitCharacterComponent.Update + UpdateCharacterUI）。
	sc := &protocol.M2C_SendCharacter{
		UnitCharacter: buildUnitCharacter(ch.session),
		Id:            p.ID,
		ActorId:       p.ID,
	}
	scOut, err := proto.Marshal(sc)
	if err != nil {
		log.Printf("[S=%d] marshal M2C_SendCharacter err=%v", ch.id, err)
		return nil
	}
	s.SendToChannel(ch, packOuter(protocol.OpM2C_SendCharacter, scOut))
	log.Printf("[S=%d] push M2C_SendCharacter id=%d", ch.id, p.ID)

	// 恢复角色上次保存的地图与坐标。新角色的数据库默认值是海滩第 1 层
	// 1000601 / (-1.8,-0.84)。客户端 ChangeMapEvent 成功路径发布
	// TranslateSceneEnd，进度条才会到 100%。
	// ⚠ 每次登录固定回主城（用户要求"重新登录固定回到主城最左边"）：
	// 地图不是主城 → 强制 10004 + 光圈坐标（层 1 左光圈 (-14.5,-1.47)）。
	mapID := ch.session.mapID
	cm := &protocol.M2C_ChangeMap{
		MapId:   wireMapID(mapID),
		X:       ch.session.x,
		Y:       ch.session.y,
		ActorId: p.ID,
	}
	if mapID <= 0 {
		// Legacy rows without a map use the online new-character spawn.
		mapID = 1000601
		cm.MapId = wireMapID(mapID)
		cm.X, cm.Y = -1.8, -0.84
		ch.session.mapID = mapID
	}
	mapSeq := ch.session.markMapChange()
	cmOut, err := proto.Marshal(cm)
	if err != nil {
		log.Printf("[S=%d] marshal M2C_ChangeMap err=%v", ch.id, err)
		return nil
	}
	s.SendToChannel(ch, packOuter(protocol.OpM2C_ChangeMap, cmOut))
	log.Printf("[S=%d] push M2C_ChangeMap mapId=%d pos=(%.2f,%.2f)", ch.id, cm.MapId, cm.X, cm.Y)

	// ⚠ 推送顺序关键：20036 UnitsInMap 必须【紧跟】ChangeMap。
	// 客户端 ChangeMapEvent 本地生成场景 NPC；M2C_UnitsInMapHandler 先
	// UnitComponent.RemoveAll() 清全部单位——若 20036 晚到（在 20039/20169 之后），
	// RemoveAll 会把 ChangeMapEvent 刚建好的 NPC 也清掉且不再重建 → 主城 NPC 消失。
	// （退出战斗走 changeMap()：20036 紧跟 ChangeMap → 先清后建 → NPC 正常。）
	// 同场景玩家同步：先推 20036（含自己 + 同场景其他玩家），再广播给其他人。
	s.pushUnitsInMap(ch)
	s.broadcastUnitsInMapExcept(ch)
	// 字段怪按当前场景（MainStory SceneId/Layer）推；主城/广场等无怪场景不推。
	// 也必须在 20036 之后（RemoveAll 会清已建字段怪）。
	s.pushFieldMonsters(ch)

	// 推送 M2C_StartupTransPoint(20039)：客户端 M2C_StartupTransPointHandler 据此调用
	// UnitySceneComponent.InitScenePoint(zoneScene, MapId/100, MapId%100)，遍历本地 SceneTransConfig
	// 创建场景传送点（光圈）。若不推送，客户端不初始化任何光圈（进入下一层/回主城的光圈都不会出现）。
	s.scheduleStartupTransPoint(ch, mapID, mapSeq)

	// 清除上次会话未完成的移动插值。
	ch.session.resetMovement(cm.X, cm.Y)
	ch.session.x, ch.session.y = cm.X, cm.Y // 登录固定回主城光圈（保存时同步坐标）

	// 推送 M2C_SyncUnitAttribute(20169) 基础数值属性：客户端 NumericComponent.Set 据此
	// 初始化血蓝/六维（血蓝显示 0 的根因 = 服务器从未推过属性）。数值 = 线上职业成长
	// （RoleGrowth）+ 加点 + 装备（growth.go，1 级 MaxHp=93/MaxMp=31/攻 6/防 1）。
	s.pushPlayerAttrs(ch)
	// 推送货币（铜币 1028 / 元宝 1024 / 兑换券 1025），主界面货币栏显示。
	s.pushMoney(ch)
	// 推送宠物状态（M2C_SyncPet）：客户端 PetUI 打开时直接读 MyUnit 上的 Pet 组件。
	s.pushSyncPet(ch)
	// 推送活动（签到）面板状态（M2C_SendActiveInfo）。
	s.pushActiveInfo(ch)
	log.Printf("[S=%d] push numeric attrs level=%d charPoint=%d", ch.id, p.Level, ch.session.charPoint)

	log.Printf("[S=%d] enter game ok playerID=%d", ch.id, p.ID)
	return nil
}

// 20012 → 20013：心跳。
func (s *Server) onHeartBeat(ch *channel, req *protocol.C2G_HeartBeat) proto.Message {
	resp := &protocol.G2C_HeartBeat{RpcId: req.RpcId}
	resp.DelayTime = 0
	resp.Key = 0
	return resp
}

// 20333 → 20334：网关 Ping。
func (s *Server) onPing(ch *channel, req *protocol.C2G_Ping) proto.Message {
	resp := &protocol.G2C_Ping{RpcId: req.RpcId}
	resp.Time = time.Now().UnixMilli()
	resp.Token = 0
	log.Printf("[S=%d] ping ok", ch.id)
	return resp
}

// 20223 → 20224：任务状态查询。实现在 task.go。
// 20239 → 20240：技能查询。实现在 skill.go。

// 20022：点击地图寻路。客户端点击地面发此消息，服务器推送 M2C_PathfindingResult(20024)，
// 客户端 handler 用 (X,Y)+(TX,TY) 构造路径 → MoveComponent.MoveToAsync(list, MoveSpeed) 走位。
// 注意：这是推送式消息（无独立响应帧），处理器自行 sendPush 后返回 nil。
// 主城移动速度常量（Unity 场景坐标下每秒移动距离）。
const moveSpeed = 5.0

func (s *Server) onFrameClickMap(ch *channel, req *protocol.Frame_ClickMap) proto.Message {
	pid := ch.session.playerID
	if pid == 0 || req.UnitInfo == nil {
		return nil
	}
	// 连续点击时客户端可能尚未到达上一个目标。按经过时间插值真实起点，
	// 否则客户端会先瞬移到上一个目标，再从那里开始新路径。
	tx, ty := req.UnitInfo.X, req.UnitInfo.Y
	sx, sy := ch.session.beginMovement(time.Now(), tx, ty, moveSpeed)
	pf := &protocol.M2C_PathfindingResult{
		Id:        pid,
		X:         sx,
		Y:         sy,
		TX:        tx,
		TY:        ty,
		MoveSpeed: moveSpeed,
		ActorId:   pid,
	}
	s.sendPush(ch, protocol.OpM2C_PathfindingResult, pf)
	// 同场景其他玩家同步移动（"显示玩家"）。
	s.broadcastMove(ch, pf)
	log.Printf("[S=%d] frame click map id=%d (%.2f,%.2f)->(%.2f,%.2f)",
		ch.id, pid, sx, sy, tx, ty)
	return nil
}

// 20251 → 20252：角色信息查询（返回 UnitCharacter + WornBagMapList）。
// 实现在 equip.go（onGetCharacter），客户端 CharacterUI 打开时请求。

// changeMap：推送换图（M2C_ChangeMap + M2C_StartupTransPoint），更新会话地图/坐标。
// 客户端 ChangeMap 处理重建场景单位与 NPC（按 mapId 的 SceneId/Layer 过滤），
// StartupTransPoint 重新初始化传送点光圈。先清理旧场景字段怪，再按新场景推字段怪。
func (s *Server) changeMap(ch *channel, mapID int32, x, y float32) {
	pid := ch.session.playerID
	// 离开旧场景：同场景其他玩家推 20037 清除其单位。
	s.broadcastLeaveMap(ch)
	s.clearFieldMonsters(ch)
	mapSeq := ch.session.markMapChange()
	s.sendPush(ch, protocol.OpM2C_ChangeMap, &protocol.M2C_ChangeMap{
		MapId: wireMapID(mapID), X: x, Y: y, ActorId: pid,
	})
	ch.session.mapID = mapID
	ch.session.resetMovement(x, y)
	s.saveData(ch)
	// 新场景玩家同步（含自己）→ 广播给同场景其他人 → 再推字段怪（20036 的
	// RemoveAll 会清已建字段怪，故字段怪必须最后推）。
	s.pushUnitsInMap(ch)
	s.broadcastUnitsInMapExcept(ch)
	s.pushFieldMonsters(ch)
	s.scheduleStartupTransPoint(ch, mapID, mapSeq)
	log.Printf("[S=%d] change map -> mapId=%d pos=(%.2f,%.2f)", ch.id, mapID, x, y)
}

// 20031 → 20032：请求进入地图（点传送点光圈）。
// 客户端点光圈发 C2M_RequestEnterMap{MapId=目标} → 服务器换图。
// 支持：海滩层间切换（1000601~1000610，出生点 (-1.8,-0.84)）与回主城（10004）。
func validMapTransition(current, target int32) bool {
	currentCity := current == 10004 || current == 1000401
	targetCity := target == 10004 || target == 1000401
	if targetCity {
		return true // 任意场景可回主城
	}
	if target == 1000611 {
		return current/100 == 10006 && current%100 == 10
	}
	if target >= 1000601 && target <= 1000610 {
		if current/100 == 10006 {
			return true // 海滩层间任意跳转
		}
		return currentCity || target == 1000601
	}
	// 通用：目标场景必须在 SceneTransConfig 中（主城传送门选图/奇妙广场星空旅行等）
	return knownScene(target / 100)
}

func (s *Server) onRequestEnterMap(ch *channel, req *protocol.C2M_RequestEnterMap) proto.Message {
	resp := &protocol.M2C_RequestEnterMap{RpcId: req.RpcId}
	if ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	log.Printf("[S=%d] request enter map %d", ch.id, req.MapId)
	if !validMapTransition(ch.session.mapID, req.MapId) {
		log.Printf("[S=%d] ignore out-of-order map request current=%d target=%d", ch.id, ch.session.mapID, req.MapId)
		return resp
	}
	if ch.session.mapID/100 == 10006 && req.MapId/100 == 10006 {
		currentLayer := ch.session.mapID % 100
		targetLayer := req.MapId % 100
		if targetLayer != currentLayer && targetLayer != currentLayer+1 && ch.session.mapChangedWithin(time.Second) {
			log.Printf("[S=%d] ignore rapid stale map request current=%d target=%d", ch.id, ch.session.mapID, req.MapId)
			return resp
		}
	}
	if (req.MapId == ch.session.mapID) ||
		((req.MapId == 1000401 || req.MapId == 10004) && (ch.session.mapID == 1000401 || ch.session.mapID == 10004)) {
		return resp
	}
	switch {
	case req.MapId == 10004 || req.MapId == 1000401:
		// 回主城：站主城光圈（层 1 左光圈、其余右光圈，客户端 OnEnter 按层决定）。
		x, y := mainCityReturnSpawn()
		s.changeMapForTeamLeader(ch, 10004, x, y)
	case req.MapId >= 1000601 && req.MapId <= 1000610:
		s.changeMapForTeamLeader(ch, req.MapId, -1.8, -0.84) // 海滩第 1~10 层
	case req.MapId == 1000611:
		// 客户端在第 10 层出口仍按“当前层 + 1”请求；该边界回主城。
		x, y := mainCityReturnSpawn()
		s.changeMapForTeamLeader(ch, 10004, x, y)
	default:
		// 传送门选图 / 星空旅行场景：按 SceneTransConfig 出生点进入。
		x, y := sceneSpawn(req.MapId / 100)
		s.changeMapForTeamLeader(ch, req.MapId, x, y)
	}
	return resp
}

// 20030：状态回退请求（无响应消息）。进入地图前客户端恢复本端状态用，记录日志即可。
func (s *Server) onGetStateReback(ch *channel, req *protocol.C2M_GetStateReback) {
	pid := ch.session.playerID
	if pid == 0 {
		return
	}

	// OperaComponent.ClickTarget reads TeamComponent before any target raycast.
	// Restore the real same-scene party here; the old unconditional self-only
	// packet silently overwrote a multiplayer team after every map load.
	s.pushTeamMember(ch)
	log.Printf("[S=%d] get state reback actorId=%d; restore team for player=%d", ch.id, req.ActorId, pid)
}
