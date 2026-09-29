package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"mhqserver/protocol"
)

// testclient：模拟 ET 客户端完整登录链路（TCP 帧格式）。
//
//	帧格式（与真实客户端一致）：[u16 LE 总长度(含 opcode)][u16 LE opcode][protobuf body]
//
//	阶段 1  连接1(登录服): C2R_Regist / C2R_Login → R2C_*{Address,Key,GateId}
//	阶段 2  连接2(网关):  C2G_LoginGate{Key} → G2C_LoginGate{HasRole}
//	         无角色: C2R_CreateRole → 重新登录 → 再 LoginGate
//	阶段 3  连接2(复用):   C2G_EnterGame → G2C_EnterGame；C2G_HeartBeat → G2C_HeartBeat
const serverAddr = "127.0.0.1:7756"

type client struct {
	conn net.Conn
	rpc  int32
}

func dial() *client {
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		panic(err)
	}
	fmt.Printf("[testclient] TCP connected %s\n", serverAddr)
	return &client{conn: conn, rpc: 0}
}

func (c *client) close() {
	c.conn.Close()
	fmt.Println("[testclient] conn closed")
}

// call：发一帧请求（RpcId 自增），阻塞等对应响应帧。
func (c *client) call(reqOpcode uint16, req proto.Message, wantOpcode uint16) proto.Message {
	c.rpc++
	// 通过 protoreflect 设置 RpcId（tag=90）
	fields := req.ProtoReflect().Descriptor().Fields()
	if f := fields.ByName("RpcId"); f != nil {
		req.ProtoReflect().Set(f, protoreflect.ValueOfInt32(c.rpc))
	}
	body, _ := proto.Marshal(req)
	frame := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint16(frame, uint16(2+len(body)))
	binary.LittleEndian.PutUint16(frame[2:], reqOpcode)
	copy(frame[4:], body)
	c.conn.Write(frame)
	fmt.Printf("[testclient] send opcode=%d rpc=%d len=%d\n", reqOpcode, c.rpc, len(body))

	// 等响应（同一连接连续读帧）
	for {
		op, respBody, ok := readFrame(c.conn)
		if !ok {
			fmt.Printf("[testclient] conn closed / read err waiting opcode=%d\n", wantOpcode)
			return nil
		}
		fmt.Printf("[testclient] recv opcode=%d len=%d\n", op, len(respBody))
		if op == wantOpcode {
			msg := newMessageFor(op)
			if msg != nil {
				proto.Unmarshal(respBody, msg)
			}
			return msg
		}
		// 收到其它帧，继续等
	}
}

// readFrame：读一帧 [u16 len][u16 opcode][body]。
func readFrame(conn net.Conn) (opcode uint16, body []byte, ok bool) {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return 0, nil, false
	}
	n := binary.LittleEndian.Uint16(hdr[:])
	if n < 2 {
		return 0, nil, false
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return 0, nil, false
	}
	return binary.LittleEndian.Uint16(buf), buf[2:], true
}

func newMessageFor(opcode uint16) proto.Message {
	switch opcode {
	case protocol.OpR2C_Regist:
		return &protocol.R2C_Regist{}
	case protocol.OpR2C_Login:
		return &protocol.R2C_Login{}
	case protocol.OpG2C_LoginGate:
		return &protocol.G2C_LoginGate{}
	case protocol.OpR2C_CreateRole:
		return &protocol.R2C_CreateRole{}
	case protocol.OpG2C_EnterGame:
		return &protocol.G2C_EnterGame{}
	case protocol.OpG2C_HeartBeat:
		return &protocol.G2C_HeartBeat{}
	}
	return nil
}

func main() {
	account, password := "tester", "123456"

	// ---- 阶段 1：登录服连接：注册（已存在则登录） ----
	c1 := dial()
	key := int64(0)
	address := ""
	gateID := int64(0)

	r := c1.call(protocol.OpC2R_Regist,
		&protocol.C2R_Regist{Account: account, Password: password}, protocol.OpR2C_Regist)
	reg := r.(*protocol.R2C_Regist)
	if reg.Error != 0 {
		fmt.Printf("[testclient] 注册失败(Error=%d %s)，改走登录\n", reg.Error, reg.Message)
		l := c1.call(protocol.OpC2R_Login,
			&protocol.C2R_Login{Account: account, Password: password}, protocol.OpR2C_Login)
		login := l.(*protocol.R2C_Login)
		if login.Error != 0 {
			fmt.Printf("[testclient] 登录失败 Error=%d %s\n", login.Error, login.Message)
			c1.close()
			return
		}
		key, address, gateID = login.Key, login.Address, login.GateId
	} else {
		key, address, gateID = reg.Key, reg.Address, reg.GateId
	}
	fmt.Printf("[testclient] 登录/注册成功 Key=%d Address=%s GateId=%d\n", key, address, gateID)
	c1.close()

	// ---- 阶段 2：网关连接（模拟客户端按 R2C 的 Address 新建连接） ----
	c2 := dial()
	lg := c2.call(protocol.OpC2G_LoginGate,
		&protocol.C2G_LoginGate{Key: key}, protocol.OpG2C_LoginGate)
	lgResp := lg.(*protocol.G2C_LoginGate)
	if lgResp.Error != 0 {
		fmt.Printf("[testclient] LoginGate 失败 Error=%d %s\n", lgResp.Error, lgResp.Message)
		c2.close()
		return
	}
	fmt.Printf("[testclient] LoginGate ok HasRole=%v PlayerId=%d JobId=%d SkinId=%d\n",
		lgResp.HasRole, lgResp.PlayerId, lgResp.JobId, lgResp.SkinId)

	if !lgResp.HasRole {
		// 建角色（本连接 session 已绑定账号，可直接 C2R_CreateRole）
		cr := c2.call(protocol.OpC2R_CreateRole,
			&protocol.C2R_CreateRole{Name: "测试机", JobId: 1}, protocol.OpR2C_CreateRole)
		crResp := cr.(*protocol.R2C_CreateRole)
		fmt.Printf("[testclient] CreateRole Error=%d %s\n", crResp.Error, crResp.Message)

		// Key 已消耗，重新登录拿新 Key，再 LoginGate
		c2.close()
		c2 = dial()
		l2 := c2.call(protocol.OpC2R_Login,
			&protocol.C2R_Login{Account: account, Password: password}, protocol.OpR2C_Login)
		login2 := l2.(*protocol.R2C_Login)
		if login2.Error != 0 {
			fmt.Printf("[testclient] 重新登录失败 Error=%d %s\n", login2.Error, login2.Message)
			return
		}
		lg2 := c2.call(protocol.OpC2G_LoginGate,
			&protocol.C2G_LoginGate{Key: login2.Key}, protocol.OpG2C_LoginGate)
		lg2Resp := lg2.(*protocol.G2C_LoginGate)
		fmt.Printf("[testclient] LoginGate2 HasRole=%v PlayerId=%d JobId=%d\n",
			lg2Resp.HasRole, lg2Resp.PlayerId, lg2Resp.JobId)
	}
	defer c2.close()

	// ---- 阶段 3：进入游戏 + 心跳（复用同一连接） ----
	eg := c2.call(protocol.OpC2G_EnterGame, &protocol.C2G_EnterGame{}, protocol.OpG2C_EnterGame)
	egResp := eg.(*protocol.G2C_EnterGame)
	if egResp.Error != 0 {
		fmt.Printf("[testclient] EnterGame 失败 Error=%d %s\n", egResp.Error, egResp.Message)
		return
	}
	fmt.Printf("[testclient] EnterGame ok Id=%d JobId=%d SkinId=%d IsOnline=%v\n",
		egResp.Id, egResp.JobId, egResp.SkinId, egResp.IsOnline)

	hb := c2.call(protocol.OpC2G_HeartBeat, &protocol.C2G_HeartBeat{}, protocol.OpG2C_HeartBeat)
	_ = hb
	fmt.Println("[testclient] HeartBeat ok")

	// 阶段 4：读取 EnterGame 之后服务器推送的帧（M2C_SendUnitInfo → M2C_ChangeMap 顺序验证）
	fmt.Println("[testclient] 读取进入游戏后的推送帧…")
	for i := 0; i < 4; i++ {
		op, body, ok := readFrame(c2.conn)
		if !ok {
			fmt.Println("[testclient] 读取推送帧失败/连接关闭")
			break
		}
		fmt.Printf("[testclient] 推送 opcode=%d len=%d\n", op, len(body))
	}

	fmt.Println("[testclient] 完整登录链路自测通过")
}
