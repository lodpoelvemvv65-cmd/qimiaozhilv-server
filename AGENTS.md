# agents.md — 目录结构说明
>禁止使用子代理
>禁止在除工作目录以外操作
> 本文件说明《梦幻奇遇记》自建服务器 + Unity 客户端逆向项目各目录/文件的用途，
> 供后续 agent 或人工快速定位。部署状态更新时间：2026-08-21。
> 死要求：完全、完整地实现每个功能，必须与客户端一致，不用保留。
> **当前唯一开发/运行服务端是 `server-mysql/`。本地客户端是 `client-test/`，
> 另有只改过服务器地址的 `client-127.0.0.1/`。
> `server/` 是旧 SQLite 参考实现，不得作为当前版本继续开发或部署。**
> 每次修复一个功能都必须单独提交，并写清楚修复了什么。
> 每个代码文件不得超过 1500 行。
> 新增或修改的协议实测脚本必须放到 `server-mysql/test/`；抓包文件必须放到抓包归档目录。
> 概率、随机范围、价格、折扣、时长等运营数值优先放到 `server-mysql/config/operations/*.yaml`，通过 MySQL + Redis 热重载。
> 客户端默认不修改；优先通过协议兼容和服务端实现修复。确需改客户端时必须先比对原版并保留可回滚备份。

## Server Configuration Boundary

`server-mysql/` production code, startup, deployment, and admin commands must
not read `datatable_json/` or any JSON configuration file.
Server configuration is loaded from MySQL `game_config_nodes`; editable inputs
belong in YAML/YML under `server-mysql/config/` and must be published to MySQL.
JSON is permitted only for client asset/bundle patching and offline
reverse-engineering/reference tests. Client embedded TextAssets are not server
runtime configuration sources.

## 当前运行与部署基线

| 用途 | 当前目录/地址 | 说明 |
|------|---------------|------|
| 本地服务端 | `server-mysql/` | 本地进程连接本机 MySQL + Redis，监听 `127.0.0.1:7756` |
| 本地客户端 | `client-test/` | `isLocal=true`，实际使用 `LocalAddress=127.0.0.1:7756` |
| Docker 部署 | `server-mysql/deploy/` | 目标主机由 `deploy-server.ps1 -ServerHost` 指定（默认 `127.0.0.1`）；只发布游戏 TCP `7756` |
| 地址补丁客户端 | `client-127.0.0.1/` | `Address` 与 `LocalAddress` 均为 `127.0.0.1:7756`；不得覆盖 `client-test/` |
| 旧实现 | `server/` | SQLite 旧版，仅供协议、实现和回归脚本参考，不是当前运行服 |

## 总览

```
.\
├── 文档/            ← 全部逆向/分析文档（00-25，26 份）
├── tools/           ← 可复用的逆向分析工具（python + protoc）
├── 参考数据/         ← 核心反汇编 dump、协议原始数据与抓包归档
├── server/          ← 旧 SQLite Go 服务端，仅作参考，不再部署
├── server-mysql/    ← 当前唯一 Go 服务端（MySQL + Redis、本地运行及远程 Docker 部署）
├── datatable_json/  ← 原版数据表（解包后的 JSON，76 张）
├── datatable/       ← 数据表中间产物（base64/zlib 原始包）
├── client-test/     ← 本地测试客户端，只连接 127.0.0.1:7756
├── client-127.0.0.1/ ← 只改过服务器地址的测试客户端，连接 127.0.0.1:7756
├── _archive/        ← 一次性脚本、旧迁移产物和临时分析文件归档
├── .spec-workflow/  ← DSH 工作流模板（工具自带，勿动）
└── fgui_skill.bin   ← 技能栏 FGUI 分析产物
```

---

## 文档/ — 全部分析文档（26 份）

逆向成果汇总。**00-索引与状态.md** 是总入口（目录 + 当前状态 + 已验证结论）。

| 文件 | 内容 |
|------|------|
| `00-索引与状态.md` | **总索引**：文档目录、核心结论速览、任务进度、当前运行状态 |
| `01` 游戏与逆向总览 | 游戏信息、Unity 版本、DLL 分布、分析工具链、服务器地址 |
| `02` 传输层-KCP握手协议 | ⚠ **早期 KCP/UDP 分析（已过时）**。实测客户端走 **TService=TCP**：NetKcpComponentAwakeSystem.Awake `newobj ET.TService::.ctor`（Unity.Model.dll Rva=0xE17C）；现行帧 = `[u16 LE 总长度][u16 LE opcode][protobuf body]`，见 `server-mysql/tcp_srv.go` |
| `03` 消息层-分帧与序列化 | 帧格式/序列化（KCP 部分过时；实际用 PacketParser TCP 变体分帧） |
| `04` KCP参数与网络实现细节 | ⚠ 早期 KCP 参数分析（客户端实际未用 KCP） |
| `05` 客户端网络组件与连接流程 | NetKcpComponent/Session/MessageDispatcher（组件真实存在；底层实为 TService=TCP） |
| `06` 登录协议与消息清单 | 登录链路 C2R/C2G 消息、opcode、字段定义 |
| `07` 服务器实现指南 | ⚠ 早期 UDP/KCP 设计稿（已过时）；**实际实现 = TCP**（`server-mysql/tcp_srv.go`：`net.Listen("tcp")` + readFrame） |
| `08` 数据库设计 | SQLite 表结构、登录流程与数据库映射 |
| `09` 技能栏与主线战斗 | 技能栏初始化/点击链路、战斗消息 opcode、datatable 加载、20396 修复 |
| `10` 资源包加密与Bundle逆向 | Bundle AES-CBC 解密链路、GameKeyComponent 密钥、manifest 结构 |
| `11` 背包装备与线上数值 | 背包/穿戴/任务奖励协议、线上属性公式（RoleGrowth）、NumericType 全表 |
| `12` 战斗任务界面Bug与后续协议 | EffectId=0 刷屏根因、ShowSkin NRE、20240-20410 全协议 |
| `13` 商店与货币系统 | 商店/多商店/寄售/远程商店协议、货币 NumericType、BagMapList 快照 |
| `14` 装备强化升级洗练宝石 | 强化公式/成功率、洗练/宝石、AttributeType(1-31) 键空间 |
| `15` 技能学习与重置 | 技能 ID×100+等级 编码、学习/洗点、字段级 SkillInfoLsit |
| `16` 邮件与聊天 | 邮件分页领取、聊天广播、MailState/ChatType |
| `17` 宠物系统 | 宠物 20370 单推驱动、petState 枚举 |
| `18` 星魂系统 | 20400 三字段级 List、穿戴/升级/分解/锁定 |
| `19` 转职与转生 | 转职无 UI 入口（GM）、转生 20342 双入口 |
| `20` 组队系统 | 邀请/申请/处理/转让/退队/踢人全协议、M2C_TeamMember 全量同步 |
| `21` 当前真实实现与回归基线 | 当前 MySQL/Redis/TCP 实现边界、运行和回归基线 |
| `22` 每日活动与五人战斗 | 每日活动、五人组队战斗及对应协议 |
| `23` 签到与活动配置生效边界 | 签到、活动配置及运行时生效边界 |
| `24` SQLite转MySQL迁移方案 | 旧 SQLite 数据迁移到规范化 MySQL 的方案和完成状态 |
| `25` 客户端补丁清单与回滚图 | 本地/远程客户端实际补丁状态、叠加图、作用、备份、回滚及验收标准 |

---

## tools/ — 逆向分析工具（python，可复用）

在**根目录**下运行：`python tools/<脚本> ...`

| 工具 | 用途 |
|------|------|
| `_disasm.py` | **核心反汇编**：`python tools/_disasm.py <Hotfix\|HotfixView> <TypeDef rid>`，token 已解析成 类::方法 名 |
| `_dump_prop.py` | **消息字段**：`python tools/_dump_prop.py <类名>`，输出属性 + ProtoMember tag（含字段级 List） |
| `_parse_manifest2.py` | YooAsset PackageManifest 精确解析（bundle 名/hash/LoadMethod/依赖） |
| `_patch_datatable.py` | datatable 历史补丁工具；当前两个客户端的 datatable 均为原版 LoadMethod 2，状态以 25 文档为准 |
| `_patch_mainui_buttons.py` | 本地客户端 legacy 综合补丁入口；已生效但不得盲目重跑，具体子功能见 25 文档 |
| `_patch_title_visibility.py` | 本地称号初始同步补丁，写入 `Hotfix.dll` |
| `_patch_shop_quantity.py` | 本地商城数量与元宝/代金券购买 UI 补丁，写入 `HotfixView.dll` |
| `_patch_equip_tooltip.py` | 本地查看他人装备悬浮详情补丁，写入 `HotfixView.dll` |
| `_patch_trade_request.py` | 本地交易申请和兼容交易工作区补丁，同时写入两个 Hotfix DLL |
| `_patch_force_offline.py` | 本地重复登录确认后退出补丁，写入 `Hotfix.dll` |
| `_extract_datatable.py` | 从 datatable bundle 提取配置 JSON 到 `datatable/` |
| `_decompress_datatable.py` | 批量解压 `datatable/`（base64→zlib→JSON）到 `datatable_json/` |
| `_decode_datatable.py` | 单文件解码（调试用） |
| `_gen_pb.py` | 用 protoc 重新生成协议代码；当前版本必须输出到 `server-mysql/protocol/protocol.pb.go`，不得更新旧 `server/` |
| `gen_opcodes.py` | 从协议生成 opcodes 常量 |
| `protoc/` | protoc + protoc-gen-go 工具链（`tools/protoc/bin/protoc.exe`） |

> 注：`_extract_datatable.py` / `_decompress_datatable.py` / `_patch_datatable.py` 使用根目录基准路径。
> `_patch_datatable.py` 中的 `server/` 仅是生成 bundle 的临时输出位置，不代表当前服务端。

---

## 参考数据/ — 核心反汇编原始数据（勿删）

| 文件 | 内容 |
|------|------|
| `protocol.proto` | 429 条消息、13 个枚举（从 Hotfix.dll 提取，可生成 Go protobuf） |
| `protocol_dump.txt` | **每条消息的 opcode + ProtoMember tag 清单**（最常用权威参考） |
| `kchannel_dump.txt` | KChannel 全部方法反汇编（InitKcp/Connect/Update/Output/Send） |
| `kservice_dump.txt` | KService.Recv 完整反汇编（SYN/ACK/FIN/MSG switch） |
| `kservice2_dump.txt` | KService.KcpOutput/Disconnect/GetByLocalConn |
| `session_dump.txt` | Session/MessageSerializeHelper/PacketParser/Kcp P/Invoke |
| `aservice_dump.txt` | AService 通道 Id 生成、KChannel.OnRead |
| `netkcp_dump.txt` | NetKcpComponent/NetThreadComponent/NetworkHelper |
| `dump_protocol.py` 等 6 个 py | 生成上述 dump 的正式分析脚本 |
| `抓包归档/` | TCP 抓包归档（已匿名化）；新增抓包也必须放这里 |

---

## server/ — 旧 SQLite 服务端（只作参考）

该目录是早期 SQLite 实现，保留用于协议分析、实现对照和旧回归脚本。
**不得把 `server/` 当作当前服务端修改、启动或部署；当前服务端统一使用 `server-mysql/`。**

```
server/
├── *.go               ← 服务器实现（main/login/equip/battle/task/skill/session/playerdata/store/growth/pb_extra…）
├── protocol/          ← protocol.pb.go（protoc 生成）+ opcodes.go
├── data/mhq.db        ← 旧 SQLite 玩家数据库
├── mhqserver.exe      ← 构建产物（go build -buildvcs=false）
├── verify_*.py        ← 回归验证脚本（8 个，全部可独立跑，连接 127.0.0.1:7756 实测）
│     verify_fieldmonster / verify_maptest / verify_levelup / verify_bag /
│     verify_bag_persist / verify_taskbattle / verify_unitchar / verify_tags
└── e2e_test.py        ← 端到端全链路测试
```

旧版启动方式（仅供追溯）：`mhqserver.exe -listen :7756 -db data/mhq.db -table ../datatable_json`
日志只输出到启动终端（手动启动时重定向 server.log/server.err）。

> ⚠️ `server/server.log` 可能是旧进程残留，分析前核对进程 pid 与时间戳。

---

## server-mysql/ — 当前唯一服务端

- 玩家、账号和业务状态使用规范化 MySQL 表；登录/在线缓存和配置通知使用 Redis。
- 运行配置从 MySQL `game_config_nodes` 加载，不依赖 `server/data/mhq.db`，也不在生产运行时读取 `datatable_json/`。
- 本地开发服务端为 `server-mysql/mhqserver-mysql.exe`，连接本机 MySQL/Redis，配合 `client-test/` 测试。
- 远程部署目录为服务器 `/opt/mhq`。Docker 内运行 `game`、`mysql`、`redis` 三个服务；只有 `game` 发布 `7756/tcp`，MySQL `3306` 和 Redis `6379` 不发布到远程。
- 远程 Docker 镜像只包含 Linux ELF 二进制 `/mhqserver`，不得上传 Go 源码。
- 当前服务端地址为 `127.0.0.1:7756`，登录/注册响应也必须返回该地址。
- 普通登录必须严格比较账号密码；账号不存在、密码错误或空凭证统一返回 `Error=200001, Message="账号或密码错误"`，不得泄露账号是否存在。客户端 `ErrorCode.IsRpcNeedThrowException` 仅允许 `Error>200000` 的业务响应进入 `LoginHelper` 并显示 `Message`，不得再使用 `1003`。
- 注册必须按原版界面规则由服务端权威校验：账号只能含 ASCII 数字/字母，密码只能含 6-16 位 ASCII 数字/字母；空账号、空密码、账号格式、密码格式、重复账号分别返回 `200003/200004/200005/200006/200008` 及原版中文提示，拒绝时不得写入 MySQL。
- `LoginGate Key` 是 5 分钟一次性凭证；`LoginVoucher` 是独立的 30 天一次性轮换重连凭证。无效 Voucher 不得按账号名免密回退。原版登录 UI 载入缓存 Voucher 后不会在用户改输密码时重置 `LoginType`，因此 Voucher 模式收到账号的精确原密码时必须走严格密码登录；其他伪造、失配或重放值仍返回 `200001`。
- 同一角色新的 `LoginGate` 成功后必须向旧会话推原生 `G2C_ForceOffLine(20328)`，立即把旧会话排除出在线路由且只允许心跳；旧连接最多保留 2 分钟供提示框确认。原版处理器会立即退出，`client-test` 的确认后退出行为来自独立可回滚补丁，详见 25 文档。
- 删除角色走 `C2R_DelRole(20018)`，只删除角色及其关联数据，必须保留账号和原密码，允许随后重新创建角色。
- `config/operations/Gameplay.yaml` 统一管理答题题数、新角色代金券、宠物时长/立即完成价格、随机代金券范围、商城每日浮动、装备主属性浮动及家族 Boss 额外掉落。
- `test/` 是当前服务端正式 TCP 协议实测脚本的唯一目录；从 `server-mysql/` 运行 `python .\test\<脚本>.py`，清单见 `test/README.md`。根目录不得再放 `verify_*.py` 或 `e2e_test.py`。
- Go 的 `*_test.go` 保持与 `package main` 源码同目录，确保能覆盖未导出实现；不得为了目录观感移到子包。运行日志统一写入 `logs/runtime/`，历史日志归档到 `logs/archive/`。
- 远程部署脚本统一位于 `deploy/scripts/`；`deploy-server.ps1` 与 `build-linux-release.ps1` 不再放 `server-mysql/` 根目录。

本地启动示例：

```powershell
$env:MHQ_MYSQL_DSN = 'mhq:密码@tcp(127.0.0.1:3306)/mhq?charset=utf8mb4&collation=utf8mb4_bin&parseTime=true&loc=Local'
$env:MHQ_REDIS_ADDR = '127.0.0.1:6379'
Set-Location .\server-mysql
go build -buildvcs=false -o mhqserver-mysql.exe .
.\mhqserver-mysql.exe -listen 127.0.0.1:7756 -advertise 127.0.0.1:7756
```

远程代码更新统一运行：

```powershell
$env:MHQ_SSH_PASSWORD = '在当前终端临时设置；不要写入脚本或提交到仓库'
.\server-mysql\deploy\scripts\deploy-server.ps1
Remove-Item Env:MHQ_SSH_PASSWORD
```

`deploy/scripts/deploy-server.ps1` 的固定流程：

1. 严格校验 `config/operations/Gameplay.yaml`，再执行 `go test ./...`。
2. 交叉编译游戏与临时配置发布器两个 `linux/amd64` ELF，并校验 ELF 头与 SHA256。
3. 校验服务器 SSH 主机指纹，上传游戏二进制、临时配置发布器和 `Gameplay.yaml`，不上传 Go 源码。
4. 在 `/root/mhq-backups` 备份旧二进制，重建并只重启 Docker `game` 容器；MySQL/Redis 容器和数据卷不重建。
5. 校验容器运行状态、MySQL/Redis 初始化日志、远程网关地址、TCP 监听和本机到远程 `7756` 的连通性；失败时恢复上一版二进制。
6. 远程游戏验证通过后，临时发布器通过 Docker 内网仅更新 `Gameplay` 配置树，通过 Redis 热重载，并等待运行服确认对应 revision；随后删除远端临时发布器和 YAML。

数据库迁移不包含在代码发布脚本内。除非明确要求迁移数据，否则禁止覆盖远端 MySQL；更不能删除 `mysql-data` 数据卷。

`deploy/scripts/deploy-server.ps1` 默认同时发布代码二进制和已校验的 `Gameplay.yaml`，只事务替换 MySQL 中配置名为 `Gameplay` 的节点；不会覆盖其他配置、账号、角色或任何玩家数据。需要只热改运营参数时，也可单独执行 `cmd/publish-config`，无需重启服务端。

---

## datatable_json/ — 原版数据表（解包后，76 张）

`[key, {字段...}]` 二元组 JSON 数组，是逆向核对和首次导入 MySQL 的参考源
（MainStory/MonsterBase/SkillConfig/SkillGroupBase/TaskBase/NPCBase/SkillLearn/
RoleGrowth/EquipBase/GoodsBase 等）。当前 `server-mysql` 生产运行时不直接读取该目录。
读取时注意先去掉 JSON 尾逗号（`re.sub(r',\s*([\]}])', r'\1', s)`）。

## datatable/ — 数据表中间产物

`_extract_datatable.py` 的原始输出（base64/zlib 编码的每张表），由
`_decompress_datatable.py` 转成 `datatable_json/`。可删可留（可再生成）。

## client-test/ — 本地 MySQL 服务端测试客户端

这是当前本地测试客户端，对应本机运行的 `server-mysql`。其 `resources.assets`
保持 `isLocal=true`，因此实际连接 `LocalAddress=127.0.0.1:7756`；`Address` 中保留的旧远程地址不参与本地模式连接。
修改远程地址、制作远程包时不得覆盖该目录的 `梦幻奇遇记_Data\resources.assets`。

客户端内容：
- `梦幻奇遇记_Data\StreamingAssets\yoo\` — aa0 manifest、config/datatable bundle、补丁 DLL 和分析副本
- 当前 config bundle 为明文 LoadMethod 0 并包含功能 DLL 补丁；datatable bundle 当前仍为原版 LoadMethod 2。完整清单见 `文档/25-客户端补丁清单与回滚图.md`。
- `_extracted\` — 提取出的 DLL（Hotfix.dll / HotfixView.dll / protocol_dump.txt 副本）+ 分析脚本副本
- `_dec\` — 解密后的 bundle
- 客户端日志：`%LOCALAPPDATA%Low\CTT\梦幻奇遇记\Player.log`

## client-127.0.0.1/ — 只改过服务器地址的测试客户端

这是 `client-test/` 的独立副本：

- 只连接 `127.0.0.1:7756`，用于测试服务器 `/opt/mhq` 中的 Docker 版 `server-mysql`。
- `梦幻奇遇记_Data\resources.assets` 中 `Address`、`LocalAddress` 都是 `127.0.0.1:7756`，`isLocal=true`。
- `Hotfix.dll` 与 `HotfixView.dll` 当前保持原版哈希，未安装本地功能 DLL 补丁。
- 不得拿它测试本地服，也不得把它的 `resources.assets` 覆盖回 `client-test/`。
- 地址补丁不随仓库发布（客户端资源属他人版权）；用 `tools/_patch_client_server.py` 在自备客户端上生成，用法见 `client-patches/README.md`。

## _archive/ — 一次性/临时文件归档

移动进来只是归档（可随时移回），**不影响任何文档引用**：

| 子目录 | 数量 | 内容 |
|--------|------|------|
| 临时脚本-tmp | 104 | 根目录 + server/ 下所有 `tmp_*.py`（一次性反汇编实验） |
| 查找扫描脚本 | 52 | `_find_*.py` / `_scan_*.py` |
| 调试脚本 | 30 | `_dbg_*.py` / `_t_*.py` / `_us*.py` / `_tok*.py` |
| 审查脚本 | 70 | `_audit_*.py`（文档核查时的一次性脚本） |
| 一次性脚本 | 50 | 其余一次性 py（含 udp_capture/tcp_listen） |
| 临时输出txt | 156 | 所有下划线开头 txt（临时反汇编输出） |
| 构建缓存日志 | 7 | 各 `.gocache*` 的 trim.txt |

## 根目录杂项文件

| 文件 | 说明 |
|------|------|
| `fgui_skill.bin` | 技能栏 FGUI 分析产物（可删） |
| `.gocache*`（5 个目录） | Go 构建缓存（可随时删，不影响构建） |
| `.spec-workflow/` | DSH 工作流模板（工具自带，勿动） |

---

## 常用工作流速查

1. **看协议**：`参考数据/protocol_dump.txt`（opcode+tag）→ 字段细节 `python tools/_dump_prop.py <类名>`
2. **看客户端逻辑**：找 TypeDef rid → `python tools/_disasm.py Hotfix <rid>` / `HotfixView <rid>`
3. **看数据表**：`datatable_json/<表名>.json`
4. **跑当前服务端回归**：在 `server-mysql/` 运行 `go test ./...`；协议实测统一运行 `python .\test\<脚本>.py`，脚本只放 `server-mysql/test/`
5. **重打客户端补丁**：先按 `文档/25-客户端补丁清单与回滚图.md` 确认目标和叠加顺序，再运行对应的单功能安装脚本；禁止用一个旧脚本覆盖全部补丁
6. **改本地服务端**：只改 `server-mysql/*.go` → 构建 `mhqserver-mysql.exe` → 重启本地进程 → 用 `client-test/` 测试
7. **更新远程服务端**：本地测试通过后运行 `.\server-mysql\deploy\scripts\deploy-server.ps1`；脚本部署二进制并自动发布、确认 `Gameplay` 热重载
8. **只热更 Gameplay**：在 `server-mysql/` 运行 `go run ./cmd/publish-config -name Gameplay -source ./config/operations/Gameplay.yaml ...`；无需重启服务端
