package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	json "github.com/goccy/go-json"
	"github.com/redis/go-redis/v9"
)

const gmCommandStream = "mhq:gm:commands"

type commandRequest struct {
	Reason  string          `json:"reason"`
	Payload json.RawMessage `json:"payload"`
}

// GM 命令作用域。命令流里始终带上它（envelope.targetType），游戏服据此决定把
// 命令交给单角色动作还是全服动作。空值等同于 player。
const (
	gmTargetTypePlayer = "player"
	gmTargetTypeServer = "server"
)

// errInvalidTargetType 由 submitCommand 返回，映射成 400。作用域是调用方（本进程
// 的路由表）决定的常量，正常永远命中，这里挡的是日后新增调用点写错字面量的情况：
// 把未知作用域当成 player 处理，会让一个全服动作拿着空的角色 ID 去执行。
var errInvalidTargetType = errors.New("命令作用域无效")

func gmNormalizeTargetType(value string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}
	return gmTargetTypePlayer
}

func gmTargetTypeKnown(value string) bool {
	return value == gmTargetTypePlayer || value == gmTargetTypeServer
}

// gmCommandExpiry 是命令在游戏服侧的有效期。过期判定只发生在命令被消费的那一刻，
// 所以这个窗口要覆盖「命令排在队列里等多久」而不是「执行要多久」：全服动作要遍历
// 全部角色，比单角色动作更容易在队列里积压，给 5 分钟。
func gmCommandExpiry(targetType string) time.Duration {
	if gmNormalizeTargetType(targetType) == gmTargetTypeServer {
		return 5 * time.Minute
	}
	return 60 * time.Second
}

// decodeGMCommandRequest 是所有 GM 写命令共同的入口校验：CSRF、请求体解码、
// Idempotency-Key。三条写路径（单角色 / 全服公告 / 全服邮件）共用它，避免校验
// 规则各写一份后互相漂移。返回 ok=false 时已经写好了错误响应，调用方直接 return。
//
// 调用点要放在「目标是否可见」这类查询之前：请求本身是否可信（CSRF）与是否合法
// （JSON、幂等键）不该依赖查库结果，也不该为伪造请求消耗一次查询。
//
// GM 操作不收集自由文本理由，reason 一律清空。报文里保留该字段只是为了兼容旧
// 前端，值既不入库也不转发。
func (a *App) decodeGMCommandRequest(w http.ResponseWriter, r *http.Request, rid string, session *sessionData) (commandRequest, string, bool) {
	var req commandRequest
	if !a.checkCSRF(r, session) {
		writeError(w, http.StatusForbidden, rid, "csrf_failed", "请求校验失败")
		return req, "", false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, rid, "invalid_json", "请求格式错误")
		return req, "", false
	}
	req.Reason = ""
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 191 {
		writeError(w, http.StatusBadRequest, rid, "idempotency_required", "请提供有效的 Idempotency-Key")
		return req, "", false
	}
	if len(req.Payload) == 0 || string(req.Payload) == "null" {
		req.Payload = json.RawMessage(`{}`)
	}
	return req, key, true
}

func (a *App) handlePlayerCommand(w http.ResponseWriter, r *http.Request, rid string, playerID int64, action, permission string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, rid, "method_not_allowed", "请求方法不支持")
		return
	}
	session, apiErr := a.requirePermission(r, permission)
	if apiErr != nil {
		writeJSON(w, statusForAPIError(apiErr), rid, nil, apiErr)
		return
	}
	req, key, ok := a.decodeGMCommandRequest(w, r, rid, session)
	if !ok {
		return
	}
	visible, err := a.subGMCanViewPlayer(r.Context(), session, playerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, rid, "query_failed", "角色权限查询失败")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, rid, "player_not_found", "角色不存在")
		return
	}
	result, status, err := a.submitCommand(r.Context(), session, rid, key, action, gmTargetTypePlayer, strconvI64(playerID), req.Reason, req.Payload, r)
	if err != nil {
		writeError(w, status, rid, commandErrorCode(err), err.Error())
		return
	}
	commandStatus := statusForCommand(result)
	data, apiErr := commandResponse(result, commandStatus)
	writeJSON(w, commandStatus, rid, data, apiErr)
}

// commandResponse 把游戏服的 gmCommandResult 拆成 (data, apiError)。
//
// 业务拒绝（status=rejected → 422）必须回填真正的 apiError：游戏服写在
// gmCommandResult.Message 里的中文原因（例如「装备已强化至最高等级」）如果只留在
// data 里，前端 client.ts 的 `envelope.error?.message` 取到 undefined，玩家看到的
// 就只有「请求失败（422）」。data 仍然照原样回传，失败时的 before/after 结构化结果
// 不能丢。
func commandResponse(result map[string]any, status int) (map[string]any, *apiError) {
	if status < 400 {
		return result, nil
	}
	code := stringValue(result["errorCode"])
	if code == "" {
		code = "command_rejected"
	}
	message := stringValue(result["message"])
	if message == "" {
		message = "命令未执行"
	}
	return result, &apiError{Code: code, Message: message}
}

func commandRequiresReason(_ string) bool { return false }

func statusForAPIError(err *apiError) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	switch err.Code {
	case "unauthorized":
		return http.StatusUnauthorized
	case "forbidden", "csrf_failed":
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

func commandErrorCode(err error) string {
	if errors.Is(err, redis.ErrClosed) {
		return "redis_unavailable"
	}
	if errors.Is(err, errInvalidTargetType) {
		return "invalid_target_type"
	}
	return "command_failed"
}

func statusForCommand(result map[string]any) int {
	if result == nil {
		return http.StatusInternalServerError
	}
	status, _ := result["status"].(string)
	switch status {
	case "completed":
		return http.StatusOK
	case "pending":
		return http.StatusAccepted
	case "rejected":
		return http.StatusUnprocessableEntity
	}
	return http.StatusAccepted
}

// submitCommand 下发一条 GM 命令。targetType/targetID 是命令的作用对象：
// player 作用域传角色 ID 的十进制字符串，server 作用域传空串。targetID 一律以
// 字符串形式流转，落到 gm_command_records.target_id 与 envelope 里，保证
// 「全服」这种没有数字 ID 的作用域不需要造一个假的 0。
func (a *App) submitCommand(ctx context.Context, session *sessionData, rid, idempotency, action, targetType, targetID string, reason string, payload json.RawMessage, r *http.Request) (map[string]any, int, error) {
	if a.redis == nil {
		return nil, http.StatusServiceUnavailable, errors.New("Redis 控制通道不可用，已拒绝写操作")
	}
	targetType = gmNormalizeTargetType(targetType)
	if !gmTargetTypeKnown(targetType) {
		return nil, http.StatusBadRequest, errInvalidTargetType
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	var existingHash, existingStatus, existingResult, existingAction, existingTarget string
	var existingAdmin int64
	err := a.db.QueryRowContext(ctx, `SELECT payload_hash, status, COALESCE(result_json, ''), action, target_id, admin_id FROM gm_command_records WHERE request_id = ?`, idempotency).
		Scan(&existingHash, &existingStatus, &existingResult, &existingAction, &existingTarget, &existingAdmin)
	if err == nil {
		if existingHash != hash || existingAction != action || existingTarget != targetID || existingAdmin != session.AdminID {
			return nil, http.StatusConflict, errors.New("Idempotency-Key 已用于其他参数")
		}
		if existingResult != "" {
			var value map[string]any
			if json.Unmarshal([]byte(existingResult), &value) == nil {
				return value, statusForCommand(value), nil
			}
		}
		return map[string]any{"requestId": idempotency, "status": existingStatus, "message": "命令仍在处理"}, http.StatusAccepted, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, http.StatusInternalServerError, errors.New("命令幂等记录读取失败")
	}
	// Validate a new mail before enqueueing or creating an audit/command row.
	// Completed retries above keep their original result if config later changes.
	// 全服邮件同样要过这一关，而且更必须：它在游戏服侧是一条 INSERT ... SELECT，
	// 附件 ID 非法要等到那里才发现的话，错误会以「命令被拒」的形式出现，运营还得
	// 自己去命令记录里翻原因。
	if action == "player.mail" || action == "server.mail_all" {
		if err := a.validateMailAttachments(ctx, payload); err != nil {
			return nil, http.StatusUnprocessableEntity, err
		}
	}
	expiresAt := time.Now().Add(gmCommandExpiry(targetType)).Unix()
	_, err = a.db.ExecContext(ctx, `INSERT INTO gm_command_records
		(request_id, action, target_id, admin_id, status, payload_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, 'accepted', ?, ?, ?)`, idempotency, action, targetID, session.AdminID, hash, expiresAt, time.Now().Unix())
	if err != nil {
		return nil, http.StatusInternalServerError, errors.New("命令幂等记录写入失败")
	}
	envelope := gmCommandEnvelope{RequestID: idempotency, Action: action, TargetType: targetType, TargetPlayerID: targetID,
		OperatorID: strconvI64(session.AdminID), Payload: payload, ExpectedVersion: "", ExpiresAt: expiresAt}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, http.StatusInternalServerError, errors.New("命令编码失败")
	}
	addCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	_, err = a.redis.XAdd(addCtx, &redis.XAddArgs{Stream: gmCommandStream, Values: map[string]any{"payload": string(data), "reason": reason}}).Result()
	cancel()
	if err != nil {
		_ = a.updateCommand(ctx, idempotency, "rejected", nil, "redis_publish_failed")
		_ = a.writeAudit(ctx, auditRecord{RequestID: rid, AdminID: session.AdminID, Action: action, TargetType: targetType, TargetID: targetID, Reason: reason, IdempotencyKey: idempotency, Result: "rejected", ErrorCode: "redis_publish_failed", ClientIP: clientIP(r), UserAgent: r.UserAgent()})
		return nil, http.StatusServiceUnavailable, errors.New("Redis 控制命令发送失败")
	}
	result, completed := a.waitCommandResult(ctx, idempotency, 8*time.Second)
	if completed {
		status, _ := result["status"].(string)
		_ = a.updateCommand(ctx, idempotency, status, result, stringValue(result["errorCode"]))
		_ = a.writeAudit(ctx, auditRecord{RequestID: rid, AdminID: session.AdminID, Action: action, TargetType: targetType, TargetID: targetID, Reason: reason, IdempotencyKey: idempotency, Result: status, ErrorCode: stringValue(result["errorCode"]), ClientIP: clientIP(r), UserAgent: r.UserAgent()})
		return result, statusForCommand(result), nil
	}
	pending := map[string]any{"requestId": idempotency, "status": "pending", "action": action, "targetId": targetID, "message": "命令已发送，等待游戏服确认"}
	_ = a.updateCommand(ctx, idempotency, "pending", nil, "")
	_ = a.writeAudit(ctx, auditRecord{RequestID: rid, AdminID: session.AdminID, Action: action, TargetType: targetType, TargetID: targetID, Reason: reason, IdempotencyKey: idempotency, Result: "pending", ClientIP: clientIP(r), UserAgent: r.UserAgent()})
	return pending, http.StatusAccepted, nil
}

type gmCommandEnvelope struct {
	RequestID string `json:"requestId"`
	Action    string `json:"action"`
	// TargetType 用 omitempty：老 GM API 的 envelope 里没有这个字段，新游戏服
	// 把缺省值当 player；反过来新 API 也必须让单角色命令的报文与升级前逐字节
	// 一致，避免滚动升级期间两边对同一份命令的理解产生分歧。
	TargetType      string          `json:"targetType,omitempty"`
	TargetPlayerID  string          `json:"targetPlayerId"`
	OperatorID      string          `json:"operatorId"`
	Payload         json.RawMessage `json:"payload"`
	ExpectedVersion string          `json:"expectedVersion,omitempty"`
	ExpiresAt       int64           `json:"expiresAt"`
}

func (a *App) waitCommandResult(ctx context.Context, requestID string, max time.Duration) (map[string]any, bool) {
	if a.redis == nil {
		return nil, false
	}
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		value, err := a.redis.Get(readCtx, "mhq:gm:results:"+requestID).Result()
		cancel()
		if err == nil && value != "" {
			var result map[string]any
			if json.Unmarshal([]byte(value), &result) == nil {
				return result, true
			}
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil, false
}

func (a *App) updateCommand(ctx context.Context, requestID, status string, result map[string]any, errorCode string) error {
	var resultJSON any
	if result != nil {
		encoded, err := json.Marshal(result)
		if err == nil {
			resultJSON = string(encoded)
		}
	}
	_, err := a.db.ExecContext(ctx, `UPDATE gm_command_records SET status = ?, result_json = COALESCE(?, result_json), error_code = ?, accepted_at = COALESCE(accepted_at, ?), finished_at = CASE WHEN ? IN ('completed','rejected') THEN ? ELSE finished_at END WHERE request_id = ?`, status, resultJSON, errorCode, time.Now().Unix(), status, time.Now().Unix(), requestID)
	return err
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}
