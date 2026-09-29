# 梦幻奇遇记 · 自建服务端与逆向工程

> **A Magical Journey** — 一款 Unity 手游的协议逆向、自建服务端（Go + MySQL + Redis）与客户端补丁工具链。
> 全部协议字段、数值公式与消息时序均来自客户端 DLL 反汇编与真机抓包实测，不依赖任何服务端源码。

---

## 目录

- [项目简介](#项目简介)
- [功能覆盖](#功能覆盖)
- [技术架构](#技术架构)
- [仓库结构](#仓库结构)
- [快速开始](#快速开始)
- [客户端准备与服务器地址修改](#客户端准备与服务器地址修改)
- [协议与文档](#协议与文档)
- [测试](#测试)
- [免责声明](#免责声明)
- [许可](#许可)

---

## 项目简介

本项目从零实现了一个可运行的《梦幻奇遇记》服务端，并完整还原了客户端与服务器之间的
通信协议。工作内容包括：

- **协议逆向**：从 `Hotfix.dll` / `HotfixView.dll`（Mono）提取全部 429 条消息与 13 个枚举，
  还原 opcode 与 `ProtoMember` tag。
- **传输层还原**：客户端使用 `ET.TService`（**TCP**，非 KCP/UDP），帧格式为
  `[u16 LE 总长度][u16 LE opcode][protobuf body]`。
- **服务端实现**：Go 语言，MySQL 持久化 + Redis 缓存/配置热重载，单端口 TCP `7756`。
- **数值还原**：角色成长、装备、技能、战斗结算、家族 BOSS、状态/增益等公式全部按线上抓包校准。
- **客户端补丁工具链**：用 Mono.Cecil 修改 `Hotfix.dll` / `HotfixView.dll`，用资源改写修改
  `resources.assets`（服务器地址、UI 行为等）。

## 功能覆盖

| 模块 | 说明 |
|---|---|
| 账号 / 登录 | 注册校验、登录、`LoginGate` 一次性凭证、`LoginVoucher` 30 天轮换重连、顶号强退 |
| 角色 / 成长 | 新角色、职业、转职、转生、属性成长、等级/经验 |
| 地图 / 场景 | 场景切换、传送、NPC、怪物刷新、每日活动场景 |
| 战斗 | 回合制战斗、技能结算、状态/增益/护盾、周期性伤害、伤害与命中判定、五人组队战斗 |
| 背包 / 装备 | 背包整理、装备穿戴、强化/升级/洗练/宝石、词条（Affix）、套装 |
| 商店 / 货币 | 多商店、寄售行、元宝/代金券、每日价格浮动 |
| 技能 | 技能学习/重置、`SkillInfoLsit` 字段级同步、技能组 |
| 宠物 / 星魂 | 宠物养成与单推驱动、星魂穿戴/升级/分解/锁定 |
| 社交 | 好友、聊天广播、邮件分页领取、组队（邀请/申请/转让/踢人） |
| 家族 | 家族成员、家族 BOSS（全阶属性公式与叠层）、额外掉落 |
| 其他 | 签到、每日活动、排行、答题、PVP、主线 AI 循环、GM 后台与 GM API |

## 技术架构

```
Unity 客户端 ──TCP 7756──> Go 服务端 ──┬── MySQL（账号 / 角色 / 业务状态）
   (Mono)      protobuf    (tcp_srv)   └── Redis（在线缓存 / 配置通知 / 热重载）
```

- **服务端**：Go（`server-mysql/`），单进程，配置从 MySQL `game_config_nodes` 加载，
  运营数值由 `config/operations/*.yaml` 发布并热重载。
- **GM 后台**：`cmd/gm-api` + `cmd/gm-admin`（Go）+ `web/`（React + Vite 控制台）。
- **协议**：`protocol.proto` → `protoc-gen-go` → `protocol/protocol.pb.go`。

## 仓库结构

```
.
├── server-mysql/        # ★ 当前唯一服务端（Go + MySQL + Redis）
│   ├── *.go             #   业务实现（登录/战斗/背包/商店/家族/…）
│   ├── cmd/             #   gm-api / gm-admin / publish-config 等工具
│   ├── internal/        #   MySQL schema、物品/技能目录、运营配置
│   ├── config/          #   运营 YAML（概率、价格、时长等）
│   ├── deploy/          #   Docker Compose、部署脚本、数据库 schema
│   ├── protocol/        #   protoc 生成的协议代码
│   └── test/            #   TCP 协议实测脚本（Python）
├── server/              # 早期 SQLite 参考实现（仅供协议与回归对照）
├── web/                 # GM 控制台前端（React + TypeScript + Vite）
├── tools/               # 逆向与补丁工具（Python / C#）
├── client-patches/      # 客户端成品补丁（服务器地址等）
├── 参考数据/            # protocol.proto、反汇编 dump、抓包归档
├── datatable_json/      # 原版数据表（76 张，逆向核对用）
├── datatable/           # 数据表中间产物
└── 文档/                # 50+ 篇逆向与实现文档
```

## 快速开始

### 依赖

- Go 1.22+
- MySQL 8.0
- Redis 6+
- Python 3.10+（仅测试与工具需要）
- `protoc`（仅在重新生成协议代码时需要）

### 本地运行

```bash
export MHQ_MYSQL_DSN='mhq:password@tcp(127.0.0.1:3306)/mhq?charset=utf8mb4&collation=utf8mb4_bin&parseTime=true&loc=Local'
export MHQ_REDIS_ADDR='127.0.0.1:6379'

cd server-mysql
go build -buildvcs=false -o mhqserver-mysql .
./mhqserver-mysql -listen 127.0.0.1:7756 -advertise 127.0.0.1:7756
```

数据库表结构由 `server-mysql/internal/mysqlschema` 在启动时自动创建；
GM 增量表见 `server-mysql/deploy/database/002-gm.sql`。

### 运行测试

```bash
cd server-mysql
go test ./...

# TCP 协议实测（需服务端已启动）
python test/<脚本>.py
```

### Docker 部署

见 `server-mysql/deploy/DEPLOY.md` 与 `server-mysql/deploy/compose.yaml`。

## 客户端准备与服务器地址修改

> 本仓库**不包含游戏客户端本体**（含原版美术、音频与代码，属版权资源）。
> 请自备原版客户端，然后按下面的方式修改服务器地址。

客户端为 **Unity + Mono** 构建。服务器地址不是写死在代码里，而是存放在
`梦幻奇遇记_Data/resources.assets` 内的一段文本资源 `GlobalProto` 中：

```json
{ "_t": "GlobalProto", "Address": "...", "LocalAddress": "...", "isLocal": true, ... }
```

- `isLocal = true` 时，客户端使用 `LocalAddress` 连接，并从本地 bundle 加载资源。
- 修改工具：`tools/_patch_client_server.py`（在原始 JSON 分配区内覆盖并补空格，**不改变文件长度**）。

```bash
# 查看当前地址
python tools/_patch_client_server.py --show --asset "<客户端>/梦幻奇遇记_Data/resources.assets"

# 指向本地服务端
python tools/_patch_client_server.py 127.0.0.1:7756 --asset "<客户端>/梦幻奇遇记_Data/resources.assets"

# 指向远程服务端（生成独立成品，不改原文件）
python tools/_patch_client_server.py 1.2.3.4:7756 \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets" \
    --output client-patches/1.2.3.4/resources.assets

# 回滚
python tools/_patch_client_server.py --restore --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

`client-patches/127.0.0.1/resources.assets` 是已经打好的成品示例（指向 `127.0.0.1:7756`）。

其他客户端功能补丁（UI 行为、界面可见性、交易、商城数量等）见
`文档/25-客户端补丁清单与回滚图.md`，安装脚本位于 `tools/`。

## 协议与文档

- `参考数据/protocol.proto`：429 条消息、13 个枚举的完整定义。
- `参考数据/protocol_dump.txt`：每条消息的 opcode 与 `ProtoMember` tag 清单。
- `参考数据/抓包归档/`：线上真机抓包（`.pcapng`，已匿名化）与解码文本。
- `文档/00-索引与状态.md`：**总入口**，含目录、当前状态与已验证结论。
- 客户端逻辑反汇编：`python tools/_disasm.py Hotfix <TypeDef rid>`
- 消息字段查询：`python tools/_dump_prop.py <类名>`

重新生成协议代码（需自行安装 `protoc` 与 `protoc-gen-go`）：

```bash
python tools/_gen_pb.py     # 输出到 server-mysql/protocol/protocol.pb.go
```

## 测试

- `server-mysql/`：`go test ./...`（单元 + 集成，覆盖未导出实现）。
  - 战斗相关测试依赖 `server-mysql/testdata/SkillLogicConfig`（技能逻辑配置夹具，已随仓库提供，无需游戏客户端）。
  - 需要 MySQL 的持久化测试在未设置 `MHQ_TEST_MYSQL_DSN` 时会自动跳过。
- `server-mysql/test/`：Python TCP 协议实测脚本，直接连接运行中的服务端。
- `server/`：早期 SQLite 实现的 `verify_*.py` 回归脚本（协议对照参考）。

## 免责声明

本项目**仅供学习与研究**，用于协议分析、网络编程与分布式服务端实现的学习目的。

- 本项目为**独立实现的第三方服务端**。研究对象（游戏客户端与线上服务器）
  并非本项目作者所拥有，也**不是**任何官方发行版本：它由第三方自行搭建，
  并使用来自其他作品的版权资源。
- 本项目与上述第三方、以及任何相关作品的权利人**均无任何关联**，
  未获得其授权、认可或支持。
- 仓库中**不包含**游戏客户端本体、美术资源、音频或原版源码。
  涉及游戏数据的文件（如 `datatable_json/`）仅作为协议逆向的分析参考。
- 所有游戏名称、商标、美术与内容的版权归其各自权利人所有。
- 请勿将本项目用于商业运营或任何侵犯他人权益的用途。
  使用者需自行承担因使用本项目产生的一切法律责任。

如权利人认为本仓库内容侵犯其权益，请通过 Issue 联系，我们将立即配合处理。

## 许可

本仓库中由本项目作者编写的源代码与文档，以 **MIT License** 发布（见 `LICENSE`）。
第三方组件（Go 依赖、npm 依赖、`protoc` 等）遵循其各自许可。

> 注意：MIT 许可**仅覆盖作者原创的代码与文档**，不覆盖任何第三方版权内容。
