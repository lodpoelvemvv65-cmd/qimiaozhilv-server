package main

// broadcast.go：「全服作用域」的 GM HTTP 入口。
//
// 走的是和单角色命令完全同一条通道（gm_command_records 幂等 → Redis Stream →
// 游戏服消费 → mhq:gm:results 轮询 → gm_audit_logs），区别只有作用域：命令以
// targetType=server、空 targetId 下发，审计里同样是 TargetType=server。游戏服
// 侧不需要任何新协议，浏览器也永远不直连游戏服。

import (
	"net/http"
)

const (
	permissionServerBroadcast = "server.broadcast"
	permissionServerMailAll   = "server.mail_all"
)

func init() {
	// superadmin 由 rolePermissions 里的 "*" 通配覆盖，不必单独授予；operator
	// 是真正做运营动作的角色，这里显式加一条 server.* 权限。
	grantRolePermission("operator", permissionServerBroadcast)
	// 全服邮件比公告更重：公告只进聊天频道，全服邮件会把物品写进每一个角色的
	// 邮件箱，发错了只能靠一个个角色去删。权限和公告分开，运营可以只给其中一项。
	grantRolePermission("operator", permissionServerMailAll)
}

// handleBroadcast 分发 /api/v1/broadcast/*。
//
// audience（受众预览）与对应的发送动作共用同一权限：预览存在的意义就是让发送者
// 确认「要发给多少个角色」，把读取权限分开只会让有发送权的人看不到受众。公告和
// 全服邮件的权限则必须分开 —— 两者的影响面差得远，运营要能只给其中一项。
func (a *App) handleBroadcast(w http.ResponseWriter, r *http.Request, rid string, session *sessionData, path string) {
	// 先只判定权限，再分发执行。未知路径拿不到 permission，会被当成「没权限」挡在
	// 第一个 switch 里 —— 也就是说漏给新路径写权限的后果是 404，而不是默认放行。
	var permission string
	switch path {
	case "/api/v1/broadcast/audience", "/api/v1/broadcast/announce":
		permission = permissionServerBroadcast
	case "/api/v1/broadcast/mail":
		permission = permissionServerMailAll
	default:
		writeError(w, http.StatusNotFound, rid, "not_found", "接口不存在")
		return
	}
	if !hasPermission(session, permission) {
		writeError(w, http.StatusForbidden, rid, "forbidden", "没有执行此操作的权限")
		return
	}
	switch path {
	case "/api/v1/broadcast/audience":
		a.handleBroadcastAudience(w, r, rid)
	case "/api/v1/broadcast/announce":
		a.handleBroadcastAnnounce(w, r, rid, session)
	case "/api/v1/broadcast/mail":
		a.handleBroadcastMail(w, r, rid, session)
	}
}

// handleBroadcastAudience 返回这次全服操作的受众规模，给发送表单做二次确认用。
// online 与公告的实际生效范围一致（公告只发给在线角色）；totalPlayers 是全服
// 角色总数，供后续全服邮件复用。
func (a *App) handleBroadcastAudience(w http.ResponseWriter, r *http.Request, rid string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	var total int64
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM players`).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色总数查询失败")
		return
	}
	online, err := a.onlineCount(ctx)
	if err != nil {
		// 与健康检查一致：Redis 不可用时 online 记 -1，前端据此显示「在线数未知」
		// 而不是假装 0（0 会被读成「没人在线，发了也没用」）。
		online = -1
	}
	writeJSON(w, http.StatusOK, rid, map[string]any{
		"totalPlayers": strconvI64(total), "online": strconvI64(online), "onlineSource": "redis-markers",
	}, nil)
}

func (a *App) handleBroadcastAnnounce(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	req, key, ok := a.decodeGMCommandRequest(w, r, rid, session)
	if !ok {
		return
	}
	// 公告内容只在游戏服校验一次：它是唯一的权威，而且校验规则（行数上限、单行
	// 长度）必须和真正发广播的那段代码在一起才不会漂移。前端展示的字数提示只是
	// 即时反馈，不是第二道判据。
	result, status, err := a.submitCommand(r.Context(), session, rid, key, "server.announce", gmTargetTypeServer, "", req.Reason, req.Payload, r)
	if err != nil {
		writeError(w, status, rid, commandErrorCode(err), err.Error())
		return
	}
	commandStatus := statusForCommand(result)
	data, apiErr := commandResponse(result, commandStatus)
	writeJSON(w, commandStatus, rid, data, apiErr)
}

// handleBroadcastMail 给全部角色（在线 + 离线）发同一封邮件。
//
// 附件在这里就先过一遍 validateMailAttachments（submitCommand 里按 action 分流），
// 无效的附件在写命令记录和入队之前就被拒。这一点比单人发信更要紧：全服邮件在游戏服
// 侧是一条写全表的 SQL，等它失败再回滚，运营看到的是一句泛泛的「邮件保存失败」。
func (a *App) handleBroadcastMail(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	req, key, ok := a.decodeGMCommandRequest(w, r, rid, session)
	if !ok {
		return
	}
	result, status, err := a.submitCommand(r.Context(), session, rid, key, "server.mail_all", gmTargetTypeServer, "", req.Reason, req.Payload, r)
	if err != nil {
		writeError(w, status, rid, commandErrorCode(err), err.Error())
		return
	}
	commandStatus := statusForCommand(result)
	data, apiErr := commandResponse(result, commandStatus)
	writeJSON(w, commandStatus, rid, data, apiErr)
}
