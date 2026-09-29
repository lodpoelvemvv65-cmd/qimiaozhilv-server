package main

import (
	"encoding/binary"
	"io"
	"log"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// 连接超时：一段时间内无任何请求则断开（TCP 下主要防泄漏）。
const (
	// Native opcode 20333 arrives every two seconds. This still tolerates a
	// transient stall but removes a half-open player long before ten minutes.
	connIdleTimeout  = 30 * time.Second
	cleanupInterval  = 10 * time.Second // 空闲清理扫描间隔
	connWriteTimeout = 5 * time.Second  // 坏连接不得无限占住战斗锁
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
	ln                net.Listener
	conns             map[int64]*channel // id → channel
	mu                sync.RWMutex
	loginMu           sync.Mutex
	pvpMu             sync.Mutex
	mapTransitionMu   sync.Mutex
	activityStartMu   sync.Mutex
	activityStartSeq  uint64
	pendingActivities map[uint64]*pendingActivityStart
	launcherTeamMu    sync.Mutex
	launcherTeamPlans map[uint64]*launcherTeamPlan
	launcherTeamSeq   uint64
	sceneVisibilityMu sync.Mutex
	sceneVisibility   map[sceneVisibilityKey]sceneVisibilityVersion
	mapCoinRoll       func() float64
	combatScheduler   combatPhaseScheduler
	closed            atomic.Bool
	seq               int64 // 连接序号发生器

	store   *Store         // MySQL 存取层（main 注入）
	onClose func(*channel) // 钩子：连接关闭时通知上层（下线清理）
}

func NewServer(addr string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Server{
		ln:                ln,
		conns:             make(map[int64]*channel),
		pendingActivities: make(map[uint64]*pendingActivityStart),
		launcherTeamPlans: make(map[uint64]*launcherTeamPlan),
		mapCoinRoll:       rand.Float64,
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

// removeChannelLocked only removes one routing entry. It must never wait for a
// session or transport lock while holding the global connection registry.
func (s *Server) removeChannelLocked(ch *channel) bool {
	if ch == nil {
		return false
	}
	if _, ok := s.conns[ch.id]; ok {
		delete(s.conns, ch.id)
		log.Printf("[S=%d] conn closed", ch.id)
		return true
	}
	return false
}

// removeChannel：锁外安全移除连接并触发 onClose（onClose 可能 RLock 遍历 conns）。
func (s *Server) removeChannel(ch *channel) {
	if ch == nil {
		return
	}
	ch.disconnecting.Store(true)
	// Close first so any concurrent battle push blocked in Write is released.
	// Do this before taking s.mu: Close and battle cleanup must never stall the
	// accept loop or prevent another broken connection from being removed.
	if ch.conn != nil {
		_ = ch.conn.Close()
	}
	s.mu.Lock()
	removed := s.removeChannelLocked(ch)
	s.mu.Unlock()
	if !removed {
		return
	}
	if ch.session != nil {
		ch.session.stopAutoBattle()
	}
	if s.onClose != nil {
		s.onClose(ch)
	}
}

// scheduleRemoveChannel starts cleanup outside writeMu and battleMu. Pushes can
// fail while their caller owns battleMu, while removeChannel stops automatic
// combat by taking that same lock.
func (s *Server) scheduleRemoveChannel(ch *channel) {
	if s == nil || ch == nil || !ch.disconnecting.CompareAndSwap(false, true) {
		return
	}
	go s.removeChannel(ch)
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
	if ch == nil || ch.conn == nil || ch.disconnecting.Load() {
		return
	}
	ch.writeMu.Lock()
	defer ch.writeMu.Unlock()
	if err := ch.conn.SetWriteDeadline(time.Now().Add(connWriteTimeout)); err != nil {
		log.Printf("[S=%d] set write deadline err=%v", ch.id, err)
	}
	defer ch.conn.SetWriteDeadline(time.Time{})
	for len(frame) > 0 {
		n, err := ch.conn.Write(frame)
		if err != nil {
			log.Printf("[S=%d] write err=%v", ch.id, err)
			s.scheduleRemoveChannel(ch)
			return
		}
		if n <= 0 {
			log.Printf("[S=%d] write made no progress", ch.id)
			s.scheduleRemoveChannel(ch)
			return
		}
		frame = frame[n:]
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
		if ch.session != nil && ch.session.playerID == playerID &&
			ch.session.state == sessInGame && !ch.session.superseded.Load() {
			return ch
		}
	}
	return nil
}

// onlineChannels：返回全部已完成 EnterGame 的连接。LoginGate 已填 playerID
// 但客户端仍停在角色页，不能接收地图、队伍或战斗推送。
func (s *Server) onlineChannels() []*channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*channel, 0, len(s.conns))
	for _, ch := range s.conns {
		if ch.session != nil && ch.session.playerID > 0 &&
			ch.session.state == sessInGame && !ch.session.superseded.Load() {
			out = append(out, ch)
		}
	}
	return out
}

func (s *Server) isCurrentOnlineChannel(ch *channel) bool {
	if s == nil || ch == nil || ch.session == nil {
		return false
	}
	// Unit tests often exercise packet builders without a connection registry.
	if s.conns == nil {
		return !ch.session.superseded.Load()
	}
	s.mu.RLock()
	current := s.conns[ch.id] == ch
	s.mu.RUnlock()
	return current && ch.session.state == sessInGame && !ch.session.superseded.Load()
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
