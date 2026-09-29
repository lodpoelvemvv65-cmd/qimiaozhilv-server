# 10 — 资源包(Bundle)加密链路逆向

> 记录《梦幻奇遇记》客户端资源包（Addressable Bundle）加密/解密链路的逆向结论，
> 以及为「海滩第 1 层字段怪改成正式配置」所需的数据表落点判断。
> 分析对象：`client-test/梦幻奇遇记_Data/Managed/` 下的托管程序集 + `StreamingAssets/yoo/` 资源目录。

## 1. 背景与目标

用户要求海滩第 1 层字段怪改为**正式配置**（急速荷叶球，MonsterBase 10001 / PrefabId 281，×2 @ (-8,-2.5)/(5,-1)）。

卡点：客户端建怪走 `M2C_CreateMapMonsterHandler` → `MapMonsterConfigCategory.Instance.Get(ConfigId)` 查表，
而客户端 `MapMonsterConfig` **没有 PrefabId=281 的行** → 需要在客户端配置里补一行。
数据表以加密 Bundle 形式存在 `yoo/aa0/`，要改表就必须先弄清加密链路、判断改动落点。

## 2. Bundle 解密链路（已确认）

程序集：`Unity.ModelView.dll`，类 `ET.GameDecryptionServices`，方法 `LoadFromMemory`（Rva=0x6998）。

```
LoadFromMemory(info)
  ├─ info.FilePath              → 取 bundle 文件路径
  ├─ GameKeyComponent.Instance.key   ─┐
  ├─ GameKeyComponent.Instance.keyIV ─┼→ Encryption.AesCBCDecrypt(...)
  └─                                  │
```

`Encryption` 类（Unity.ModelView.dll）的 AesCBCDecrypt：
- `RijndaelManaged`，key / IV 均为 **UTF-8 字符串**
- Mode = 1（CBC），Padding = 2（Zeros）

**结论**：aa0 下的 Bundle 是 AES-CBC 加密，密钥来自全局组件 `GameKeyComponent`（静态 `Instance`）。

## 3. 密钥赋值链路（已确认）

`GameKeyComponent.key / keyIV / xorKey` 由 async 事件处理器填充。程序集 `Unity.Model.dll`：

| 元素 | 位置 |
|------|------|
| `ET.AppStartEvent.Run` | TypeDef rid 174，Rva=0xCF44 |
| `<Run>d__0.MoveNext`（AppStartEvent 的状态机） | TypeDef rid 366，Rva=0x167FC |
| 状态机 `args` 字段 | 类型 = **`ET.EventType.AppStart`**（TypeDef rid 301，字段签名 `06 11 84 b4`） |
| AppStart 字段 token | key=0x040002ea / keyIV=0x040002eb / xorKey=0x040002ec |

`<Run>d__0.MoveNext` 完整逻辑（0x167FC）：

```
set_targetFrameRate(FrameRate)
Scene ×3 → 各种 GetComponent（ComponentView/UI 等）
GlobalConfigComponent.Instance.GlobalProto.isEditorMode → ET.GlobalConfigComponent.IsEditorMode
Options.LogLevel = 4
scene.AddComponent<GameKeyComponent>()           // MethodSpec 137 → Entity.AddComponent<T>
  ├─ stfld GameKeyComponent.key    = args.key      // ← 从 AppStart 事件参数读入
  ├─ stfld GameKeyComponent.keyIV  = args.keyIV
  ├─ stfld GameKeyComponent.xorKey = args.xorKey
EventSystem.Publish(new AppStartInitFinish())     // 通知初始化完成
await CompletedTask
```

**结论**：三个密钥在游戏启动时从 `EventType.AppStart` 事件参数写入 `GameKeyComponent`。

## 4. 关键发现：async 状态机是 struct（newobj 扫描无效）

此前对 4 个 DLL（Unity.Model / Unity.ModelView / Hotfix / HotfixView）做 `newobj AppStart`、
`newobj <Run>d__0`、跨程序集 `AppStart.ctor` 扫描，**全部 0 命中**，一度以为密钥另有来源。

`AppStartEvent.Run`（Rva=0xCF44）反汇编解开了谜团：

```
ldloca.s  0                          ; ← 状态机是 struct！(ldloca 取地址，非 newobj)
ldarg.1                               ; AppStart 事件参数
stfld args                           ; 存入状态机
ldloca.s  0
call AsyncETTaskMethodBuilder.Create
stfld <>t__builder
ldloca.s  0 / ldc.i4.m1 / stfld <>1__state
ldloc.0 → ldfld <>t__builder → ldloca + ldloca.s 0
call AsyncETTaskMethodBuilder<>.Start<状态机>(ref 状态机)
ldloca.s 0 → ldflda <>t__builder → get_Task
ret
```

**结论**：`AppStartEvent.Run` 的 async 状态机是 **struct**，用 `ldloca.s 0` + `stfld args` +
`AsyncETTaskMethodBuilder<>.Start` 构造，**永远不会出现 `newobj <Run>d__0`** → 之前的 newobj 扫描注定无效。
真正的构造点是 `AppStartEvent.Run` 的调用者（事件系统 `EventSystem.Publish`/`PublishAsync`），
以及 `AppStart` 事件实例的**发布者**（谁填充 key/keyIV/xorKey）。

## 5. 当前困难（未解决）

### 5.1 AppStart 发布者未定位
`EventType.AppStart` 实例由谁创建、三个密钥值从哪来（硬编码？读文件？服务器下发？）**未定位**。
- 没有 `newobj AppStart` 命中，说明可能也是 struct 构造、或密钥字段默认 null。
- 若 `key == null`，`GameDecryptionServices.LoadFromMemory` 运行时一定走**未加密路径**
  （`_dec/aa0/` 或 `_extracted/`），那么改配置根本不需要密钥。

### 5.2 客户端运行时实际读取哪份资源 未确认
`yoo/` 下有三份：
- `aa0/` — 加密 bundle（含 DataTable bundle `87c13c7e…bundle`）
- `_dec/aa0/` — 解密副本
- `_extracted/` — 明文提取（`GlobalConfig` = 明文 JSON）

决定配置修改落点：明文副本可**直接改**；加密 bundle 需拿到 key+iv（且 xorKey 可能还有异或层）。
**待验证**：跑一次客户端看日志/文件访问，或反汇编 Addressable 加载入口确认优先级。

### 5.3 点击 NPC/怪无反应（已解决）
`Player-prev.log` 定位到 `OperaComponent.ClickTarget` IL `0x42`：客户端在射线检测前读取队伍，
而服务器未下发 `M2C_TeamMember(20162)`，导致每次右键先抛空引用。服务器现于场景恢复消息后同步
当前玩家组成的单人队伍；Collider/layer 不是本次共同失效的根因。

## 6. 与「改成正式配置」的关系

| 侧 | 改动 | 依赖 |
|----|------|------|
| 服务器 `server-mysql/mapmonster.go` | ⚠ **本节已被取代**：早期做法是给 `beachFieldMonsters` 硬编码 ConfigId 1006，该函数**已不存在**。现行规则是 `configIDForMonsterID`：**`ConfigId = 10000 + PrefabId`**（`mapmonster.go:238`），由怪物 `MonsterId` 推导，不再手改坐标常量 | 客户端行存在才建得出怪 |
| 客户端 `MapMonsterConfig` | 补一行 `PrefabId=281`（急速荷叶球） | 数据表落点：明文副本（直接改）或加密 bundle（需密钥） |
| 战斗链路 | `battle.go` 已按正式配置（MonsterBase 10001 ×2） | 无需改动 |

## 7. 下一步

1. 验证客户端实际读取路径（aa0 加密 vs `_dec`/`_extracted` 明文）→ 决定 MapMonsterConfig 落点。
2. 若需写回加密 bundle：先定位 AppStart 发布者拿 key+iv（扫描 `Publish(AppStart)` 调用点；
   确认是否 struct 构造），再用 AES-CBC 解密→改表→重加密。
3. 服务器 `mapmonster.go` 换 ConfigId 并回出生点正式坐标 → `go build -buildvcs=false -o mhqserver.exe .` → 重启。
4. 点击无反应：查 prefab Collider2D/layer（与本次配置改动并行）。

## 附：分析脚本

`E:\cache\tmp\`：
- `dump_crypto3.py` — 反汇编 GameDecryptionServices / Encryption / GameKeyComponent / AppStartEvent 等
- `find_appstart_new.py` / `find_appstart_set.py` / `find_key_set.py` — 旧扫描脚本
  （⚠️ `find_appstart_set.py` 字节扫描有误报；结论以 `dump_crypto3.py` 的 opmap 精确反汇编为准）
