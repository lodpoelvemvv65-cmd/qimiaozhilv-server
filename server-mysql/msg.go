package main

import (
	"encoding/binary"
)

// 序列化规则（00 文档 §核心结论 4 / 03 文档）：
//
// 本游戏的有效 opcode 均小于 40000，并使用 protobuf。
//
// 传输分帧（本会话实测客户端 TCP 抓包确认）：
//
//	[u16 LE 总长度(含 opcode)][u16 LE opcode][protobuf body]
//
// RpcId（tag=90）是 protobuf 普通字段，随 body 一起编解码。

const protobufMaxOpcode = 40000

// packOuter：封装一帧 Outer 消息（TCP 帧：长度头 + opcode + body）。
func packOuter(opcode uint16, body []byte) []byte {
	out := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint16(out, uint16(2+len(body)))
	binary.LittleEndian.PutUint16(out[2:], opcode)
	copy(out[4:], body)
	return out
}

// parseOuter：拆分一帧消息体，返回 opcode + body（不含长度头）。
func parseOuter(seg []byte) (opcode uint16, body []byte, ok bool) {
	if len(seg) < 2 {
		return 0, nil, false
	}
	return binary.LittleEndian.Uint16(seg), seg[2:], true
}

// isProtobufOpcode：该 opcode 是否走 protobuf 序列化。
func isProtobufOpcode(opcode uint16) bool {
	return opcode < protobufMaxOpcode
}
