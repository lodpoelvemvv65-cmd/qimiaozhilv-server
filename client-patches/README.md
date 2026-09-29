# 客户端准备与补丁

> ⚠️ 本仓库**不包含**游戏客户端本体，也**不包含**打好补丁的客户端文件。
>
> 注意：客户端**程序**由他人制作，但里面的**美术、音频、模型、数值等资源仍属原作品权利人**。
> **需要尊重的是官方（原作品权利人）的版权，而不是私服运营者的**——
> 「客户端不是官方的」并不等于可以自由分发。

---

# 一、最快的方式：直接下载打好补丁的客户端

自己从零打一套补丁**顺序要求很严**，容易打错。所以推荐直接用打好补丁的成品：

> **网盘下载**：<https://pan.baidu.com/s/12qZqQ-939XKrPGOpUMck1g?pwd=sfsb>
> **提取码**：`sfsb`
>
> 下载解压即用，已包含下面列出的全部补丁。

成品里已经包含：

- 服务器地址（默认指向作者自己的服务器，可用下面的脚本改成你的）
- MainUI 综合修复（**总补丁**，不装基本玩不了）
- 交易兼容 / 交易拖入数量
- 商城购买数量
- 他人装备悬浮详情
- 仓库取出修复
- 称号初始同步 / 重复登录确认
- 人物背景、自定义形象等外观补丁

拿到成品后，如果只想改成**你自己的**服务器地址，跳到 [§3 改服务器地址](#3-改服务器地址必做)。

---

# 二、客户端从哪来（如果不用成品）

客户端**不是本项目作者做的**，是现在的私服运营者做的。他们的贴吧里有群，群文件里有客户端：

> 贴吧：<https://tieba.baidu.com/f?kw=奇妙之旅>
> —— **这不是作者的贴吧，作者从未开过服。**

---

# 三、改服务器地址（必做）

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
**在原始 JSON 分配区内覆盖并补空格，不改变文件长度**——所以不会破坏 Unity 的资源索引。

### 3.1 先看当前地址

```bash
python tools/_patch_client_server.py --show \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

### 3.2 指向你的服务端

```bash
python tools/_patch_client_server.py 127.0.0.1:7756 \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

### 3.3 校验 / 回滚

```bash
# 校验
python tools/_patch_client_server.py --show --asset "<客户端>/梦幻奇遇记_Data/resources.assets"

# 回滚（依赖同目录的 .server-address.bak）
python tools/_patch_client_server.py --restore --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

### 3.4 参数速查

| 参数 | 说明 |
|---|---|
| `server` | 目标地址：`IP`、`IP:端口`、`[IPv6]:端口` 或域名；省略端口时用 `--port` |
| `--port` | 默认端口，默认 `7756` |
| `--asset` | `resources.assets` 路径 |
| `--output` | 写到新文件，保留 `--asset` 不变 |
| `--show` | 只看当前值，不改文件 |
| `--restore` | 从第一次打补丁前的备份还原 |

---

# 四、功能补丁（进阶，顺序很重要）

**有些功能必须在客户端打补丁才能用**——因为它们原本是连官方服务器设计的，或者原版 UI 本身有 bug。

> ⚠️ **顺序错了会把后面的补丁覆盖掉。** 下面第 5 节的顺序是硬要求。
> 如果只是想玩，强烈建议直接用第 1 节的成品，不要自己打。

## 4.1 真正需要用到的补丁

| # | 补丁 | 改哪个文件 | 作用 |
|---|---|---|---|
| 1 | **MainUI 综合修复**（总补丁） | `HotfixView.dll` | 主界面入口、左右键、普攻弹道调用签名、背包丢弃、熔炼刷新 |
| 2 | **交易兼容** | 两个 DLL | 独立交易申请、保留组队申请、接受后打开兼容交易工作区 |
| 3 | **交易拖入数量确认** | `HotfixView.dll` | 交易模式下每次从背包拖入都弹数量框；仓库保持原行为 |
| 4 | **商城购买数量** | `HotfixView.dll` | 普通商店和商城都弹数量输入；元宝/代金券按页签购买 |
| 5 | 他人装备悬浮详情 | `HotfixView.dll` | 查询装备时用被查看角色窗口的 `CharacterUI.id` |
| 6 | 仓库取出修复 | `HotfixView.dll` | 恢复 `SendPutOutProto` 正常异步取出 |
| 7 | 称号初始同步 | `Hotfix.dll` | 把他人 `UnitCharacter.Title` 初始化到 `NumericType 1038` |
| 8 | 重复登录确认 | `Hotfix.dll` | 收到原生 `20328` 后显示提示，点确定才退出旧客户端 |
| 9 | 服务端商城/杂货店价格 | 两个 DLL | 同步服务端价格与描述，修复杂货店悬浮详情 |
| 10 | 人物背景显示 | `HotfixView.dll` + PNG | 读穿戴槽 1，在原动画背景上方插入静态背景层 |

## 4.2 ⚠️ 「总补丁」是哪个

**`tools/_patch_mainui_buttons.py` 就是那个早期合并补丁（总补丁）。**

它把 MainUI 综合修复的多项改动打在一起（主界面入口、左右键、普攻弹道调用签名、
背包丢弃、熔炼刷新），**已经生效**，脚本顶部已标为 `legacy`。

> **不要重复运行它。** 在当前叠加链上再跑一次会破坏后续补丁。
> 只有「从原版重建客户端」时才用它，并且必须按下面的顺序从头走一遍。

## 4.3 从原版重建的完整顺序

完全退出客户端（包括启动器拉起的进程），然后按此顺序：

```
 1. 改服务器地址          tools/_patch_client_server.py
 2. MainUI 综合修复       tools/_patch_mainui_buttons.py        ← 总补丁
 3. 称号初始同步          tools/_patch_title_visibility.py
 4. 商城购买数量          tools/_patch_shop_quantity.py
 5. 他人装备悬浮详情      tools/_patch_equip_tooltip.py
 6. 交易兼容              tools/_patch_trade_request.py
 7. 重复登录确认          tools/_patch_force_offline.py
 8. 人物背景显示          tools/_patch_character_backgrounds.py
 9. 大黄蜂 Skin25         tools/_patch_bumblebee_skin.py --install
10. 交易拖入数量确认      tools/_patch_trade_quantity.py --client client-test
11. 启动器直登            server-mysql/tools/account-launcher/client-patch/install_client_patch.py --install
12. 启动器自动进入游戏    server-mysql/tools/account-launcher/client-patch/install_auto_enter_patch.py --install
13. 交易请求窗口修复      tools/_repair_trade_request_ui.py
14. 商城/杂货店价格       tools/_patch_client_server_shop_price.py
15. 仓库取出修复          tools/_repair_store_takeout.py
16. 校验 manifest / bundle 内 DLL / current DLL 三者一致
```

三条硬规则：

1. **资源类补丁先于 DLL 类补丁**；商城价格和仓库取出固定在最后两步。
2. **每个 DLL 补丁都以“当前 bundle”为输入**，禁止用旧 `.bak`、远程客户端或
   `_archive` 里的早期文件直接覆盖。
3. **回滚只恢复该补丁自己的备份。** 想删中间某个补丁但保留后面的？
   必须从目标前置快照开始**按顺序重打后续补丁**，不能直接覆盖旧快照。

## 4.4 完整补丁清单（17 项，含外观类）

| # | 补丁 | 改哪个文件 | 作用 |
|---|---|---|---|
| 1 | MainUI 综合修复 | `HotfixView.dll` | 主界面入口、左右键、普攻弹道调用签名、背包丢弃、熔炼刷新 |
| 2 | 称号初始同步 | `Hotfix.dll` | 把他人 `UnitCharacter.Title` 初始化到 `NumericType 1038` |
| 3 | 商城购买数量 | `HotfixView.dll` | 普通商店和商城都弹数量输入；元宝/代金券按页签购买 |
| 4 | 他人装备悬浮详情 | `HotfixView.dll` | 查询装备时用被查看角色窗口的 `CharacterUI.id` |
| 5 | 交易兼容 | 两个 DLL | 独立交易申请、保留组队申请、接受后打开兼容交易工作区 |
| 6 | 重复登录确认 | `Hotfix.dll` | 收到原生 `20328` 后显示提示，点确定才退出旧客户端 |
| 7 | 服务器地址 | `resources.assets` | 固定本地/远程 TCP 地址，不改变文件大小 |
| 8 | 大黄蜂 Skin25 | Manifest + 3 Bundle + `Unity.ModelView.dll` | v13 八姿势待机、汽车尾喷和战斗重炮 |
| 9 | 人物背景显示 | `HotfixView.dll` + PNG | 读穿戴槽 1，插入静态背景层，切换/脱下均刷新 |
| 10 | 交易拖入数量确认 | `HotfixView.dll` | 交易模式下每次从背包拖入都弹数量框 |
| 11 | 朽木白哉完整动画 | Manifest + 2 Bundle + `Unity.ModelView.dll` | 稳定待机施法、灵压剑与循环瞬步 |
| 12 | 卍解一护完整动画 | Manifest + 2 Bundle + `Unity.ModelView.dll` | 双刀卐字灵压、武器落地、御剑飞行、灵压斩和受击 |
| 13 | 启动器直登 | `HotfixView.dll` | 直接填 FGUI 登录框并调原有回调，不依赖延时/模拟输入 |
| 14 | 启动器自动进入游戏 | `HotfixView.dll` | 角色模型加载完成后调“开始游戏”的原生回调 |
| 15 | 交易请求窗口修复 | `HotfixView.dll` | 连续邀请时避免重复挂载 `TeamRequestUI` |
| 16 | 服务端商城/杂货店价格 | 两个 DLL | 同步服务端价格与描述，修复杂货店悬浮详情 |
| 17 | 仓库取出修复 | `HotfixView.dll` | 恢复 `SendPutOutProto` 正常异步取出 |

每个补丁的**安装器、IL 工具、叠加位置与逐条回滚方式**写在
`文档/25-客户端补丁清单与回滚图.md`。

---

# 五、常见问题

**Q：连不上服务器？**
A：① `--show` 确认地址写进去了；② 确认服务端在监听（`netstat -ano | findstr 7756`）；
③ 确认 `isLocal = True`。

**Q：客户端起不来 / 资源加载失败？**
A：说明补丁打坏了。跑 `--restore` 回滚地址补丁；功能补丁则用第 1 节的成品重来一遍。

**Q：为什么改地址不会破坏文件？**
A：工具只在原始 JSON 的**已分配长度内**覆盖，不足部分补空格，文件总长度不变，
Unity 的资源偏移表不会错位。

**Q：我只需要改地址，别的补丁不要行不行？**
A：可以。改地址是独立的，不影响其它文件。但 **MainUI 综合修复不装基本玩不了**，
所以推荐直接用第 1 节的成品。

---

# 六、相关文档

- 完整补丁清单与回滚图：`文档/25-客户端补丁清单与回滚图.md`
- 补丁安装脚本：`tools/_patch_*.py`、`tools/client-*-patcher/`
- 启动器补丁：`server-mysql/tools/account-launcher/client-patch/`
- 客户端地址在协议层的作用：`文档/05-客户端网络组件与连接流程.md`
