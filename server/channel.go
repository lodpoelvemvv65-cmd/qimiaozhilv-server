package main

import (
	"net"
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
	id         int64
	conn       net.Conn
	session    *session
	lastActive atomic.Int64
}

func newChannel(id int64, conn net.Conn) *channel {
	ch := &channel{id: id, conn: conn, session: newSession()}
	ch.lastActive.Store(time.Now().UnixNano())
	return ch
}
