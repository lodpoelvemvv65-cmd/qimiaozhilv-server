# 24-SQLite 转 MySQL 迁移方案

> ## ⚠ 状态更正（2026-09-14 复核）
>
> 本文原标题下的状态写「**仅设计，尚未实施**」「生产权威库仍是 `server/data/mhq.db`」，
> **这已经与事实相反，务必不要再按它判断现状**：
>
> - **迁移早已实施完成**。当前唯一开发/运行服务端是 `server-mysql/`，权威库是 **MySQL**，
>   登录/在线缓存和配置通知使用 **Redis**；`server/data/mhq.db` 只是旧 SQLite 实现的历史文件，
>   不再被任何在跑的服务端读写。
> - 远程 `/opt/mhq` 以 Docker Compose 运行 `game` / `mysql` / `redis` 三个服务，
>   只发布游戏 TCP `7756`，MySQL `3306` 与 Redis `6379` **不发布到远程**。
> - 运行配置从 MySQL `game_config_nodes` 加载；生产运行**不读取** `datatable_json/` 或任何 JSON 配置文件。
> - 因此下文各节应作为**迁移的设计依据与历史记录**来读（DDL 设计、方言改造清单、数据搬迁与回滚步骤），
>   **不是待办事项**。现状与回归基线以 [21-当前真实实现与回归基线](21-当前真实实现与回归基线.md) 为准。
>
> 保留本节以下原始内容不改写，是为了留档当初的迁移方案与验收口径。

> 原始状态：仅设计，尚未实施。本文以 2026-08-18 的 `server/store.go` 和所有直接 SQL 调用为准。
> 在完成代码兼容、演练、备份和验收前，生产权威库仍是 `server/data/mhq.db`。

## 1. 目标与前提

建议目标版本为 MySQL 8.0.34 或更高，字符集 `utf8mb4`，时区固定为 UTC。迁移目标是保持所有账号、角色、
背包、装备、技能、体力、签到、宠物、邮件、好友、家族、寄售和 BOSS 刷新状态不变，而不是顺便重构 JSON
存储格式。

当前数据库并不只有 `accounts/players`：

| 表 | 权威数据 |
|----|----------|
| `accounts` | 账号、密码、创建时间 |
| `players` | 角色主体和各模块 JSON/数值 |
| `families` | 家族、成员和申请列表 |
| `world_boss_states` | 分层世界 BOSS 死亡时间和击杀者 |
| `family_boss_states` | 各家族 BOSS 当前 HP、领奖状态 |
| `consignment_items` | 寄售行和装备详情 |
| `server_meta` | 寄售序列及一次性迁移版本 |

登录 Key、当前 TCP 会话、临时队伍邀请、战斗实例和匹配池仍是内存运行态，不迁入 MySQL。

## 2. 不是只换驱动名称

推荐新增 `github.com/go-sql-driver/mysql`，但不能把 `sql.Open("sqlite", path)` 简单替换成 MySQL：

| SQLite 写法 | MySQL 8 写法 |
|-------------|--------------|
| `PRAGMA busy_timeout/journal_mode` | 删除；改连接池、超时和事务参数 |
| `INTEGER PRIMARY KEY AUTOINCREMENT` | `BIGINT AUTO_INCREMENT PRIMARY KEY` |
| `REAL` | 坐标用 `DOUBLE` |
| `ON CONFLICT(k) DO UPDATE` | `ON DUPLICATE KEY UPDATE` |
| `pragma_table_info(?)` | `information_schema.columns` 或版本化迁移 |
| 单连接 `SetMaxOpenConns(1)` | 根据容量配置连接池，例如 20/10/30m |
| `?` 占位符 | `go-sql-driver/mysql` 仍支持 `?`，大部分普通 CRUD 可保留 |
| `LastInsertId()` | MySQL 驱动支持，可保留 |

更重要的是，SQL 目前不全在 `store.go`。`boss.go`、`consignment.go`、`family.go`、`growth.go`、
`role_admin.go` 都直接访问 `store.db`，其中包含 SQLite upsert。实施时必须全仓搜索 `store.db`、
`globalServer.store.db`、`ON CONFLICT`、`PRAGMA`，统一处理，不能只改存取层入口。

## 3. 建议的代码结构

实施阶段建议增加：

```text
-db-driver sqlite|mysql
-db data/mhq.db                         # sqlite
-db "user:pass@tcp(host:3306)/mhq?..." # mysql，实际密码走环境变量
```

`OpenStore` 改为接收 driver 和 DSN；建表/迁移拆为 `migrations/sqlite` 与 `migrations/mysql`。业务 CRUD
尽量收回 `Store` 方法，至少把以下方言差异封装：

- `UpsertWorldBossState`
- `UpsertFamilyBossState`
- `NextConsignmentID`
- `UpsertServerMeta`
- `ColumnExists`（只供迁移器）

MySQL DSN 最低要求：

```text
charset=utf8mb4&parseTime=true&loc=UTC&timeout=5s&readTimeout=10s&writeTimeout=10s
```

账号密码不得提交进 Git；从环境变量或独立部署配置读取。启动时执行 `PingContext`，数据库不可用就直接退出，
不能悄悄回退到一个空 SQLite 库。

## 4. MySQL 目标 DDL

下面 DDL 保持当前字段含义和整数 Unix 时间戳。`LONGTEXT DEFAULT ('')` 要求 MySQL 8.0.13+。

```sql
CREATE DATABASE mhq CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
USE mhq;

CREATE TABLE accounts (
  id BIGINT NOT NULL AUTO_INCREMENT,
  account VARCHAR(191) NOT NULL,
  password VARCHAR(255) NOT NULL,
  create_time BIGINT NOT NULL,
  PRIMARY KEY (id), UNIQUE KEY uk_accounts_account (account)
) ENGINE=InnoDB;

CREATE TABLE players (
  id BIGINT NOT NULL AUTO_INCREMENT,
  account_id BIGINT NOT NULL,
  name VARCHAR(191) NOT NULL,
  job_id INT NOT NULL DEFAULT 1, skin_id INT NOT NULL DEFAULT 0,
  level INT NOT NULL DEFAULT 1, exp BIGINT NOT NULL DEFAULT 0,
  energy INT NOT NULL DEFAULT 2000,
  map_id INT NOT NULL DEFAULT 1000601,
  pos_x DOUBLE NOT NULL DEFAULT -1.8, pos_y DOUBLE NOT NULL DEFAULT -0.84,
  char_point INT NOT NULL DEFAULT 0, skill_point INT NOT NULL DEFAULT 0,
  str_add INT NOT NULL DEFAULT 0, quk_add INT NOT NULL DEFAULT 0,
  spi_add INT NOT NULL DEFAULT 0, wim_add INT NOT NULL DEFAULT 0,
  skills LONGTEXT NOT NULL DEFAULT (''), auto_skills LONGTEXT NOT NULL DEFAULT (''),
  auto_battle TINYINT NOT NULL DEFAULT 0, tasks LONGTEXT NOT NULL DEFAULT (''),
  last_login BIGINT NOT NULL DEFAULT 0, is_online TINYINT NOT NULL DEFAULT 0,
  coin BIGINT NOT NULL DEFAULT 100000, yuan_bao BIGINT NOT NULL DEFAULT 100,
  voucher BIGINT NOT NULL DEFAULT 100,
  store_json LONGTEXT NOT NULL DEFAULT (''), store_coin BIGINT NOT NULL DEFAULT 0,
  store_pages INT NOT NULL DEFAULT 2,
  pet_json LONGTEXT NOT NULL DEFAULT (''), mails_json LONGTEXT NOT NULL DEFAULT (''),
  friends_json LONGTEXT NOT NULL DEFAULT (''), signin_json LONGTEXT NOT NULL DEFAULT (''),
  mainui_json LONGTEXT NOT NULL DEFAULT (''), item_buffs_json LONGTEXT NOT NULL DEFAULT (''),
  trans INT NOT NULL DEFAULT 0,
  family_id BIGINT NOT NULL DEFAULT 0,
  family_contribute INT NOT NULL DEFAULT 0,
  personal_contribute INT NOT NULL DEFAULT 0,
  bag_json LONGTEXT NOT NULL DEFAULT (''), worn_json LONGTEXT NOT NULL DEFAULT (''),
  starsoul_json LONGTEXT NOT NULL DEFAULT (''),
  PRIMARY KEY (id), KEY idx_players_account (account_id),
  CONSTRAINT fk_players_account FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE families (
  id BIGINT NOT NULL AUTO_INCREMENT, name VARCHAR(191) NOT NULL,
  leader BIGINT NOT NULL, level INT NOT NULL DEFAULT 1,
  hornor INT NOT NULL DEFAULT 0, notice LONGTEXT NOT NULL DEFAULT (''),
  members_json LONGTEXT NOT NULL DEFAULT (''), requests_json LONGTEXT NOT NULL DEFAULT (''),
  PRIMARY KEY (id), UNIQUE KEY uk_families_name (name)
) ENGINE=InnoDB;

CREATE TABLE world_boss_states (
  layer INT NOT NULL, dead_at BIGINT NOT NULL DEFAULT 0,
  killer VARCHAR(191) NOT NULL DEFAULT '', PRIMARY KEY (layer)
) ENGINE=InnoDB;

CREATE TABLE family_boss_states (
  family_id BIGINT NOT NULL, boss_id INT NOT NULL,
  hp BIGINT NOT NULL, max_hp BIGINT NOT NULL, has_reward TINYINT NOT NULL DEFAULT 0,
  PRIMARY KEY (family_id, boss_id)
) ENGINE=InnoDB;

CREATE TABLE consignment_items (
  id BIGINT NOT NULL, seller_id BIGINT NOT NULL,
  seller_name VARCHAR(191) NOT NULL DEFAULT '', item_id INT NOT NULL,
  item_type INT NOT NULL DEFAULT 2, count INT NOT NULL DEFAULT 1,
  price BIGINT NOT NULL DEFAULT 0, need_pwd TINYINT NOT NULL DEFAULT 0,
  pwd VARCHAR(255) NOT NULL DEFAULT '', put_at BIGINT NOT NULL,
  special_key BIGINT NOT NULL DEFAULT 0, is_lock TINYINT NOT NULL DEFAULT 0,
  star INT NOT NULL DEFAULT 0, quality INT NOT NULL DEFAULT 0,
  level INT NOT NULL DEFAULT 0, special_id INT NOT NULL DEFAULT 0,
  gems LONGTEXT NOT NULL DEFAULT (''),
  PRIMARY KEY (id), KEY idx_consign_seller (seller_id)
) ENGINE=InnoDB;

CREATE TABLE server_meta (
  name VARCHAR(191) NOT NULL, value BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (name)
) ENGINE=InnoDB;
```

不要在首轮迁移中把现有 JSON 强制改为 MySQL `JSON` 类型：旧记录允许空字符串，直接改类型会导入失败。
先按 `LONGTEXT` 无损迁移，后续另做数据清洗和独立版本迁移。

## 5. 数据迁移程序

推荐写一次性 Go 工具 `tools/migrate_sqlite_to_mysql`，同时用 `modernc.org/sqlite` 和 MySQL 驱动读取/写入。
不推荐 CSV：玩家 JSON 内含逗号、引号和 Unicode，人工转义很容易损坏装备或邮件。

迁移顺序固定为：

```text
accounts -> players -> families -> world_boss_states -> family_boss_states
         -> consignment_items -> server_meta
```

每张表显式列出字段，不使用 `SELECT *`；保留原始主键 ID；批量事务写入；任何行失败整表回滚并打印表名、
主键和错误。导入结束后设置自增起点为 `MAX(id)+1`：

```sql
ALTER TABLE accounts AUTO_INCREMENT = <max_id_plus_1>;
ALTER TABLE players  AUTO_INCREMENT = <max_id_plus_1>;
ALTER TABLE families AUTO_INCREMENT = <max_id_plus_1>;
```

SQLite 读取使用只读连接。迁移工具不得启动游戏监听端口，也不得修改源库。

## 6. 正式切换步骤

1. 在测试 MySQL 建库并执行版本化 DDL，运行空库注册、建角、战斗、保存、重登测试。
2. 复制 `mhq.db` 做演练迁移，完成行数、主键、JSON 和业务回归；此阶段线上 SQLite 继续工作。
3. 确定维护窗口，停止服务端，确认 7756 已无进程，备份 `mhq.db`、`-wal`、`-shm`；先执行 SQLite
   checkpoint，再对冻结副本迁移。不能在服务仍写库时直接复制三个文件中的一个。
4. 对空的正式 MySQL 库执行全量迁移。导入期间禁止客户端连接。
5. 执行第 7 节校验，全部通过后才把服务启动参数切到 MySQL。
6. 用隔离账号做注册、登录、背包、装备、技能、签到、家族、寄售、BOSS 状态和五人战斗冒烟测试。
7. 观察数据库连接错误、慢查询、死锁和服务端 panic；保留冻结 SQLite 备份，不立即删除。

不建议第一版做双写。当前保存路径很多，双写中任一侧失败会产生难以判断的分叉。停机全量迁移的风险更低，
也更容易回滚。

## 7. 必须通过的验收

每张表对比：

```sql
COUNT(*)
MIN(primary_key), MAX(primary_key)
```

另外必须检查：

- `players.account_id` 均能关联到 `accounts.id`；
- `accounts.account`、`families.name` 无重复；
- `players` 所有 JSON/文本列逐字节长度一致，随机抽样反序列化；
- 所有 `coin/yuan_bao/voucher/store_coin/exp` 和 BOSS HP 使用 64 位，未截断；
- `server_meta.consign_seq >= MAX(consignment_items.id)`；
- `players.is_online` 在冷启动后统一由服务端实际会话修正，不能沿用假在线；
- 同一测试账号在迁移前后等级、经验、属性、背包、穿戴、宝石、技能、体力、签到、宠物、家族完全一致；
- `go test ./...`、全部 `server-mysql/test/verify_*.py`、`server-mysql/test/e2e_test.py` 和真实五人战斗均通过 MySQL 模式。

建议迁移工具输出 JSON 校验报告，记录源/目标行数和每表稳定哈希。只有报告全绿才允许切流。

## 8. 回滚

切换后发现问题时：立即停止服务，避免继续写 MySQL；保存 MySQL 故障快照；将启动参数恢复到冻结的 SQLite
副本，再启动并跑登录/重登冒烟。这样可以快速恢复，但会丢失 MySQL 上线后新增的写入。

如果上线后已经允许真实玩家产生数据，就不能简单回退旧 SQLite。必须先停服，把 MySQL 切换后的增量按主键和
更新时间规则回灌；当前表没有统一 `updated_at`，所以在实施 MySQL 前应先增加数据库迁移批次/切换时间审计，
或把首次切换控制在无真实写入的验收窗口内。

## 9. 实施工作清单

- [ ] 增加 MySQL 驱动、driver/DSN 参数和密钥读取；
- [ ] 拆分 SQLite/MySQL DDL 与版本化迁移；
- [ ] 封装五处以上直接 `store.db` 的方言 SQL；
- [ ] 增加 MySQL Store 集成测试（独立测试库）；
- [ ] 编写只读 SQLite -> MySQL 迁移工具和校验报告；
- [ ] 完成演练、备份恢复和故障注入；
- [ ] 维护窗口全量迁移、验收、切换；
- [ ] 保留可验证回滚点并更新运行文档。

在这些项目完成之前，不能把“能连接 MySQL”视为迁移完成。
