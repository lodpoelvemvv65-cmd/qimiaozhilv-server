# TCP 协议实测

此目录是当前 MySQL + Redis 服务端所有正式 TCP 协议实测脚本的唯一位置。
脚本默认连接本地 `127.0.0.1:7756`；支持环境变量的脚本可通过
`MHQ_TEST_HOST` / `MHQ_TEST_PORT`（部分旧脚本为 `MHQ_HOST` / `MHQ_PORT`）覆盖目标。

从 `server-mysql/` 目录运行，例如：

```powershell
python .\test\verify_login_auth.py
python .\test\verify_trade_compat.py
python .\test\e2e_test.py
$env:MHQ_TEST_SCOPE = 'mainui'
python .\test\e2e_test.py
Remove-Item Env:MHQ_TEST_SCOPE
$env:MHQ_TEST_PORT = '7757'
python .\test\verify_mysql_total.py
```

## 写脚本时的三个坑（自建 TCP 客户端都要处理，2026-09-16 踩过）

打长观察窗口的脚本（如上面的怪物周期效果/反伤）必须处理这三件事，否则会以
「服务端把我踢了」或「帧丢了」的形式随机失败：

1. **`connIdleTimeout = 30s`**（`tcp_srv.go:18`）：客户端 30 秒不发任何请求就被断开。
   原生客户端每 2 秒发一次 `20333 C2G_Ping`，长等待里要照做（`resp` 是 `20334`）。
2. **TCP 是字节流，`recv` 可能只回半个帧**：`settimeout` + `read_exactly(N)` 的写法
   在超时时可能已经吃掉帧头的一部分字节，字节流随即错位、后面读到的全是垃圾
   （症状：窗口跑到一半 `WinError 10053` 断线，服务端日志 `broadcast OffLine`、
   战斗被 `battle_ended_or_replaced` 取消）。正确做法是把收到的字节全部留在
   一个持久缓冲里，让超时只可能发生在「一帧都还没开始」时。
   注意线上帧长字段**包含**那 2 字节 opcode，整帧 = `length + 2` 字节。
3. **不要只分析 `collect_timed` 的返回值**：`call()` 在等 RPC 响应时会把途中的帧
   收进 inbox，漏掉它们会让「成对帧」「周期跳伤」这类判据随机失败。
   统一记一条带到达时刻的 `timeline`，分析时从它取（可用时间下界切窗口）。

参考实现：`verify_monster_effects.py` / `verify_monster_reflect.py` 的 `Client`。

## 分类

- GM 物品与邮件：`verify_gm_items_mail.py`（由 `MHQ_TEST_GM_ITEMS_TCP=1 go test ./cmd/gm-api -run TestGMItemsMailLiveTCP -v` 提供临时本地管理会话；覆盖中文/职业/部位目录、直接发放、邮件多附件、在线推送、领取、防重及重连持久化，自动清理测试角色）
- 技能点与技能学习：`verify_skill_point.py`（由 `MHQ_TEST_GM_SKILLS_TCP=1 go test ./cmd/gm-api -run TestSkillPointLiveTCP -v` 提供临时本地管理会话，只用 GM 造「技能点 + 秘籍」的干净角色；随后全程走 20243/20244/20245 实测：每升一级（含首次学习）都扣 1 技能点并另扣当前等级行的秘籍，技能点为 0 时即使秘籍管够也拒绝且 `Error=0`、状态不变，每次扣点推 20025 的 `UnitCharacter.SkillPoint`，免费洗点按等级返还并能重新学习，重登落库一致，且前置技能只有 1 级时下一个技能被拒绝、前置学满后才解锁）
- 星币兑换方向：`verify_charge_coin.py`（由 `MHQ_TEST_GM_CHARGECOIN_TCP=1 go test ./cmd/gm-api -run TestChargeCoinLiveTCP -v` 提供临时本地管理会话，星币只能由 GM 产出；实测 20314/20315 是「花星币换铜币」——没有星币时回 `Error=0 + Message="星币不足"` 且余额不变，有 1000 星币时 `Gem=100` 扣 100 星币加 2000 铜币、星币不出现在可见背包快照 20260，超额请求同样拒绝且原状不变，重登与 GM 侧 `player_items` 落库一致）
- GM 手工装备：`verify_gm_manual_equip.py`（由 `MHQ_TEST_GM_MANUAL_TCP=1 go test ./cmd/gm-api -run TestGMManualEquipLiveTCP -v` 提供临时本地管理会话；覆盖候选池的中文名与部位宝石规则、品质/星级/强化/随机属性/词缀/宝石一次设成后的在线推送与落库、**重登不变样**——洗练词缀三种稳定态（空 / 正好槽位数 / 1 条六维）重登后一个字段都不变、16 类非法输入回传具体中文原因、被拒提交不留部分写入，自动清理测试角色）
- GM 调整词缀：`verify_gm_equip_affix.py`（由 `MHQ_TEST_GM_AFFIX_TCP=1 go test ./cmd/gm-api -run TestGMEquipAffixLiveTCP -v` 提供临时本地管理会话；词缀是从「手工装备」里独立出来的单独入口 `player.equip_affix`，覆盖整组设置 / 换成 1 条六维 / 清空三种最终状态、**只碰词缀**——星级/品质/强化/随机属性/宝石在动作前后逐字段不变、三种稳定态**重登都不被 `ensureEquipmentAffixCount` 补齐或裁掉**、非法词缀（条数不符 / 档位不对 / 不在词缀表 / 重复 / 六维混普通）回传具体中文原因且被拒后词缀不动，并用「星级 5 却 0 条随机属性」做对照：手工那条改星级被联动规则拒绝、词缀这条不受影响，自动清理测试角色）
- GM 镶嵌宝石：`verify_gm_equip_gem.py`（由 `MHQ_TEST_GM_GEM_TCP=1 go test ./cmd/gm-api -run TestGMEquipGemLiveTCP -v` 提供临时本地管理会话；宝石是从「手工装备」里独立出来的单独入口 `player.equip_gem`，覆盖满槽镶嵌 / 只换一个孔 / 整件卸下（全 0）三种最终状态、**只碰宝石**——星级/品质/强化/随机属性/词缀在动作前后逐字段不变、重登后宝石槽原样、6 类非法宝石回传具体中文原因且被拒后宝石槽不动、锁定装备被拒且解锁后可用，并用「星级 1 却 0 条随机属性的新装备」做对照：手工那条改星级会被联动规则拒绝、宝石这条不受影响，自动清理测试角色）
- 登录与覆盖：`verify_login_auth.py`、`verify_voucher.py`、`verify_duplicate_login.py`、`verify_protocol_coverage.py`
- 端到端与压力：`e2e_test.py`、`verify_mysql_total.py`、`verify_concurrency.py`
- 背包与成长：`verify_bag.py`、`verify_discard.py`、`verify_item_upgrade.py`、`verify_levelup.py`、`verify_mainui.py`、`verify_starsoul_random.py`、`verify_task_reward_selection.py`（任务奖励少选/多选提示后连接可继续）
- 地图与战斗：`verify_fieldmonster.py`、`verify_maptest.py`（含战斗奖励 `20301/System` 实测）、`verify_manual_equip_dungeon.py`（三人手工副本字段怪、阵位配置和专用 `20099` 实测）、`verify_monsterhp.py`、`verify_monster_damage.py`（怪物 20075/20077/20078/20169 真实伤害与 HP 回归）、`verify_portal_items.py`、`verify_quitbattle.py`、`verify_region.py`、`verify_scenes.py`、`verify_spawn.py`、`verify_taskbattle.py`、`verify_trial.py`
- 主城挂机经验与原版面板：`verify_town_idle_exp.py`（新角色停在主城 `10004`，逐跳核对线上抓包的 56/67/74 三跳：每跳 +24560、经验余数 868/264/757、且 `1002/1004/1005/1006/1007/1008/1009/1011/1012/1017/1034/1035` 十二项面板全等于线上原值；换到非城镇地图 `1000601` 再跨一个节拍等级/经验不变，验证挂机经验只在城镇生效）
- 持续伤害/持续治疗节拍：`verify_periodic_tick.py`（护士「持续治疗」`310501` 落到施法者身上后逐帧核对线上抓包节拍：`20080` Add 的 `Time=14000`、之后恰好 3 次回血 `20078` 落在 +4/+8/+12 秒、越过 14 秒持续期不再出现第 4 跳；节拍取自运营配置 `Gameplay.combat.effect_tick_interval_ms`（默认 4000、热重载），规则是首跳延后一个间隔、跳数 = floor(时长/间隔)，与客户端表里的 `thinkInterval` 无关。只连本机服，临时账号自动清理，帧归档到 `参考数据/抓包归档/periodic-tick-local-*.jsonl`）。带几率触发的持续伤害（开大脚 `210603` 流血 20% 几率 / 每跳 4% 最大生命值 / 14 秒，热眼射线 `410601` 灼烧 14% 几率 / 每跳 3% 最大生命值 / 10 秒）用一次性探针 `_probe_periodic_dot.py`：`python ./test/_probe_periodic_dot.py 210603 3 10 20`；它把层数进度预置到沙滩第 10 层再逐层发 `20031` 走过去（不用真打过去），在 2000 血的怪上逐个打印「子弹直伤」和「每 4 秒一跳、等于目标最大生命值 pct%」的持续伤害。几率不固定，确定性验证看 `skill_periodic_damage_test.go` 与 `battle_periodic_tick_test.go`
- 怪物有害状态与周期效果：`verify_monster_effects.py`（打**家族 5 阶 BOSS**——怪物 50005 的技能组 10030 有 4/7 的槽是 DOT（500010 中毒 26 秒 / 500021 流血 11 秒 / 500023 燃烧 14 秒），是唯一可由单账号直接开打的 DOT 怪物，主线地图 `20031` 有逐层推进硬门禁到不了带 DOT 的主线怪、开家族只要一个名字。判据是**同一个伤害值以约 4 秒间隔反复出现**——不要用「到达前若干秒没有怪物施法」判，怪物每 ~6 秒一波、DOT 每 4 秒一跳必然重叠，会把挨着施法的跳漏掉只剩 8 秒假间隔。这个脚本是 `statusContestChance` 那处修复的验收：修复前同一场景 **0 跳**（怪物有害状态被玩家抗性挡死，见 `文档/32` §八），修复后实测 9 跳、间隔 4.0s。`MHQ_MONSTER_EFFECT_SECONDS` 调窗口（默认 75））
- 怪物反伤：`verify_monster_reflect.py`（同一只家族 5 阶 BOSS 抽到 500032「打我试试」给自己 100% 反伤 11 秒后，玩家主动出手，断言「我打中 BOSS」与**等量**打回我两帧成对出现——实测 `dt=0.000s`、1 → 1。反伤属 `buffType=0` 有益状态，不走状态命中判定，与上面那条是两条路，各自守一条。`MHQ_REFLECT_WAIT_SECONDS` 调等待上限，默认 200 秒：500032 占 1/7 槽位）
- 战斗状态清理：`test_battle_state_cleanup.py`（真实 TCP 打满 `20054` 胜利后换图，逐帧核对 `20080` 只复用本会话先 Add 过的 Id 且 Time=0，不再重放固定图标表；线上不为战斗结束补发清理帧，胜利后不再出现 Reduce，特效由客户端按 Add 包里的 Time 自行销毁）
- 军官反击：`verify_tactical_counter.py`（连接已启动的本地服，覆盖战术*反击 Lv1/Lv2/Lv3 的五人状态与广播、队长/队员施放、睚眦必报仅自身、单人对照，以及军官＋运动员的职业普攻反击，共 6 个场景；Lv3 多人场景核对反击触发后持有者那条「不播施法动作」的普攻弹道（`20077`，飞行时长取配置 `DelayTime`）、命中与扣血，直接核对 MySQL 技能配置，临时账号自动清理，战斗协议帧归档到 `参考数据/抓包归档/`）
- 多人玩法：`verify_multiplayer.py`（含队员技能动作、`20077` 特效、伤害与 HP 广播）、`verify_playersync.py`（含称号空值、宠物初次创建及 `20370` 实时互看）、`verify_team.py`、`verify_trial_party.py`、`verify_launcher_team.py`（启动器账号验证、短期计划及自动组队）
- 组队头像血蓝：`verify_team_head_vitals.py`（自己注册 5 个一次性账号和角色、自己把血量/蓝量改成 0 / 1 / 半血 / 差一点满并塞一瓶紫药，跑完自己清理；逐视角核对 `20162` 重建之后的每一轮「抖动值 → 精确值」补发都停在 MySQL 落库值上、同一个队友在 5 个视角完全一致、0 血 0 蓝就是 0，以及半血当场吃药回满后队友那一格跟着满、满血再吃被拒且不推任何血蓝）、`verify_solo_battle_team_vitals.py`（组队时打「单人战斗」的收尾残血同步：打怪的人建家族、队友不入家族，两人在主城开家族 BOSS——家族过滤把队友排除出参与者，于是队伍完整但 `battle.party == nil`；断言队友当场收到并停在收尾残血上、队友头像 / 打怪的人自己 / MySQL 三处一致、这些帧不可能是头像重建链路送的（窗口里没有 `20162`、帧也不是「抖动值 → 精确值」的后半帧）、而且两人换图分开后队友那格仍然对。自己注册 2 个一次性账号和角色、自己把打怪的人血量改成 2、跑完自己清理）、`diagnose_team_head_hp.py`（只读诊断：登录现有 5 人队伍把 `20162/20169` 原样打出来，用于判断血蓝不一致是服务端没推还是客户端画错）
- 面对面交易：`verify_trade_compat.py`（交易/组队申请隔离、双方物品和金银铜、远端只读、取消重开、双锁自动成交、成交后重开普通仓库并取出物品；64 项断言且不发送二次确认请求）
- 五人副本：`verify_five_member_dungeon.py`（5 个真实 TCP 客户端组队进入时空旅行并逐人扣次）
- 界面与综合模块：`verify_newmodules.py`、`verify_npc.py`、`verify_rank_quiz_pvp.py`（含答满 10 题后回结束提示、不再下发新题、误点不报错、可用 20304 重新开始一轮）
- 左上状态右键：`verify_buff_status_tip.py`（20045 -> 20262 原版 TipUI 动态状态）
- 线级诊断：`verify_tags.py`、`verify_unitchar.py`
- 战斗抓包诊断：`inspect_combat_capture.py`（重组 TCP 并解析 `20075`-`20078`、`20169` 的原版包序与字段）
- 线上 opcode 普查：`inspect_opcode_census.py`（离线读 `参考数据/抓包归档/*.pcapng`，按 7756/7757/7758 三个线上游戏端口统计每个 opcode 的出现次数，用来回答「线上到底发不发这条包」；`--absent 20081 20076` 把「必须没有样本」变成退出码断言，当前线上 12 份样本里这两个都是 0 条）
- 角色构建诊断（面板 / 星魂 / 穿戴）：`inspect_character_build_capture.py`（线上角色属性多出一截时定位是谁给的：解 `20400/20401/20402/20403` 星魂全量与增量同步——逐件 typeId/等级/品质/主属性/副属性及 `usedIdMap` 槽位，解 `20003/20252` 的角色等级/转生/潜能点/宠物，解 `20259/20260/20274` 的背包（字段 1）与穿戴（字段 2）快照，`--bonus` 用服务端同一套 `starSoulBonus` 公式把已穿戴星魂加成算出来和面板对账，`--op N` 对任意 opcode 做泛型 protobuf 递归 dump，`--json` 导出结果。注意线上只推属性「值」，不推 `*_Base/*_Add` 拆分键）
- 线上穿戴属性复核：`analyze_online_worn_attributes.py`（离线读取线上解帧 JSONL，解 `20252 M2C_GetCharacter` 未文档化的 tag 3 `WornBagMapList`，按装备模板、强化主属性、随机属性、词缀和宝石汇总 31 类穿戴属性，并计算只基于穿戴 `Spi/Sta` 的抵抗保守下界；用于复核 `online-control-resist-20260918` 五名玩家的直接装备 Res 130~170。该脚本读取 `datatable_json/` 仅作离线逆向对照，不是服务端运行配置）
- 属性与挂机诊断：`inspect_idle_attribute_capture.py`（解析 `20170` 的重复 `AttributeMap{Key=NumericType,Value}` 与 `20169` 单条，输出 opcode 间隔统计、面板属性快照、`1027` 经验变化与 `20069/20087-20090` 位置，用于对齐线上基础属性与确认主城挂机经验）

新增或修改协议行为时，应同步新增或更新本目录中的真实 TCP 回归。抓包文件不得放在
此目录或仓库根目录，统一归档到工作区 `参考数据/抓包归档/`。

## 军官反击复测

先启动本地 `server-mysql`，再运行：

```powershell
python .\test\verify_tactical_counter.py
python .\test\verify_tactical_counter.py --case tactical3
python .\test\verify_tactical_counter.py --case sportsman
```

默认运行全部 6 个场景；`--case tactical3` 只运行 Lv3 五人场景；
`--case sportsman` 只运行军官＋运动员双人场景，要求运动员触发自己的普通攻击 `200001`。
反击按持有者职业执行普通攻击，且**不发任何施法包**：脚本断言区间内没有该反击者的
`20075`（原版仅带 `UnitId`/`SkillId`，会弹技能名横幅）与 `20076`（会播攻击动作），
判据只剩三样——触发点后 ≤0.4s 立刻发射 `20077` 弹道、弹道字段 `Time>0`（等于配置的
`DelayTime`，归零会让客户端看不到子弹）、伤害在弹道飞完后约 1s 落地（`20078` 扣血）。
服务端不再发 `20081 M2C_BattleTouchState`：线上 12 份抓包里它一条都没有，
客户端 `M2C_BattleTouchStateHandler.Run` 也是空实现，脚本会断言它不出现。

TCP 检查两个反击技能下发 `Time=14000`。精确到期停止触发由
`battle_counter_test.go` 的假时钟测试覆盖；军官普攻本身也能添加独立的反击状态，
因此不把实战 14 秒后仍有任意 `StateType=102` 当作战术反击未到期。
同一 Go 测试文件还覆盖四职业使用自己的普攻与属性、伤害不随入伤量变化、
前摇和命中时序、多人广播不重复执行、冷却和蓝量不变，以及死亡、控制、离场和反击链中止。

此脚本只允许连接本机游戏服和 MySQL。数据库连接依次读取 `MHQ_TEST_MYSQL_DSN`、
`MHQ_MYSQL_DSN` 或服务端现有本地开发默认值；需能调用 `mysql`，也可用
`MHQ_TEST_MYSQL_EXE` 指定客户端路径。仅为新注册的测试角色设置职业、等级、属性、血蓝和技能，
不修改技能配置。结果摘要保存到 `logs/runtime/tactical-counter-result-*.json`。
