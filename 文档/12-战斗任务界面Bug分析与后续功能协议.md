# 12-战斗任务界面Bug分析与后续功能协议

> 本文档**只做逆向分析，不改服务端代码**。⚠ 2026-09-14 复核更正：原文写的
> 「冻结 `server/*.go`」指的是旧的 SQLite 实现——**当前唯一服务端是 `server-mysql/`**，
> 下文所有 `battle.go` / `growth.go` / `login.go` 等路径均指 `server-mysql/` 下的同名文件。
> 内容：战斗界面 bug 根因、任务"完成不了"的客户端侧分析、
> 12:44 Player.log 逐条解读、以及后续功能（商店/强化/邮件/好友/星魂等）协议解出清单。

## 0. 当前服务器实测结论（2026-08-14 14:20，`server-mysql/test/verify_taskbattle.py` 全绿）

| 链路 | 结果 |
|------|------|
| 点苹果JJ 1012 → OpenTaskUI(20216) TaskList | ✅ 10011(3 可交付)/10086(1 可接) |
| CompleteTask 10011 | ✅ 成功（对话任务，直接完成） |
| 再点 NPC → 10012(1 可接)/10013(1 可接) | ✅ 前置判定正确 |
| 点字段怪进战斗 → 20047/20050 | ✅ |
| 技能 20233 → 20075/20077(EffectId=2101)/20078/20237 | ✅ |
| 怪物反击 20076(500001) | ✅ |
| 胜利 20054+20084 → 接 10012 → 打怪 → CompleteTask 10012 | ✅ |

**服务器侧任务链路与战斗特效均已正常**。若客户端仍"完成不了"，见 §3 客户端侧排查。

## 1. 战斗界面 bug 根因：20077.EffectId=0 → 客户端特效刷屏

### 1.1 现象（Player.log 实锤）

```
config == null which type = Cal.DataTable.EffectConfigCategory when id = 0
effectConfig == null where id= 0        （×几十次，每次技能都刷）
```

### 1.2 链路

```
服务器 onUseMainUISkill(20233)
  → effectID = SkillConfig[skillID*100].EffectId
  → 20077 M2C_PlaySkillEffect{EffectId=effectID}
客户端 M2C_PlaySkillEffectHandler (Hotfix rid 216)
  → PlayEffect{effectId=message.EffectId}
PlayEffectEvent.Run (HotfixView rid 704)
  → EffectConfigCategory.Instance.Get(effectId)   ← effectId=0 → null → 刷屏
  → 按 EffectType 分支 PlayBulletEffect/PlayCommonEffect/PlayContinueEffect
```

### 1.3 根因：82 个技能没有 EffectId

SkillConfig 共 130 个技能主体行（xx00），**48 个有 EffectId（2101~2401），82 个没有**。
无 EffectId 的多为辅助/被动/强化类：
- 110301 魔鬼式训练、110401 神明护佑、110501 铜墙铁壁、110601 坚不可摧、
  110402 缴械、110502 金刚不坏体、110303 团结一心、120201 不动如山、
  210602 幸运宠儿、310301 急救、310401 高级急救、310501 连锁注射、310601 人体学……

**服务器对无 EffectId 的技能也推 20077{EffectId=0}** → 客户端 Get(0)=null → 刷屏。

### 1.4 修复建议（服务端，用户自行改 battle.go）

```go
effectID := int32(num(cfg["EffectId"]))
...
// 仅当特效 id 有效时才推 20077（线上只对有特效的技能播特效）
if effectID > 0 {
    s.sendPush(ch, protocol.OpM2C_PlaySkillEffect, &protocol.M2C_PlaySkillEffect{
        UnitId: pid, TargetId: first.id, Time: delay, EffectId: effectID,
    })
}
```

另外建议：**怪物的 20076 MonsterPlaySkill 本来就不带特效字段**（客户端只播攻击动画，
不触发 PlayEffect），怪物侧无需 20077——当前实现正确，无需改动。

## 2. CharacterUI.ShowSkin NRE 根因：SkinId=0

### 2.1 现象

```
SkinBase == null where Id = 0
System.NullReferenceException: at ET.CharacterUI+<ShowSkin>d__21.MoveNext (CharacterUI.cs:328)
data =null where index = 0..11      ← 角色面板属性列表空
System.NullReferenceException: at ET.CharacterUI.Destroy (CharacterUI.cs:355)
```

### 2.2 链路

```
M2C_SendCharacter(20257) / M2C_GetCharacter(20252) → ClientUnitCharacterComponent.Update
  → UpdateCharacterUI → CharacterUI.ShowNumText → ShowSkin(SkinId)
ShowSkin: SkinBaseCategory.Instance.Get(SkinId).PrfabId  → SkinId=0 → SkinBase[0]=null → NRE
```

### 2.3 修复建议（服务端，用户自行改）

SkinBase 表 86 行（`{_id, PrfabId}`，_id 1~86）。把角色 SkinId 设为 **1~4（按职业）**：
- 服务端创建角色时 `skinID = jobID`（1~4），或 buildUnitCharacter 里
  `skin := ss.skinID; if skin <= 0 { skin = jobID }`。
- 已建的旧角色（skin_id=0）需在 DB 或加载时兜底。
  ✅ 2026-09-14 复核：加载时兜底**已实现** —— `server-mysql/equip.go:679 applySkinEquipOnLoad`
  在没穿皮肤道具且 `!isSkinIDValid(ss.skinID)` 时置 `ss.skinID = ss.jobID`。

### 2.4 "data =null where index = 0..11"

角色面板 `m_infoList` 12 个属性格的数据来自 `CharacterUI.AttributeDic`
（AwakeAsync 按 GObject.name 建字典）。`data=null` 是**列表格子没有对应属性数据**时的
兜底打印（非致命）。属性值本身经 NumericComponent 推送（12 条 20169），
只要 20169 在进游戏/升级/加点后推送，ShowNumText 能显示——但需**重开角色面板**（打开时
才走 ShowNumText）。建议实机验证：进游戏后打开角色面板看数值是否显示。

## 3. "任务完成不了"客户端侧排查

服务器侧 10011/10012 提交全部通过（§0）。客户端侧可能原因：

1. **任务面板重复创建**：日志 `ui.Name(ui://Task/NPCTaskUI) already exist`（每次点 NPC 都
   OpenUI 新实例，旧的未 Hide）。快速连点 NPC 会堆叠多个面板互相遮挡 → 点不到按钮。
   （OpenNPCTaskUIEvent.Run：`CheckSceneError(zoneScene, {10004,10006,10007,0,...})` 通过后
   `CreateAsync → GWindow.add_OnHideEvent`。10006 在允许场景列表，场景判定不是问题。）
2. **按钮三态逻辑**（NPCTaskUI.AwakeAsync 1269 + b__2 1267）：
   - TaskState=1 → isAccept=true，按钮显示"接取"（0x70004B03）
   - TaskState=2 → isAccept=false，按钮显示"进行中"（0x700046FB 交付?，需实机看）
   - TaskState=3 → isAccept=false，按钮显示"交付"
   - 点按钮 → isAccept ? AcceptTask : **CompleteTask{TaskId, IndexList=m_RewardList.GetSelection()}**
   - 关键：交付时把 `m_RewardList.GetSelection()` 原样写入 `IndexList`，但按钮没有按
     `TaskBase.RwardCount` 校验数量，实机会发送空列表。
   - ⚠ **2026-09-14 更正：下面这段结论已作废。** 原文说「服务端忽略该字段、发放 `RewardArr` 全部奖励，
     不能新增“必须恰好选择 N 个”的校验，否则客户端会掉线」——**现行服务端恰恰会校验**：
     `server-mysql/task.go:536 taskRewardSelectionMessage` 在 `RwardCount > 0` 时要求
     `len(indexList) == RwardCount`，并拒绝越界与重复选择（`task.go:346` 交付前调用，
     回归见 `task_test.go::TestCompleteTaskWithInvalidSelectionReturnsNativeTip`）。
     原文担心的“掉线”并**没有**发生：拒绝时只填 `resp.Message = "请选择数量的奖励！"`、
     **刻意保持 `Error=0`**（`task.go:342` 注释写明：非零 Error 会让 `Session.Call` 抛异常，
     把异步按钮处理器卡死），因此不会走 RPC 异常路径。
     所以「必须恰好选 N 个」这个校验是可以做、而且已经做了的。
3. **NPC 点击距离**：右键需距离 ≤2.5 且非战斗/移动中（OperaComponent.ClickTarget 逻辑）。
4. **任务状态刷新**：M2C_SendTaskState(20225) → `ZoneScene.ChangeNPCTaskState(NPCState)` 更新
   NPC 头顶感叹号/问号。若服务器不推，NPC 头顶标记不变化（不影响面板内操作）。

建议实机操作顺序：走近苹果JJ(1012) → 右键 → 面板出现（若多面板重叠先关掉旧的）→
点任务行（10011）→ 按钮显示"交付" → 点交付 → 面板隐藏 → 背包出现奖励（药水+马桶塞）。

## 4. 后续功能协议解出（20240-20410 全表）

> 字段级 List 字段（BagMapList 等）未列，见 11 文档 §1。以下为线上协议完整清单，
> 服务器可按需逐个实现。

### 4.1 技能学习/重置（20239-20250）

| opcode | 消息 | 字段 |
|--------|------|------|
| 20239 | C2M_GetSkill | — |
| 20240 | M2C_GetSkill | —（含 SkillInfo 列表，字段级） |
| 20241/42 | C2M/M2C_SaveAutoSkill | 自动技能槽 |
| 20243/44 | C2M/M2C_LearnSkill | SkillId=1 |
| 20245/46 | C2M/M2C_FreeReSetSkill | 免费洗技能 |
| 20247/48 | C2M/M2C_GetReSetSkillPrice | Voucher=1 |
| 20249/50 | C2M/M2C_ReSetSkill | 付费洗技能 |

### 4.2 角色/背包（20251-20276）— 已实现，见 11 文档

### 4.3 装备强化/升级/词条/宝石（20277-20278, 20340-20350）

| opcode | 消息 | 字段 | 说明 |
|--------|------|------|------|
| 20277/78 | C2M/M2C_Upgrade | Index=1, isLock=2 | 装备升级（背包格） |
| 20340/41 | C2M/M2C_StrengthEquip | bagIndex=1, plusItemIndex=2 → isSuccess=1 | 强化（消耗材料） |
| 20342/43 | C2M/M2C_TransLevel | Level=1 | 转生等级 |
| 20345/46 | C2M/M2C_RefreshEquipMainAttribute | bagIndex=1 | 洗主属性 |
| 20347/48 | C2M/M2C_RefreshEquipAffix | bagIndex=1 | 洗词条 |
| 20349/50 | C2M/M2C_DismountGem | bagIndex=1, gemIndex=2 | 卸宝石 |
| 20355/56 | C2M/M2C_TransferJob | jobType=1 | 转职 |

对应数据表：EquipUpgrade.json / Strengthentable.json / StrengthPlusConfig.json /
EquipAffixConfig.json / GemInlayConfig.json / TransferJobConfig.json。

### 4.4 商店（20294-20297, ShopBase）

| opcode | 消息 | 字段 |
|--------|------|------|
| 20294/95 | C2M/M2C_GetOpenRemoteStore | → Price=1（远程商店开启价） |
| 20296/97 | C2M/M2C_OpenRemoteStore | 打开远程商店 |

商店购买另有 BuyInShop 段（20000 附近，见 protocol_dump.txt）。

### 4.5 邮件（20279-20290）

```
MailItem{ItemId=1, Count=4, IsLock=5, IsHasItem=6}
Mail{Title=2, Content=3, RemainTime=4, SenderName=5, State=7, Id=8}
20281/82 GetMail(Page=1) → MailCount=2
20283/84 ReceiveMail(MailId=1, Page=2) → MailCount=3
20285/86 DeleteMail / 20287/88 ReceiveAllMail / 20289/90 DeleteAllMail
```

### 4.6 聊天（20298-20301）

```
20298/99 C2M/M2C_RequestChat(Content=1, Type=2, Id=3)
20300 M2C_SendNormalChat(Content=1, Type=2, Id=3, Name=4, IsSystemBrocast=5)
20301 M2C_SendSystemChat(Type=2, Name=4, IsSystemBrocast=5)
```

### 4.7 答题活动（20302-20311）

```
20302 M2C_OpenQuestUI(Card=1)
20304/05 StartAnswerQuest → ConfigId=1
20306/07 NextAnswerQuest → ConfigId=1
20308/09 AnswerQuest(Answer=1) → Time=1, ContinueCorrectCount=2, Scord=3
20310/11 GetQuestScordInfo（QuestScordInfo{Id=1,Scord=2,Name=3,Level=4,Time=5}）
```

一轮固定 `Gameplay.quiz.questions_per_round`（默认 10）题，答对答错都计入。答满后
`20306/07` 必须回非空 `Message`、`Error=0`、不下发 `ConfigId` 来结束本轮：原版客户端
`ET.QuestAnswerUI::<OnClickAnswer>d__7` 只认「`M2C_NextAnswerQuest.Message` 非空」这一条
结束路径（弹提示后不再请求下一题）；回 `Error` 会让 `Session.Call` 抛异常、界面卡在最后一题上，
继续下发 `ConfigId` 则永远出新题、玩家结束不了答题。结束后界面仍停在最后一题，误点选项时
`20308/09` 也回同样的结束提示而不是业务错误码；要再答一轮由「开始答题」重新发 `20304`。

### 4.8 充值/体力（20312-20319）

```
20312/13 ChargeVoucher(YuanBao=1)   20314/15 ChargeCoin(Gem=1)
20316/17 GetAddEnergyPrice → Price=1, Energy=2   20318/19 AddEnergy
```

`20314/20315` 的两个弹窗文案在 `HotfixView.dll` 里是成对的：
「请输入您要兑换的元宝数量，元宝：代金券=1：2」（20312）与
「请输入您要兑换的星币数量，星币：铜币=1：20」（20314，`ET.BagUI::<AwakeAsync>b__14_3`）。
输入框填的是**冒号左边的数量**，冒号右边是拿到的货币：`20314 Gem=100` 就是花 100 星币、
按 1 星币 = 20 铜币进账 2000 铜币（星币是 GoodsBase 110205、藏在 bagSlot `-1000000`，
走 `starCoinBalance`/`spendStarCoin`）。星币不足时回 `Error=0 + Message="星币不足"`，
不扣星币也不加铜币。旧实现方向反了（扣铜币给星币），已修复。

### 4.9 手动装备副本 / PVP（20322-20327, 20338-20344）

```
20322/23 StartManulEquipBattle(Id=1) → ConfigId=1
20324/25 MakeMunalEquip(Type=1, IsRare=StoneType)   手搓装备
20326/27 RequestPersonalPvp
20338/39 GetPvpBoardInfo → battleCount=1, scord=2, isMatch=3, matchCount=4
20344 PvpRankInfo{name=1, level=2, scord=3, jobIs=4}
```

### 4.10 宠物（20370-20390）

```
20370 M2C_SyncPet(petId=1, level=2, exp=3, intimacy=4, name=5, isShow=6, UnitId=7, active=8)
20371 C2M_ShowPet
20375/76 GetPetInfo → eatCount=1, active=2, petState=3, remainTime=4
20377-20386 StartPetPlay/Explore/Experience/EndPetAction/GetPetActionReword
20387/88 GetPetQuickEndPrice → voucher=1
20389/90 UpgradePet(index=1)
```

### 4.11 星魂背包（20395-20410，**已完整实现**）

> ⚠ 2026-09-14 更正：原标题写「当前空 ACK」，**已不成立**。现行 `server-mysql/starsoul.go:635`
> 的 `onGetStarSoulBag` 会返回完整字段级 `ItemList` / `UsedIdMap` / `SuitKVs`（见 18 文档）。
> 原文 §4.11 中“20396 空 ACK”的旧描述仅在 2026-08 修复前成立。

```
StarSoulNetItem{Id=1, typeId=2, level=3, exp=4, posType=5, quality=6, isUsed=7, main=8, isLocked=11}
20396/20400 GetStarSoulBag       ← 客户端进主城固定请求，必须 ACK（已修）
20401 SyncStarSoulBagInfo(UnitId=3)   20402 SyncStarSoulBag(Id=1, item=2)
20403 SyncStarSoulBagItemUsed(key=3, value=4)
20404/05 UpgradeStarSoulItem(itemId=2)   20406/07 PutonStarSoulItem(itemId=2)
20408 LockStarSoulItem(isLock=1, itemId=2)   20409/10 StartActive(itemId=2)
```

### 4.12 地图/传送/其他（20031-20040, 20331-20332, 20351-20354, 20363-20368）

```
20031/32 RequestEnterMap(MapId=1)        20033 ChangeMap(X=2,Y=3,MapId=4)
20034 EnterMap(UnitInfo=1, PvpUnitCharacter=2)   20039 StartupTransPoint(MapId=1)
20331/32 TransferRequest(MapIndex=1)     地图传送（TierId）
20351/52 GetMapCoin   20353/54 AddMapCoinCount   地图金币
20357/58 GetUserInfo(Id=1) → Name=1,Level=2,JobId=3
20359/60 SendGMCMD(cmd=1)   20361/62 OpenGMUI    GM 调试
20363/64 ChangeNickName(name=1)
20365-20368 StartMainStoryAI(Index=1)/EndMainStoryAI   主线 AI 托管
20391/92 ClickMapUnit(Id=1, type=2)    ← 字段怪点击（已实现）
20393/94 StartSpaceTravel(index=1)    星空旅行
```

## 5. 客户端关键 UI 链路备忘（HotfixView rid）

| 组件 | rid | 说明 |
|------|-----|------|
| OpenNPCTaskUIEvent | 113 | CheckSceneError({10004,10006,10007,...}) → CreateAsync → OnHideEvent |
| NPCTaskUI.AwakeAsync | 1269 | 任务行渲染（Name+GetTaskStateString+点击） |
| NPCTaskUI b__2 | 1267 | 任务行点击：三态 → 按钮显示/隐藏 + 详情填充 |
| NPCTaskUI b__0 | 1317 | 按钮：isAccept ? AcceptTask : CompleteTask{TaskId, IndexList=GetSelection} |
| M2C_OpenTaskUIHandler | 258(Hotfix) | → NPCTaskUI_Open 事件 |
| M2C_SendTaskStateHandler | 262(Hotfix) | → ZoneScene.ChangeNPCTaskState |
| UpdateNPCTaskStateEvent | 826 | 设置 NPC 头顶 m_taskState Controller |
| M2C_PlaySkillEffectHandler | 216(Hotfix) | → PlayEffect{effectId=20077.EffectId} |
| PlayEffectEvent | 704 | EffectConfigCategory.Get(effectId) → 按 EffectType 分支播放 |
| CharacterUI.ShowSkin | d__21 | SkinBaseCategory.Get(SkinId) → null 崩溃（SkinId=0） |
| BagUI.AwakeAsync | 1047 | C2M_GetBag → UpdateBagUI（见 11 文档） |

## 6. 建议的服务端待办（**第 1、2 项已完成**，2026-09-14 复核）

> ⚠ 本节标题的「待办」仅对第 3、4 项仍成立；第 1、2 项**早已实现**：
>
> 1. ✅ `battle.go`：`effectID > 0` 才推 20077 —— 已实现，见
>    `server-mysql/battle_actions.go:416`（`if spec.effectID > 0` 才推 `M2C_PlaySkillEffect`）
>    与 `battle_skill_events.go:44`（`case CombatEventVisual: if event.EffectID <= 0 { continue }`）。
> 2. ✅ `growth.go / login.go`：SkinId 兜底 = jobID(1~4) —— 已实现，见
>    `server-mysql/login.go:1377`（`if skinID <= 0 || !isSkinIDValid(...) { skinID = p.JobID }`，
>    注释直接引用了本文档 §2.3）及 `equip.go:679 applySkinEquipOnLoad`。

1. ~~**battle.go**：`effectID > 0` 才推 20077（消除特效刷屏）。~~ ✅ 已完成
2. ~~**growth.go / login.go**：SkinId 兜底 = jobID(1~4)（消除 ShowSkin NRE + "没有skinId=0"）。~~ ✅ 已完成
3. **实机重测**：重启客户端 → 进游戏 → 苹果JJ 任务链 → 战斗（确认无 EffectConfig 刷屏）→
   开角色面板（确认属性数值显示、无 NRE）。
4. 后续功能按 §4 协议表逐项实现（优先：商店 20294、强化 20340、技能学习 20243、
   邮件 20281、聊天 20298）。
