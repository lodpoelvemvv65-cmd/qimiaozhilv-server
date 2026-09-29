# client-test 补丁归档

> 审计记录（2026-09-03）：本文件只记录 `client-test` 的本地补丁状态；本次审计没有安装、回滚或改写任何客户端资源。运行包中的 `Hotfix.dll`、`HotfixView.dll` 与 `current/` 字节一致，但两个 aa0 bundle 的 manifest 仍保留原始 hash/size（见“已知风险”）。

## 本次审计结论

- 当前有效：服务端商城/杂货店价格与描述同步、杂货店悬浮详情、普通商店货币格式、仓库取出修复、交易拖入数量确认、交易兼容、称号初始同步、MainUI 综合修复、人物背景、aa1 角色资源，以及启动器“直登 + 自动进入游戏”。
- 当前未生效或已移除：交易请求窗口修复当前未发现 `RemoveComponent<TeamRequestUI>` 前置序列；人物 Skin 即时刷新补丁的 `RefreshCharacterSkin*` 和 `characterDisplayedSkinId` 不在当前 DLL；独立旧货币格式、旧商城价格、旧 datatable/map-monster/旧商城魔法球等脚本均不是当前运行链。
- `GoodsBase` 与 `ShopBase` 是两个独立数据源：普通商店购买价看 `ShopBase.Price`；物品名称、容量等看本地 `GoodsBase`。当前商城补丁只把服务端描述写入背包/悬浮详情，不热更名称、容量等字段，因此截图中名称仍是客户端本地文本而描述可变，这是预期边界。
- aa0 本地 config、datatable 均为 `LoadMethod=0` 明文 bundle；manifest 仍声明原始 hash/size，当前可运行但清缓存、重新下载或严格校验时可能失败。本次只记录，不自动修复。

只读审计命令：

```powershell
python server-mysql/client-patches/client-test/audit_patch_state.py
```

审计脚本不会写入客户端；`WARN` 项只代表状态或文档风险，不会自动修复。

这里集中保存本地测试客户端的功能 DLL、运行 bundle 副本和回滚备份。

- `current/`：当前已安装到客户端 config bundle 的补丁内容副本；
- `backups/aa0/`：config bundle 的历史回滚副本；
- `backups/_extracted/`：历史 Hotfix DLL 回滚副本；
- `backups/legacy-20260830/`：从根目录 `_archive/client-test-backups/20260830` 迁入的旧快照，仅供审计，
  不能作为当前安装输入。

客户端运行时仍必须保留
`client-test/梦幻奇遇记_Data/StreamingAssets/yoo/aa0/5ff0cd3f4dadb1ed406add45f5bbb636.bundle`；
该文件是 YooAsset 实际加载的资源，不是可移除的分析副本。客户端不再保存
`_extracted` 目录和 `.bak` 回滚文件。

归档文件来自 2026-09-03 交易/仓库修复后的本地客户端状态。安装器应优先从本目录
读取当前 DLL 和回滚基线，生成的新回滚文件也应写入本目录，不要写回客户端目录。

## 当前补丁链（本地 client-test）

运行时 bundle 仍在 `client-test/梦幻奇遇记_Data/StreamingAssets/yoo/aa0/`；
`yoo/_extracted` 是指向这里 `current/` 的 junction。`client-127.0.0.1`
完全不属于这条补丁链，不能拿它的 DLL、bundle 或 `resources.assets` 覆盖本地客户端。

| 安装器 | 改动与作用 | 是否依赖前一个补丁 | 回滚 |
|---|---|---|---|
| `python tools/_patch_client_server_shop_price.py` | 同步服务端商城/杂货店价格和商品描述；修复杂货店悬浮详情。重建 `Hotfix.dll`、`HotfixView.dll`，输入是当前 bundle | 否；会保留当前已有仓库/交易/启动器修复 | `python tools/_patch_client_server_shop_price.py --restore` |
| `python tools/_repair_store_takeout.py` | 移除误插入普通仓库入口的交易守卫，恢复 `SendPutOutProto` 取出物品 | 否；建议在商城价格补丁后执行 | `python tools/_repair_store_takeout.py --restore` |
| `python tools/_repair_trade_request_ui.py` | 修复连续交易/组队邀请时重复挂载请求窗口 | 否；只改当前 `HotfixView.dll` | `python tools/_repair_trade_request_ui.py --restore` |
| `python tools/_patch_trade_quantity.py --client client-test` | 交易模式拖入物品时询问数量，普通仓库保持原逻辑 | 否；只改交易模式分支 | `python tools/_patch_trade_quantity.py --client client-test --restore` |
| `py server-mysql/tools/account-launcher/client-patch/install_client_patch.py --install` | 启动器直登 | 否 | `py server-mysql/tools/account-launcher/client-patch/install_client_patch.py --restore` |
| `py server-mysql/tools/account-launcher/client-patch/install_auto_enter_patch.py --install` | 角色页自动进入；依赖直登 | 是，依赖直登 | `py server-mysql/tools/account-launcher/client-patch/install_auto_enter_patch.py --restore` |

安装前先完全退出客户端。只预览不写文件可使用商城/仓库安装器的 `--dry-run`。商城价格安装器
从当前 bundle 提取两个 DLL 后再生成新版本，不会使用根目录 `_archive` 的旧快照；因此重新运行不会
把仓库取出修复、交易修复或启动器修复抹掉。两者修改的方法集合也不重叠：商城补丁改
`ServerShopPriceCache`/价格回调，仓库补丁只处理 `StoreUI.SendPutOutProto`。

上述 6 个安装器是当前集中回滚链：快照写入
`server-mysql/client-patches/client-test/backups/{aa0,_extracted}/`，并同步
`current/`、运行 bundle 以及存在时的 `_dec/aa0`。每层使用不同的
`.pre-<feature>.bak` 后缀，重复执行不会叠加同名 IL；回滚某层只执行该层的 `--restore`。

交易数量层的前置快照为从 2026-08-24 历史归档迁入的
`backups/_extracted/HotfixView.dll.pre-trade-quantity.bak` 和
`backups/aa0/5ff0cd3f4dadb1ed406add45f5bbb636.bundle.pre-trade-quantity.bak`。
它是中间基线；执行该层回滚会撤销其后叠加的商城/仓库层，若要保留后续功能必须按文档顺序重新安装。

## 旧补丁的处理方式

称号、MainUI、商城购买数量、他人装备悬浮、重复登录、人物背景、Skin25/白哉/卍解一护、普通商店货币格式和
服务器地址等补丁的详细作用见项目根目录的 `文档/25-客户端补丁清单与回滚图.md`。其中带 `legacy` 标记的脚本
有的没有 `--restore`，有的仍会把备份写到客户端旁边；它们只用于从历史基线重建，**不要在当前链上重复执行**。
要重新启用某个旧功能，先用 `backups/legacy-*` 或干净客户端建立临时基线，再按文档 3.18 的顺序重打，不能把
旧 `.bak` 直接覆盖当前 bundle。

## 单独安装和回滚规则

当前集中回滚链中的每个功能只运行自己的安装器。每个这类安装器使用独立后缀（例如
`.pre-server-shop-price.bak`、`.pre-store-takeout-fix.bak`），备份集中在
`backups/aa0/`（bundle）和 `backups/_extracted/`（DLL），不会写入客户端目录。回滚某一层只执行
该层的 `--restore`；不要把另一个功能的旧 bundle 直接复制回来。

如果要删除中间补丁但保留后续补丁，不能直接恢复中间层快照：应从该层之前的 bundle 快照开始，按需要
重新安装后续层。回滚后核对：

```powershell
Get-FileHash server-mysql/client-patches/client-test/current/* -Algorithm SHA256
```

并确认 `current/` 中 DLL 与 `aa0` bundle 内的 `Hotfix.dll`、`HotfixView.dll` 字节完全一致；必要时清理
Unity/YooAsset 缓存后再启动。

## 其他客户端补丁

MainUI、称号、商城数量、他人装备悬浮、交易兼容、交易数量、重复登录、人物背景、Skin25/白哉/卍解一护、
服务器地址等作用、命令和历史备份后缀见 `文档/25-客户端补丁清单与回滚图.md`。这些旧安装器部分仍按
历史方式写入客户端目录，当前链上不要重复执行；需要重建时必须以 `current/` 为输入，并按文档的固定顺序
安装。`_patch_datatable.py`、`_patch_mapmonster_config.py`、`_patch_skin_refresh.py` 等历史试验补丁当前
未启用。
