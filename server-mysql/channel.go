package main

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// channel：一条 TCP 客户端连接（登录/网关同址）。
//
//	id        —— 服务器侧连接序号（日志区分用）
//	conn      —— 客户端 TCP 连接（读帧/回包）
//	session   —— 会话状态（登录态/玩家数据）
//	lastActive —— 最近活跃时间（超时清理用）
type channel struct {
	id      int64
	conn    net.Conn
	session *session
	// A session may be written by the request loop and by background
	// notifications (battle ticks, currency updates, map fan-out) at the same
	// time.  net.Conn does not preserve frame boundaries across concurrent
	// writes, so every frame must share one transport lock.
	writeMu    sync.Mutex
	lastActive atomic.Int64
	// disconnecting prevents repeated failed background pushes from scheduling
	// multiple copies of the full offline cleanup.
	disconnecting atomic.Bool
	// teamHeadReloadGen 记录组队头像补发的代数：每次重建队伍头像都会 +1，
	// 上一轮还没触发的补发定时器发现代数变了就放弃，避免多轮补发互相盖值。
	teamHeadReloadGen atomic.Uint64
	// teamVitalSyncPending 标记「已排了一次血蓝补推」：单人战斗每挨一下都会
	// 请求补推，同一个间隔内只保留一次（见 scheduleTeamVitalSync）。
	teamVitalSyncPending atomic.Bool
}

func newChannel(id int64, conn net.Conn) *channel {
	ch := &channel{id: id, conn: conn, session: newSession()}
	ch.lastActive.Store(time.Now().UnixNano())
	return ch
}
