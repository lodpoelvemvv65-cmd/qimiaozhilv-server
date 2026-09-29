package main

import (
	"log"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// mainui.go：主界面快捷栏（20227-20236 段）—— 与客户端完全一致。
//
// 客户端链路（HotfixView 反汇编，rid 27/144/1127/1339/1341）：
//   - 快捷栏固定 9 格（MainUISlotComponent.ctor newarr 9；FGUI m_mainUISlotList 9 子项）。
//   - InitMainUISlotAndSetting：发 C2M_GetMainUISetting(20227) → 20228 MainUISlotList
//     → UpdateMainUISlotEvent 按 Info.Index 取子槽渲染（SkillSlot=技能图标+等级；
//     ItemSlot=GoodsBase 图标+数量；NoneSlot=清空）。
//   - 【技能进快捷栏】= 玩家把已学技能拖到槽（技能面板 onDragStart →
//     DragDropManager.StartDrag(UIDragArgs{Index=skillId, Type=3}) → 快捷栏 onDrop）
//     → 发 C2M_DropSkill(20229){SlotId, SkillId} → 20230 响应带 MainUISlotList(tag1)
//     全量刷新。→ 学技能【不会】自动进快捷栏，必须玩家拖。
//   - 【物品进快捷栏】= 拖背包物品到槽（UIDragArgs{Index=bagIndex, Type=1}）
//     → C2M_DropItem(20231){SlotId, BagIndex} → 20232 带 MainUISlotList。
//   - 点击技能槽 → C2M_UseMainUISkill(20233){SlotId} → 20234（Message 空=成功）。
//   - 点击物品槽 → C2M_UseMainUIGoods(20235){SlotId} → 20236 带 MainUISlotList+BagMapList。
//
// ⚠ 客户端健壮性：
//   - UpdateMainUISlotEvent.Run 直接用 Index 做 GetChildAt(index)，越界即
//     `Invalid child index` 崩溃卡 Loading（Player.log 实证）→ 服务器恒发 9 槽 Index 0..8。
//   - Session.Call 对 Error!=0 响应直接抛 Rpc error 崩溃（MainUI.cs:332 实证）
//     → 业务失败 Error=0 + Message（客户端弹提示，不崩）。
//   - ItemSlot 的 Id 必须存在 GoodsBase（否则客户端 Log.Error + 中断整列渲染）。

// mainUISlot 一个快捷栏槽。
type mainUISlot struct {
	Type int32 `json:"t"` // 0=None 1=Skill 2=Item
	Id   int32 `json:"i"`
}

// mainUISlotCount 客户端快捷栏固定格数（MainUISlotComponent.ctor newarr 9）。
const mainUISlotCount = 9

// mainUISlotList 生成 20228/20230/20232 的 MainUISlotList（恒 9 项，Index 0..8）。
// 技能槽 Level = 已学等级（未学=0）；物品槽 ItemCount = 背包持有量；
// 空槽 = NoneSlot(Id=0)（客户端清空图标/数量）。
// pruneEmptyMainUIItemSlots clears item shortcuts that have no bag stock
// so the client receives NoneSlot instead of an icon with count 0.
func (ss *session) pruneEmptyMainUIItemSlots() bool {
	if ss == nil {
		return false
	}
	changed := false
	for i, sl := range ss.mainUISlots {
		if sl.Type != 2 {
			continue
		}
		if sl.Id > 0 && ss.bagCountOf(sl.Id) > 0 {
			continue
		}
		if ss.mainUISlots[i] != (mainUISlot{}) {
			ss.mainUISlots[i] = mainUISlot{}
			changed = true
		}
	}
	return changed
}

func (ss *session) mainUISlotList() []*protocol.MainUISlotInfo {
	ss.pruneEmptyMainUIItemSlots()
	out := make([]*protocol.MainUISlotInfo, 0, mainUISlotCount)
	for i := 0; i < mainUISlotCount; i++ {
		sl := ss.mainUISlots[i]
		switch sl.Type {
		case 1: // 技能槽
			out = append(out, &protocol.MainUISlotInfo{
				Id:         sl.Id,
				MainUIType: protocol.MainUIType_SkillSlot,
				Level:      ss.skills[sl.Id],
				Index:      int32(i),
			})
		case 2: // 物品槽
			if sl.Id <= 0 {
				out = append(out, &protocol.MainUISlotInfo{
					Id:         0,
					MainUIType: protocol.MainUIType_NoneSlot,
					Index:      int32(i),
				})
				continue
			}
			out = append(out, &protocol.MainUISlotInfo{
				Id:         sl.Id,
				MainUIType: protocol.MainUIType_ItemSlot,
				ItemCount:  ss.bagCountOf(sl.Id),
				Index:      int32(i),
			})
		default: // 空槽
			out = append(out, &protocol.MainUISlotInfo{
				Id:         0,
				MainUIType: protocol.MainUIType_NoneSlot,
				Index:      int32(i),
			})
		}
	}
	return out
}

// bagCountOf 返回背包中该物品 id 的总数量（跨格求和）。
func (ss *session) bagCountOf(itemID int32) int32 {
	if ss == nil {
		return 0
	}
	var total int32
	for _, it := range ss.bag {
		if it != nil && it.ItemId == itemID {
			total += it.Count
		}
	}
	return total
}

func (ss *session) firstBagIndexOf(itemID int32) int32 {
	if ss == nil {
		return invalidBagSlot
	}
	for index, item := range ss.bag {
		if item != nil && item.ItemId == itemID && item.Count > 0 {
			return index
		}
	}
	return invalidBagSlot
}

// ===================== 处理器 =====================

// 20227 → 20228：主 UI 设置查询。返回全量快捷栏（9 槽）+ 自动技能开关。
func (s *Server) onGetMainUISetting(ch *channel, req *protocol.C2M_GetMainUISetting) proto.Message {
	ss := ch.session
	ss.battleMu.Lock()
	pruned := ss.pruneEmptyMainUIItemSlots()
	resp := &protocol.M2C_GetMainUISetting{
		RpcId:           req.RpcId,
		MainUISlotList:  ss.mainUISlotList(),
		IsAutoSkill:     ss.autoBattleEnabled,
		IsDisplayOthers: false,
	}
	ss.battleMu.Unlock()
	if pruned {
		s.saveData(ch)
	}
	log.Printf("[S=%d] get main ui setting ok slots=%d", ch.id, len(resp.MainUISlotList))
	return resp
}

// 20229 → 20230：把技能拖到快捷栏槽（SlotId = 目标槽，SkillId = 技能）。
// 线上：玩家从技能面板拖【已学】技能到槽 → 设置槽；SkillId=0 清空槽。
// 响应 raw 20230 带 MainUISlotList(tag1) 全量刷新。
func (s *Server) onDropSkill(ch *channel, req *protocol.C2M_DropSkill) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	ss := ch.session
	resp := &protocol.M2C_DropSkill{RpcId: req.RpcId}
	reject := func(msg string) proto.Message {
		log.Printf("[S=%d] drop skill rejected: %s", ch.id, msg)
		resp.Message = msg
		return resp
	}
	slot := req.SlotId
	if slot < 0 || int(slot) >= mainUISlotCount {
		return reject("槽位无效")
	}
	sid := req.SkillId
	if sid == 0 {
		// 清空槽
		ss.mainUISlots[slot] = mainUISlot{}
	} else {
		if !isJobSkill(ss.jobID, sid) {
			return reject("不能放置该技能")
		}
		if _, ok := ss.skills[sid]; !ok {
			return reject("技能未学习")
		}
		ss.mainUISlots[slot] = mainUISlot{Type: 1, Id: sid}
	}
	s.saveData(ch)
	base, err := proto.Marshal(&protocol.M2C_DropSkill{RpcId: req.RpcId})
	if err != nil {
		return reject("序列化失败")
	}
	base = appendMainUISlotList(base, 1, ss.mainUISlotList())
	s.sendRawPush(ch, protocol.OpM2C_DropSkill, base)
	log.Printf("[S=%d] drop skill slot=%d skill=%d", ch.id, slot, sid)
	return nil
}

// 20231 → 20232：把背包物品拖到快捷栏槽（SlotId = 目标槽，BagIndex = 背包格）。
// 响应 raw 20232 带 MainUISlotList(tag1) 全量刷新。
func (s *Server) onDropItem(ch *channel, req *protocol.C2M_DropItem) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	ss := ch.session
	resp := &protocol.M2C_DropItem{RpcId: req.RpcId}
	reject := func(msg string) proto.Message {
		log.Printf("[S=%d] drop item rejected: %s", ch.id, msg)
		resp.Message = msg
		return resp
	}
	slot := req.SlotId
	if slot < 0 || int(slot) >= mainUISlotCount {
		return reject("槽位无效")
	}
	it, ok := ss.bag[req.BagIndex]
	if !ok || it == nil {
		return reject("物品不存在")
	}
	if it.ItemType != 2 {
		return reject("该物品不能放入物品槽")
	}
	if _, ok := tables.goodsBase[int64(it.ItemId)]; !ok {
		return reject("物品配置不存在")
	}
	ss.mainUISlots[slot] = mainUISlot{Type: 2, Id: it.ItemId}
	s.saveData(ch)
	base, err := proto.Marshal(&protocol.M2C_DropItem{RpcId: req.RpcId})
	if err != nil {
		return reject("序列化失败")
	}
	base = appendMainUISlotList(base, 1, ss.mainUISlotList())
	s.sendRawPush(ch, protocol.OpM2C_DropItem, base)
	log.Printf("[S=%d] drop item slot=%d item=%d", ch.id, slot, it.ItemId)
	return nil
}

// mainUISlotAt 返回槽位配置（越界/空槽返回 0）。
func (ss *session) mainUISlotAt(slot int32) mainUISlot {
	if ss == nil || slot < 0 || int(slot) >= mainUISlotCount {
		return mainUISlot{}
	}
	return ss.mainUISlots[slot]
}

// 20235 → 20236：使用主界面物品槽（SlotId = 槽序号）。物品槽里是药水/消耗品，
// 复用 onUseGoods 逻辑（恢复血蓝/喂宠物），并返回 MainUISlotList + BagMapList 全量。
func (s *Server) onUseMainUIGoods(ch *channel, req *protocol.C2M_UseMainUIGoods) proto.Message {
	ss := ch.session
	resp := &protocol.M2C_UseMainUIGoods{RpcId: req.RpcId}
	sl := ss.mainUISlotAt(req.SlotId)
	if sl.Type != 2 || sl.Id <= 0 {
		resp.Message = "物品槽为空"
		return resp
	}
	itemID := sl.Id
	// 找到背包中该物品的一格（有则消耗）
	index := ss.firstBagIndexOf(itemID)
	if index == invalidBagSlot {
		// 背包已无该物品：槽配置保留（玩家自定义），使用失败提示（Error=0 防客户端崩）
		resp.Message = "物品已用完"
		return resp
	}
	gb, ok := tables.goodsBase[int64(itemID)]
	if !ok {
		resp.Error, resp.Message = errBadParam, "物品配置不存在"
		return resp
	}
	if itemID == strengthCouponItemID && int32(num(gb["EffectType"])) == 23 {
		if applied, rejection := s.useEquipStrengthCoupon(ch, index); !applied {
			resp.Message = rejection
			return resp
		}
		base, err := proto.Marshal(resp)
		if err != nil {
			resp.Error, resp.Message = errBadParam, "marshal failed"
			return resp
		}
		base = appendMainUISlotList(base, 1, ss.mainUISlotList())
		base = encodeBagMapList(base, 2, bagPairs(ss.bag))
		s.sendRawPush(ch, protocol.OpM2C_UseMainUIGoods, base)
		s.pushPlayerAttrs(ch)
		s.pushUnitCharacter(ch)
		s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{
			Id: itemID, SkillCD: goodsItemCD(gb), Type: protocol.MainUIType_ItemSlot,
		})
		s.saveData(ch)
		log.Printf("[S=%d] use main ui strength coupon slot=%d item=%d target=0 level=20", ch.id, req.SlotId, itemID)
		return nil
	}
	if s.petFeed(ch, gb) {
		consumeGoods(ss, index)
	} else if handled, rejection := s.useGoodsEffect(ch, gb, 1); handled {
		if rejection != "" {
			resp.Message = rejection
			return resp
		}
		// 开箱/货币/精力/跑图等特殊消耗品：走 equip.go 的 useGoodsEffect 分支。
		// 货币类物品在快捷槽：整格兑换后清槽。
		if isCurrencyGoods(itemID) {
			delete(ss.bag, index)
		} else {
			consumeGoods(ss, index)
		}
	} else {
		ss.battleMu.Lock()
		if rejection := goodsRestoreRejection(ss, gb); rejection != "" {
			ss.battleMu.Unlock()
			resp.Message = rejection
			return resp
		}
		goodsHeal(ss, gb)
		consumeGoods(ss, index)
		// 血蓝恢复必须推 20169（1001/1003）→ NumericComponent → 主界面血蓝条动画。
		currentHP, currentMP := ss.battleHP(), ss.battleMP()
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1001, Value: float32(currentHP), ActorId: ss.playerID,
		})
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: ss.playerID, NumericType: 1003, Value: float32(currentMP), ActorId: ss.playerID,
		})
		ss.battleMu.Unlock()
		// 同一份恢复值也要发给队友，否则组队头像上还停在吃药前的血蓝
		// （见 team_vital_sync.go；必须在 battleMu 之外调用）。
		s.pushTeamVitals(ch, currentHP, currentMP)
	}
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = appendMainUISlotList(base, 1, ss.mainUISlotList())
	base = encodeBagMapList(base, 2, bagPairs(ss.bag))
	s.sendRawPush(ch, protocol.OpM2C_UseMainUIGoods, base)
	// 物品槽 CD：客户端 StartMainUISlotCDEvent 在槽位上显示 CD 遮罩 + 时间。
	s.sendPush(ch, protocol.OpM2C_StartCD, &protocol.M2C_StartCD{
		Id: itemID, SkillCD: goodsItemCD(gb), Type: protocol.MainUIType_ItemSlot,
	})
	s.saveData(ch)
	log.Printf("[S=%d] use main ui goods slot=%d item=%d", ch.id, req.SlotId, itemID)
	return nil
}

// goodsItemCD 消耗品使用后的物品槽 CD（毫秒）。GoodsBase 无 CD 字段，
// 统一 1000ms：客户端槽位出现 1 秒 CD 遮罩 + 倒计时反馈（不会造成长时间锁定）。
func goodsItemCD(gb map[string]interface{}) int32 {
	return 1000
}
