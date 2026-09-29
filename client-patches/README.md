# 客户端服务器地址补丁

本目录说明**怎么把客户端指向你自己的服务端**。

> ⚠️ 本仓库**不包含**游戏客户端，也**不包含**打好补丁的客户端资源文件。
> 客户端请按 [README 的「在哪里获取客户端」](../README.md#在哪里获取客户端) 自行获取；
> 补丁用下面的脚本在你**自己的**客户端上生成。
>
> 注意：客户端**程序**由他人制作，但里面的**美术、音频、模型、数值等资源仍属原作品权利人**。
> **需要尊重的是官方（原作品权利人）的版权，而不是私服运营者的**——
> 「客户端不是官方的」并不等于可以自由分发。

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

## 2. 前置条件

| 项 | 要求 |
|---|---|
| 客户端 | 已按 README 获取，目录里有 `梦幻奇遇记_Data/resources.assets` |
| Python | 3.8+（脚本只用标准库，无需装依赖） |
| 备份 | 补丁会原地改写，脚本会自动留 `.server-address.bak`；**建议再手工备份一份** |

---

## 3. 怎么用

### 3.1 先看当前地址

```bash
python tools/_patch_client_server.py --show \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

输出示例：

```
file: .../梦幻奇遇记_Data/resources.assets
Address: 101.43.6.155:7756
LocalAddress: 127.0.0.1:7756
isLocal: True
```

### 3.2 指向本地服务端

```bash
python tools/_patch_client_server.py 127.0.0.1:7756 \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

### 3.3 指向公网服务端（生成独立文件，不动原文件）

```bash
python tools/_patch_client_server.py 1.2.3.4:7756 \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets" \
    --output client-patches/1.2.3.4/resources.assets
```

### 3.4 校验

```bash
python tools/_patch_client_server.py --show \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

`Address` / `LocalAddress` 应等于你填的地址，`isLocal` 应为 `True`。

### 3.5 回滚

```bash
python tools/_patch_client_server.py --restore \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

> `--restore` 依赖同目录下的 `resources.assets.server-address.bak`。
> 如果这个备份不存在，请直接用你第 2 步的原始客户端文件覆盖回去。

---

## 4. 参数速查

| 参数 | 说明 |
|---|---|
| `server` | 目标地址：`IP`、`IP:端口`、`[IPv6]:端口` 或域名；省略端口时用 `--port` |
| `--port` | 默认端口，默认 `7756` |
| `--asset` | `resources.assets` 路径（默认 `client-test/梦幻奇遇记_Data/resources.assets`） |
| `--output` | 写到新文件，保留 `--asset` 不变（生成独立成品时用） |
| `--show` | 只显示当前 `Address` / `LocalAddress` / `isLocal`，不改文件 |
| `--restore` | 从第一次打补丁前的备份还原 |

---

## 5. 常见问题

**Q：连不上服务器？**
A：① `--show` 确认地址写进去了；② 确认服务端在监听（`netstat -ano | findstr 7756`）；
③ 确认 `isLocal = True`。

**Q：客户端起不来 / 资源加载失败？**
A：说明补丁写坏了或客户端本身有问题。跑 `--restore` 回滚，确认原始客户端能正常启动，
再重新打一次。

**Q：为什么改地址不会破坏文件？**
A：工具只在原始 JSON 的**已分配长度内**覆盖，不足部分补空格，文件总长度不变，
Unity 的资源偏移表不会错位。脚本内部还会校验改写前后文件长度一致。

**Q：客户端地址被改坏了，但没有 `.server-address.bak`？**
A：用你手工备份的原始 `resources.assets` 覆盖回去，或从客户端安装包重新解一份。

**Q：想改回连接其它服务器？**
A：直接再用目标地址打一次补丁即可，不需要先回滚。

---

## 6. 相关文档

- 客户端功能补丁（UI 行为、界面可见性、交易、商城数量等）清单与回滚方式：
  `文档/25-客户端补丁清单与回滚图.md`
- 补丁安装脚本：`tools/_patch_*.py`、`tools/client-*-patcher/`
- 客户端地址在协议层的作用：`文档/05-客户端网络组件与连接流程.md`
