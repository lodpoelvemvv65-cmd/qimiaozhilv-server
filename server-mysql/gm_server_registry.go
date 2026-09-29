package main

import (
	"strings"

	json "github.com/goccy/go-json"
)

// gm_server_registry.go：全服作用域 GM 命令的注册表。
//
// 单角色命令走 gm_command_registry.go 的 gmActionFunc，第一个参数恒为一个有效
// 角色 ID，因为 executeGMCommand 在分发前就要求 TargetPlayerID 能解析成 >0 的
// 整数。全服命令没有角色 ID —— 如果把空目标塞进 gmActionFunc，动作函数会拿着
// 0 去查一个不存在的角色并返回「角色不存在」，看起来像执行失败，实际只是作用域
// 不匹配。所以这里单开一张注册表，executeGMCommand 先按作用域分流，再由各自的
// 注册表处理。
//
// 另起一个文件的另一个原因：CLAUDE.md 的 GM 修改原则要求不修改游戏服务端既有
// 实现文件，新增能力放进新文件。

const (
	gmTargetTypePlayer = "player"
	gmTargetTypeServer = "server"
)

// gmServerActionFunc 处理一个全服作用域动作，返回
// (status, data, errorCode, message)，签名与 gmActionFunc 一致，只少了 playerID。
type gmServerActionFunc func(*Server, json.RawMessage) (string, map[string]any, string, string)

var gmServerActionRegistry = map[string]gmServerActionFunc{}

// registerGMServerAction 注册一个全服作用域动作。action 与单角色动作共用同一个
// 命名空间（GM API 侧下发的也是同一个字符串），重名会互相覆盖。
func registerGMServerAction(action string, fn gmServerActionFunc) {
	if action == "" || fn == nil {
		return
	}
	gmServerActionRegistry[action] = fn
}

func dispatchRegisteredGMServerAction(s *Server, action string, payload json.RawMessage) (bool, string, map[string]any, string, string) {
	fn, ok := gmServerActionRegistry[action]
	if !ok {
		return false, "", nil, "", ""
	}
	status, data, code, msg := fn(s, payload)
	return true, status, data, code, msg
}

// gmNormalizeTargetType 规范化命令作用域。既有 GM API 在 envelope 里不带
// targetType，解析出来是空串，必须等同于 "player"，否则升级期间所有单角色命令
// 都会被判成全服作用域。其他未知值不猜测、原样返回，会落到单角色分支并由
// TargetPlayerID 的解析挡下（invalid_target）。
func gmNormalizeTargetType(value string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}
	return gmTargetTypePlayer
}
