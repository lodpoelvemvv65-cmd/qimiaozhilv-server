# 客户端普通商店货币显示修复

## 变更

客户端兼容层的 `ET.ServerShopPriceCache.FormatCoinPrice` 以前把
`ShopBase.Price` 的原始整数直接标成金币。现在按原版客户端的货币拆分显示：

- `100` 铜币 = `1` 银币
- `10000` 铜币 = `1` 金币

普通商店服务端逻辑和 `ShopBase.Price` 未改动，价格仍使用铜币最小单位保存、扣除
和通过 MySQL/YAML 配置发布。例如 `500` 会显示为原版的 `0 金图标 5 银图标
0 铜图标`。

## 安装与回滚

补丁只修改本地 `client-test` 的 config bundle 内 `Hotfix.dll`，不覆盖
`HotfixView.dll`，也不改远程客户端。安装器：

```powershell
python tools/_patch_client_coin_format.py
```

首次安装会在同目录创建：

```text
5ff0cd3f4dadb1ed406add45f5bbb636.bundle.pre-shop-coin-format.bak
```

恢复安装前的完整 bundle：

```powershell
python tools/_patch_client_coin_format.py --restore
```

安装前 bundle SHA256：
`945021161A25F6A88F9B788637156C16F37D92FB7545A3E44E16E412C6877AC2`

修复后嵌入 `Hotfix.dll` SHA256：
`52762BBDE7B1BC02F441F1D62F8BDCD3D8BE568B6BE59F5C900778472C819CD9`

修复后 bundle SHA256：
`75024079452E9C37BC1DAB14EF81EDFE0630DFEC95294D8E888AF81BDAC33E10`
