# 奇妙之旅 / 梦幻奇遇记 · 自建服务端与逆向工程

> **A Magical Journey** — 一款**已停服的 2D 角色扮演网游**的协议逆向、社区自建服务端（Go + MySQL + Redis）与客户端补丁工具链。
> 全部协议字段、数值公式与消息时序均来自客户端 DLL 反汇编与真机抓包实测，**不依赖任何服务端源码**。

---

## 写在前面

《奇妙之旅》是我人生中的第一款游戏。

小时候最想干的事，就是**用足球把怪踢爆、用超能力甩扑克牌、用军官发导弹**。
长大以后想再把它找回来玩，才发现**它早就停服了**。
后来在 QQ 上看到一个群，我以为是官方，进去才知道是私服。
于是我想：那就借这个客户端，做一个属于自己的服务端吧。

做完之后，却发现自己并没有想象中那么开心。
又听群里说，这个群以前是开过充值的——
那一刻我忽然想明白了：**如果我只是想玩，那我干脆自己重新搭一个。**
在自己的服务器上，我可以随心所欲，不必再被别人的运营节奏牵着走。

我对这个游戏的热情，一点都没变。
现在终于能在自己的服务器上，把小时候那些没实现的想法一个个试一遍。

只是这个游戏已经很少有人听说过、更少有人玩过了。
所以我把这些东西整理出来放到这里——**希望还有机会让你也体验一下。**

### 我的目的与立场

- **我的目的永远是：让更多小时候玩过这款游戏的人，找到当初的感觉。**
- **本人从不盈利**，也**从未开过服**。
- 这个服务端**可能还有些没修复的地方**，欢迎反馈。
- **支持任何形式的二开与修复**——拿去改、拿去用、拿去继续完善，都可以。

### 客户端在哪里


---

**如果想自己从原版客户端开始**：客户端**不是本项目作者做的**，
是现在的私服运营者做的。他们的贴吧里有群，群文件里有客户端：

> 贴吧：<https://tieba.baidu.com/f?kw=奇妙之旅>
> —— **这不是作者的贴吧，作者从未开过服。**

> ⚠️ 不论从哪个渠道拿到客户端，里面的美术、音频、模型、数值等资源
> **仍属原作品权利人（官方）**。需要尊重的是**官方的版权，而不是私服运营者的**。

---

## 关于这款游戏

| 项 | 内容 |
|---|---|
| 原名 | **《奇妙之旅》** |
| 开发 | **蓝航科技**，2009 年 |
| 类型 | 2D 卡通风格角色扮演（网页游戏） |
| 题材 | 现代职业体系 + 中国神话 |
| 玩法 | 扮演足球运动员、特种兵、医护人员等职业，半即时回合制战斗，穿梭于古今场景；8 种职业进阶分支 |
| 状态 | **2023 年停止运营** |
| 本项目对应的客户端 | 第三方制作的 **Unity PC 客户端**（`梦幻奇遇记`），非官方发行版 |

---

## 目录

- [写在前面](#写在前面)
- [关于这款游戏](#关于这款游戏)
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

《奇妙之旅》是蓝航科技 2009 年开发的 2D 卡通风格角色扮演网页游戏，**已于 2023 年停运**。
本项目是社区从零实现的独立服务端，完整还原了客户端与服务器之间的通信协议，
让这款游戏可以在自建服务器上重新运行。

研究对象是**第三方制作的 Unity PC 客户端**（`梦幻奇遇记`），工作内容包括：

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
├── client-patches/      # 客户端服务器地址补丁说明（脚本用法）
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

### Docker 部署（推荐）

仓库自带完整的 Docker 部署脚本，一个 `compose.yaml` 同时拉起 `game` + `mysql` + `redis`：

```bash
cd server-mysql/deploy
cp .env.example .env      # 填 MYSQL_ROOT_PASSWORD / MHQ_DB_PASSWORD / MHQ_PUBLIC_ADDR
chmod 600 .env
docker compose up -d mysql redis      # 先起数据库
docker compose up -d --build          # 再起游戏
```

| 文件 | 用途 |
|---|---|
| `deploy/compose.yaml` | 三个服务的编排（仅 `game` 对外发布 `7756/tcp`） |
| `deploy/.env.example` | 环境变量模板（密码、公网地址、端口、镜像 tag） |
| `deploy/runtime/Dockerfile` | 游戏镜像（`scratch` + 静态 ELF，不含 Go 源码） |
| `deploy/database/` | 首次启动时自动导入的建表 SQL |
| `deploy/gm/` | GM 后台（API + nginx 前端），可选 |
| `deploy/DEPLOY.md` | 完整部署手册（构建、数据库导出、上传、启动、回滚） |

一键部署到自己的 Linux 服务器（Windows PowerShell）：

```powershell
$env:MHQ_SSH_PASSWORD = '<你的服务器密码>'
.\server-mysql\deploy\scripts\deploy-server.ps1 -ServerHost <你的IP> -SshUser <你的用户>
Remove-Item Env:MHQ_SSH_PASSWORD
```

脚本会：跑 `go test` → 交叉编译 `linux/amd64` → 上传二进制与 `Gameplay.yaml` →
重建并只重启 `game` 容器 → 校验 TCP 与配置热重载；失败自动回滚上一版。

> 不传 `-ExpectedHostKey` 时会跳过 SSH 主机指纹校验（仅建议在可信网络使用）。

## 客户端准备与服务器地址修改

> 本仓库**不包含游戏客户端本体**（含美术、音频与代码，属他人版权资源）。

### 在哪里获取客户端

本项目的客户端由**该游戏当前的服务端作者**制作（他同时是现在运营中的服务器作者），
**不是**原始官方发行版本。获取方式：

| 渠道 | 方法 |
|---|---|
| **Bilibili** | 搜索「**奇妙之旅**」 |
| **QQ** | 搜索「**奇妙之旅**」（群 / 频道） |

客户端目录结构大致如下：

```
梦幻奇遇记/
├── 梦幻奇遇记.exe
├── UnityPlayer.dll
└── 梦幻奇遇记_Data/
    ├── Managed/                    # 程序集
    ├── StreamingAssets/yoo/        # 资源 bundle
    └── resources.assets            # ★ 服务器地址存在这里
```

> 客户端**不是**本项目作者的作品，本项目仅对其做协议兼容与地址改写，**不重新分发**。
>
> ⚠️ **资源归属**：客户端**程序**由他人制作，但客户端**内部的美术、音频、模型、
> 数值等资源仍然是原作品权利人的官方资源**——它不会因为「客户端由谁打包」而改变归属。
> 所以**需要尊重的是官方（原作品权利人）的版权，而不是私服运营者的**。

### 修改服务器地址

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

`client-patches/README.md` 里有**用法、校验与回滚的完整说明**。
仓库**不提供**打好补丁的成品文件（客户端资源属他人版权），请用上面的脚本在你自己的客户端上生成。

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

- **《奇妙之旅》由蓝航科技于 2009 年开发，已于 2023 年停止运营**。
  本项目是社区从零实现的独立服务端，与开发方、运营方**均无任何关联**，
  未获得其授权、认可或支持。
- 逆向所依据的抓包来自**第三方玩家自建的服务器**，并非官方线上环境。
  该服务器同样非本项目作者所有，并使用来自其他作品的版权资源。
- 客户端**程序**由他人制作，但客户端**内部的美术、音频、模型、数值等资源仍属原作品权利人**。
  ⚠️ **需要尊重的是官方（原作品权利人）的版权，而不是私服运营者的**——
  私服运营者并没有这些资源的版权，他只是把它们打包进了自己的客户端；
  「客户端不是官方的」**不能**作为可以自由分发的理由。
- 仓库中**不包含**游戏客户端本体、美术资源、音频、原版服务端源码，
  也**不包含**打好地址补丁的客户端资源文件。
  涉及游戏数据的文件（如 `datatable_json/`）仅作为协议逆向的分析参考。
- 所有游戏名称、商标、美术与内容的版权归其各自权利人（包括但不限于蓝航科技）所有。
- 本项目**不盈利**，作者**从未开过服**；本仓库只是代码与文档的分享，
  不对任何第三方服务器的运营行为负责。
- 请勿将本项目用于商业运营或任何侵犯他人权益的用途。
  使用者需自行承担因使用本项目产生的一切法律责任。

如权利人认为本仓库内容侵犯其权益，请通过 Issue 联系，我们将立即配合处理。

## 许可

本仓库中由本项目作者编写的源代码与文档，以 **MIT License** 发布（见 `LICENSE`）。
第三方组件（Go 依赖、npm 依赖、`protoc` 等）遵循其各自许可。

> 注意：MIT 许可**仅覆盖作者原创的代码与文档**，不覆盖任何第三方版权内容。
