package main

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// 会话状态（07 文档 §3 / 08 文档 §4）
type session struct {
	// battleMu serializes player input, automatic casts and periodic effect ticks.
	// A single TCP connection reads requests serially, but automatic battle runs in
	// a background worker and therefore must share this lock with battle handlers.
	battleMu       sync.Mutex
	battleSequence int64
	storePages     int32
	// combatIcons tracks the 20080 states the client's BuffComponent still holds.
	// The client only drops an entry when its own 500ms timer expires it, so a
	// forced clear must replay the exact unit and Id the Add used (see
	// clearPlayerBattleStates).
	combatIconMu sync.Mutex
	combatIcons  map[combatIconKey]combatIconState
	// Guards delayed scene initialisation after a map change.
	mapChangeMu   sync.Mutex
	mapChangeSeq  uint64
	mapReadySeq   uint64
	mapStartupSeq uint64
	mapChangeAt   time.Time
	// A normal login must receive one complete Numeric snapshot after the
	// first scene that actually contains MyUnit.  Launcher-team/map fan-out
	// can replace that scene before its delayed startup callback runs, so this
	// flag carries the pending snapshot to the replacement map.
	loginNumericPending atomic.Bool
	teamMoveSeq         atomic.Uint64 // invalidates delayed party-follow paths
	movementMu          sync.Mutex    // protects interpolated position during delayed follow

	state            int               // 登录状态
	superseded       atomic.Bool       // 同角色的新会话已接管；旧会话只保留心跳等待确认退出
	offlineRun       atomic.Bool       // 防止顶号与 TCP defer 重复执行下线保存
	accountID        int64             // 账号 id（登录后填充）
	playerID         int64             // 角色 id（LoginGate 后填充）
	reservedPlayerID int64             // 无角色时 LoginGate 预留，CreateRole 必须沿用
	account          string            // 账号名
	name             string            // 角色名（loadData 时填充，升级重推 SendUnitInfo 用）
	jobID            int32             // 职业
	skinID           int32             // 皮肤
	titleID          int32             // 当前称号（NumericType 1038 / UnitCharacter.Title）
	level            int32             // 角色等级
	exp              int64             // 当前经验（100 倍定点单位；门槛见 expNeed）
	energy           int32             // 行动值（NumericType 1039，主线普通战斗每次消耗 3）
	trans            int32             // 转生等级 0-3（NumericType 1029；TransmigrationAddConfig 职业×10+转生等级）
	transBonus       map[int32]float32 // 转生属性加成（NumericType → 加值，保留百分比小数）
	// 家族系统（family.go）
	familyID                 int64        // 所属家族 id（0=无家族）
	familyContribute         int32        // 家族贡献
	personalContribute       int32        // 个人贡献
	charPoint                int32        // 剩余属性点（当前转生段内每 4 级 +1）
	skillPoint               int32        // 剩余技能点（每 Gameplay.skill_point_levels 级 +1；仅首次学习技能消耗 1）
	strAdd                   int32        // 手动加点：力量
	wimAdd                   int32        // 手动加点：智慧
	phyAdd                   int32        // 手动加点：体质
	staAdd                   int32        // 手动加点：耐力
	qukAdd                   int32        // 手动加点：敏捷
	spiAdd                   int32        // 手动加点：精神
	battle                   *battleState // 当前战斗状态（进入主线战斗后填充）
	mapID                    int32        // 当前地图（mapId = sceneId*100 + layer，ChangeMap 时更新）
	x                        float32      // 服务端估算的当前坐标
	y                        float32      // 当前坐标
	yAngle                   int32        // 当前朝向：右行 0，左行 180
	moveStartX, moveStartY   float32
	moveTargetX, moveTargetY float32
	moveStartedAt            time.Time
	moving                   bool

	// 技能系统（EnterGame 时从 DB 加载，或按职业初始化）
	skills            map[int32]int32 // 已学技能 skillID → 等级
	skillOrder        []int32         // 已学技能顺序（自动技能/施放候选按此索引）
	autoSkills        []int32         // 自动施放技能列表（仅含已学技能）
	autoBattleEnabled bool            // 主界面自动战斗开关（持久化；跨战斗保留）
	// 主界面快捷栏（mainui.go）：9 格固定（MainUISlotComponent ctor newarr 9）。
	// 玩家从技能面板拖技能（C2M_DropSkill）或从背包拖物品（C2M_DropItem）自定义；
	// player_mainui_slots 关系表持久化。新号 9 格全空，由玩家自行配置。
	mainUISlots [9]mainUISlot
	// 当前场景已推送的字段怪 id（mapmonster.go；换图时用 20321 清理）
	fieldMonsterIDs       []int64
	fieldMonsterConfigIDs map[int64]int32 // 字段怪单位 id -> MapMonsterConfig._id
	mapCoinUnitID         int64           // 当前地图有效金币怪；0 表示未遇到或已领取
	activitySceneMu       sync.Mutex
	activityScene         *activitySceneState
	// 当前场景的 BOSS 字段怪单位 id（10010 世界 BOSS；BossDead 按各自 id 移除）
	fieldBossUnitID int64
	// Native world-boss click handshake: 20057 arms one 20059 challenge.
	bossChallengePending bool
	// 非战斗血蓝（equip.go）：战斗中读 battleState，战斗外持久在此；
	// 初始 = 满值，战斗结束写回残血 → 吃药可恢复（主界面血条刷新）。
	// -1 = 未初始化（battleHP() 视为满血）；0 = 真实残血。
	hp int32
	mp int32
	// Packed MaxHP/current HP last sent while creating or updating this player's
	// scene Unit. Team-head refreshes read it without rerunning mutable stat logic.
	teamHeadHP atomic.Uint64
	// 服务端权威技能冷却。客户端的 CD 动画只用于显示，不能作为施放校验。
	skillCooldowns        map[int32]time.Time // skillID -> 下次可施放时间
	battleActionReadyAt   time.Time           // this player's next manual or automatic action
	autoBattle            bool                // 当前是否启用战斗内自动施放
	idleBattle            bool                // 当前战斗由挂机入口 20087 发起
	autoBattleEpoch       uint64              // 每次切换递增，使旧 worker 立即失效
	autoBattleNextCastAt  time.Time           // next automatic attempt for this player
	autoBattleSkillCursor int                 // round-robin cursor in the saved automatic-skill list
	mainStoryAIRunning    bool                // 20365 continuous main-story runner (not persisted)
	mainStoryAIEpoch      uint64              // invalidates delayed map/battle continuations
	// 经验倍率 buff（经验卡 EffectType 6/7/16：ExpRange × ContinuedSeconds 毫秒）。
	expMult      float64                   // 当前经验倍率（1.0 = 无 buff）
	expMultUntil int64                     // 到期 unix 毫秒（0 = 无 buff）
	itemBuffMu   sync.Mutex                // 保护自动战斗读取、物品使用与存档之间的状态并发
	itemBuffs    map[int32]*activeItemBuff // 持久化道具状态，按互斥类别保存
	// 魔法球 buff（EffectType 2/3：100万生命/精力魔法球，"战斗结束后自动回满，存储N"）。
	// 使用后左上角状态栏显示 buff 时间；战斗结束（胜利/败北/退出）时若活跃 → 自动回满。
	hpBallUntil int64 // 到期 unix 毫秒（0 = 无）
	hpBallCap   int64 // 存储容量（Capacity；缺省 = 满血）
	mpBallUntil int64
	mpBallCap   int64

	// 任务系统（EnterGame 时从 DB 加载）
	tasks     map[int32]int32 // taskID → TaskState（0/1/2/3）
	killCount map[int32]int32 // 怪物ID → 已击杀数（KillSpecial 任务计数）
	// New roles receive the native introductory task window once after their
	// first scene startup. Existing roles only receive normal task-state sync.
	newRolePending       bool
	initialTaskUIPending bool

	// 背包系统（EnterGame 时从 DB 加载，初始按职业发 YOYO 四件套）
	bag      map[int32]*bagItem // 背包格子 Index → 物品（客户端固定 0..47）
	worn     map[int32]*bagItem // 穿戴槽位（EquipBase.Type）→ 装备（8=背心 9=裤 10=手套 11=鞋 等）
	starSoul *starSoulBag       // 星魂实例、12 个穿戴槽和套装状态

	// 货币（state_extra.go）。铜币是唯一基础铜币余额；金/银由客户端按
	// 100/10000 铜币换算显示。星币沿用背包物品 110205，其他专属币独立持久化。
	coin        int64
	yuanBao     int64
	voucher     int64
	honor       int64 // NumericType 1030
	pvpCurrency int64 // NumericType 1040

	// 仓库（player_items 的 store location + players.store_coin）
	store     map[int32]*bagItem // 仓库格子 Index → 物品
	storeCoin int64              // 仓库铜币
	// shopCatalog is the last server directory sent to this client. It maps
	// compact visible slots to config IDs, so a disabled row cannot silently
	// shift a stale Page+Slot/Type+Index purchase onto another item.
	shopCatalogMu   sync.RWMutex
	shopCatalog     map[shopCatalogSlot]int32
	saleDestination int32

	// 宠物（pet.go；player_pets 表）
	pet *petState

	// 邮件（mail.go；player_mails/player_mail_items 表）
	mails   []*mailMsg
	mailSeq int64 // 邮件 id 发生器（持久化后恢复用）
	// 在线奖励以单调时钟计算本次会话增量，并把累计余量持久化到 player_activity。
	onlineRewardMu         sync.Mutex
	onlineRewardCheckpoint time.Time

	// 好友（friend.go；player_friends/player_friend_requests 表）
	socialMu      sync.Mutex // 好友、好友申请的跨会话同步
	friends       map[int64]*friendInfo
	friendReqFrom map[int64]bool // 待处理的好友申请（对方 Id）
	friendReqTo   map[int64]bool // 我发出的申请（对方 Id）

	// 组队（team.go；仅内存，Server.teams 全服表 + session.teamID 指针）
	teamID int64
	// 面对面交易仅保存当前内存会话 id；背包和铜币仍由 MySQL 持久化。
	tradeID           int64
	tradeCompatOpen   bool
	tradeCompatClosed bool

	// 签到、活动和任务进度（player_activity 及关联进度表）
	dungeonQuotaMu sync.Mutex
	pvpStateMu     sync.Mutex
	signin         *signinState

	// 答题小游戏的单局瞬时状态；累计成绩保存在 player_activity。
	quizQuestionID  int32
	quizStartedAt   time.Time
	quizRunScore    int32
	quizRunTimeMS   int64
	quizStreak      int32
	quizAnswerCount int32
	quizAnswered    bool
	lastNPCID       int32
}

func (s *session) setActivityScene(state *activitySceneState) {
	if s == nil {
		return
	}
	s.activitySceneMu.Lock()
	s.activityScene = state
	s.activitySceneMu.Unlock()
}

func (s *session) currentActivityScene() *activitySceneState {
	if s == nil {
		return nil
	}
	s.activitySceneMu.Lock()
	defer s.activitySceneMu.Unlock()
	return s.activityScene
}

func (s *session) clearActivityScene(state *activitySceneState) {
	if s == nil {
		return
	}
	s.activitySceneMu.Lock()
	if state == nil || s.activityScene == state {
		s.activityScene = nil
	}
	s.activitySceneMu.Unlock()
}

func (s *session) storeTeamHeadHP(hp, maxHP int32) {
	if s == nil || maxHP <= 0 {
		return
	}
	if hp < 0 {
		hp = 0
	} else if hp > maxHP {
		hp = maxHP
	}
	s.teamHeadHP.Store(uint64(uint32(maxHP))<<32 | uint64(uint32(hp)))
}

func (s *session) updateTeamHeadHP(hp int32) {
	if s == nil {
		return
	}
	for {
		packed := s.teamHeadHP.Load()
		if packed == 0 {
			return
		}
		maxHP := int32(uint32(packed >> 32))
		if hp < 0 {
			hp = 0
		} else if hp > maxHP {
			hp = maxHP
		}
		next := uint64(uint32(maxHP))<<32 | uint64(uint32(hp))
		if s.teamHeadHP.CompareAndSwap(packed, next) {
			return
		}
	}
}

func (s *session) loadTeamHeadHP() (hp, maxHP int32, ok bool) {
	if s == nil {
		return 0, 0, false
	}
	packed := s.teamHeadHP.Load()
	if packed == 0 {
		return 0, 0, false
	}
	return int32(uint32(packed)), int32(uint32(packed >> 32)), true
}

func (s *session) markMapChange() uint64 {
	if s == nil {
		return 0
	}
	s.mapChangeMu.Lock()
	s.mapChangeSeq++
	s.mapChangeAt = time.Now()
	seq := s.mapChangeSeq
	s.mapChangeMu.Unlock()
	return seq
}

func (s *session) mapChangedWithin(d time.Duration) bool {
	if s == nil {
		return false
	}
	s.mapChangeMu.Lock()
	at := s.mapChangeAt
	s.mapChangeMu.Unlock()
	return !at.IsZero() && time.Since(at) < d
}

func (s *session) isCurrentMapChange(seq uint64) bool {
	if s == nil {
		return false
	}
	s.mapChangeMu.Lock()
	defer s.mapChangeMu.Unlock()
	return s.mapChangeSeq == seq
}

// markMapReady completes one delayed scene startup exactly once. A newer map
// change invalidates an older callback through mapChangeSeq.
func (s *session) markMapReady(seq uint64) bool {
	if s == nil {
		return false
	}
	s.mapChangeMu.Lock()
	defer s.mapChangeMu.Unlock()
	if s.mapChangeSeq != seq || s.mapReadySeq == seq {
		return false
	}
	s.mapReadySeq = seq
	return true
}

func (s *session) currentMapSceneVersion() (uint64, bool) {
	if s == nil {
		return 0, false
	}
	s.mapChangeMu.Lock()
	defer s.mapChangeMu.Unlock()
	return s.mapChangeSeq, s.mapReadySeq == s.mapChangeSeq
}

func (s *session) markMapStartupComplete(seq uint64) bool {
	if s == nil {
		return false
	}
	s.mapChangeMu.Lock()
	defer s.mapChangeMu.Unlock()
	if s.mapChangeSeq != seq || s.mapReadySeq != seq {
		return false
	}
	s.mapStartupSeq = seq
	return true
}

func (s *session) isCurrentMapStartupComplete(seq uint64) bool {
	if s == nil {
		return false
	}
	s.mapChangeMu.Lock()
	defer s.mapChangeMu.Unlock()
	return s.mapChangeSeq == seq && s.mapStartupSeq == seq
}

func (s *session) currentMapStartupVersion() (uint64, bool) {
	if s == nil {
		return 0, false
	}
	s.mapChangeMu.Lock()
	defer s.mapChangeMu.Unlock()
	return s.mapChangeSeq, s.mapStartupSeq == s.mapChangeSeq
}

func (s *session) isCurrentMapReady(seq uint64) bool {
	if s == nil {
		return false
	}
	s.mapChangeMu.Lock()
	defer s.mapChangeMu.Unlock()
	return s.mapChangeSeq == seq && s.mapReadySeq == seq
}

func (s *session) nextBattleUnitBase() int64 {
	base := int64(1000000000) + s.battleSequence*1000
	s.battleSequence++
	return base
}

// currentPosition returns the interpolated position of an in-flight movement.
// The client snaps to the X/Y carried by each path packet before starting the
// new path, so reusing the previous destination here causes visible teleports.
func (s *session) currentPosition(now time.Time, speed float32) (float32, float32) {
	if s == nil {
		return 0, 0
	}
	s.movementMu.Lock()
	defer s.movementMu.Unlock()
	return s.currentPositionLocked(now, speed)
}

func (s *session) currentPositionLocked(now time.Time, speed float32) (float32, float32) {
	if !s.moving || speed <= 0 {
		return s.x, s.y
	}
	dx := float64(s.moveTargetX - s.moveStartX)
	dy := float64(s.moveTargetY - s.moveStartY)
	distance := math.Hypot(dx, dy)
	if distance <= 0.0001 {
		s.x, s.y = s.moveTargetX, s.moveTargetY
		s.moving = false
		return s.x, s.y
	}
	travelled := now.Sub(s.moveStartedAt).Seconds() * float64(speed)
	if travelled <= 0 {
		return s.moveStartX, s.moveStartY
	}
	if travelled >= distance {
		s.x, s.y = s.moveTargetX, s.moveTargetY
		s.moving = false
		return s.x, s.y
	}
	ratio := travelled / distance
	s.x = s.moveStartX + float32(dx*ratio)
	s.y = s.moveStartY + float32(dy*ratio)
	return s.x, s.y
}

func (s *session) beginMovement(now time.Time, targetX, targetY, speed float32) (float32, float32) {
	if s == nil {
		return 0, 0
	}
	s.movementMu.Lock()
	defer s.movementMu.Unlock()
	startX, startY := s.currentPositionLocked(now, speed)
	// Native MoveComponent derives facing from the x component of the path.
	if targetX-startX > 0.0001 {
		s.yAngle = 0
	} else if targetX-startX < -0.0001 {
		s.yAngle = 180
	}
	s.moveStartX, s.moveStartY = startX, startY
	s.moveTargetX, s.moveTargetY = targetX, targetY
	s.moveStartedAt = now
	s.moving = math.Hypot(float64(targetX-startX), float64(targetY-startY)) > 0.0001
	if !s.moving {
		s.x, s.y = targetX, targetY
	}
	return startX, startY
}

func (s *session) resetMovement(x, y float32) {
	if s == nil {
		return
	}
	s.movementMu.Lock()
	defer s.movementMu.Unlock()
	s.resetMovementLocked(x, y)
}

func (s *session) resetMovementLocked(x, y float32) {
	s.x, s.y = x, y
	s.moveStartX, s.moveStartY = x, y
	s.moveTargetX, s.moveTargetY = x, y
	s.moveStartedAt = time.Time{}
	s.moving = false
}

func (s *session) stopMovement(now time.Time, speed float32) (float32, float32) {
	if s == nil {
		return 0, 0
	}
	s.movementMu.Lock()
	defer s.movementMu.Unlock()
	x, y := s.currentPositionLocked(now, speed)
	s.resetMovementLocked(x, y)
	return x, y
}

func (s *session) movementTarget() (x, y float32, moving bool) {
	if s == nil {
		return 0, 0, false
	}
	s.movementMu.Lock()
	defer s.movementMu.Unlock()
	return s.moveTargetX, s.moveTargetY, s.moving
}

const (
	sessHandshake = iota // 已建 KCP 连接，未登录
	sessLogged           // C2G_LoginGate 成功
	sessInGame           // C2G_EnterGame 成功
)

func newSession() *session {
	return &session{
		state:          sessHandshake,
		skillCooldowns: make(map[int32]time.Time),
		store:          make(map[int32]*bagItem),
		friends:        make(map[int64]*friendInfo),
		friendReqFrom:  make(map[int64]bool),
		friendReqTo:    make(map[int64]bool),
		transBonus:     make(map[int32]float32),
		hp:             -1, // 未初始化血蓝（battleHP() 视为满血）
		mp:             -1,
	}
}

// tryStartSkillCooldown 检查并登记技能冷却。每个会话的请求由单连接读循环串行处理。
func (s *session) tryStartSkillCooldown(skillID int32, now time.Time, cd time.Duration) (remaining time.Duration, ok bool) {
	if s == nil {
		return 0, false
	}
	if readyAt, exists := s.skillCooldowns[skillID]; exists && now.Before(readyAt) {
		return readyAt.Sub(now), false
	}
	if s.skillCooldowns == nil {
		s.skillCooldowns = make(map[int32]time.Time)
	}
	if cd > 0 {
		s.skillCooldowns[skillID] = now.Add(cd)
	} else {
		delete(s.skillCooldowns, skillID)
	}
	return 0, true
}
