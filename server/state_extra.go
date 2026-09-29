package main

import (
	"encoding/json"
	"log"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// state_extra.go：商店/邮件/聊天/宠物/好友/组队/签到 等新增模块的共享状态类型与辅助函数。
//
// 持久化约定：玩家侧状态（货币/仓库/宠物/邮件/好友/签到）经 players 表的 JSON 列持久化
// （store_json / pet_json / mails_json / friends_json / signin_json），由 loadData/saveData
// 统一加载与保存（playerdata.go）。组队为全服内存态（Server.teamMu / Server.teams）。

// ===================== 货币（NumericType） =====================
// 线上 NumericType（Unity.Model.dll ET.NumericType，见 13 文档 §2.9）：
//
//	元宝=1024 兑换券=1025 铜币=1028 荣誉=1030 家族贡献=1037 体力=1039
//
// 玩家基础货币存 session（coin/yuanBao/voucher，DB 列），其余体系暂不启用。
const (
	ntYuanBao            = 1024
	ntVoucher            = 1025
	ntCoin               = 1028
	ntHornor             = 1030
	ntFamilyContribution = 1037
	ntEnergy             = 1039
)

// pushMoney 推送三项货币属性（主界面货币栏刷新）。
func (s *Server) pushMoney(ch *channel) {
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		return
	}
	pid := ch.session.playerID
	push := func(t int32, v float32) {
		s.sendPush(ch, protocol.OpM2C_SyncUnitAttribute, &protocol.M2C_SyncUnitAttribute{
			UnitId: pid, NumericType: t, Value: v, ActorId: pid,
		})
	}
	push(ntCoin, float32(ch.session.coin))
	push(ntYuanBao, float32(ch.session.yuanBao))
	push(ntVoucher, float32(ch.session.voucher))
}

// addCoin 增加铜币并推送余额。
func (s *Server) addCoin(ch *channel, v int64) {
	if v == 0 {
		return
	}
	ch.session.coin += v
	s.pushMoney(ch)
}

// spendCoin 扣减铜币；不足返回 false（不推送）。
func (s *Server) spendCoin(ch *channel, v int64) bool {
	if v < 0 || ch.session.coin < v {
		return false
	}
	ch.session.coin -= v
	s.pushMoney(ch)
	return true
}

// addYuanBao 增加元宝并推送。
func (s *Server) addYuanBao(ch *channel, v int64) {
	if v == 0 {
		return
	}
	ch.session.yuanBao += v
	s.pushMoney(ch)
}

// spendYuanBao 扣减元宝；不足返回 false。
func (s *Server) spendYuanBao(ch *channel, v int64) bool {
	if v < 0 || ch.session.yuanBao < v {
		return false
	}
	ch.session.yuanBao -= v
	s.pushMoney(ch)
	return true
}

// spendVoucher 扣减兑换券；不足返回 false。
func (s *Server) spendVoucher(ch *channel, v int64) bool {
	if v < 0 || ch.session.voucher < v {
		return false
	}
	ch.session.voucher -= v
	s.pushMoney(ch)
	return true
}

// ===================== 背包通用辅助 =====================

type bagGrant struct {
	itemID int32
	count  int32
}

func cloneBagItem(it *bagItem) *bagItem {
	if it == nil {
		return nil
	}
	cloned := *it
	if it.MainAttr != nil {
		cloned.MainAttr = make(map[int32]float32, len(it.MainAttr))
		for key, value := range it.MainAttr {
			cloned.MainAttr[key] = value
		}
	}
	cloned.RandomAttrs = append([]int32(nil), it.RandomAttrs...)
	cloned.AddAttrs = append([]int32(nil), it.AddAttrs...)
	cloned.GemList = append([]int32(nil), it.GemList...)
	return &cloned
}

func cloneBagMap(src map[int32]*bagItem) map[int32]*bagItem {
	cloned := make(map[int32]*bagItem, len(src))
	for index, item := range src {
		if item != nil {
			cloned[index] = cloneBagItem(item)
		}
	}
	return cloned
}

// addItemToBagInPlace applies one grant to a staging bag. Equipment always uses
// one slot per instance; goods and materials fill existing stacks before using
// new slots and honor the MaxAmount value from the online tables.
func addItemToBagInPlace(ss *session, itemID, count int32) (int32, bool) {
	if ss == nil || ss.bag == nil || itemID <= 0 || count <= 0 {
		return invalidBagSlot, false
	}
	template := newBagItem(itemID)
	firstIndex := invalidBagSlot
	if template.ItemType == int32(protocol.ItemType_EquipItem) {
		for count > 0 {
			index := nextBagIndex(ss)
			if index < 0 {
				return invalidBagSlot, false
			}
			item := newBagItem(itemID)
			item.Count = 1
			ss.bag[index] = item
			if firstIndex < 0 {
				firstIndex = index
			}
			count--
		}
		return firstIndex, true
	}

	limit64 := rewardStackLimit(itemID)
	if limit64 <= 0 || limit64 > int64(^uint32(0)>>1) {
		limit64 = 999
	}
	limit := int32(limit64)
	for index := int32(0); index < bagSlotCount && count > 0; index++ {
		item := ss.bag[index]
		if item == nil || item.ItemId != itemID || item.ItemType != template.ItemType || item.Count >= limit {
			continue
		}
		add := limit - item.Count
		if add > count {
			add = count
		}
		item.Count += add
		count -= add
		if firstIndex < 0 {
			firstIndex = index
		}
	}
	for count > 0 {
		index := nextBagIndex(ss)
		if index < 0 {
			return invalidBagSlot, false
		}
		add := limit
		if add > count {
			add = count
		}
		item := newBagItem(itemID)
		item.Count = add
		ss.bag[index] = item
		count -= add
		if firstIndex < 0 {
			firstIndex = index
		}
	}
	return firstIndex, true
}

// stageBagGrants validates a complete reward batch against a cloned bag. The
// caller commits the returned map only when every item fits, so full-bag errors
// never leave partial stacks or partially delivered rewards behind.
func stageBagGrants(ss *session, grants []bagGrant) (map[int32]*bagItem, []int32, bool) {
	if ss == nil || ss.bag == nil {
		return nil, nil, false
	}
	shadow := &session{bag: cloneBagMap(ss.bag)}
	indices := make([]int32, 0, len(grants))
	for _, grant := range grants {
		index, ok := addItemToBagInPlace(shadow, grant.itemID, grant.count)
		if !ok {
			return nil, nil, false
		}
		indices = append(indices, index)
	}
	return shadow.bag, indices, true
}

// addItemToBag atomically adds one item grant and returns the first stack/slot
// touched by it. A full bag returns -1 and leaves the original bag unchanged.
func (ss *session) addItemToBag(itemID int32, count int32) int32 {
	staged, indices, ok := stageBagGrants(ss, []bagGrant{{itemID: itemID, count: count}})
	if !ok {
		log.Printf("[P=%d] add item=%d count=%d rejected: bag full", ss.playerID, itemID, count)
		return invalidBagSlot
	}
	ss.bag = staged
	return indices[0]
}

// removeBagCount 从背包格扣减数量；扣完删格。成功返回 true。
func (ss *session) removeBagCount(index int32, count int32) bool {
	it, ok := ss.bag[index]
	if !ok || it == nil || count <= 0 || it.Count < count {
		return false
	}
	it.Count -= count
	if it.Count <= 0 {
		delete(ss.bag, index)
	}
	return true
}

// removeBagCountByItem 按物品 id 跨格扣除数量（技能书等堆叠物）；扣完删格。
func (ss *session) removeBagCountByItem(itemID int32, count int32) {
	if ss == nil || count <= 0 {
		return
	}
	for i, it := range ss.bag {
		if count <= 0 {
			break
		}
		if it == nil || it.ItemId != itemID {
			continue
		}
		if it.Count <= count {
			count -= it.Count
			delete(ss.bag, i)
		} else {
			it.Count -= count
			count = 0
		}
	}
}

// pushBagSnapshot 推送背包全量快照（M2C_SendBag）。
func (s *Server) pushBagSnapshot(ch *channel) {
	ss := ch.session
	if ss == nil || ss.playerID == 0 {
		return
	}
	sb, err := proto.Marshal(&protocol.M2C_SendBag{RpcId: 0, ActorId: ss.playerID})
	if err == nil {
		sb = encodeBagMapList(sb, 1, bagPairs(ss.bag))
		s.sendRawPush(ch, protocol.OpM2C_SendBag, sb)
	}
}

// ===================== 宠物 =====================
// petState：单宠物模型（见 17 文档）。宠物状态存会话并随 saveData 持久化。

type petState struct {
	PetId     int32  `json:"id"`   // 宠物配置 id（2101 基础，2102/2103 升级形态）
	Level     int32  `json:"lv"`   // 等级
	Exp       int32  `json:"exp"`  // 经验
	Intimacy  int32  `json:"im"`   // 亲密度
	Name      string `json:"name"` // 宠物名
	IsShow    bool   `json:"show"` // 是否跟随展示
	Active    int32  `json:"act"`  // 活跃度
	EatCount  int32  `json:"eat"`  // 今日已喂食次数
	PetState  int32  `json:"ps"`   // 0等待 1跟随 2嬉戏 3锻炼 4探险
	ActionEnd int64  `json:"ae"`   // 动作结束 unix 秒；0=无动作
	LastDay   string `json:"day"`  // 上次喂食/动作日（yyyyMMdd），跨日重置
	Rewarded  bool   `json:"rw"`   // 当前动作奖励是否已领
}

// newPet 创建默认宠物（PetConfig 2101 起手）。
func newPet() *petState {
	p := &petState{PetId: 2101, Level: 1, Name: "小宠物", IsShow: true}
	if tables != nil {
		if row, ok := tables.petConfig[2101]; ok {
			if n, ok := row["Name"].(string); ok && n != "" {
				p.Name = n
			}
		}
	}
	return p
}

// petDailyReset 跨日重置喂食次数。
func (p *petState) petDailyReset() {
	day := time.Now().Format("20060102")
	if p.LastDay != day {
		p.LastDay = day
		p.EatCount = 0
	}
}

// ===================== 邮件 =====================

type mailItemMsg struct {
	ItemId    int32 `json:"i"`
	Count     int32 `json:"c"`
	IsLock    bool  `json:"l"`
	IsHasItem bool  `json:"h"`
}

type mailMsg struct {
	Id         int64         `json:"id"`
	Title      string        `json:"t"`
	Content    string        `json:"c"`
	SenderName string        `json:"s"`
	State      int32         `json:"st"` // 0 未领取 1 已领取
	RemainTime int64         `json:"rt"` // 剩余秒（0=永久）
	Items      []mailItemMsg `json:"it"` // 附件
}

// ===================== 好友 =====================

type friendInfo struct {
	Id        int64  `json:"id"`
	Name      string `json:"n"`
	Job       int32  `json:"j"`
	Level     int32  `json:"l"`
	LastLogin int64  `json:"ll"`
}

// ===================== 签到 =====================

type signinState struct {
	LastDay             string                    `json:"day"`           // yyyyMMdd 最近签到日
	LastMonth           string                    `json:"m"`             // yyyyMM 本月记录月份
	MonthCount          int32                     `json:"mc"`            // 本月累计签到次数
	MonthGotIDs         []int32                   `json:"mg,omitempty"`  // 本月已领累计奖励的 SignInRewardMonth._id；推客户端时转换成 Times
	MonthGotMonth       string                    `json:"mm,omitempty"`  // 记录月（跨月清空 MonthGotIDs）
	MapCoinDay          string                    `json:"mcd,omitempty"` // yyyyMMdd 地图金币次数所属日期
	MapCoinExtra        int32                     `json:"mce,omitempty"` // 当日购买的额外拾取次数
	MapCoinClaimed      int32                     `json:"mcc,omitempty"` // 当日已经拾取的次数
	TrialHighestID      int32                     `json:"th,omitempty"`  // 已通关的最高 TrialCopy._id
	TrialRewardDay      string                    `json:"trd,omitempty"` // yyyyMMdd 最近一次批量领取试炼奖励日期
	QuizScore           int32                     `json:"qs,omitempty"`  // 答题累计积分
	QuizTimeMS          int64                     `json:"qt,omitempty"`  // 答题累计用时（同分时越短越前）
	QuizCompleted       bool                      `json:"qc,omitempty"`  // 已完成过答题任务
	PVPScore            int32                     `json:"ps,omitempty"`  // 个人竞技积分
	PVPBattleCount      int32                     `json:"pbc,omitempty"` // 个人竞技场次
	PVPMatchCount       int32                     `json:"pmc,omitempty"` // 当前匹配池人数快照
	PVPIsMatching       bool                      `json:"pim,omitempty"` // 是否正在匹配
	TaskKillProgress    map[int32]map[int32]int32 `json:"tk,omitempty"`
	TaskAnyKillProgress map[int32]int32           `json:"tak,omitempty"`
	TaskDialogProgress  map[int32]bool            `json:"td,omitempty"`
	TaskQuizProgress    map[int32]bool            `json:"tq,omitempty"`
}

func (s *signinState) ensureTaskProgress() {
	if s.TaskKillProgress == nil {
		s.TaskKillProgress = make(map[int32]map[int32]int32)
	}
	if s.TaskAnyKillProgress == nil {
		s.TaskAnyKillProgress = make(map[int32]int32)
	}
	if s.TaskDialogProgress == nil {
		s.TaskDialogProgress = make(map[int32]bool)
	}
	if s.TaskQuizProgress == nil {
		s.TaskQuizProgress = make(map[int32]bool)
	}
}

// ===================== 仓库（物品仓库） =====================
// session.store：仓库格子（0 基全局 Index，每页 60 格）；session.storeCoin：仓库铜币。

// ===================== JSON 序列化辅助 =====================

// jsonKV：map[int32]*bagItem → JSON（与 equip.go bagToJSON 同构）。
func storeToJSON(m map[int32]*bagItem) string {
	return bagToJSON(m)
}

func storeFromJSON(s string) map[int32]*bagItem {
	return bagFromJSON(s)
}

func petsToJSON(p *petState) string {
	if p == nil {
		return ""
	}
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func petsFromJSON(s string) *petState {
	if s == "" {
		return nil
	}
	var p petState
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil
	}
	return &p
}

func mailsToJSON(list []*mailMsg) string {
	if len(list) == 0 {
		return ""
	}
	b, err := json.Marshal(list)
	if err != nil {
		return ""
	}
	return string(b)
}

func mailsFromJSON(s string) []*mailMsg {
	if s == "" {
		return nil
	}
	var list []*mailMsg
	if err := json.Unmarshal([]byte(s), &list); err != nil {
		return nil
	}
	return list
}

func friendsToJSON(m map[int64]*friendInfo) string {
	if len(m) == 0 {
		return ""
	}
	type kv struct {
		K int64       `json:"k"`
		V *friendInfo `json:"v"`
	}
	var list []kv
	for k, v := range m {
		list = append(list, kv{k, v})
	}
	b, err := json.Marshal(list)
	if err != nil {
		return ""
	}
	return string(b)
}

func friendsFromJSON(s string) map[int64]*friendInfo {
	m := make(map[int64]*friendInfo)
	if s == "" {
		return m
	}
	type kv struct {
		K int64       `json:"k"`
		V *friendInfo `json:"v"`
	}
	var list []kv
	if err := json.Unmarshal([]byte(s), &list); err != nil {
		return m
	}
	for _, e := range list {
		if e.V != nil {
			m[e.K] = e.V
		}
	}
	return m
}

func signinToJSON(s *signinState) string {
	if s == nil {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

func signinFromJSON(s string) *signinState {
	if s == "" {
		return nil
	}
	var st signinState
	if err := json.Unmarshal([]byte(s), &st); err != nil {
		return nil
	}
	return &st
}

// ===================== 在线玩家查找（聊天/组队/好友广播） =====================

// sortedMailIDs 按 id 升序排序（分页稳定）。
func sortedMailIDs(list []*mailMsg) []int64 {
	ids := make([]int64, 0, len(list))
	for _, m := range list {
		if m != nil {
			ids = append(ids, m.Id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
