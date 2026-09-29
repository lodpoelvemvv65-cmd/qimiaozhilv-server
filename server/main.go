package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var (
	listenAddr     = flag.String("listen", ":7756", "TCP 监听地址")
	dbPath         = flag.String("db", "data/mhq.db", "SQLite 数据库文件路径")
	tableDir       = flag.String("table", "../datatable_json", "datatable 配置表目录（相对 server 工作目录）")
	skillLogicPath = flag.String("skill-logic", "", "SkillLogicConfig 路径（默认用仓库内 testdata 夹具或本地客户端解包目录）")
	battleEffects  = flag.Bool("battle-effects", false, "启用战斗位移特效（当前客户端 DOTween 版本不兼容，默认关闭）")
)

func main() {
	flag.Parse()
	battleVisualEffectsEnabled = *battleEffects
	if !battleVisualEffectsEnabled {
		log.Printf("battle visual effects disabled (use -battle-effects after fixing DOTween)")
	}

	// SQLite 存取层
	st, err := OpenStore(*dbPath)
	if err != nil {
		log.Fatalf("open store %s failed: %v", *dbPath, err)
	}
	defer st.Close()
	log.Printf("store ready: %s", *dbPath)

	// 加载战斗配置表（datatable_json/ 位于 server 上级目录，可用 -table 覆盖）
	if err := loadDatatables(*tableDir); err != nil {
		log.Fatalf("load datatables failed: %v", err)
	}
	resolvedSkillLogic := *skillLogicPath
	if resolvedSkillLogic == "" {
		resolvedSkillLogic = skillLogicFixturePath()
	}
	skillLogicCatalog, err = LoadSkillLogicCatalog(resolvedSkillLogic)
	if err != nil {
		log.Fatalf("load SkillLogicConfig %s failed: %v", resolvedSkillLogic, err)
	}
	log.Printf("SkillLogicConfig loaded: %d skills, %d modifiers", len(skillLogicCatalog.Skills), len(skillLogicCatalog.Modifiers))

	srv, err := NewServer(*listenAddr)
	if err != nil {
		log.Fatalf("listen %s failed: %v", *listenAddr, err)
	}
	srv.store = st
	globalServer = srv       // 供世界 BOSS/家族 BOSS/寄售等全局状态走 DB
	loadConsignmentsFromDB() // 寄售条目从 DB 恢复（重启不丢）
	// channel 关闭（断开/超时/退出）→ 角色下线标记
	srv.onClose = func(ch *channel) {
		if ch.session != nil && ch.session.playerID != 0 {
			ch.session.currentPosition(time.Now(), moveSpeed)
			if ch.session.signin != nil {
				ch.session.signin.PVPIsMatching = false
				ch.session.signin.PVPMatchCount = 0
			}
			// 同场景其他玩家移除该玩家单位（"显示玩家"同步）。
			srv.broadcastLeaveMap(ch)
			// LeaveMap is sent on the line above before this can wait for battleMu.
			srv.leaveBattleOnClose(ch)
			srv.saveData(ch)
			st.SetPlayerOnline(ch.session.playerID, false)
			log.Printf("[S=%d] player %d offline", ch.id, ch.session.playerID)
		}
		// 下线清理：退出队伍并通知剩余成员（team.go）
		if ch.session != nil && ch.session.playerID != 0 {
			srv.leaveTeamOnClose(ch.session.playerID)
		}
	}
	log.Printf("mhqserver listening on tcp %s", srv.Addr())

	go srv.serveLoop()
	go srv.tickerLoop()
	go srv.combatTickerLoop()
	go srv.bossSweepLoop() // 世界 BOSS 刷新（BossBase.RefreshInterval 到点广播 BossRefresh）
	// 周期清理过期登录 Key
	go func() {
		tick := time.NewTicker(2 * time.Minute)
		defer tick.Stop()
		for range tick.C {
			st.CleanupKeys()
		}
	}()

	// 等待退出信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	srv.Close()
}
