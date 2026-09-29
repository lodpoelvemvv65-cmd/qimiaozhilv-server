# 组队头像的血量/蓝量同步规则

队伍左上角每个队友那一格的当前血/蓝，**只能**由 `M2C_SyncUnitAttribute(20169)` 的
数值变化写进去。这一条决定了服务端所有「血蓝变了」的路径必须怎么推。

## 客户端为什么这么挑

- 重建列表（HotfixView rid 146 `ET.UpdateTeamHeadInfoEvent`）会
  `TeamMemberHeadInfoDic.Clear()` → `RemoveChildrenToPool()` → 每人
  `AddItemFromPool("ui://kqsmrpxleh2a1b")` → `UpdateHeadInfo(...)`。
  `UpdateHeadInfo` 只写等级和 `1002/1004`（上限），**不写当前值**。
- 当前值由 `1001` 的变化监听驱动：`UpdateHeadInfo_ChangeHp`（rid 152）从当前进度条
  的位置补间到新值，时长 `4.0 / max(bar.max, max(from,to)) * |to-from|`。
  满血补间约 4 秒，0 血约 0 秒。
- FGUI 会把还池的头像格复用给另一个队友，而上一条补间可能还在跑，于是迟到的补间
  把**上一个占用这格的人**的血量写了进来：满血的人那格显示出别人的 35891804，
  超过了自己的上限，进度条被夹成满血；0 血的人反而看着正常 —— 结果就是队里每个
  视角看到的血量互不相同。

结论：只推一次精确值不够，必须让这一格在重建之后**再收到几次数值变化**，并且每次
都重新读权威值，否则各视角停在各自重建时的快照上。

## 服务端怎么推

`team_head_vitals.go`：

- `teamHeadNudge(exact, max)`：给精确值造一个相邻的、float32 里真的能表示出来的
  抖动值。0 血抖到 1（proto3 的 `float` 为 0 时字段不上线，客户端解出来就是 0，
  等于没有变化）；`exact+1` / `exact-1` 在 float32 里表示不出来时（超过 2^24 之后）
  退化成 `math.Nextafter32` 的相邻可表示数。
- 重建前（`prepareTeamHeadHPRefresh`）先抖一下 `1001` **和** `1003`，紧接着重建
  （`sendTeamMember`）时给出精确值 —— 血量和蓝量走的是同一条链路，两个都要抖。
- 重建之后再补四轮「抖动值 → 精确值」：`150ms / 800ms / 2500ms / 5000ms`
  （`teamHeadVitalReloadDelays`），每轮在触发时才重读权威值。
- 每轮补发带一个 per-channel 代数 `channel.teamHeadReloadGen`：新一次重建会作废
  上一轮还没触发的定时器，避免多轮补发互相盖值。
- 只补别人的格子：自己那一格由本机数据驱动，服务端推给自己没有意义。

## 非战斗路径的血蓝变化

`team_vital_sync.go` 的 `pushTeamVitals(ch, hp, mp)` 把「我这边血蓝变了」推给同队其它人，
调用点在 `battleMu` / `itemBuffMu` **之外**（本函数内部要取 `teamMu`，而队伍换图/跟随
里已经有 `teamMu → battleMu` 的加锁顺序，反向加锁会互相等待）：

| 路径 | 位置 |
|---|---|
| 背包吃药 `20275` | `equip.go` `onUseGoods` 血蓝恢复分支 |
| 快捷物品槽吃药 `20235` | `mainui.go` `onUseMainUIGoods` |
| 魔法球战斗结束后自动回满 | `equip.go` `applyMagicBallRecover` → `scheduleTeamVitalSync` |

`applyMagicBallRecover` 同时会被战斗结算路径（`emitVictory` / `recoverPlayerAfterDefeat`，
调用方持 `battleMu`）调到，所以它排的是锁外补推而不是直接推。

## 战斗路径的血蓝变化

战斗里的每一击走的是 `emitCombatEvents` → `battleRecipients`：**队伍战斗**时收件人是
全队（`battle.party.channels`），队员之间本来就同步；但**单人战斗**（`battle.party == nil`，
例如单人开家族 BOSS）收件人只有打怪的人自己，于是挨打的掉血和收尾写回的残血都只推给
自己，队友的头像要等下一次重建（换图、进出战斗、重新组队）才对齐。

补齐办法是 `team_vital_sync.go` 的 `scheduleTeamVitalSync(target)`：

- 战斗里的血蓝变化全发生在 `battleMu` 里，而 `pushTeamVitals` 要取 `teamMu`，与队伍换图
  的 `teamMu → battleMu` 顺序反向，直接在锁内调会 ABBA 死锁。所以它只 `CompareAndSwap`
  一个 per-channel 标记 `channel.teamVitalSyncPending`，再 `AfterFunc` 50ms
  （`teamVitalSyncDelay`）在**没有任何锁**的 goroutine 里重读权威血蓝再补推。
- 标记让同一个间隔内的连续变化合并成一次（单人战斗每挨一下都会请求，一次战斗几十下）；
  定时器触发时才读 `ss.battleHP()/battleMP()`，拿到的永远是最新值，不会补推中间态。
- 触发时若会话已被顶替、连接已不是该角色的当前连接、服务器正在关闭，直接放弃。
- `pushTeamVitals` 按**队伍成员**推送，不看场景可见性：收尾失败会把打怪的人送回主城，
  这时两人已经不同场景，两条老的补发链路（`prepareTeamHeadHPRefresh` /
  `reloadTeamHeadVitals`）都会因为 `scenePlayerVisibleTo` 把这一格跳过，队伍头像却
  必须还是对的。

调用点：

| 路径 | 位置 |
|---|---|
| 单人战斗中挨打（非怪物目标的事件） | `battle_skill_events.go` `syncCombatEventHP` |
| 单人战斗失败、复活写回残血 | `battle_skill_events.go` `settleCombatLocked` |
| 单人战斗胜利写回残血 | `battle_actions.go` `emitVictory` |
| 退出战斗 / 中断挂机战斗 | `battleexit.go` `clearBattleForTransition`、`abortCombatStayOnMap` |

### 为什么这套配置不常见

`battle.party` 只在战斗参与者 > 1 时才装上，而这套服务端把队伍钉得很紧：队长换图会把
队员一起搬走；单人副本（试炼 `10004`、决斗 `10008`，`CopyConfig.CanTeam=0`）进场时
`leaveTeamForSoloInstance` 直接把人移出队伍；活动场景会把参与者一起搬走；启动器组队
计划还会拒绝试炼图和战斗中的成员。所以在正常流程里，「人在队伍里、队伍还完整、打出来
的却是单人战斗」只剩家族 BOSS 一条路：家族 BOSS 战不换图，参与者再按 `familyID` 过滤
（`battleParticipantsForPresentation` 的 `presentationFamilyBoss` 分支），不入家族的队友
被过滤掉，参与者只剩打怪的人，`MinTeamMember=1` 又允许单人开打。

## 回归

- 单测：`team_head_vitals_test.go`（抖动值、重建后的精确值、补发序列、过期代数、多轮定时）、
  `team_vital_sync_test.go`（补推权威值、突发合并成一次并取最新值、无队伍与被顶替的会话不推）。
- 真实 TCP：`test/verify_team_head_vitals.py` —— 自己注册 5 个一次性账号和角色、自己把
  血蓝改成 0 / 1 / 半血 / 差一点满并塞一瓶紫药（`GoodsBase 110342`，PercentHp=1
  PercentMp=1），跑完自己清理。断言：每个视角看到的最终值都等于 `players.current_hp/mp`、
  同一队友在 5 个视角完全一致、0 血 0 蓝就是 0、半血吃药回满后队友那格跟着满、
  满血再吃被拒且不推任何血蓝。
- 真实 TCP（单人战斗）：`test/verify_solo_battle_team_vitals.py` —— 打怪的人建家族、队友
  不入家族，两人组队后在主城开家族 BOSS，得到一场真·单人战斗。断言队友收到并停在收尾
  残血上、三处（队友头像 / 打怪的人自己 / MySQL）一致、且这些帧不可能是「重建补发」送来
  的（窗口里没有 `20162`，帧也不是「抖动值 → 精确值」的后半帧），另外两人换图分开后
  队友那格仍然对。
- 只读诊断：`test/diagnose_team_head_hp.py`。
