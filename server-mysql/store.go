package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	"mhqserver/internal/mysqlschema"
)

var (
	ErrPlayerNameExists     = errors.New("player name already exists")
	ErrPlayerAlreadyExists  = errors.New("account already has a player")
	ErrPlayerIDMismatch     = errors.New("player id does not match reservation")
	ErrInsufficientVoucher  = errors.New("insufficient voucher")
	ErrFriendRequestMissing = errors.New("friend request missing")
)

// store.go：MySQL 存取层。
//
// 职责：
//   - 建库建表（幂等）：accounts + players
//   - 注册/登录查询、角色创建/查询
//   - 登录会话 Key 签发与校验（内存缓存，跨 KCP 连接复用）
//
// 说明：
//   - 密码当前按明文存储（08 文档 §5 简化决策），上线前应改哈希。
//   - 全部方法线程安全（database/sql 连接池 + keys 的读写锁）。

// Player 玩家角色记录。
type Player struct {
	ID         int64
	AccountID  int64
	Name       string
	JobID      int32
	SkinID     int32
	TitleID    int32
	Level      int32
	Exp        int64
	Energy     int32
	MapID      int32
	PosX       float32
	PosY       float32
	CurrentHP  int32
	CurrentMP  int32
	CharPoint  int32
	SkillPoint int32
	StrAdd     int32
	QukAdd     int32
	SpiAdd     int32
	WimAdd     int32
	PhyAdd     int32
	StaAdd     int32
	AutoBattle int32 // 主界面自动战斗开关（0=关，1=开，-1=旧存档待迁移）

	// 新增模块（state_extra.go）
	Coin        int64 // 铜币
	YuanBao     int64 // 元宝
	Voucher     int64 // 兑换券
	Honor       int64 // 荣誉（NumericType 1030）
	PVPCurrency int64 // 竞技币（NumericType 1040）
	StoreCoin   int64 // 仓库铜币
	StorePages  int32 // 仓库页数（客户端每页 60 格）
	Trans       int32 // 转生等级 0-3（TransmigrationAddConfig 职业×10+转生等级）

	// 家族系统（family.go）
	FamilyID           int64 // 所属家族 id（0=无家族）
	FamilyContribute   int32 // 家族贡献
	PersonalContribute int32 // 个人贡献
	FriendReqFrom      map[int64]bool
	FriendReqTo        map[int64]bool
	PVPScore           int32 // lightweight ranking snapshot from player_activity
	// Relations is loaded exclusively from normalized MySQL tables.
	Relations playerRelations
}

type quizScoreRecord struct {
	ID     int64
	Score  int32
	Name   string
	Level  int32
	TimeMS int64
}

type playerSocialProfile struct {
	ID    int64
	Name  string
	JobID int32
	Level int32
}

func (st *Store) findPlayerSocial(query string, arg interface{}) (playerSocialProfile, error) {
	var profile playerSocialProfile
	if st == nil || st.db == nil {
		return profile, sql.ErrNoRows
	}
	err := st.db.QueryRow(query, arg).Scan(&profile.ID, &profile.Name, &profile.JobID, &profile.Level)
	return profile, err
}

func (st *Store) FindPlayerSocialByName(name string) (playerSocialProfile, error) {
	return st.findPlayerSocial(`SELECT id, name, job_id, level FROM players WHERE name = ?`, name)
}

func (st *Store) FindPlayerSocialByID(playerID int64) (playerSocialProfile, error) {
	return st.findPlayerSocial(`SELECT id, name, job_id, level FROM players WHERE id = ?`, playerID)
}

func (st *Store) AreFriends(playerID, friendID int64) (bool, error) {
	if st == nil || st.db == nil {
		return false, sql.ErrNoRows
	}
	var count int
	err := st.db.QueryRow(`SELECT COUNT(*) FROM player_friends WHERE player_id = ? AND friend_id = ?`, playerID, friendID).Scan(&count)
	return count > 0, err
}

// AcceptFriendship atomically consumes a pending request and writes both
// directional friend rows. The target/requester may be offline; the next
// login loads the durable rows into its session cache.
func (st *Store) AcceptFriendship(acceptorID, requesterID int64) error {
	if st == nil || st.db == nil {
		return sql.ErrNoRows
	}
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var requester, acceptor playerSocialProfile
	if err := tx.QueryRow(`SELECT id, name, job_id, level FROM players WHERE id = ? FOR UPDATE`, requesterID).
		Scan(&requester.ID, &requester.Name, &requester.JobID, &requester.Level); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT id, name, job_id, level FROM players WHERE id = ? FOR UPDATE`, acceptorID).
		Scan(&acceptor.ID, &acceptor.Name, &acceptor.JobID, &acceptor.Level); err != nil {
		return err
	}
	var pending int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM player_friend_requests WHERE requester_id = ? AND target_id = ?`, requesterID, acceptorID).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return ErrFriendRequestMissing
	}
	lastLogin := time.Now().Unix()
	if _, err := tx.Exec(`INSERT INTO player_friends
		(player_id, friend_id, name, job_id, level, last_login) VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE name = VALUES(name), job_id = VALUES(job_id), level = VALUES(level), last_login = VALUES(last_login)`,
		acceptor.ID, requester.ID, requester.Name, jobTypeOf(requester.JobID), requester.Level, lastLogin); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO player_friends
		(player_id, friend_id, name, job_id, level, last_login) VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE name = VALUES(name), job_id = VALUES(job_id), level = VALUES(level), last_login = VALUES(last_login)`,
		requester.ID, acceptor.ID, acceptor.Name, jobTypeOf(acceptor.JobID), acceptor.Level, lastLogin); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM player_friend_requests WHERE requester_id = ? AND target_id = ?`, requesterID, acceptorID); err != nil {
		return err
	}
	return tx.Commit()
}

func (st *Store) DeleteFriendship(playerID, friendID int64) error {
	if st == nil || st.db == nil {
		return sql.ErrNoRows
	}
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM player_friends WHERE (player_id = ? AND friend_id = ?) OR (player_id = ? AND friend_id = ?)`,
		playerID, friendID, friendID, playerID); err != nil {
		return err
	}
	return tx.Commit()
}

// keyEntry 一条已签发未消耗的登录凭证。
type keyEntry struct {
	accountID int64
	issuedAt  time.Time
}

// Store MySQL 存取层。
type Store struct {
	db          *sql.DB
	description string
	mu          sync.RWMutex
	persistMu   sync.Mutex         // serializes normalized graph writes to avoid InnoDB secondary-index deadlocks
	keys        map[int64]keyEntry // Key → 账号（Key 由 IssueKey 生成）
	vouchers    map[int64]keyEntry // Voucher Key → 账号（客户端断线重连，一次性轮换）
	cache       *RedisCache
}

// OpenStore 打开（必要时创建）数据库并执行建表 SQL。
func OpenStore(dsn string) (*Store, error) {
	db, description, err := mysqlschema.Open(dsn)
	if err != nil {
		return nil, err
	}
	return &Store{
		db: db, description: description,
		keys: make(map[int64]keyEntry), vouchers: make(map[int64]keyEntry),
	}, nil
}

func (st *Store) Close() { st.db.Close() }

func (st *Store) Description() string { return st.description }

func (st *Store) AttachCache(cache *RedisCache) { st.cache = cache }

func isDuplicateKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func isRetryableTxError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == 1205 || mysqlErr.Number == 1213
}

// 账号表操作

// FindAccount 按账号名查密码；返回 (id, password, err)，未找到时 err=sql.ErrNoRows。
func (st *Store) FindAccount(account string) (int64, string, error) {
	var id int64
	var pwd string
	err := st.db.QueryRow(
		`SELECT id, password FROM accounts WHERE account = ?`, account,
	).Scan(&id, &pwd)
	return id, pwd, err
}

// CreateAccount 注册账号。account 已存在时返回 ErrAccountExists（由调用方判断）。
func (st *Store) CreateAccount(account, password string) (int64, error) {
	res, err := st.db.Exec(
		`INSERT INTO accounts (account, password, create_time) VALUES (?, ?, ?)`,
		account, password, time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// 角色表操作

// FirstPlayer 取某账号的第一个角色（当前每个账号只建一个角色）。
func (st *Store) FirstPlayer(accountID int64) (*Player, error) {
	var p Player
	err := st.db.QueryRow(
		`SELECT id, account_id, name, job_id, skin_id, title_id, level, exp, energy, map_id, pos_x, pos_y, current_hp, current_mp,
		        char_point, skill_point, str_add, quk_add, spi_add, wim_add, phy_add, sta_add, auto_battle,
		        coin, yuan_bao, voucher, honor, pvp_currency, store_coin, store_pages, trans,
		        family_id, family_contribute, personal_contribute
		   FROM players WHERE account_id = ? ORDER BY id LIMIT 1`, accountID,
	).Scan(&p.ID, &p.AccountID, &p.Name, &p.JobID, &p.SkinID, &p.TitleID, &p.Level, &p.Exp, &p.Energy,
		&p.MapID, &p.PosX, &p.PosY, &p.CurrentHP, &p.CurrentMP,
		&p.CharPoint, &p.SkillPoint, &p.StrAdd, &p.QukAdd, &p.SpiAdd, &p.WimAdd, &p.PhyAdd, &p.StaAdd,
		&p.AutoBattle, &p.Coin, &p.YuanBao, &p.Voucher, &p.Honor, &p.PVPCurrency, &p.StoreCoin, &p.StorePages, &p.Trans,
		&p.FamilyID, &p.FamilyContribute, &p.PersonalContribute)
	if err != nil {
		return nil, err
	}
	if err := st.loadPlayerRelations(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// findPlayerByIDForGM loads a complete offline snapshot for the private GM
// control plane. The caller serializes this path with the GM operation lock and
// must persist the resulting session through SavePlayerState.
func (st *Store) findPlayerByIDForGM(playerID int64) (*Player, error) {
	if st == nil || st.db == nil || playerID <= 0 {
		return nil, sql.ErrNoRows
	}
	var p Player
	err := st.db.QueryRow(`SELECT id, account_id, name, job_id, skin_id, title_id, level, exp, energy,
		map_id, pos_x, pos_y, current_hp, current_mp, char_point, skill_point, str_add, quk_add, spi_add, wim_add, phy_add, sta_add,
		auto_battle, coin, yuan_bao, voucher, honor, pvp_currency, store_coin, store_pages, trans,
		family_id, family_contribute, personal_contribute FROM players WHERE id = ?`, playerID).Scan(
		&p.ID, &p.AccountID, &p.Name, &p.JobID, &p.SkinID, &p.TitleID, &p.Level, &p.Exp, &p.Energy,
		&p.MapID, &p.PosX, &p.PosY, &p.CurrentHP, &p.CurrentMP, &p.CharPoint, &p.SkillPoint, &p.StrAdd, &p.QukAdd, &p.SpiAdd, &p.WimAdd,
		&p.PhyAdd, &p.StaAdd, &p.AutoBattle, &p.Coin, &p.YuanBao, &p.Voucher, &p.Honor, &p.PVPCurrency,
		&p.StoreCoin, &p.StorePages, &p.Trans, &p.FamilyID, &p.FamilyContribute, &p.PersonalContribute)
	if err != nil {
		return nil, err
	}
	if err := st.loadPlayerRelations(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPlayers returns complete player snapshots for rankings. Ranking values
// depend on equipment, star soul and persisted activity state, so selecting
// only name/level would disagree with the character panel.
func (st *Store) ListPlayers(limit int) ([]*Player, error) {
	query := `SELECT id, account_id, name, job_id, skin_id, title_id, level, exp, energy, map_id, pos_x, pos_y, current_hp, current_mp,
		        char_point, skill_point, str_add, quk_add, spi_add, wim_add, phy_add, sta_add, auto_battle,
		        coin, yuan_bao, voucher, honor, pvp_currency, store_coin, store_pages, trans,
		        family_id, family_contribute, personal_contribute
		   FROM players ORDER BY id`
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = st.db.Query(query+` LIMIT ?`, limit)
	} else {
		rows, err = st.db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	players := make([]*Player, 0)
	for rows.Next() {
		var p Player
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Name, &p.JobID, &p.SkinID, &p.TitleID, &p.Level, &p.Exp, &p.Energy,
			&p.MapID, &p.PosX, &p.PosY, &p.CurrentHP, &p.CurrentMP,
			&p.CharPoint, &p.SkillPoint, &p.StrAdd, &p.QukAdd, &p.SpiAdd, &p.WimAdd, &p.PhyAdd, &p.StaAdd,
			&p.AutoBattle, &p.Coin, &p.YuanBao, &p.Voucher, &p.Honor, &p.PVPCurrency, &p.StoreCoin, &p.StorePages, &p.Trans,
			&p.FamilyID, &p.FamilyContribute, &p.PersonalContribute); err != nil {
			return nil, err
		}
		players = append(players, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, player := range players {
		if err := st.loadPlayerRelations(player); err != nil {
			return nil, err
		}
	}
	return players, nil
}

// ListPlayersForRanking loads the exact state used by all ranking formulas.
// Base player/pet data and ranking-only equipment/star-soul relations are
// loaded in a fixed number of batch queries, avoiding per-player query fanout.
func (st *Store) ListPlayersForRanking(limit int) ([]*Player, error) {
	query := `SELECT p.id, p.account_id, p.name, p.job_id, p.skin_id, p.title_id, p.level, p.exp,
		                 p.energy, p.map_id, p.pos_x, p.pos_y, p.current_hp, p.current_mp, p.char_point, p.skill_point,
	                 p.str_add, p.quk_add, p.spi_add, p.wim_add, p.phy_add, p.sta_add, p.auto_battle,
	                 p.coin, p.yuan_bao, p.voucher, p.honor, p.pvp_currency, p.store_coin, p.store_pages, p.trans,
	                 p.family_id, p.family_contribute, p.personal_contribute,
	                 COALESCE(pa.pvp_score, 0),
	                 pp.pet_id, pp.level, pp.exp, pp.intimacy, pp.name, pp.is_show,
	                 pp.active, pp.eat_count, pp.pet_state, pp.action_end, pp.last_day, pp.rewarded
	            FROM players p
	            LEFT JOIN player_activity pa ON pa.player_id = p.id
	            LEFT JOIN player_pets pp ON pp.player_id = p.id
           ORDER BY p.id`
	if limit > 0 {
		query += ` LIMIT ?`
	}
	args := []interface{}{}
	if limit > 0 {
		args = append(args, limit)
	}
	rows, err := st.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	players := make([]*Player, 0)
	for rows.Next() {
		var p Player
		var pet petState
		var petID, petLevel, petExp, petIntimacy, petActive, petEatCount, petStateValue, petActionEnd sql.NullInt64
		var petIsShow, petRewarded sql.NullBool
		var petName, petLastDay sql.NullString
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Name, &p.JobID, &p.SkinID, &p.TitleID, &p.Level, &p.Exp,
			&p.Energy, &p.MapID, &p.PosX, &p.PosY, &p.CurrentHP, &p.CurrentMP, &p.CharPoint, &p.SkillPoint,
			&p.StrAdd, &p.QukAdd, &p.SpiAdd, &p.WimAdd, &p.PhyAdd, &p.StaAdd, &p.AutoBattle,
			&p.Coin, &p.YuanBao, &p.Voucher, &p.Honor, &p.PVPCurrency, &p.StoreCoin, &p.StorePages, &p.Trans,
			&p.FamilyID, &p.FamilyContribute, &p.PersonalContribute,
			&p.PVPScore,
			&petID, &petLevel, &petExp, &petIntimacy, &petName, &petIsShow,
			&petActive, &petEatCount, &petStateValue, &petActionEnd, &petLastDay, &petRewarded); err != nil {
			return nil, err
		}
		p.Relations = playerRelations{
			skills: make(map[int32]int32), tasks: make(map[int32]int32), killCounts: make(map[int32]int32),
			bag: make(map[int32]*bagItem), worn: make(map[int32]*bagItem), store: make(map[int32]*bagItem),
			friends: make(map[int64]*friendInfo), itemBuffs: make(map[int32]*activeItemBuff),
			mainUI: [mainUISlotCount]mainUISlot{}, starSoul: newStarSoulBag(),
		}
		if petID.Valid {
			pet.PetId = int32(petID.Int64)
			pet.Level = int32(petLevel.Int64)
			pet.Exp = int32(petExp.Int64)
			pet.Intimacy = int32(petIntimacy.Int64)
			pet.Name = petName.String
			pet.IsShow = petIsShow.Bool
			pet.Active = int32(petActive.Int64)
			pet.EatCount = int32(petEatCount.Int64)
			pet.PetState = int32(petStateValue.Int64)
			pet.ActionEnd = petActionEnd.Int64
			pet.LastDay = petLastDay.String
			pet.Rewarded = petRewarded.Bool
			p.Relations.pet = &pet
		}
		players = append(players, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := st.loadRankingRelations(players); err != nil {
		return nil, err
	}
	return players, nil
}

func (st *Store) CountPlayers() (int, error) {
	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM players`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ListQuizScores reads only the columns displayed by QuestRankUI. Sorting and
// limiting in MySQL keeps this request independent of each player's relation
// graph, which is especially important once the server has many characters.
func (st *Store) ListQuizScores(limit int) ([]quizScoreRecord, error) {
	query := `SELECT p.id, COALESCE(pa.quiz_score, 0), p.name, p.level,
	                 COALESCE(pa.quiz_time_ms, 0)
	            FROM players p
	            LEFT JOIN player_activity pa ON pa.player_id = p.id
	        ORDER BY COALESCE(pa.quiz_score, 0) DESC,
	                 COALESCE(pa.quiz_time_ms, 0) ASC,
	                 p.id ASC`
	args := []interface{}{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := st.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]quizScoreRecord, 0)
	for rows.Next() {
		var record quizScoreRecord
		if err := rows.Scan(&record.ID, &record.Score, &record.Name, &record.Level, &record.TimeMS); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// SavePlayerStorePages persists warehouse capacity.
func (st *Store) SavePlayerStorePages(playerID int64, pages int32) error {
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	_, err := st.db.Exec(`UPDATE players SET store_pages = ? WHERE id = ?`, pages, playerID)
	return err
}

// SavePlayerCurrencies persists the two dedicated non-bag currencies. They
// are kept separate from SavePlayerState's legacy argument list so existing
// migration/test callers remain source-compatible.
func (st *Store) SavePlayerCurrencies(playerID int64, honor, pvpCurrency int64) error {
	if st == nil || st.db == nil || playerID <= 0 {
		return sql.ErrNoRows
	}
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	_, err := st.db.Exec(`UPDATE players SET honor = ?, pvp_currency = ? WHERE id = ?`, honor, pvpCurrency, playerID)
	return err
}

// SavePlayerResources persists the exact out-of-battle resource snapshot.
// The -1 sentinel is reserved for a role that has not initialized HP/MP yet.
func (st *Store) SavePlayerResources(playerID int64, hp, mp int32) error {
	if st == nil || st.db == nil || playerID <= 0 {
		return sql.ErrNoRows
	}
	if hp < -1 {
		hp = -1
	}
	if mp < -1 {
		mp = -1
	}
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	_, err := st.db.Exec(`UPDATE players SET current_hp = ?, current_mp = ? WHERE id = ?`, hp, mp, playerID)
	return err
}

// SavePlayerState is the runtime persistence path. It accepts the typed
// normalized state directly and writes only relational rows.
func (st *Store) SavePlayerState(playerID int64, jobID, level int32, exp int64, energy int32,
	mapID int32, posX, posY float32,
	charPoint, skillPoint, strAdd, qukAdd, spiAdd, wimAdd, phyAdd, staAdd int32,
	autoBattle bool, coin, yuanBao, voucher, storeCoin int64,
	trans, skinID, titleID int32, familyID int64, familyContribute, personalContribute int32,
	relations playerRelations, starSoul *starSoulBag) error {
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*40) * time.Millisecond)
		}
		if err := st.savePlayerStateOnce(playerID, jobID, level, exp, energy, mapID, posX, posY,
			charPoint, skillPoint, strAdd, qukAdd, spiAdd, wimAdd, phyAdd, staAdd, autoBattle,
			coin, yuanBao, voucher, storeCoin, trans, skinID, titleID, familyID, familyContribute,
			personalContribute, relations, starSoul); err == nil {
			return nil
		} else {
			lastErr = err
			if !isRetryableTxError(err) {
				return err
			}
		}
	}
	return lastErr
}

func (st *Store) savePlayerStateOnce(playerID int64, jobID, level int32, exp int64, energy int32,
	mapID int32, posX, posY float32,
	charPoint, skillPoint, strAdd, qukAdd, spiAdd, wimAdd, phyAdd, staAdd int32,
	autoBattle bool, coin, yuanBao, voucher, storeCoin int64,
	trans, skinID, titleID int32, familyID int64, familyContribute, personalContribute int32,
	relations playerRelations, starSoul *starSoulBag) error {
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE players SET job_id = ?, level = ?, exp = ?, energy = ?, map_id = ?, pos_x = ?, pos_y = ?,
		        char_point = ?, skill_point = ?, str_add = ?, quk_add = ?,
		        spi_add = ?, wim_add = ?, phy_add = ?, sta_add = ?, auto_battle = ?, coin = ?, yuan_bao = ?, voucher = ?,
		        store_coin = ?, trans = ?, skin_id = ?, title_id = ?,
		        family_id = ?, family_contribute = ?, personal_contribute = ? WHERE id = ?`,
		jobID, level, exp, energy, mapID, posX, posY, charPoint, skillPoint, strAdd, qukAdd, spiAdd, wimAdd, phyAdd, staAdd,
		autoBattle, coin, yuanBao, voucher, storeCoin, trans, skinID, titleID,
		familyID, familyContribute, personalContribute, playerID,
	); err != nil {
		return err
	}
	if err := savePlayerRelationsTx(tx, playerID, relations); err != nil {
		return err
	}
	if err := saveStarSoulTx(tx, playerID, starSoul); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// ReservePlayerID gives an account its durable character identity before the
// role exists. The native client stores this LoginGate value as MyId and sends
// it back in C2R_CreateRole, so it must remain stable across reconnects.
func (st *Store) ReservePlayerID(accountID int64) (int64, error) {
	res, err := st.db.Exec(`INSERT INTO player_id_reservations (account_id) VALUES (?)
		ON DUPLICATE KEY UPDATE player_id = LAST_INSERT_ID(player_id)`, accountID)
	if err != nil {
		return 0, err
	}
	playerID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if playerID <= 0 {
		return 0, ErrPlayerIDMismatch
	}
	return playerID, nil
}

// CreatePlayer 创建角色，返回角色 id。所有调用都经过同一预留序列，避免
// players 与 LoginGate 各自分配 id 后出现客户端 MyId 不一致。
func (st *Store) CreatePlayer(accountID int64, name string, jobID, skinID int32) (int64, error) {
	playerID, err := st.ReservePlayerID(accountID)
	if err != nil {
		return 0, err
	}
	if err := st.CreatePlayerWithID(accountID, playerID, name, jobID, skinID); err != nil {
		return 0, err
	}
	return playerID, nil
}

// CreatePlayerWithID consumes the identity previously exposed by LoginGate.
func (st *Store) CreatePlayerWithID(accountID, playerID int64, name string, jobID, skinID int32) error {
	// 新角色默认皮肤 = 职业（1~4），避免客户端 SkinBase.Get(0) NRE（12 文档 §2.3）
	if skinID <= 0 || skinID > 4 {
		skinID = jobID
	}
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var reservedPlayerID int64
	if err := tx.QueryRow(`SELECT player_id FROM player_id_reservations
		WHERE account_id = ? FOR UPDATE`, accountID).Scan(&reservedPlayerID); err != nil {
		return err
	}
	if playerID <= 0 || playerID != reservedPlayerID {
		return ErrPlayerIDMismatch
	}
	var playerExists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM players WHERE account_id = ?`, accountID).Scan(&playerExists); err != nil {
		return err
	}
	if playerExists > 0 {
		return ErrPlayerAlreadyExists
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM players WHERE name = ?`, name).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return ErrPlayerNameExists
	}
	// 新角色代金券来自 Gameplay.new_role.voucher（默认 100，见 operationsconfig），
	// 不再依赖 players.voucher 的列默认值，这样运营改数量不需要动表结构。
	_, err = tx.Exec(
		`INSERT INTO players (id, account_id, name, job_id, skin_id, energy, store_pages, voucher) VALUES (?, ?, ?, ?, ?, 1000, 2, ?)`,
		playerID, accountID, name, jobID, skinID, gameplayNewRoleVoucher(),
	)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO player_name_claims (name, player_id) VALUES (?, ?)`, name, playerID); err != nil {
		if isDuplicateKeyError(err) {
			return ErrPlayerNameExists
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		if isDuplicateKeyError(err) {
			return ErrPlayerNameExists
		}
		return err
	}
	return nil
}

// RenamePlayer atomically claims the new name, deducts the voucher cost and
// refreshes denormalized social/market display names. No currency is consumed
// when the requested name is already owned by another player.
func (st *Store) RenamePlayer(playerID int64, name string, voucherCost int64) (int64, error) {
	st.persistMu.Lock()
	defer st.persistMu.Unlock()
	tx, err := st.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var oldName string
	var voucher int64
	if err := tx.QueryRow(`SELECT name, voucher FROM players WHERE id = ? FOR UPDATE`, playerID).Scan(&oldName, &voucher); err != nil {
		return 0, err
	}
	if oldName == name {
		return voucher, nil
	}
	if voucherCost < 0 || voucher < voucherCost {
		return voucher, ErrInsufficientVoucher
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM players WHERE name = ? AND id <> ?`, name, playerID).Scan(&exists); err != nil {
		return voucher, err
	}
	if exists > 0 {
		return voucher, ErrPlayerNameExists
	}
	if _, err := tx.Exec(`DELETE FROM player_name_claims WHERE player_id = ?`, playerID); err != nil {
		return voucher, err
	}
	if _, err := tx.Exec(`INSERT INTO player_name_claims (name, player_id) VALUES (?, ?)`, name, playerID); err != nil {
		if isDuplicateKeyError(err) {
			return voucher, ErrPlayerNameExists
		}
		return voucher, err
	}
	newVoucher := voucher - voucherCost
	if _, err := tx.Exec(`UPDATE players SET name = ?, voucher = ? WHERE id = ?`, name, newVoucher, playerID); err != nil {
		return voucher, err
	}
	if _, err := tx.Exec(`UPDATE family_members SET name = ? WHERE player_id = ?`, name, playerID); err != nil {
		return voucher, err
	}
	if _, err := tx.Exec(`UPDATE player_friends SET name = ? WHERE friend_id = ?`, name, playerID); err != nil {
		return voucher, err
	}
	if _, err := tx.Exec(`UPDATE consignment_items SET seller_name = ? WHERE seller_id = ?`, name, playerID); err != nil {
		return voucher, err
	}
	if err := tx.Commit(); err != nil {
		if isDuplicateKeyError(err) {
			return voucher, ErrPlayerNameExists
		}
		return voucher, err
	}
	return newVoucher, nil
}

// SetPlayerOnline 更新角色在线状态与最后登录时间。
func (st *Store) SetPlayerOnline(playerID int64, online bool) {
	if st.cache != nil {
		if err := st.cache.SetPlayerOnline(playerID, online); err != nil {
			log.Printf("set Redis online player=%d online=%v: %v", playerID, online, err)
		}
	}
}

func (st *Store) RefreshPlayerOnline(playerID int64) {
	if st.cache != nil {
		if err := st.cache.RefreshPlayerOnline(playerID); err != nil {
			log.Printf("refresh Redis online player=%d: %v", playerID, err)
		}
	}
}

// SetPlayerLastLogin 记录最后登录时间。
func (st *Store) SetPlayerLastLogin(playerID int64) {
	st.db.Exec(`UPDATE players SET last_login = ? WHERE id = ?`,
		time.Now().Unix(), playerID)
}

// 登录会话 Key

// IssueKey 为账号签发一个登录 Key（正 int64，内存缓存 5 分钟有效）。
// 由 C2R_Login 调用，客户端用它连接 LoginGate 时校验。
func (st *Store) IssueKey(accountID int64) int64 {
	key := randomLoginCredential()
	if st.cache != nil {
		if err := st.cache.PutLoginKey(key, accountID); err != nil {
			log.Printf("store Redis login key account=%d: %v", accountID, err)
			return 0
		}
		return key
	}
	st.mu.Lock()
	st.keys[key] = keyEntry{accountID: accountID, issuedAt: time.Now()}
	st.mu.Unlock()
	return key
}

// IssueVoucher signs a long-lived, one-use reconnect credential. It is kept
// separate from LoginGate keys because LoginGate consumes its key immediately.
func (st *Store) IssueVoucher(accountID int64) int64 {
	key := randomLoginCredential()
	if st.cache != nil {
		if err := st.cache.PutLoginVoucher(key, accountID); err != nil {
			log.Printf("store Redis login voucher account=%d: %v", accountID, err)
			return 0
		}
		return key
	}
	st.mu.Lock()
	st.vouchers[key] = keyEntry{accountID: accountID, issuedAt: time.Now()}
	st.mu.Unlock()
	return key
}

func randomLoginCredential() int64 {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		log.Printf("generate login credential: %v", err)
		return 0
	}
	key := int64(binary.LittleEndian.Uint64(buf[:]) & 0x7fffffffffffffff)
	if key == 0 {
		key = 1
	}
	return key
}

// ConsumeKey 校验并消耗 Key，返回绑定的账号 id。
// 校验失败（不存在/超时）返回 ok=false。
func (st *Store) ConsumeKey(key int64) (int64, bool) {
	if st.cache != nil {
		accountID, ok, err := st.cache.ConsumeLoginKey(key)
		if err != nil {
			log.Printf("consume Redis login key: %v", err)
			return 0, false
		}
		return accountID, ok
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	entry, ok := st.keys[key]
	if !ok {
		return 0, false
	}
	delete(st.keys, key) // 一次性
	if time.Since(entry.issuedAt) > 5*time.Minute {
		return 0, false
	}
	return entry.accountID, true
}

// ConsumeVoucher validates and rotates a reconnect voucher. Successful use
// removes it, and onLogin returns a newly issued voucher to the client.
func (st *Store) ConsumeVoucher(key int64) (int64, bool) {
	if st.cache != nil {
		accountID, ok, err := st.cache.ConsumeLoginVoucher(key)
		if err != nil {
			log.Printf("consume Redis login voucher: %v", err)
			return 0, false
		}
		return accountID, ok
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	entry, ok := st.vouchers[key]
	if !ok {
		return 0, false
	}
	delete(st.vouchers, key)
	if time.Since(entry.issuedAt) > loginVoucherTTL {
		return 0, false
	}
	return entry.accountID, true
}

// CleanupKeys 周期清理过期 Key，防止内存无限增长。
func (st *Store) CleanupKeys() {
	if st.cache != nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, e := range st.keys {
		if time.Since(e.issuedAt) > loginKeyTTL {
			delete(st.keys, k)
		}
	}
	for k, e := range st.vouchers {
		if time.Since(e.issuedAt) > loginVoucherTTL {
			delete(st.vouchers, k)
		}
	}
}
