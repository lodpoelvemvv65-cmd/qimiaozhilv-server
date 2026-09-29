# 客户端服务器地址补丁

本目录保存**已经打好服务器地址补丁的客户端资源文件**，可以直接替换到客户端里使用。

> ⚠️ 这些文件是从游戏客户端里提取并改写的，**不是本项目作者的作品**。
> 客户端本身请按 [README 的「在哪里获取客户端」](../README.md#在哪里获取客户端) 自行获取。

---

## 1. 这个补丁做了什么

客户端的服务器地址**不是写死在代码里**，而是存在 `梦幻奇遇记_Data/resources.assets`
里的一段文本资源 `GlobalProto` 中：

```json
{ "_t" : "GlobalProto",
  "AssetBundleServerUrl" : "http://...",
  "Address" : "...",          ← 连接地址
  "LocalAddress" : "...",     ← isLocal=true 时用它
  "isLocal" : true,
  "ClientVersion" : "2.5" }
```

`tools/_patch_client_server.py` 只改 `Address` / `LocalAddress` / `isLocal` 三个字段，
**在原始 JSON 分配区内覆盖并补空格，不改变文件长度**——所以补丁不会破坏 Unity 的资源索引。

---

## 2. 目录内容

```
client-patches/
├── README.md                      ← 本文件
└── 127.0.0.1/
    └── resources.assets           ← 成品：Address = LocalAddress = 127.0.0.1:7756
```

| 项 | 值 |
|---|---|
| 目标文件 | `<客户端>/梦幻奇遇记_Data/resources.assets` |
| 服务器地址 | `Address = LocalAddress = 127.0.0.1:7756` |
| 本地模式 | `isLocal = true`（从本地 bundle 加载资源，不走 CDN） |
| 生成工具 | `tools/_patch_client_server.py` |
| 文件长度 | 与客户端原始文件**完全一致** |

---

## 3. 怎么用

### 方式一：直接替换成品（最快）

```bash
# 1. 先备份客户端原始文件
cp "<客户端>/梦幻奇遇记_Data/resources.assets" \
   "<客户端>/梦幻奇遇记_Data/resources.assets.bak"

# 2. 用本目录的成品覆盖
cp client-patches/127.0.0.1/resources.assets \
   "<客户端>/梦幻奇遇记_Data/resources.assets"

# 3. 校验（应输出 127.0.0.1:7756 / isLocal: True）
python tools/_patch_client_server.py --show \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

启动客户端，登录后即连接 `127.0.0.1:7756`。

### 方式二：用自己的客户端现打（推荐）

成品文件是「别人机器上的客户端 + 这个补丁」，而 `resources.assets` 是随客户端版本变化的。
**如果你手上的客户端和成品不是同一个版本，请用这种方式**，不要直接覆盖。

```bash
# 指向本地服务端（原地改写，自动留 .server-address.bak 备份）
python tools/_patch_client_server.py 127.0.0.1:7756 \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"

# 指向公网服务端（生成独立成品，不动原文件）
python tools/_patch_client_server.py 1.2.3.4:7756 \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets" \
    --output client-patches/1.2.3.4/resources.assets
```

### 查看当前地址

```bash
python tools/_patch_client_server.py --show \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

### 回滚

```bash
# 方式一（脚本打过补丁）：从自动备份还原
python tools/_patch_client_server.py --restore \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"

# 方式二（手工覆盖过成品）：用自己第 1 步的备份还原
cp "<客户端>/梦幻奇遇记_Data/resources.assets.bak" \
   "<客户端>/梦幻奇遇记_Data/resources.assets"
```

> `--restore` 依赖同目录下的 `resources.assets.server-address.bak`。
> 如果这个备份不存在，请直接用原始客户端文件覆盖回去。

---

## 4. 参数速查

| 参数 | 说明 |
|---|---|
| `server` | 目标地址，`IP` 或 `IP:端口` 或域名；省略端口时用 `--port`（默认 `7756`） |
| `--port` | 默认端口，默认 `7756` |
| `--asset` | `resources.assets` 路径（默认 `client-test/梦幻奇遇记_Data/resources.assets`） |
| `--output` | 写到新文件，保留 `--asset` 不变（生成独立成品时用） |
| `--show` | 只显示当前 `Address` / `LocalAddress` / `isLocal`，不改文件 |
| `--restore` | 从第一次打补丁前的备份还原 |

---

## 5. 常见问题

**Q：替换后客户端起不来 / 资源加载失败？**
A：说明成品和你的客户端版本不一致。用「方式二」拿**你自己的**客户端现打。

**Q：连不上服务器？**
A：先 `--show` 确认地址写进去了；再确认服务端在监听（`netstat -ano | findstr 7756`）；
最后确认 `isLocal = true`。

**Q：为什么改地址不会破坏文件？**
A：工具只在原始 JSON 的**已分配长度内**覆盖，不足部分补空格，文件总长度不变，
Unity 的资源偏移表不会错位。

**Q：我想改回连接官方/其它服务器？**
A：跑一次 `--restore`，或重新用目标地址打一次补丁即可。

---

## 6. 相关文档

- 客户端功能补丁（UI 行为、界面可见性、交易、商城数量等）清单与回滚方式：
  `文档/25-客户端补丁清单与回滚图.md`
- 补丁安装脚本：`tools/_patch_*.py`、`tools/client-*-patcher/`
