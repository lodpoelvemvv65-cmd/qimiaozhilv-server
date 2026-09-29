package main

import (
	"log"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// gem.go：宝石镶嵌/拆卸（20212 MeltEquip / 20349 DismountGem）。
//
// 客户端链路（文档 14 §3.4 + HotfixView MeltingUI 反汇编）：
//   主城炼化大师 NPC(1011) → 服务端推 M2C_OpenMeltEquipUI(20208) → 打开 MeltingUI。
//   拖装备(ItemType==1) → equipIndex；拖宝石(MaterialBase MaterialType==2) → gemIndex；
//   点宝石槽 → AttributeIndex（0 起，GList.selectedIndex）。
//   镶嵌：C2M_MeltEquip{EquipIndex, GemIndex, AttributeIndex} → M2C_MeltEquip(20213)。
//         响应 Message 空 → 客户端 UpdateBagUI{resp.BagMapList(tag1)} + 重读 equip.gemList。
//   拆卸：C2M_DismountGem{bagIndex, gemIndex} → M2C_DismountGem(20350)。
//         响应 Message 空 → 客户端 C2M_GetBag 刷新 + 重读 gemList。
//
// 线上校验（文档 14）：
//   宝石 = MaterialBase.MaterialType==2（GemKey/GemValue/GemLevel/GemType 字段）。
//   槽位 < EquipBase.MaxHole；同 GemType 不能重复镶嵌。
//   费用 = GemPriceConfig[GemLevel].InlayPrice（镶嵌）/ DiscountPrice（拆卸），金币。
//   属性重算：GemKey(AttributeType)→NumericType 并入 → 推 20169（growth.go gemKeyToNumeric）。
//
// ⚠ 客户端健壮性（Player.log 实证 + finishPutOn 同款结论）：Session.Call 对 Error!=0
// 响应直接抛 RPC 异常 → 崩溃。可预期的业务拒绝必须 Error=0 + Message 提示（客户端弹提示）。

// gemOfBagIndex 返回背包格 itemID 对应 MaterialBase 行（非宝石返回 nil）。
func gemConfig(itemID int32) map[string]interface{} {
	if tables == nil {
		return nil
	}
	row, ok := tables.materialBase[int64(itemID)]
	if !ok {
		return nil
	}
	if int32(num(row["MaterialType"])) != 2 {
		return nil
	}
	return row
}

// gemPrice 宝石等级 1..7 的费用（镶嵌 InlayPrice / 拆卸 DiscountPrice）；无配置回 0。
func gemPrice(gemLevel int32) (inlay, discount int64) {
	if tables == nil {
		return 0, 0
	}
	if row, ok := tables.gemPrice[int64(gemLevel)]; ok {
		return int64(num(row["InlayPrice"])), int64(num(row["DiscountPrice"]))
	}
	return 0, 0
}

// equipMaxHole 装备宝石槽数（EquipBase.MaxHole；缺省 0）。
func equipMaxHole(itemID int32) int32 {
	if tables == nil {
		return 0
	}
	if eb, ok := tables.equipBase[int64(itemID)]; ok {
		return int32(num(eb["MaxHole"]))
	}
	return 0
}

// gemTypeOf 宝石 GemType（MaterialBase.GemType）。
func gemTypeOf(gemID int32) int32 {
	if tables == nil {
		return 0
	}
	if row, ok := tables.materialBase[int64(gemID)]; ok {
		return int32(num(row["GemType"]))
	}
	return 0
}

func intArrayContains(raw interface{}, value int32) bool {
	for _, entry := range arrOf(raw) {
		if int32(num(entry)) == value {
			return true
		}
	}
	return false
}

// equipAllowsGem mirrors both online filters used by the melting UI:
// EquipBase.CanInlayGemTypeArr limits gem families, while GemInlayConfig for
// EquipBase.Type limits the GemKey (attribute type) accepted by that slot.
func equipAllowsGem(equipID int32, gem map[string]interface{}) bool {
	if tables == nil || gem == nil {
		return false
	}
	equip := tables.equipBase[int64(equipID)]
	if equip == nil {
		return false
	}
	gemType := int32(num(gem["GemType"]))
	allowedTypes := arrOf(equip["CanInlayGemTypeArr"])
	if len(allowedTypes) > 0 && !intArrayContains(allowedTypes, gemType) {
		return false
	}
	position := int32(num(equip["Type"]))
	inlay := tables.gemInlay[int64(position)]
	if inlay == nil {
		return false
	}
	return intArrayContains(inlay["CanInlayArr"], int32(num(gem["GemKey"])))
}

// ===================== 镶嵌 20212 → 20213 =====================

func (s *Server) onMeltEquip(ch *channel, req *protocol.C2M_MeltEquip) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	ss := ch.session
	resp := &protocol.M2C_MeltEquip{RpcId: req.RpcId}
	// 业务拒绝：Error=0 + Message（客户端提示，不崩）
	reject := func(msg string) proto.Message {
		log.Printf("[S=%d] melt equip rejected: %s", ch.id, msg)
		resp.Message = msg
		return resp
	}

	equip, ok := ss.bag[req.EquipIndex]
	if !ok || equip == nil || equip.ItemType != 1 {
		return reject("只能对装备进行炼化！")
	}
	if equip.IsLock {
		return reject("装备已锁定，无法镶嵌")
	}
	gem, ok := ss.bag[req.GemIndex]
	if !ok || gem == nil || gem.Count <= 0 {
		return reject("只能放入宝石！")
	}
	if gem.IsLock {
		return reject("宝石已锁定，无法镶嵌")
	}
	gcfg := gemConfig(gem.ItemId)
	if gcfg == nil {
		return reject("只能放入宝石！")
	}
	if !equipAllowsGem(equip.ItemId, gcfg) {
		return reject("该部位不能镶嵌这种宝石！")
	}
	maxHole := equipMaxHole(equip.ItemId)
	if maxHole <= 0 {
		return reject("该装备没有宝石槽")
	}
	if req.AttributeIndex < 0 || req.AttributeIndex >= maxHole {
		return reject("请选择一个宝石孔！")
	}
	// 槽位已有宝石 → 先拆卸
	glist := equip.GemList
	if int(req.AttributeIndex) < len(glist) && glist[req.AttributeIndex] > 0 {
		return reject("该宝石槽已有宝石，请先拆卸")
	}
	// 同 GemType 不能重复镶嵌（线上）
	newType := gemTypeOf(gem.ItemId)
	for _, gid := range glist {
		if gid > 0 && gemTypeOf(gid) == newType {
			return reject("同一种宝石不能镶嵌多个在一件装备上，否则不生效！")
		}
	}
	// 费用：GemPriceConfig[GemLevel].InlayPrice
	inlay, _ := gemPrice(int32(num(gcfg["GemLevel"])))
	if inlay < 0 || ss.coin < inlay {
		return reject("金币不足")
	}
	gemID := gem.ItemId
	// 必须消耗客户端拖入的 GemIndex。按 ItemId 跨格扣除会让选中格不变，
	// MeltingUI 随后读取旧格，看起来就像“镶嵌不上”。
	if !ss.removeBagCount(req.GemIndex, 1) {
		return reject("宝石数量不足")
	}
	ss.coin -= inlay
	// 写槽位（gemList 定长 maxHole）
	if cap(glist) < int(maxHole) {
		nl := make([]int32, maxHole)
		copy(nl, glist)
		glist = nl
	} else if len(glist) < int(maxHole) {
		glist = append(glist, make([]int32, int(maxHole)-len(glist))...)
	}
	glist[req.AttributeIndex] = gemID
	equip.GemList = glist
	// 响应带整包 BagMapList（tag1）
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	base = encodeBagMapList(base, 1, bagPairs(ss.bag))
	s.saveData(ch)
	s.sendRawPush(ch, protocol.OpM2C_MeltEquip, base)
	s.pushMoney(ch)
	log.Printf("[S=%d] melt gem item=%d -> equip %d slot=%d coin=%d", ch.id, gemID, equip.ItemId, req.AttributeIndex, inlay)
	return nil
}

// ===================== 拆卸 20349 → 20350 =====================

func (s *Server) onDismountGem(ch *channel, req *protocol.C2M_DismountGem) proto.Message {
	ch.session.battleMu.Lock()
	defer ch.session.battleMu.Unlock()
	ss := ch.session
	resp := &protocol.M2C_DismountGem{RpcId: req.RpcId}
	reject := func(msg string) proto.Message {
		log.Printf("[S=%d] dismount gem rejected: %s", ch.id, msg)
		resp.Message = msg
		return resp
	}

	equip, ok := ss.bag[req.BagIndex]
	if !ok || equip == nil || equip.ItemType != 1 {
		return reject("只能对装备进行拆卸！")
	}
	if equip.IsLock {
		return reject("装备已锁定，无法拆卸宝石")
	}
	if req.GemIndex < 0 || int(req.GemIndex) >= len(equip.GemList) || equip.GemList[req.GemIndex] <= 0 {
		return reject("该宝石槽没有宝石")
	}
	gemID := equip.GemList[req.GemIndex]
	gcfg := gemConfig(gemID)
	if gcfg == nil {
		// 数据异常：直接清槽（不留死数据）
		equip.GemList[req.GemIndex] = 0
		s.saveData(ch)
		return resp
	}
	// 费用：GemPriceConfig[GemLevel].DiscountPrice
	_, discount := gemPrice(int32(num(gcfg["GemLevel"])))
	if discount < 0 || ss.coin < discount {
		return reject("金币不足")
	}
	// 在同一份 staging 快照上完成宝石回包和装备清槽。addItemToBag 会
	// 替换背包快照，继续修改旧 equip 指针会导致清槽没有真正持久化。
	staged, _, fits := stageBagGrants(ss, []bagGrant{{itemID: gemID, count: 1}})
	if !fits {
		return reject("背包已满")
	}
	stagedEquip := staged[req.BagIndex]
	if stagedEquip == nil || int(req.GemIndex) >= len(stagedEquip.GemList) {
		return reject("装备数据异常")
	}
	stagedEquip.GemList[req.GemIndex] = 0
	ss.bag = staged
	ss.coin -= discount
	s.saveData(ch)
	base, err := proto.Marshal(resp)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "序列化失败"
		return resp
	}
	s.sendRawPush(ch, protocol.OpM2C_DismountGem, base)
	s.pushMoney(ch)
	log.Printf("[S=%d] dismount gem item=%d from equip %d slot=%d coin=%d", ch.id, gemID, stagedEquip.ItemId, req.GemIndex, discount)
	// 客户端随后自发 C2M_GetBag 刷新；响应 Message 留空即可
	return nil
}
