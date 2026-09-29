package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// store.go：SQLite 存取层（08 文档）。
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
	ID           int64
	AccountID    int64
	Name         string
	JobID        int32
	SkinID       int32
	Level        int32
	Exp          int64
	Energy       int32
	MapID        int32
	PosX         float32
	PosY         float32
	CharPoint    int32
	SkillPoint   int32
	StrAdd       int32
	QukAdd       int32
	SpiAdd       int32
	WimAdd       int32
	Skills       string // 已学技能 "id:lvl,id:lvl,..."（含学习顺序）
	AutoSkills   string // 自动施放技能 "id,id,..."
	AutoBattle   int32  // 主界面自动战斗开关（0=关，1=开，-1=旧存档待迁移）
	Tasks        string // 任务状态 "taskId:state,..."
	BagJSON      string // 背包（JSON，equip.go bagToJSON）
	WornJSON     string // 穿戴装备（JSON）
	StarSoulJSON string // 星魂背包（JSON）

	// 新增模块（state_extra.go）
	Coin       int64  // 铜币
	YuanBao    int64  // 元宝
	Voucher    int64  // 兑换券
	StoreJSON  string // 仓库（JSON）
	StoreCoin  int64  // 仓库铜币
	StorePages int32  // 仓库页数（客户端每页 60 格）
	PetJSON    string // 宠物（JSON）
	MailsJSON  string // 邮件（JSON）
	Friends    string // 好友（JSON）
	Signin     string // 签到（JSON）
	MainUI     string // 主界面物品槽（JSON，mainui.go）
	ItemBuffs  string // 持续消耗品状态（JSON，status_buff.go）
	Trans      int32  // 转生等级 0-3（TransmigrationAddConfig 职业×10+转生等级）

	// 家族系统（family.go）
	FamilyID           int64 // 所属家族 id（0=无家族）
	FamilyContribute   int32 // 家族贡献
	PersonalContribute int32 // 个人贡献
}

// keyEntry 一条已签发未消耗的登录凭证。
type keyEntry struct {
	accountID int64
	issuedAt  time.Time
}

// Store SQLite 存取层。
type Store struct {
	db   *sql.DB
	mu   sync.RWMutex
	keys map[int64]keyEntry // Key → 账号（Key 由 IssueKey 生成）
}

// OpenStore 打开（必要时创建）数据库并执行建表 SQL。
func OpenStore(dbPath string) (*Store, error) {
	// 确保 data 目录存在（SQLite 不会自动建目录）
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	// SQLite 只有一个写入者。database/sql 若同时打开多个连接，即使使用 WAL，
	// 并发注册和角色保存仍可能立即返回 SQLITE_BUSY。进程内统一使用一条连接，
	// 再用 busy_timeout 等待短暂的外部写事务（例如管理工具更新测试账号）。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=10000;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		log.Printf("store: WAL pragma failed: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS accounts (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			account     TEXT NOT NULL UNIQUE,
			password    TEXT NOT NULL,
			create_time INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS players (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id INTEGER NOT NULL REFERENCES accounts(id),
			name       TEXT NOT NULL,
			job_id     INTEGER NOT NULL DEFAULT 1,
			skin_id    INTEGER NOT NULL DEFAULT 0,
			level      INTEGER NOT NULL DEFAULT 1,
			exp        INTEGER NOT NULL DEFAULT 0,
			energy     INTEGER NOT NULL DEFAULT 2000,
			map_id     INTEGER NOT NULL DEFAULT 1000601,
			pos_x      REAL NOT NULL DEFAULT -1.8,
			pos_y      REAL NOT NULL DEFAULT -0.84,
			skills     TEXT NOT NULL DEFAULT '',
			auto_skills TEXT NOT NULL DEFAULT '',
			auto_battle INTEGER NOT NULL DEFAULT 0,
			tasks      TEXT NOT NULL DEFAULT '',
			last_login INTEGER NOT NULL DEFAULT 0,
			is_online  INTEGER NOT NULL DEFAULT 0,
			coin       INTEGER NOT NULL DEFAULT 100000,
			yuan_bao   INTEGER NOT NULL DEFAULT 100,
			voucher    INTEGER NOT NULL DEFAULT 100,
			store_json TEXT NOT NULL DEFAULT '',
			store_coin INTEGER NOT NULL DEFAULT 0,
			store_pages INTEGER NOT NULL DEFAULT 2,
			pet_json   TEXT NOT NULL DEFAULT '',
			mails_json TEXT NOT NULL DEFAULT '',
			friends_json TEXT NOT NULL DEFAULT '',
			signin_json TEXT NOT NULL DEFAULT '',
			mainui_json TEXT NOT NULL DEFAULT '',
			item_buffs_json TEXT NOT NULL DEFAULT '',
			trans      INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS families (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			name         TEXT NOT NULL UNIQUE,
			leader       INTEGER NOT NULL,
			level        INTEGER NOT NULL DEFAULT 1,
			hornor       INTEGER NOT NULL DEFAULT 0,
			notice       TEXT NOT NULL DEFAULT '',
			members_json TEXT NOT NULL DEFAULT '',
			requests_json TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_players_account ON players(account_id);
	`); err != nil {
		return nil, err
	}
	// 兼容旧库：为已存在的 players 表补技能/任务/经验/属性点列（忽略“列已存在”错误）
	for _, stmt := range []string{
		`ALTER TABLE players ADD COLUMN skills TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN auto_skills TEXT NOT NULL DEFAULT ''`,
		// -1 lets loadData distinguish an old save from a newly-created account.
		// Old builds displayed the switch as enabled whenever auto_skills was
		// non-empty, so migration must preserve that visible state.
		`ALTER TABLE players ADD COLUMN auto_battle INTEGER NOT NULL DEFAULT -1`,
		`ALTER TABLE players ADD COLUMN tasks TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN exp INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN energy INTEGER NOT NULL DEFAULT 2000`,
		`ALTER TABLE players ADD COLUMN map_id INTEGER NOT NULL DEFAULT 1000601`,
		`ALTER TABLE players ADD COLUMN pos_x REAL NOT NULL DEFAULT -1.8`,
		`ALTER TABLE players ADD COLUMN pos_y REAL NOT NULL DEFAULT -0.84`,
		`ALTER TABLE players ADD COLUMN char_point INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN skill_point INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN str_add INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN quk_add INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN spi_add INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN wim_add INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN bag_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN worn_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN starsoul_json TEXT NOT NULL DEFAULT ''`,
		// 新增模块列（state_extra.go / 13-17-20 文档）
		`ALTER TABLE players ADD COLUMN coin INTEGER NOT NULL DEFAULT 100000`,
		`ALTER TABLE players ADD COLUMN yuan_bao INTEGER NOT NULL DEFAULT 100`,
		`ALTER TABLE players ADD COLUMN voucher INTEGER NOT NULL DEFAULT 100`,
		`ALTER TABLE players ADD COLUMN store_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN store_coin INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN store_pages INTEGER NOT NULL DEFAULT 2`,
		`ALTER TABLE players ADD COLUMN pet_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN mails_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN friends_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN signin_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN mainui_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN item_buffs_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE players ADD COLUMN trans INTEGER NOT NULL DEFAULT 0`,
		// 家族系统（family.go）
		`ALTER TABLE players ADD COLUMN family_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN family_contribute INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE players ADD COLUMN personal_contribute INTEGER NOT NULL DEFAULT 0`,
		// 持久化状态表（world_boss_states 世界 BOSS 死亡/刷新；family_boss_states 家族 BOSS 血量；
		// consignment_items 寄售行条目；server_meta 全局自增序列）——重启不丢失
		`CREATE TABLE IF NOT EXISTS world_boss_states (
			layer       INTEGER PRIMARY KEY,
			dead_at     INTEGER NOT NULL DEFAULT 0,
			killer      TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS family_boss_states (
			family_id   INTEGER NOT NULL,
			boss_id     INTEGER NOT NULL,
			hp          INTEGER NOT NULL,
			max_hp      INTEGER NOT NULL,
			has_reward  INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (family_id, boss_id)
		)`,
		`CREATE TABLE IF NOT EXISTS consignment_items (
			id          INTEGER PRIMARY KEY,
			seller_id   INTEGER NOT NULL,
			seller_name TEXT NOT NULL DEFAULT '',
			item_id     INTEGER NOT NULL,
			item_type   INTEGER NOT NULL DEFAULT 2,
			count       INTEGER NOT NULL DEFAULT 1,
			price       INTEGER NOT NULL DEFAULT 0,
			need_pwd    INTEGER NOT NULL DEFAULT 0,
			pwd         TEXT NOT NULL DEFAULT '',
			put_at      INTEGER NOT NULL,
			special_key INTEGER NOT NULL DEFAULT 0,
			is_lock     INTEGER NOT NULL DEFAULT 0,
			star        INTEGER NOT NULL DEFAULT 0,
			quality     INTEGER NOT NULL DEFAULT 0,
			level       INTEGER NOT NULL DEFAULT 0,
			special_id  INTEGER NOT NULL DEFAULT 0,
			gems        TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS server_meta (
			name  TEXT PRIMARY KEY,
			value INTEGER NOT NULL DEFAULT 0
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			// 重复添加列会报 duplicate column，属正常，忽略
		}
	}
	// 旧库迁移：consignment_items 装备详情列（CREATE TABLE IF NOT EXISTS 不会
	// 给已存在的表补列，这里按列名检查后 ALTER）。
	ensureColumn(db, "consignment_items", "special_key", "ALTER TABLE consignment_items ADD COLUMN special_key INTEGER NOT NULL DEFAULT 0")
	ensureColumn(db, "consignment_items", "is_lock", "ALTER TABLE consignment_items ADD COLUMN is_lock INTEGER NOT NULL DEFAULT 0")
	ensureColumn(db, "consignment_items", "star", "ALTER TABLE consignment_items ADD COLUMN star INTEGER NOT NULL DEFAULT 0")
	ensureColumn(db, "consignment_items", "quality", "ALTER TABLE consignment_items ADD COLUMN quality INTEGER NOT NULL DEFAULT 0")
	ensureColumn(db, "consignment_items", "level", "ALTER TABLE consignment_items ADD COLUMN level INTEGER NOT NULL DEFAULT 0")
	ensureColumn(db, "consignment_items", "special_id", "ALTER TABLE consignment_items ADD COLUMN special_id INTEGER NOT NULL DEFAULT 0")
	ensureColumn(db, "consignment_items", "gems", "ALTER TABLE consignment_items ADD COLUMN gems TEXT NOT NULL DEFAULT ''")
	if err := migrateStoreLayout(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, keys: make(map[int64]keyEntry)}, nil
}

// migrateStoreLayout converts the temporary 40-slot, one-based warehouse
// layout to the client's actual 60-slot, zero-based layout exactly once.
func migrateStoreLayout(db *sql.DB) error {
	const migrationName = "store_layout_60_zero_based"
	var version int64
	err := db.QueryRow(`SELECT value FROM server_meta WHERE name = ?`, migrationName).Scan(&version)
	if err == nil && version >= 1 {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type legacyStore struct {
		id    int64
		json  string
		pages int32
	}
	rows, err := tx.Query(`SELECT id, store_json, store_pages FROM players`)
	if err != nil {
		return err
	}
	var players []legacyStore
	for rows.Next() {
		var p legacyStore
		if err := rows.Scan(&p.id, &p.json, &p.pages); err != nil {
			rows.Close()
			return err
		}
		players = append(players, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range players {
		items := storeFromJSON(p.json)
		if len(items) > 0 {
			if _, alreadyZeroBased := items[0]; !alreadyZeroBased {
				shifted := make(map[int32]*bagItem, len(items))
				for index, item := range items {
					if index > 0 {
						index--
					}
					shifted[index] = item
				}
				items = shifted
			}
		}
		pages := (p.pages*40 + 59) / 60
		if pages < 2 {
			pages = 2
		}
		for index, item := range items {
			if item == nil || index < 0 {
				continue
			}
			if required := index/60 + 1; required > pages {
				pages = required
			}
		}
		if _, err := tx.Exec(`UPDATE players SET store_json = ?, store_pages = ? WHERE id = ?`, storeToJSON(items), pages, p.id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO server_meta (name, value) VALUES (?, 1)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value`, migrationName); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureColumn：表缺列时执行 ALTER（幂等，旧库迁移用）。
func ensureColumn(db *sql.DB, table, column, ddl string) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n)
	if err != nil || n > 0 {
		return
	}
	if _, err := db.Exec(ddl); err != nil {
		log.Printf("[DB] ensure column %s.%s: %v", table, column, err)
	}
}

func (st *Store) Close() { st.db.Close() }

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
		`SELECT id, account_id, name, job_id, skin_id, level, exp, energy, map_id, pos_x, pos_y,
		        char_point, skill_point, str_add, quk_add, spi_add, wim_add, skills, auto_skills, auto_battle, tasks,
		        bag_json, worn_json, coin, yuan_bao, voucher, store_json, store_coin, store_pages,
			   pet_json, mails_json, friends_json, signin_json, mainui_json, item_buffs_json, trans,
			        family_id, family_contribute, personal_contribute, starsoul_json
		   FROM players WHERE account_id = ? ORDER BY id LIMIT 1`, accountID,
	).Scan(&p.ID, &p.AccountID, &p.Name, &p.JobID, &p.SkinID, &p.Level, &p.Exp, &p.Energy,
		&p.MapID, &p.PosX, &p.PosY,
		&p.CharPoint, &p.SkillPoint, &p.StrAdd, &p.QukAdd, &p.SpiAdd, &p.WimAdd,
		&p.Skills, &p.AutoSkills, &p.AutoBattle, &p.Tasks, &p.BagJSON, &p.WornJSON,
		&p.Coin, &p.YuanBao, &p.Voucher, &p.StoreJSON, &p.StoreCoin, &p.StorePages,
		&p.PetJSON, &p.MailsJSON, &p.Friends, &p.Signin, &p.MainUI, &p.ItemBuffs, &p.Trans,
		&p.FamilyID, &p.FamilyContribute, &p.PersonalContribute, &p.StarSoulJSON)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPlayers returns complete player snapshots for rankings. Ranking values
// depend on equipment, star soul and persisted activity state, so selecting
// only name/level would disagree with the character panel.
func (st *Store) ListPlayers(limit int) ([]*Player, error) {
	query := `SELECT id, account_id, name, job_id, skin_id, level, exp, energy, map_id, pos_x, pos_y,
		        char_point, skill_point, str_add, quk_add, spi_add, wim_add, skills, auto_skills, auto_battle, tasks,
		        bag_json, worn_json, coin, yuan_bao, voucher, store_json, store_coin, store_pages,
		        pet_json, mails_json, friends_json, signin_json, mainui_json, item_buffs_json, trans,
		        family_id, family_contribute, personal_contribute, starsoul_json
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
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Name, &p.JobID, &p.SkinID, &p.Level, &p.Exp, &p.Energy,
			&p.MapID, &p.PosX, &p.PosY,
			&p.CharPoint, &p.SkillPoint, &p.StrAdd, &p.QukAdd, &p.SpiAdd, &p.WimAdd,
			&p.Skills, &p.AutoSkills, &p.AutoBattle, &p.Tasks, &p.BagJSON, &p.WornJSON,
			&p.Coin, &p.YuanBao, &p.Voucher, &p.StoreJSON, &p.StoreCoin, &p.StorePages,
			&p.PetJSON, &p.MailsJSON, &p.Friends, &p.Signin, &p.MainUI, &p.ItemBuffs, &p.Trans,
			&p.FamilyID, &p.FamilyContribute, &p.PersonalContribute, &p.StarSoulJSON); err != nil {
			return nil, err
		}
		players = append(players, &p)
	}
	return players, rows.Err()
}

// SavePlayerStarSoul persists the independently versioned star-soul bag.
// Keeping this separate from SavePlayerData avoids changing the long-standing
// player save call used by older regression clients.
func (st *Store) SavePlayerStarSoul(playerID int64, value string) error {
	_, err := st.db.Exec(`UPDATE players SET starsoul_json = ? WHERE id = ?`, value, playerID)
	return err
}

// SavePlayerStorePages persists warehouse capacity without changing the
// long-standing SavePlayerData call signature used by regression tests.
func (st *Store) SavePlayerStorePages(playerID int64, pages int32) error {
	_, err := st.db.Exec(`UPDATE players SET store_pages = ? WHERE id = ?`, pages, playerID)
	return err
}

// SavePlayerData 持久化角色等级 / 经验 / 属性点 / 技能点 / 加点 / 技能 / 任务 / 背包 / 穿戴 / 皮肤 / 货币 / 仓库 / 宠物 / 邮件 / 好友 / 签到 / 主界面物品槽 / 转生 / 家族。
func (st *Store) SavePlayerData(playerID int64, level int32, exp int64, energy int32,
	mapID int32, posX, posY float32,
	charPoint, skillPoint, strAdd, qukAdd, spiAdd, wimAdd int32,
	skills, autoSkills string, autoBattle bool, tasks, bagJSON, wornJSON string,
	coin, yuanBao, voucher, storeCoin int64,
	storeJSON, petJSON, mailsJSON, friendsJSON, signinJSON, mainUIJSON, itemBuffsJSON string,
	trans, skinID int32, familyID int64, familyContribute, personalContribute int32) error {
	_, err := st.db.Exec(
		`UPDATE players SET level = ?, exp = ?, energy = ?, map_id = ?, pos_x = ?, pos_y = ?,
		        char_point = ?, skill_point = ?, str_add = ?, quk_add = ?,
		        spi_add = ?, wim_add = ?, skills = ?, auto_skills = ?, auto_battle = ?, tasks = ?,
		        bag_json = ?, worn_json = ?, coin = ?, yuan_bao = ?, voucher = ?,
		        store_coin = ?, store_json = ?, pet_json = ?, mails_json = ?,
		        friends_json = ?, signin_json = ?, mainui_json = ?, item_buffs_json = ?, trans = ?, skin_id = ?,
		        family_id = ?, family_contribute = ?, personal_contribute = ? WHERE id = ?`,
		level, exp, energy, mapID, posX, posY, charPoint, skillPoint, strAdd, qukAdd, spiAdd, wimAdd,
		skills, autoSkills, autoBattle, tasks, bagJSON, wornJSON,
		coin, yuanBao, voucher, storeCoin, storeJSON, petJSON, mailsJSON, friendsJSON, signinJSON,
		mainUIJSON, itemBuffsJSON, trans, skinID, familyID, familyContribute, personalContribute, playerID,
	)
	return err
}

// CreatePlayer 创建角色，返回角色 id。
func (st *Store) CreatePlayer(accountID int64, name string, jobID, skinID int32) (int64, error) {
	// 新角色默认皮肤 = 职业（1~4），避免客户端 SkinBase.Get(0) NRE（12 文档 §2.3）
	if skinID <= 0 || skinID > 4 {
		skinID = jobID
	}
	res, err := st.db.Exec(
		`INSERT INTO players (account_id, name, job_id, skin_id, store_pages) VALUES (?, ?, ?, ?, 2)`,
		accountID, name, jobID, skinID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetPlayerOnline 更新角色在线状态与最后登录时间。
func (st *Store) SetPlayerOnline(playerID int64, online bool) {
	st.db.Exec(`UPDATE players SET is_online = ? WHERE id = ?`,
		online, playerID)
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
	var buf [8]byte
	rand.Read(buf[:])
	key := int64(binary.LittleEndian.Uint64(buf[:]) & 0x7fffffffffffffff)
	if key == 0 {
		key = 1
	}
	st.mu.Lock()
	st.keys[key] = keyEntry{accountID: accountID, issuedAt: time.Now()}
	st.mu.Unlock()
	return key
}

// ConsumeKey 校验并消耗 Key，返回绑定的账号 id。
// 校验失败（不存在/超时）返回 ok=false。
func (st *Store) ConsumeKey(key int64) (int64, bool) {
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

// CleanupKeys 周期清理过期 Key，防止内存无限增长。
func (st *Store) CleanupKeys() {
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, e := range st.keys {
		if time.Since(e.issuedAt) > 5*time.Minute {
			delete(st.keys, k)
		}
	}
}
