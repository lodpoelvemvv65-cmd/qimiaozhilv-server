package main

import (
	"math"
	"sync"
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
	// Guards delayed scene initialisation after a map change.
	mapChangeMu  sync.Mutex
	mapChangeSeq uint64
	mapChangeAt  time.Time

	state      int               // 登录状态
	accountID  int64             // 账号 id（登录后填充）
	playerID   int64             // 角色 id（LoginGate 后填充）
	account    string            // 账号名
	name       string            // 角色名（loadData 时填充，升级重推 SendUnitInfo 用）
	jobID      int32             // 职业
	skinID     int32             // 皮肤
	level      int32             // 角色等级
	exp        int64             // 当前经验（升级用，expNeed=100*Lv³）
	energy     int32             // 行动值（NumericType 1039，主线普通战斗每次消耗 3）
	trans      int32             // 转生等级 0-3（NumericType 1029；TransmigrationAddConfig 职业×10+转生等级）
	transBonus map[int32]float32 // 转生属性加成（NumericType → 加值，保留百分比小数）
	// 家族系统（family.go）
	familyID                 int64        // 所属家族 id（0=无家族）
	familyContribute         int32        // 家族贡献
	personalContribute       int32        // 个人贡献
	charPoint                int32        // 剩余属性点（升级每级 +1）
	skillPoint               int32        // 剩余技能点（角色升级每级 +1；仅首次学习技能消耗 1）
	strAdd                   int32        // 手动加点：力量
	qukAdd                   int32        // 手动加点：敏捷
	spiAdd                   int32        // 手动加点：精神
	wimAdd                   int32        // 手动加点：智力
	battle                   *battleState // 当前战斗状态（进入主线战斗后填充）
	mapID                    int32        // 当前地图（mapId = sceneId*100 + layer，ChangeMap 时更新）
	x                        float32      // 服务端估算的当前坐标
	y                        float32      // 当前坐标
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
	// mainui_json 列持久化。新号默认槽 0 = 职业基础攻击。
	mainUISlots [9]mainUISlot
	// 当前场景已推送的字段怪 id（mapmonster.go；换图时用 20321 清理）
	fieldMonsterIDs       []int64
	fieldMonsterConfigIDs map[int64]int32 // 字段怪单位 id -> MapMonsterConfig._id
	// 当前场景的 BOSS 字段怪单位 id（10010 世界 BOSS；BossDead 按各自 id 移除）
	fieldBossUnitID int64
	// Native world-boss click handshake: 20057 arms one 20059 challenge.
	bossChallengePending bool
	// 非战斗血蓝（equip.go）：战斗中读 battleState，战斗外持久在此；
	// 初始 = 满值，战斗结束写回残血 → 吃药可恢复（主界面血条刷新）。
	// -1 = 未初始化（battleHP() 视为满血）；0 = 真实残血。
	hp int32
	mp int32
	// 服务端权威技能冷却。客户端的 CD 动画只用于显示，不能作为施放校验。
	skillCooldowns        map[int32]time.Time // skillID -> 下次可施放时间
	autoBattle            bool                // 当前是否启用战斗内自动施放
	idleBattle            bool                // 当前战斗由挂机入口 20087 发起
	autoBattleEpoch       uint64              // 每次切换递增，使旧 worker 立即失效
	autoBattleNextCastAt  time.Time           // next shared automatic-combat action
	autoBattleSkillCursor int                 // round-robin cursor in the saved automatic-skill list
	// 经验倍率 buff（经验卡 EffectType 6/7/16：ExpRange × ContinuedSeconds 毫秒）。
	expMult      float64                   // 当前经验倍率（1.0 = 无 buff）
	expMultUntil int64                     // 到期 unix 毫秒（0 = 无 buff）
	itemBuffMu   sync.Mutex                // 保护自动战斗读取、物品使用与存档之间的状态并发
	itemBuffs    map[int32]*activeItemBuff // 持久化道具状态，按互斥类别保存
	// 魔法球 buff（EffectType 2/3：100万生命/精力魔法球，"战斗结束后自动回满，存储N"）。
	// 使用后左上角状态栏显示 buff 时间；战斗结束（胜利/败北/退出）时若活跃 → 自动回满。
	hpBallUntil int64 // 到期 unix 毫秒（0 = 无）
	hpBallCap   int32 // 存储容量（Capacity；缺省 = 满血）
	mpBallUntil int64
	mpBallCap   int32

	// 任务系统（EnterGame 时从 DB 加载）
	tasks     map[int32]int32 // taskID → TaskState（0/1/2/3）
	killCount map[int32]int32 // 怪物ID → 已击杀数（KillSpecial 任务计数）

	// 背包系统（EnterGame 时从 DB 加载，初始按职业发 YOYO 四件套）
	bag      map[int32]*bagItem // 背包格子 Index → 物品（客户端固定 0..47）
	worn     map[int32]*bagItem // 穿戴槽位（EquipBase.Type）→ 装备（8=背心 9=裤 10=手套 11=鞋 等）
	starSoul *starSoulBag       // 星魂实例、12 个穿戴槽和套装状态

	// 货币（state_extra.go；DB 列 coin/yuan_bao/voucher，NumericType 1028/1024/1025）
	coin    int64
	yuanBao int64
	voucher int64

	// 仓库（store.go 扩展；store_json / store_coin 列）
	store     map[int32]*bagItem // 仓库格子 Index → 物品
	storeCoin int64              // 仓库铜币

	// 宠物（pet.go；pet_json 列）
	pet *petState

	// 邮件（mail.go；mails_json 列）
	mails   []*mailMsg
	mailSeq int64 // 邮件 id 发生器（持久化后恢复用）

	// 好友（friend.go；friends_json 列）
	socialMu      sync.Mutex // 好友、好友申请的跨会话同步
	friends       map[int64]*friendInfo
	friendReqFrom map[int64]bool // 待处理的好友申请（对方 Id）
	friendReqTo   map[int64]bool // 我发出的申请（对方 Id）

	// 组队（team.go；仅内存，Server.teams 全服表 + session.teamID 指针）
	teamID int64

	// 签到（signin.go；signin_json 列）
	signin *signinState

	// 答题小游戏的单局瞬时状态；累计成绩保存在 signin_json。
	quizQuestionID  int32
	quizStartedAt   time.Time
	quizRunScore    int32
	quizRunTimeMS   int64
	quizStreak      int32
	quizAnswerCount int32
	quizAnswered    bool
	lastNPCID       int32
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

func (s *session) isCurrentMapChange(mapID int32, seq uint64) bool {
	if s == nil {
		return false
	}
	s.mapChangeMu.Lock()
	defer s.mapChangeMu.Unlock()
	return s.mapChangeSeq == seq && s.mapID == mapID
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
	if s == nil || !s.moving || speed <= 0 {
		if s == nil {
			return 0, 0
		}
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
	startX, startY := s.currentPosition(now, speed)
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
	s.x, s.y = x, y
	s.moveStartX, s.moveStartY = x, y
	s.moveTargetX, s.moveTargetY = x, y
	s.moveStartedAt = time.Time{}
	s.moving = false
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
