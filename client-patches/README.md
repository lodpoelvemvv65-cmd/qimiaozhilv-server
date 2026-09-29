# 客户端补丁成品

本目录保存**已经打好服务器地址补丁的客户端资源文件**，可直接替换到原版客户端使用。

## 127.0.0.1/resources.assets

| 项 | 值 |
|---|---|
| 目标文件 | `梦幻奇遇记_Data/resources.assets` |
| 服务器地址 | `Address = LocalAddress = 127.0.0.1:7756` |
| 本地模式 | `isLocal = true`（从本地 bundle 加载资源） |
| 生成工具 | `tools/_patch_client_server.py` |
| 文件长度 | 与原始 `resources.assets` **完全一致**（原地覆盖 JSON 分配区并补空格） |

### 使用方法

1. 备份原版客户端的 `梦幻奇遇记_Data/resources.assets`。
2. 用本目录的 `resources.assets` 覆盖它。
3. 启动客户端，登录后即连接 `127.0.0.1:7756`。

### 校验

```bash
python tools/_patch_client_server.py --show \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

输出中的 `Address` / `LocalAddress` 应等于你的服务端地址，`isLocal` 应为 `True`。

### 自行生成

```bash
# 生成到本目录（不改动原客户端）
python tools/_patch_client_server.py 1.2.3.4:7756 \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets" \
    --output client-patches/1.2.3.4/resources.assets
```

### 回滚

```bash
python tools/_patch_client_server.py --restore \
    --asset "<客户端>/梦幻奇遇记_Data/resources.assets"
```

> `--restore` 依赖同目录下的 `.server-address.bak`。若该备份不存在，
> 请直接用原版客户端文件覆盖回去。

## 说明

- 本仓库**不包含游戏客户端本体**。请自备原版客户端后按上述步骤修改。
- 客户端功能补丁（UI、交易、商城等）不在此目录，见
  `文档/25-客户端补丁清单与回滚图.md` 与 `tools/` 下的安装脚本。
