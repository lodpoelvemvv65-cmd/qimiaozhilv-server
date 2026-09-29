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

// 消息分发入口（07 §7 / 08 §4）。
// 所有请求 RpcId（tag=90）原样回填到响应；错误走 Error + Message 字段。
// 登录/网关同址：Address 由 -advertise/MHQ_PUBLIC_ADDR 配置。

var gateAddress = "127.0.0.1:7756"

const gateId = 1

// 错误码（自建服自定义，客户端按 Message 提示）
const (
	errOK                  = 0
	errBadParam            = 1      // 参数错误/通用
	errPwdError            = 200001 // 客户端业务错误区间（>200000），响应 Message 会显示为提示
	errKeyInvalid          = 200002 // 登录凭证无效/过期，同样必须进入客户端 Message 分支
	errRegAccountRequired  = 200003 // Sys_Code: 请输入账号
	errRegPasswordRequired = 200004 // Sys_Code: 请输入密码
	errRegAccountFormat    = 200005 // Sys_Code: 账号格式不正确
	errRegPasswordFormat   = 200006 // Sys_Code: 密码格式不正确
	errAccountExists       = 200008 // Sys_Code: 注册失败，账号已存在
	errNotLogged           = 1005   // 未登录/未进入游戏
)

// handleOpcode：按 opcode 反序列化请求并分发，统一发送响应。
func (s *Server) handleOpcode(ch *channel, opcode uint16, body []byte) {
	if ch != nil && ch.session != nil && ch.session.superseded.Load() &&
		opcode != protocol.OpC2G_HeartBeat && opcode != protocol.OpC2G_Ping {
		log.Printf("[S=%d] ignore opcode=%d from superseded session", ch.id, opcode)
		return
	}
	if opcode == launcherTeamRequestOpcode {
		s.handleLauncherTeamPlan(ch, body)
		return
	}
	configStateMu.RLock()
	defer configStateMu.RUnlock()
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
			respOpcode = protocol.OpM2C_RequestEnterMap
			var transition func()
			resp, transition = s.onRequestEnterMap(ch, &req)
			if transition != nil {
				// The client is still enumerating its old transfer points while it
				// awaits this RPC. Complete the call before ChangeMap mutates them.
				if s.sendResponse(ch, respOpcode, resp) {
					transition()
				}
				return
			}
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
			s.onStopMove(ch, &req)
		}
		return
	case protocol.OpC2M_GetBuffTime:
		// 20045：客户端右键点击左上等级区域。先重建 20080 状态图标，
		// 再通过 20262 打开原版 TipUI 展示权威剩余状态。
		var req protocol.C2M_GetBuffTime
		if err = proto.Unmarshal(body, &req); err == nil {
			s.pushActiveItemBuffs(ch)
			s.pushBuffStatusTip(ch)
			log.Printf("[S=%d] get buff time: active item states and status tip pushed", ch.id)
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
	case protocol.OpC2M_RequestTrade:
		var req protocol.C2M_RequestTrade
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_RequestTrade, s.onRequestTrade(ch, &req)
		}
	case protocol.OpC2M_HandleTrade:
		var req protocol.C2M_HandleTrade
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_HandleTrade, s.onHandleTrade(ch, &req)
		}
	case protocol.OpC2M_TradeOffer:
		var req protocol.C2M_TradeOffer
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TradeOffer, s.onTradeOffer(ch, &req)
		}
	case protocol.OpC2M_TradeLock:
		var req protocol.C2M_TradeLock
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TradeLock, s.onTradeLock(ch, &req)
		}
	case protocol.OpC2M_TradeConfirm:
		var req protocol.C2M_TradeConfirm
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TradeConfirm, s.onTradeConfirm(ch, &req)
		}
	case protocol.OpC2M_TradeCancel:
		var req protocol.C2M_TradeCancel
		if err = proto.Unmarshal(body, &req); err == nil {
			respOpcode, resp = protocol.OpM2C_TradeCancel, s.onTradeCancel(ch, &req)
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
	if !s.sendResponse(ch, respOpcode, resp) {
		return
	}
	if opcode == protocol.OpC2M_StartFamilyBossFight {
		if start, ok := resp.(*protocol.M2C_StartFamilyBossFight); ok && start.Error == 0 && start.Message == "" {
			s.pushStartedFamilyBossPresentation(ch)
		}
	}
}

func (s *Server) sendResponse(ch *channel, opcode uint16, resp proto.Message) bool {
	out, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("[S=%d] marshal resp opcode=%d err=%v", ch.id, opcode, err)
		return false
	}
	s.SendToChannel(ch, packOuter(opcode, out))
	log.Printf("[S=%d] resp opcode=%d len=%d", ch.id, opcode, len(out))
	return true
}

// 20008 → 20009：登录。校验账号密码，分别签发 LoginGate Key 和重连 Voucher Key。
// LoginType=3（Voucher）：客户端断线重连用 LoginVoucher("account|key") 免密重登，
// Account 字段带账号、Password 字段带 voucher key。LoginGate 只消耗独立的 gate key，
// 因此 voucher key 仍可用于一次安全重连；无效或过期凭证绝不按账号名免密放行。
// 响应必须填 LoginVoucher（tag4），否则客户端断线重连时
// LoginHelper.ReLogin 里 loginVoucher 为 null → NRE（LoginHelper.cs:215 刷屏）。
func (s *Server) onLogin(ch *channel, req *protocol.C2R_Login) proto.Message {
	resp := &protocol.R2C_Login{RpcId: req.RpcId}

	var accountID int64
	account := req.Account
	if req.LoginType == protocol.LoginType_Voucher {
		voucherAccount, voucherKey := parseLoginVoucher(req.Password)
		if voucherAccount != "" {
			if account != "" && account != voucherAccount {
				resp.Error, resp.Message = errPwdError, "账号或密码错误"
				return resp
			}
			account = voucherAccount
		}
		if account == "" {
			resp.Error, resp.Message = errPwdError, "账号或密码错误"
			return resp
		}
		expectedID, password, err := s.store.FindAccount(account)
		if errors.Is(err, sql.ErrNoRows) {
			resp.Error, resp.Message = errPwdError, "账号或密码错误"
			return resp
		}
		if err != nil {
			log.Printf("[S=%d] validate voucher account %q: %v", ch.id, account, err)
			resp.Error, resp.Message = errBadParam, "服务器内部错误"
			return resp
		}
		// The original UI keeps LoginType=Voucher after loading a cached token,
		// even when the user replaces the password field. A strict password
		// comparison restores normal login without reviving the old account-only
		// fallback that allowed passwordless access.
		if password == req.Password {
			accountID = expectedID
		} else {
			aid, ok := s.store.ConsumeVoucher(voucherKey)
			if voucherKey <= 0 || !ok || expectedID != aid {
				resp.Error, resp.Message = errPwdError, "账号或密码错误"
				return resp
			}
			accountID = aid
		}
	} else {
		if account == "" || req.Password == "" {
			resp.Error, resp.Message = errPwdError, "账号或密码错误"
			return resp
		}
		aid, pwd, err := s.store.FindAccount(account)
		if errors.Is(err, sql.ErrNoRows) {
			resp.Error, resp.Message = errPwdError, "账号或密码错误"
			return resp
		}
		if err != nil {
			log.Printf("[S=%d] FindAccount(%q) err=%v", ch.id, account, err)
			resp.Error, resp.Message = errBadParam, "服务器内部错误"
			return resp
		}
		if pwd != req.Password {
			resp.Error, resp.Message = errPwdError, "账号或密码错误"
			return resp
		}
		accountID = aid
	}

	// 两个凭证用途隔离：LoginGate 会消耗 key，断线重连只消耗 voucherKey。
	key, voucherKey := s.store.IssueKey(accountID), s.store.IssueVoucher(accountID)
	if key <= 0 || voucherKey <= 0 {
		resp.Error, resp.Message = errBadParam, "服务器内部错误"
		return resp
	}
	ch.session.state = sessLogged
	ch.session.accountID = accountID
	ch.session.account = account

	resp.Address = gateAddress
	resp.Key = key
	resp.GateId = gateId
	resp.LoginVoucher = fmt.Sprintf("%s|%d", account, voucherKey)
	log.Printf("[S=%d] login ok account=%q -> Key=%d", ch.id, account, key)
	return resp
}

// parseLoginVoucher 解析客户端 ReLogin 传回的 "account|key"，兼容旧的裸 key。
func parseLoginVoucher(v string) (string, int64) {
	account := ""
	if i := strings.LastIndexByte(v, '|'); i >= 0 {
		account, v = v[:i], v[i+1:]
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return account, n
}

func isASCIIAlphaNumeric(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

// validateRegistration matches the requirements printed by the original
// registration panel: accounts contain only letters/digits, and passwords
// contain 6-16 letters/digits.
func validateRegistration(account, password string) (int32, string) {
	if account == "" {
		return errRegAccountRequired, "请输入账号"
	}
	if password == "" {
		return errRegPasswordRequired, "请输入密码"
	}
	if !isASCIIAlphaNumeric(account) || len(account) > 191 {
		return errRegAccountFormat, "账号格式不正确"
	}
	if len(password) < 6 || len(password) > 16 || !isASCIIAlphaNumeric(password) {
		return errRegPasswordFormat, "密码格式不正确"
	}
	return errOK, ""
}

// 20010 → 20011：注册。成功即视为登录，签发 Key。
func (s *Server) onRegist(ch *channel, req *protocol.C2R_Regist) proto.Message {
	resp := &protocol.R2C_Regist{RpcId: req.RpcId}

	if code, message := validateRegistration(req.Account, req.Password); code != errOK {
		resp.Error, resp.Message = code, message
		return resp
	}
	accountID, err := s.store.CreateAccount(req.Account, req.Password)
	if err != nil {
		if isDuplicateKeyError(err) {
			resp.Error, resp.Message = errAccountExists, "注册失败，账号已存在"
		} else {
			log.Printf("[S=%d] CreateAccount(%q) err=%v", ch.id, req.Account, err)
			resp.Error, resp.Message = errBadParam, "注册失败"
		}
		return resp
	}

	key, voucherKey := s.store.IssueKey(accountID), s.store.IssueVoucher(accountID)
	if key <= 0 || voucherKey <= 0 {
		resp.Error, resp.Message = errBadParam, "服务器内部错误"
		return resp
	}
	ch.session.state = sessLogged
	ch.session.accountID = accountID
	ch.session.account = req.Account

	resp.Address = gateAddress
	resp.Key = key
	resp.GateId = gateId
	resp.LoginVoucher = fmt.Sprintf("%s|%d", req.Account, voucherKey)
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
	} else if normalized, message := normalizePlayerName(name); message != "" {
		resp.Error, resp.Message = errBadParam, message
		return resp
	} else {
		name = normalized
	}
	reservedPlayerID := ch.session.reservedPlayerID
	if reservedPlayerID == 0 {
		var err error
		reservedPlayerID, err = s.store.ReservePlayerID(ch.session.accountID)
		if err != nil {
			log.Printf("[S=%d] ReservePlayerID err=%v", ch.id, err)
			resp.Error, resp.Message = errBadParam, "创建角色失败"
			return resp
		}
		ch.session.reservedPlayerID = reservedPlayerID
	}
	// The stock client always echoes PlayerId. Older protocol probes omitted
	// the proto3 field; they are safe because LoginGate has already installed
	// the same reserved id in this session. Any explicit mismatch is rejected.
	if req.PlayerId != 0 && req.PlayerId != reservedPlayerID {
		resp.Error, resp.Message = errBadParam, "角色标识无效，请重新登录"
		return resp
	}
	playerID := reservedPlayerID
	err := s.store.CreatePlayerWithID(ch.session.accountID, playerID, name, req.JobId, 0)
	if err != nil {
		if errors.Is(err, ErrPlayerNameExists) {
			resp.Error, resp.Message = errBadParam, "昵称已存在"
			return resp
		}
		if errors.Is(err, ErrPlayerAlreadyExists) {
			resp.Error, resp.Message = errBadParam, "已有角色"
			return resp
		}
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
	ch.session.newRolePending = true
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
		// 原版在角色尚未创建时就返回非零 PlayerId。客户端会把它保存为
		// GlobalVariable.MyId，并在 C2R_CreateRole 中原样带回。
		playerID, reserveErr := s.store.ReservePlayerID(accountID)
		if reserveErr != nil {
			log.Printf("[S=%d] ReservePlayerID(%d) err=%v", ch.id, accountID, reserveErr)
			resp.Error, resp.Message = errBadParam, "服务器内部错误"
			return resp
		}
		ch.session.playerID = playerID
		ch.session.reservedPlayerID = playerID
		resp.PlayerId = playerID
		resp.HasRole = false
		log.Printf("[S=%d] logingate reserve accountID=%d playerID=%d", ch.id, accountID, playerID)
		return resp
	}
	if err != nil {
		log.Printf("[S=%d] FirstPlayer(%d) err=%v", ch.id, accountID, err)
		resp.Error, resp.Message = errBadParam, "服务器内部错误"
		return resp
	}

	ch.session.playerID = p.ID
	ch.session.reservedPlayerID = p.ID
	ch.session.jobID = p.JobID
	ch.session.skinID = p.SkinID
	if ch.session.skinID <= 0 || !isSkinIDValid(ch.session.skinID) {
		ch.session.skinID = p.JobID // 旧角色兜底（12 文档 §2.3）
	}
	s.replacePlayerSession(ch, p.ID)
	resp.PlayerId = p.ID
	resp.HasRole = true
	resp.JobId = p.JobID
	resp.SkinId = ch.session.skinID
	log.Printf("[S=%d] logingate ok accountID=%d playerID=%d", ch.id, accountID, p.ID)
	return resp
}

// 20012 → 20013：心跳。
func (s *Server) onHeartBeat(ch *channel, req *protocol.C2G_HeartBeat) proto.Message {
	resp := &protocol.G2C_HeartBeat{RpcId: req.RpcId}
	if ch != nil && ch.session != nil && !ch.session.superseded.Load() {
		s.store.RefreshPlayerOnline(ch.session.playerID)
		if checkpointOnlineReward(ch, time.Now()) > 0 {
			s.saveData(ch)
		}
	}
	resp.DelayTime = 0
	resp.Key = 0
	return resp
}

// 20333 → 20334：网关 Ping。
func (s *Server) onPing(ch *channel, req *protocol.C2G_Ping) proto.Message {
	resp := &protocol.G2C_Ping{RpcId: req.RpcId}
	if ch != nil && ch.session != nil && !ch.session.superseded.Load() {
		s.store.RefreshPlayerOnline(ch.session.playerID)
		if checkpointOnlineReward(ch, time.Now()) > 0 {
			s.saveData(ch)
		}
	}
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
// 点击移动速度（Unity 场景坐标/秒）。客户端完全采用服务端下发值；
// 原版线上服的 M2C_PathfindingResult 抓包值为 3.5。
const moveSpeed = 3.5

func (s *Server) onFrameClickMap(ch *channel, req *protocol.Frame_ClickMap) proto.Message {
	pid := ch.session.playerID
	if pid == 0 || req.UnitInfo == nil {
		return nil
	}
	// ChangeMap is asynchronous on the client. Ground clicks queued by the old
	// scene can arrive after the server has installed the new spawn and would
	// otherwise drag the player away before NPCs and portals finish rebuilding.
	if ch.session.mapChangedWithin(mapStartupDelay) {
		return nil
	}
	if s.teamFollowerIsControlled(ch) {
		return nil
	}
	// 连续点击时客户端可能尚未到达上一个目标。按经过时间插值真实起点，
	// 否则客户端会先瞬移到上一个目标，再从那里开始新路径。
	tx, ty := req.UnitInfo.X, req.UnitInfo.Y
	now := time.Now()
	sx, sy := ch.session.beginMovement(now, tx, ty, moveSpeed)
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
	s.moveTeamFollowers(ch, pf, now)
	log.Printf("[S=%d] frame click map id=%d (%.2f,%.2f)->(%.2f,%.2f)",
		ch.id, pid, sx, sy, tx, ty)
	return nil
}

func (s *Server) onStopMove(ch *channel, _ *protocol.C2M_StopMove) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	ch.session.stopMovement(time.Now(), moveSpeed)
}
