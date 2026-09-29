package main

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// 连接超时：一段时间内无任何请求则断开（TCP 下主要防泄漏）。
const (
	// Native opcode 20333 arrives every two seconds. This still tolerates a
	// transient stall but removes a half-open player long before ten minutes.
	connIdleTimeout = 30 * time.Second
	cleanupInterval = 10 * time.Second // 空闲清理扫描间隔
)

// Server：单 TCP 端口 7756，同时承担登录/网关角色。
// 每个客户端连接 = 一个 channel。conns 路由表 key = 服务器连接序号 id。
//
// 实测客户端协议（本会话抓包确认）：
//
//	TCP 帧：[u16 LE 总长度(含 opcode)][u16 LE opcode][protobuf body]
//
// 即包一层 2 字节长度头的 Outer 消息（与 KCP 的"一条消息=一条 Session 消息"等效，
// 区别仅在于 TCP 需要长度字段自行分帧）。
type Server struct {
	ln     net.Listener
	conns  map[int64]*channel // id → channel
	mu     sync.RWMutex
	closed atomic.Bool
	seq    int64 // 连接序号发生器

	store   *Store         // SQLite 存取层（main 注入）
	onClose func(*channel) // 钩子：连接关闭时通知上层（下线清理）
}

func NewServer(addr string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Server{
		ln:    ln,
		conns: make(map[int64]*channel),
	}, nil
}

func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close：关闭监听并清理全部连接。
func (s *Server) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.ln.Close()
	s.mu.Lock()
	list := make([]*channel, 0, len(s.conns))
	for _, ch := range s.conns {
		list = append(list, ch)
	}
	s.mu.Unlock()
	for _, ch := range list {
		s.removeChannel(ch)
	}
}

// removeChannelLocked：移除连接并触发关闭钩子（调用方须持锁）。
// ⚠ 不在此调用 onClose——onClose 里会遍历 onlineChannels()（RLock），
// 持写锁时调用会导致 RWMutex 同 goroutine 重入死锁（玩家离线广播卡死服务器）。
// 请用 removeChannel（锁外）或手动解锁后调 onClose。
func (s *Server) removeChannelLocked(ch *channel) {
	if _, ok := s.conns[ch.id]; ok {
		delete(s.conns, ch.id)
		// Close the transport before waiting for battleMu. An automatic cast
		// holds battleMu while sending its result; closing first releases a
		// blocked Write and prevents disconnect cleanup from deadlocking.
		if ch.conn != nil {
			ch.conn.Close()
		}
		if ch.session != nil {
			ch.session.stopAutoBattle()
		}
		log.Printf("[S=%d] conn closed", ch.id)
	}
}

// removeChannel：锁外安全移除连接并触发 onClose（onClose 可能 RLock 遍历 conns）。
func (s *Server) removeChannel(ch *channel) {
	s.mu.Lock()
	s.removeChannelLocked(ch)
	s.mu.Unlock()
	if s.onClose != nil {
		s.onClose(ch)
	}
}

// 接受连接循环。
func (s *Server) serveLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			continue
		}
		id := atomic.AddInt64(&s.seq, 1)
		ch := newChannel(id, conn)
		s.mu.Lock()
		s.conns[id] = ch
		s.mu.Unlock()
		log.Printf("[S=%d] CONNECT from %v", id, conn.RemoteAddr())
		go s.handleConn(ch)
	}
}

// handleConn：单连接读帧循环。一连接上可连续读多个请求
// （LoginGate → EnterGame 等 RPC 复用同一连接）。
func (s *Server) handleConn(ch *channel) {
	defer func() {
		s.removeChannel(ch) // 锁外：onClose 可能 RLock 遍历 conns（玩家离线广播）
	}()

	for {
		if s.closed.Load() {
			return
		}
		ch.conn.SetReadDeadline(time.Now().Add(connIdleTimeout))
		opcode, body, ok := readFrame(ch.conn)
		if !ok {
			return // 客户端关闭 / 协议错误
		}
		ch.lastActive.Store(time.Now().UnixNano())
		log.Printf("[S=%d] recv opcode=%d len=%d", ch.id, opcode, len(body))
		s.handleOpcode(ch, opcode, body)
	}
}

// readFrame：读一帧 TCP 消息。
//
//	[u16 LE 总长度 N][opcode u16 LE][body N-2 字节]
//
// 返回 opcode + body；连接关闭/帧损坏返回 ok=false。
func readFrame(conn net.Conn) (opcode uint16, body []byte, ok bool) {
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return 0, nil, false
	}
	n := binary.LittleEndian.Uint16(hdr[:])
	if n < 2 {
		return 0, nil, false // 长度至少含 opcode
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return 0, nil, false
	}
	return binary.LittleEndian.Uint16(buf), buf[2:], true
}

// SendToChannel：把一条帧（[u16 len][u16 opcode][body]）写给客户端连接。
func (s *Server) SendToChannel(ch *channel, frame []byte) {
	if ch == nil || ch.conn == nil {
		return
	}
	if _, err := ch.conn.Write(frame); err != nil {
		log.Printf("[S=%d] write err=%v", ch.id, err)
	}
}

// findChannelByPlayerID：按角色 id 查找在线连接（聊天/组队/好友广播用）。
// 返回 nil 表示目标不在线。
func (s *Server) findChannelByPlayerID(playerID int64) *channel {
	if playerID <= 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.conns {
		if ch.session != nil && ch.session.playerID == playerID {
			return ch
		}
	}
	return nil
}

// onlineChannels：返回全部已进入游戏（playerID>0）的连接。
func (s *Server) onlineChannels() []*channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*channel, 0, len(s.conns))
	for _, ch := range s.conns {
		if ch.session != nil && ch.session.playerID > 0 {
			out = append(out, ch)
		}
	}
	return out
}

// 后台协程：清理长时间无请求的连接。
func (s *Server) tickerLoop() {
	cleanup := time.NewTicker(cleanupInterval)
	defer cleanup.Stop()
	for range cleanup.C {
		now := time.Now()
		var stale []*channel
		s.mu.RLock()
		for _, ch := range s.conns {
			last := time.Unix(0, ch.lastActive.Load())
			if now.Sub(last) > connIdleTimeout {
				stale = append(stale, ch)
			}
		}
		s.mu.RUnlock()
		for _, ch := range stale {
			s.removeChannel(ch)
		}
	}
}
