# server-mysql 运行与配置

运行服只连接 MySQL 和 Redis。SQLite 不参与启动、存档或运行时读取；玩家状态使用规范化 MySQL 表，配置使用 `game_config_nodes` 配置树。

## 启动

```powershell
$env:MHQ_MYSQL_DSN = 'mhq:密码@tcp(127.0.0.1:3306)/mhq?charset=utf8mb4&collation=utf8mb4_bin&parseTime=true&loc=Local'
$env:MHQ_REDIS_ADDR = '127.0.0.1:6379'
go build -buildvcs=false -o mhqserver-mysql.exe .
./mhqserver-mysql.exe -listen :7756
```

`-table` 和 `-skill-logic` 仅为旧服务包装脚本保留，参数值会被忽略；启动前配置必须已经导入 MySQL。

## 首次导入配置

`cmd/import-config` 只接受 YAML/YML 源，会导入全部原版数据表及 `SkillLogicConfig`：

```powershell
go run ./cmd/import-config `
  -table ..\datatable_yaml `
  -mysql "$env:MHQ_MYSQL_DSN"
```

YAML 适合版本控制、审核和运营维护；运行服不会直接读取 YAML 文件。

## Custom skins and item grants

Custom character equipment is maintained in YAML and published directly to
MySQL. The server does not read the client `datatable_json` files:

```powershell
go run ./cmd/publish-custom-skins -source ./config/operations/CustomSkins.yaml
go run ./cmd/grant-items -account a12312304 -items 130001,130002
```

The grant command is idempotent, checks `EquipBase` in MySQL, and writes only
new instances into free bag slots. It does not add anything to a shop table.

## 配置发布与热更

商城、市场、世界 Boss、家族 Boss、日常活动等配置均可单独发布：

```powershell
go run ./cmd/publish-config `
  -name WorldBossConfig `
  -source .\config\operations\WorldBossConfig.yaml `
  -mysql "$env:MHQ_MYSQL_DSN" `
  -publisher operator `
  -note "调整世界 Boss 刷新间隔"
```

发布流程是：校验 YAML → MySQL 事务替换配置树 → 写入 `game_config_revisions` 审计版本 → Redis 发布 `mhq:config:reload`。所有服务收到通知后从 MySQL 完整重建配置；校验成功才原子替换内存快照，失败保留旧配置。无需重启进程。

## 存储检查

```powershell
go run ./cmd/verify-storage `
  -mysql "$env:MHQ_MYSQL_DSN" `
  -redis "$env:MHQ_REDIS_ADDR"
```

检查项包括配置节点数量、遗留 JSON 列数量和 Redis 连通性。生产路径不使用 JSON 存档列。

## 总测

```powershell
$env:MHQ_TEST_PORT = '7757'
python .\test\verify_mysql_total.py 2>&1 | Tee-Object total-test.log
```

总测创建 25 个客户端（5 队×5 人），验证城镇互见、并发组队、聊天、心跳、技能、背包、邮件、排行榜、商城/市场、仓库、PVP、星魂、家族 Boss、签到活动、世界 Boss、断线重连和 MySQL/Redis 持久化。

其余正式 TCP 协议实测脚本也统一位于 `test/`，完整清单和运行约定见
[`test/README.md`](test/README.md)。
