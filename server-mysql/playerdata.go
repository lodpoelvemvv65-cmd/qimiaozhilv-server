package main

import (
	"log"
	"strconv"
	"strings"
)

// playerdata.go：角色技能 / 自动技能 / 任务状态 的 DB 字符串序列化，
// 以及从 Player 记录加载到 session 运行时状态的逻辑。

// 序列化格式（DB TEXT 列）：
//   skills:     "id:level,id:level,..."  顺序即学习顺序
//   autoSkills: "id,id,..."
//   tasks:      "taskId:state,taskId:state,..."

// skillsToString 将已学技能 + 顺序表编码为字符串。
func skillsToString(m map[int32]int32, order []int32) string {
	var sb strings.Builder
	for i, id := range order {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatInt(int64(id), 10))
		sb.WriteByte(':')
		sb.WriteString(strconv.Itoa(int(m[id])))
	}
	return sb.String()
}

// parseSkills 解码技能字符串，返回 技能→等级 映射与学习顺序。
func parseSkills(str string) (map[int32]int32, []int32) {
	m := make(map[int32]int32)
	var order []int32
	if str == "" {
		return m, order
	}
	for _, part := range strings.Split(str, ",") {
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		id, err1 := strconv.Atoi(kv[0])
		if err1 != nil {
			continue
		}
		lvl := 1
		if len(kv) == 2 {
			if v, err := strconv.Atoi(kv[1]); err == nil {
				lvl = v
			}
		}
		if id <= 0 {
			continue
		}
		m[int32(id)] = int32(lvl)
		order = append(order, int32(id))
	}
	return m, order
}

// autoSkillsToString 编码自动技能列表。
func autoSkillsToString(list []int32) string {
	parts := make([]string, 0, len(list))
	for _, id := range list {
		parts = append(parts, strconv.FormatInt(int64(id), 10))
	}
	return strings.Join(parts, ",")
}

// parseAutoSkills 解码自动技能列表。
func parseAutoSkills(str string) []int32 {
	var out []int32
	if str == "" {
		return out
	}
	for _, part := range strings.Split(str, ",") {
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 {
			continue
		}
		out = append(out, int32(id))
	}
	return out
}

// tasksToString 编码任务状态 + 击杀计数（KillSpecial 任务进度，重登保留）。
// 格式："taskId:state,taskId:state,...@k:mid=count,mid=count"
func tasksToString(m map[int32]int32, kills map[int32]int32) string {
	var sb strings.Builder
	first := true
	for id, st := range m {
		if !first {
			sb.WriteByte(',')
		}
		first = false
		sb.WriteString(strconv.FormatInt(int64(id), 10))
		sb.WriteByte(':')
		sb.WriteString(strconv.Itoa(int(st)))
	}
	if len(kills) > 0 {
		sb.WriteString("@k:")
		first = true
		for mid, cnt := range kills {
			if !first {
				sb.WriteByte(',')
			}
			first = false
			sb.WriteString(strconv.FormatInt(int64(mid), 10))
			sb.WriteByte('=')
			sb.WriteString(strconv.Itoa(int(cnt)))
		}
	}
	return sb.String()
}

// parseTasks 解码任务状态（含 @k 击杀计数段）。
func parseTasks(str string) (map[int32]int32, map[int32]int32) {
	m := make(map[int32]int32)
	kills := make(map[int32]int32)
	if str == "" {
		return m, kills
	}
	// 分离击杀段
	tasksPart := str
	if idx := strings.Index(str, "@k:"); idx >= 0 {
		tasksPart = str[:idx]
		for _, part := range strings.Split(str[idx+3:], ",") {
			if part == "" {
				continue
			}
			kv := strings.SplitN(part, "=", 2)
			mid, err1 := strconv.Atoi(kv[0])
			if err1 != nil || mid <= 0 {
				continue
			}
			cnt := 0
			if len(kv) == 2 {
				if v, err := strconv.Atoi(kv[1]); err == nil && v > 0 {
					cnt = v
				}
			}
			kills[int32(mid)] = int32(cnt)
		}
	}
	for _, part := range strings.Split(tasksPart, ",") {
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		id, err1 := strconv.Atoi(kv[0])
		if err1 != nil {
			continue
		}
		st := 0
		if len(kv) == 2 {
			if v, err := strconv.Atoi(kv[1]); err == nil {
				st = v
			}
		}
		if id <= 0 {
			continue
		}
		m[int32(id)] = int32(st)
	}
	return m, kills
}

// loadData 从 Player 记录加载技能/任务/背包到 session 运行时。
// 新号（skills 为空）按职业初始化基础攻击，任务由 autoAcceptInitialTasks 初始化，
// 背包为空时按职业发放线上 YOYO 四件套。
func (s *session) loadData(p *Player) bool {
	migratedData := false
	s.level = p.Level
	s.exp = p.Exp
	s.energy = p.Energy
	s.mapID = p.MapID
	if s.mapID <= 0 {
		s.mapID = 1000601
	}
	s.resetMovement(p.PosX, p.PosY)
	s.hp = p.CurrentHP
	s.mp = p.CurrentMP
	s.name = p.Name
	s.titleID = p.TitleID
	s.charPoint = p.CharPoint
	s.skillPoint = p.SkillPoint
	s.strAdd = p.StrAdd
	s.wimAdd = p.WimAdd
	s.phyAdd = p.PhyAdd
	s.staAdd = p.StaAdd
	s.qukAdd = p.QukAdd
	s.spiAdd = p.SpiAdd
	s.skills, s.skillOrder = p.Relations.skills, p.Relations.skillOrder
	s.autoSkills = p.Relations.autoSkills
	if s.skills == nil {
		s.skills = make(map[int32]int32)
	}
	s.autoBattleEnabled = p.AutoBattle > 0
	if p.AutoBattle < 0 {
		// Compatibility with the old M2C_GetMainUISetting implementation: it
		// rendered the toggle as on whenever a saved automatic-skill list existed.
		s.autoBattleEnabled = len(s.autoSkills) > 0
		migratedData = true
	}
	s.tasks, s.killCount = p.Relations.tasks, p.Relations.killCounts
	if s.tasks == nil {
		s.tasks = make(map[int32]int32)
	}
	if s.killCount == nil {
		s.killCount = make(map[int32]int32)
	}
	// 背包 / 穿戴（equip.go）
	s.bag, s.worn = p.Relations.bag, p.Relations.worn
	// Migrate legacy star-coin GoodsBase stacks out of visible bag slots. The
	// count remains persisted through player_items, while the client receives
	// it only as NumericType 1041.
	if canonicalizeStarCoins(s) {
		migratedData = true
	}
	normalizeWornSlots(s)
	if applyWornTitle(s) {
		migratedData = true
	}
	if p.Relations.starSoul != nil {
		s.starSoul = p.Relations.starSoul
	} else {
		s.starSoul = newStarSoulBag()
	}
	if ensureStarSoulViceGrowth(s.starSoul) {
		migratedData = true
	}
	// 防御：清掉 GoodsBase 表不存在的物品（客户端 UpdateBagUIEvent 对 GoodsBase==null
	// 的物品 NRE 崩溃，Player.log 实证 `GoodsBase == null where Id = 20001`）。
	pruneInvalidBagItems(s)
	if len(s.bag) == 0 && len(s.worn) == 0 {
		initBag(s)
	}
	// A worn skin item is authoritative; otherwise keep a valid direct SkinId
	// so custom appearances do not require a shop item.
	applySkinEquipOnLoad(s)
	// 货币 / 仓库 / 宠物 / 邮件 / 好友 / 签到（state_extra.go）
	s.coin = p.Coin
	s.yuanBao = p.YuanBao
	s.voucher = p.Voucher
	s.honor = p.Honor
	s.pvpCurrency = p.PVPCurrency
	s.store = p.Relations.store
	if s.store == nil {
		s.store = make(map[int32]*bagItem)
	}
	s.storePages = p.StorePages
	if s.storePages < defaultStorePages {
		s.storePages = defaultStorePages
	}
	// Older saves had a fixed 120-slot warehouse and no page column. Preserve
	// every item even if a manually repaired save contains a higher slot.
	for index, item := range s.store {
		if item == nil || index < 0 {
			continue
		}
		pages := index/storeSlotsPerPage + 1
		if pages > s.storePages {
			s.storePages = pages
			migratedData = true
		}
	}
	if normalizeBagSlots(s) {
		migratedData = true
	}
	if ensureEquipmentVariations(s.bag, s.worn, s.store) {
		migratedData = true
	}
	s.storeCoin = p.StoreCoin
	s.pet = p.Relations.pet
	if s.pet == nil {
		s.pet = newPet()
	}
	s.mails = p.Relations.mails
	if len(s.mails) > 0 {
		var max int64
		for _, m := range s.mails {
			if m != nil && m.Id > max {
				max = m.Id
			}
		}
		s.mailSeq = max
	}
	s.friends = p.Relations.friends
	if s.friends == nil {
		s.friends = make(map[int64]*friendInfo)
	}
	s.friendReqFrom = cloneRequestIDs(p.FriendReqFrom)
	s.friendReqTo = cloneRequestIDs(p.FriendReqTo)
	s.signin = p.Relations.signin
	if s.signin == nil {
		s.signin = &signinState{}
	}
	if s.signin.PVPIsMatching || s.signin.PVPMatchCount != 0 {
		s.signin.PVPIsMatching = false
		s.signin.PVPMatchCount = 0
		migratedData = true
	}
	s.signin.ensureTaskProgress()
	// 主界面快捷栏（mainui.go，9 格）
	s.mainUISlots = p.Relations.mainUI
	// 持续消耗品效果（经验卡、魔法球、跑图卡）跨重登保留。
	s.itemBuffs = p.Relations.itemBuffs
	s.syncLegacyItemBuffFields()
	// 转生等级（trans 列；TransmigrationAddConfig 加成）
	s.trans = p.Trans
	// Old direct grants stored the stage-local level after setting Trans. The
	// client expects one cumulative total level and subtracts this stage start.
	if s.trans > 0 && s.trans <= transMaxLevel && s.level < transStart(s.trans) {
		s.level += transStart(s.trans)
		migratedData = true
	}
	// 家族系统（family.go）
	s.familyID = p.FamilyID
	s.familyContribute = p.FamilyContribute
	s.personalContribute = p.PersonalContribute
	s.recalcTransBonus()
	// Reconnect restores the last exact resource values. Only values above a
	// changed maximum are clamped; partial HP/MP is never refilled on login.
	if s.hp >= 0 && s.hp > s.playerMaxHp() {
		s.hp = s.playerMaxHp()
		migratedData = true
	}
	if s.mp >= 0 && s.mp > s.playerMaxMp() {
		s.mp = s.playerMaxMp()
		migratedData = true
	}
	// Every profession always owns its level-1 basic attack. Older transfer
	// saves could retain other learned skills while omitting this entry.
	if s.ensureCurrentJobBaseSkill() {
		migratedData = true
	}
	s.autoSkills = filterLearnedSkills(s.autoSkills, s.skills)
	if restorePersistedTrialLayer(s) {
		migratedData = true
	}
	return migratedData
}

// saveData 将 session 技能/任务/背包状态写入 DB（含等级/经验/属性点，升级加点持久化）。
func (s *Server) saveData(ch *channel) {
	if s == nil || s.store == nil || ch == nil || ch.session == nil || ch.session.playerID <= 0 {
		return
	}
	ss := ch.session
	ss.pvpStateMu.Lock()
	defer ss.pvpStateMu.Unlock()
	relations := playerRelations{
		skills: ss.skills, skillOrder: ss.skillOrder, autoSkills: ss.autoSkills,
		tasks: ss.tasks, killCounts: ss.killCount,
		bag: ss.bag, worn: ss.worn, store: ss.store, starSoul: ss.starSoul,
		pet: ss.pet, mails: ss.mails, friends: sessionFriendsSnapshot(ss),
		signin: ss.signin, mainUI: ss.mainUISlots, itemBuffs: ss.itemBuffs,
	}
	if err := s.store.SavePlayerState(ss.playerID, ss.jobID, ss.level, ss.exp, ss.energy,
		ss.mapID, ss.x, ss.y,
		ss.charPoint, ss.skillPoint, ss.strAdd, ss.qukAdd, ss.spiAdd, ss.wimAdd, ss.phyAdd, ss.staAdd,
		ss.autoBattleEnabled, ss.coin, ss.yuanBao, ss.voucher, ss.storeCoin,
		ss.trans, ss.skinID, ss.titleID, ss.familyID, ss.familyContribute, ss.personalContribute,
		relations, ss.starSoul); err != nil {
		log.Printf("[S=%d] save player data err: %v", ch.id, err)
	}
	if err := s.store.SavePlayerResources(ss.playerID, ss.hp, ss.mp); err != nil {
		log.Printf("[S=%d] save player resources err: %v", ch.id, err)
	}
	if err := s.store.SavePlayerCurrencies(ss.playerID, ss.honor, ss.pvpCurrency); err != nil {
		log.Printf("[S=%d] save player currencies err: %v", ch.id, err)
	}
	if err := s.store.SavePlayerStorePages(ss.playerID, storePageCount(ss)); err != nil {
		log.Printf("[S=%d] save store pages err: %v", ch.id, err)
	}
	ss.socialMu.Lock()
	requestsFrom := cloneRequestIDs(ss.friendReqFrom)
	requestsTo := cloneRequestIDs(ss.friendReqTo)
	ss.socialMu.Unlock()
	if err := s.store.SaveFriendRequests(ss.playerID, requestsFrom, requestsTo); err != nil {
		log.Printf("[S=%d] save friend requests err: %v", ch.id, err)
	}
}

// filterLearnedSkills 仅保留已学技能。
func filterLearnedSkills(list []int32, learned map[int32]int32) []int32 {
	out := make([]int32, 0, len(list))
	for _, id := range list {
		if _, ok := learned[id]; ok {
			out = append(out, id)
		}
	}
	return out
}
