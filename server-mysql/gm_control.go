package main

// gm_control.go is the private control-plane consumer for the GM API. It is
// intentionally separate from the public TCP protocol: browsers never reach
// this code directly and every mutation is executed through the same session
// and persistence paths used by the game.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"
	"github.com/redis/go-redis/v9"
	"mhqserver/protocol"
)

const (
	gmCommandStream      = "mhq:gm:commands"
	gmCommandGroup       = "game"
	gmCommandConsumer    = "game-control"
	gmCommandResultTTL   = 10 * time.Minute
	gmCommandReadTimeout = 3 * time.Second
	// A command that was claimed by a process which crashed must become
	// available to the replacement process. The idle window avoids reclaiming
	// a command while a healthy consumer is still executing it.
	gmCommandClaimIdle = 30 * time.Second
)

type gmCommand struct {
	RequestID string `json:"requestId"`
	Action    string `json:"action"`
	// TargetType 为 "server" 时 TargetPlayerID 为空，命令交给
	// gm_server_registry.go 的全服注册表。空值等同于 "player"，这样升级期间
	// 老 GM API 下发的命令（envelope 里没有这个字段）行为完全不变。
	TargetType      string          `json:"targetType"`
	TargetPlayerID  string          `json:"targetPlayerId"`
	OperatorID      string          `json:"operatorId"`
	Payload         json.RawMessage `json:"payload"`
	ExpectedVersion string          `json:"expectedVersion"`
	ExpiresAt       int64           `json:"expiresAt"`
}

type gmCommandResult struct {
	RequestID    string         `json:"requestId"`
	Status       string         `json:"status"`
	Action       string         `json:"action"`
	TargetID     string         `json:"targetId"`
	AcceptedAt   int64          `json:"acceptedAt"`
	CompletedAt  int64          `json:"completedAt,omitempty"`
	GameRevision int64          `json:"gameRevision,omitempty"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	Message      string         `json:"message,omitempty"`
	Data         map[string]any `json:"data,omitempty"`
}

var gmOfflineMu sync.Mutex

func startGMCommandConsumer(ctx context.Context, s *Server) {
	if s == nil || s.store == nil || s.store.cache == nil || s.store.cache.client == nil {
		log.Print("GM control consumer disabled: Redis cache is unavailable")
		return
	}
	client := s.store.cache.client
	setupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := client.XGroupCreateMkStream(setupCtx, gmCommandStream, gmCommandGroup, "0").Err()
	cancel()
	if err != nil && !strings.Contains(strings.ToUpper(err.Error()), "BUSYGROUP") {
		log.Printf("GM control consumer setup failed: %v", err)
		return
	}
	log.Printf("GM control consumer ready stream=%s group=%s", gmCommandStream, gmCommandGroup)
	for {
		if err := reclaimGMCommands(ctx, s, client); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("GM control pending command reclaim failed: %v", err)
		}
		readCtx, readCancel := context.WithTimeout(ctx, gmCommandReadTimeout+time.Second)
		streams, err := client.XReadGroup(readCtx, &redis.XReadGroupArgs{Group: gmCommandGroup, Consumer: gmCommandConsumer,
			Streams: []string{gmCommandStream, ">"}, Count: 10, Block: gmCommandReadTimeout}).Result()
		readCancel()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return
			}
			if err != redis.Nil {
				log.Printf("GM control stream read failed: %v", err)
			}
			continue
		}
		for _, stream := range streams {
			for _, message := range stream.Messages {
				if err := handleGMStreamMessage(ctx, s, client, message); err != nil {
					log.Printf("GM command id=%s failed: %v", message.ID, err)
				}
				ackCtx, ackCancel := context.WithTimeout(ctx, 3*time.Second)
				_ = client.XAck(ackCtx, gmCommandStream, gmCommandGroup, message.ID).Err()
				ackCancel()
			}
		}
	}
}

// reclaimGMCommands picks up messages left pending by an earlier game
// process. XREADGROUP with "\u003e" only returns never-delivered messages, so
// without this pass a crash (or a binary upgrade) could leave a GM operation
// stuck in pending state forever.
func reclaimGMCommands(ctx context.Context, s *Server, client *redis.Client) error {
	start := "0-0"
	claimCtx, cancel := context.WithTimeout(ctx, gmCommandReadTimeout)
	messages, _, err := client.XAutoClaim(claimCtx, &redis.XAutoClaimArgs{
		Stream: gmCommandStream, Group: gmCommandGroup, Consumer: gmCommandConsumer,
		MinIdle: gmCommandClaimIdle, Start: start, Count: 10,
	}).Result()
	cancel()
	if err != nil {
		return err
	}
	for _, message := range messages {
		if err := handleGMStreamMessage(ctx, s, client, message); err != nil {
			log.Printf("GM reclaimed command id=%s failed: %v", message.ID, err)
		}
		ackCtx, ackCancel := context.WithTimeout(ctx, 3*time.Second)
		_ = client.XAck(ackCtx, gmCommandStream, gmCommandGroup, message.ID).Err()
		ackCancel()
	}
	return nil
}

func handleGMStreamMessage(ctx context.Context, s *Server, client *redis.Client, message redis.XMessage) error {
	raw, ok := message.Values["payload"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return fmt.Errorf("stream message has no payload")
	}
	var command gmCommand
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		return err
	}
	if strings.TrimSpace(command.RequestID) == "" {
		return fmt.Errorf("requestId is required")
	}
	resultKey := "mhq:gm:results:" + command.RequestID
	if existing, err := client.Get(ctx, resultKey).Result(); err == nil && existing != "" {
		return nil // idempotent replay after an acknowledged result
	}
	accepted := time.Now().UnixMilli()
	if command.ExpiresAt > 0 && time.Now().Unix() > command.ExpiresAt {
		return writeGMResult(ctx, client, resultKey, gmCommandResult{RequestID: command.RequestID, Action: command.Action,
			TargetID: command.TargetPlayerID, Status: "rejected", AcceptedAt: accepted, CompletedAt: time.Now().UnixMilli(),
			ErrorCode: "command_expired", Message: "命令已过期"})
	}
	result := executeGMCommand(s, command)
	result.AcceptedAt = accepted
	if result.CompletedAt == 0 {
		result.CompletedAt = time.Now().UnixMilli()
	}
	return writeGMResult(ctx, client, resultKey, result)
}

func writeGMResult(ctx context.Context, client *redis.Client, key string, result gmCommandResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return client.Set(ctx, key, data, gmCommandResultTTL).Err()
}

func executeGMCommand(s *Server, command gmCommand) gmCommandResult {
	result := gmCommandResult{RequestID: command.RequestID, Action: command.Action, TargetID: command.TargetPlayerID, Status: "rejected"}
	// 作用域必须先分流，而且要在解析 TargetPlayerID 之前：全服命令本来就没有
	// 角色 ID，先解析会被 invalid_target 直接挡掉；反过来单角色命令也不该落进
	// 全服注册表。
	if gmNormalizeTargetType(command.TargetType) == gmTargetTypeServer {
		if ok, status, data, code, msg := dispatchRegisteredGMServerAction(s, command.Action, command.Payload); ok {
			result.Status, result.Data, result.ErrorCode, result.Message = status, data, code, msg
			return result
		}
		result.ErrorCode, result.Message = "unknown_action", "不支持的全服管理动作"
		return result
	}
	playerID, err := strconv.ParseInt(strings.TrimSpace(command.TargetPlayerID), 10, 64)
	if err != nil || playerID <= 0 {
		result.ErrorCode, result.Message = "invalid_target", "角色 ID 无效"
		return result
	}
	switch command.Action {
	case "player.kick":
		result.Status, result.Data = s.gmKickPlayer(playerID)
	case "player.reload":
		result.Status, result.Data = s.gmReloadPlayer(playerID)
	case "player.currency_adjust":
		result.Status, result.Data, result.ErrorCode, result.Message = s.gmAdjustCurrency(playerID, command.Payload)
	case "player.level_adjust":
		result.Status, result.Data, result.ErrorCode, result.Message = s.gmAdjustLevel(playerID, command.Payload)
	case "player.pet_adjust":
		result.Status, result.Data, result.ErrorCode, result.Message = s.gmAdjustPet(playerID, command.Payload)
	case "player.mail":
		result.Status, result.Data, result.ErrorCode, result.Message = s.gmSendMail(playerID, command.Payload)
	case "player.items.grant":
		result.Status, result.Data, result.ErrorCode, result.Message = s.gmGrantItems(playerID, command.Payload)
	default:
		if ok, status, data, code, msg := dispatchRegisteredGMAction(s, command.Action, playerID, command.Payload); ok {
			result.Status, result.Data, result.ErrorCode, result.Message = status, data, code, msg
			break
		}
		result.ErrorCode, result.Message = "unknown_action", "不支持的管理动作"
	}
	return result
}

func (s *Server) gmOnlineChannel(playerID int64) *channel {
	if s == nil {
		return nil
	}
	return s.findChannelByPlayerID(playerID)
}

func (s *Server) gmKickPlayer(playerID int64) (string, map[string]any) {
	ch := s.gmOnlineChannel(playerID)
	if ch == nil || ch.session == nil {
		return "completed", map[string]any{"online": false, "message": "角色当前不在线"}
	}
	if !ch.session.superseded.CompareAndSwap(false, true) {
		return "completed", map[string]any{"online": false, "message": "角色已在下线流程"}
	}
	s.sendPush(ch, 20328, &protocol.G2C_ForceOffLine{})
	s.cleanupPlayerSession(ch)
	time.AfterFunc(forceOfflineGracePeriod, func() { s.removeChannel(ch) })
	return "completed", map[string]any{"online": true, "playerId": strconvI64(playerID)}
}

func (s *Server) gmReloadPlayer(playerID int64) (string, map[string]any) {
	ch := s.gmOnlineChannel(playerID)
	if ch == nil || ch.session == nil {
		return "completed", map[string]any{"online": false}
	}
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	s.pushPlayerProgress(ch)
	s.pushPlayerAttrs(ch)
	s.pushMoney(ch)
	s.pushBagSnapshot(ch)
	s.sendCurrentTeamSnapshotTo(ch)
	s.saveData(ch)
	return "completed", map[string]any{"online": true, "playerId": strconvI64(playerID)}
}

func (s *Server) gmResolveSession(playerID int64) (*channel, func(), error) {
	if ch := s.gmOnlineChannel(playerID); ch != nil && ch.session != nil {
		return ch, func() {}, nil
	}
	if s == nil || s.store == nil {
		return nil, nil, errors.New("store unavailable")
	}
	gmOfflineMu.Lock()
	p, err := s.store.findPlayerByIDForGM(playerID)
	if err != nil {
		gmOfflineMu.Unlock()
		return nil, nil, err
	}
	ss := newSession()
	ss.playerID, ss.accountID, ss.jobID, ss.skinID = p.ID, p.AccountID, p.JobID, p.SkinID
	ss.state = sessInGame
	ss.loadData(p)
	return &channel{id: -playerID, session: ss}, func() { gmOfflineMu.Unlock() }, nil
}

type gmCurrencyPayload struct {
	Currency string `json:"currency"`
	Delta    string `json:"delta"`
}

// gmCoinPerGold 是金币与铜币的换算率：10000 铜 = 1 金。
//
// players.coin 和 session.coin 存的一律是铜，客户端 TabHelper.GetCoinFormat
// 再按 10000 铜=1 金、100 铜=1 银拆成三位显示（文档/13 货币 NumericType）。
// 但 GM 后台的“货币调整 → 金币”是给运营按金币填的，所以 coin 这一路的入参和
// 回显都按金币走，只有余额本身保持铜。
const gmCoinPerGold = 10000

// gmPlayerLevelMax 是 GM 直接改等级时允许的上限，与 gmAdjustLevel 原有的校验一致。
// 抽成常量只是为了让报错能写出具体数字（「等级不能超过 10000000」）。
const gmPlayerLevelMax = 10000000

// gmCurrencyDeltaToBaseUnits 把 GM 后台填的增减量换算成 session 余额的单位。
// 目前只有 coin 存在辅币单位（界面按金币填，余额存铜），其余货币单位和余额一致，
// 原样返回。换算溢出时返回 ok=false，由调用方回 4xx，不写库。
func gmCurrencyDeltaToBaseUnits(currency string, delta int64) (int64, bool) {
	if normalizeGMCurrency(currency) != "coin" {
		return delta, true
	}
	if delta > math.MaxInt64/gmCoinPerGold || delta < math.MinInt64/gmCoinPerGold {
		return 0, false
	}
	return delta * gmCoinPerGold, true
}

// gmCurrencyDisplayValue 把 session 余额换算回 GM 后台展示用的单位，与
// gmCurrencyDeltaToBaseUnits 互逆。铜币余额未必是 10000 的整数倍（例如
// 11106365357），这里取下整保证回显和后台看到的是同一口径；精确铜值由调用方
// 额外放进审计结果，余额本身不受影响。
func gmCurrencyDisplayValue(currency string, base int64) int64 {
	if normalizeGMCurrency(currency) == "coin" {
		return base / gmCoinPerGold
	}
	return base
}

func (s *Server) gmAdjustCurrency(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmCurrencyPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "货币参数错误"
	}
	goldDelta, err := strconv.ParseInt(strings.TrimSpace(payload.Delta), 10, 64)
	if err != nil {
		return "rejected", nil, "invalid_delta", "货币变更量必须是数字"
	}
	if goldDelta == 0 {
		return "rejected", nil, "invalid_delta", "货币变更量不能为 0"
	}
	// “货币调整 → 金币”填的是金币数，换算成铜再落到余额上；界面回显同样按金币，
	// 使 GM 后台看到的单位和它填的单位一致。其他货币没有辅币单位，原样使用。
	delta, ok := gmCurrencyDeltaToBaseUnits(payload.Currency, goldDelta)
	if !ok {
		return "rejected", nil, "currency_limit",
			fmt.Sprintf("货币变更量不能超过 %d（超出可调整范围）", math.MaxInt64/gmCoinPerGold)
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "rejected", nil, "player_not_found", "角色不存在"
		}
		return "rejected", nil, "resolve_failed", "角色状态读取失败"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	before, supported := gmCurrencyValue(ss, payload.Currency)
	if !supported {
		return "rejected", nil, "unknown_currency", "不支持的货币类型"
	}
	if delta > 0 && before > math.MaxInt64-delta || delta < 0 && before < -delta {
		return "rejected", nil, "currency_limit", fmt.Sprintf("调整后货币余额不能超过 %d", int64(math.MaxInt64))
	}
	after := before + delta
	if err := gmSetCurrency(ss, payload.Currency, after); err != nil {
		return "rejected", nil, "currency_limit", err.Error()
	}
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "货币变更保存失败"
	}
	if ch.conn != nil {
		s.pushMoney(ch)
	}
	result := map[string]any{
		"currency": payload.Currency,
		"before":   strconvI64(gmCurrencyDisplayValue(payload.Currency, before)),
		"after":    strconvI64(gmCurrencyDisplayValue(payload.Currency, after)),
		"delta":    strconvI64(goldDelta),
	}
	// 铜币余额未必是整金（例如 11106365357 铜 = 1110636.5357 金），回显取下整，
	// 所以额外附上精确铜值，审计和排查按这个对账。
	if normalizeGMCurrency(payload.Currency) == "coin" {
		result["beforeCoin"] = strconvI64(before)
		result["afterCoin"] = strconvI64(after)
		result["deltaCoin"] = strconvI64(delta)
	}
	return "completed", result, "", ""
}

func gmCurrencyValue(ss *session, currency string) (int64, bool) {
	switch normalizeGMCurrency(currency) {
	case "coin":
		return ss.coin, true
	case "yuanbao":
		return ss.yuanBao, true
	case "voucher":
		return ss.voucher, true
	case "honor":
		return ss.honor, true
	case "pvp", "pvpcurrency":
		return ss.pvpCurrency, true
	case "starcoin", "starcoins", "gem":
		return starCoinBalance(ss), true
	case "familycontribute", "familycontribution":
		return int64(ss.familyContribute), true
	default:
		return 0, false
	}
}

func gmSetCurrency(ss *session, currency string, value int64) error {
	if value < 0 {
		return errors.New("货币余额不能为负数")
	}
	switch normalizeGMCurrency(currency) {
	case "coin":
		ss.coin = value
	case "yuanbao":
		ss.yuanBao = value
	case "voucher":
		ss.voucher = value
	case "honor":
		ss.honor = value
	case "pvp", "pvpcurrency":
		ss.pvpCurrency = value
	case "starcoin", "starcoins", "gem":
		return gmSetStarCoinBalance(ss, value)
	case "familycontribute", "familycontribution":
		if value > math.MaxInt32 {
			return fmt.Errorf("家族贡献不能超过 %d", int64(math.MaxInt32))
		}
		ss.familyContribute = int32(value)
	default:
		return errors.New("不支持的货币类型")
	}
	return nil
}

func gmSetStarCoinBalance(ss *session, value int64) error {
	if ss == nil {
		return errors.New("角色状态不可用")
	}
	// NetItem.Count is int32 and normal grants use the same upper bound. Keep a
	// GM adjustment in one hidden currency stack so persistence and subsequent
	// bag operations stay bounded and deterministic.
	if value > math.MaxInt32 {
		return fmt.Errorf("星币不能超过 %d", int64(math.MaxInt32))
	}
	if ss.bag == nil {
		ss.bag = make(map[int32]*bagItem)
	}
	for index, item := range ss.bag {
		if item != nil && isStarCoinItem(item.ItemId) {
			delete(ss.bag, index)
		}
	}
	if value > 0 {
		ss.bag[starCoinBagSlot] = &bagItem{
			ItemId: consignmentCurrencyItemID, ItemType: int32(protocol.ItemType_GoodsItem), Count: int32(value),
		}
	}
	return nil
}

// normalizeGMCurrency accepts both the wire names used by the React console
// (for example pvpCurrency/familyContribution) and the snake_case names used
// by older automation clients. Separators are insignificant so all callers
// resolve to one canonical key before reading or writing a session balance.
func normalizeGMCurrency(currency string) string {
	value := strings.ToLower(strings.TrimSpace(currency))
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, "-", "")
	return value
}

type gmLevelPayload struct {
	Level string `json:"level"`
	Exp   string `json:"exp"`
}

func (s *Server) gmAdjustLevel(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmLevelPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "rejected", nil, "invalid_payload", "等级参数错误"
	}
	level64, err := strconv.ParseInt(strings.TrimSpace(payload.Level), 10, 64)
	if err != nil {
		return "rejected", nil, "invalid_level", "等级必须是数字"
	}
	if level64 < 1 {
		return "rejected", nil, "invalid_level", "等级不能小于 1"
	}
	if level64 > gmPlayerLevelMax {
		return "rejected", nil, "invalid_level", fmt.Sprintf("等级不能超过 %d", gmPlayerLevelMax)
	}
	exp := int64(0)
	if strings.TrimSpace(payload.Exp) != "" {
		exp, err = strconv.ParseInt(strings.TrimSpace(payload.Exp), 10, 64)
		if err != nil {
			return "rejected", nil, "invalid_exp", "经验值必须是数字"
		}
		if exp < 0 {
			return "rejected", nil, "invalid_exp", "经验值不能小于 0"
		}
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	beforeLevel, beforeChar, beforeSkill := ss.level, ss.charPoint, ss.skillPoint
	applyGMLevelAdjust(ss, int32(level64), exp)
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "等级变更保存失败"
	}
	if ch.conn != nil {
		s.pushPlayerProgress(ch)
		s.pushPlayerAttrs(ch)
		s.pushUnitCharacter(ch)
		s.broadcastPlayerUpdate(ch)
	}
	return "completed", map[string]any{
		"beforeLevel": beforeLevel, "afterLevel": ss.level, "exp": strconvI64(exp),
		"beforeCharPoint": beforeChar, "afterCharPoint": ss.charPoint,
		"beforeSkillPoint": beforeSkill, "afterSkillPoint": ss.skillPoint,
	}, "", ""
}

func applyGMLevelAdjust(ss *session, level int32, exp int64) {
	if ss == nil {
		return
	}
	grantProgressionPoints(ss, ss.level, level)
	ss.level, ss.exp = level, exp
}

type gmMailPayload struct {
	Title      string       `json:"title"`
	Content    string       `json:"content"`
	SenderName string       `json:"senderName"`
	RemainTime int64        `json:"remainTime"`
	Items      []gmMailItem `json:"items"`
}
type gmMailItem struct {
	ItemID int32 `json:"itemId"`
	Count  int32 `json:"count"`
}

func (s *Server) gmSendMail(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmMailPayload
	if err := json.Unmarshal(raw, &payload); err != nil || strings.TrimSpace(payload.Title) == "" || len(payload.Title) > 191 || len(payload.Content) > 4000 {
		return "rejected", nil, "invalid_payload", "邮件内容错误"
	}
	for _, item := range payload.Items {
		if item.ItemID <= 0 || item.Count <= 0 {
			return "rejected", nil, "invalid_item", "邮件附件无效"
		}
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	ss.mailSeq++
	mail := &mailMsg{Id: ss.mailSeq, Title: strings.TrimSpace(payload.Title), Content: payload.Content,
		SenderName: truncateGM(payload.SenderName, 64), State: 0, RemainTime: payload.RemainTime}
	for _, item := range payload.Items {
		mail.Items = append(mail.Items, mailItemMsg{ItemId: item.ItemID, Count: item.Count, IsHasItem: true})
	}
	ss.mails = append(ss.mails, mail)
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "邮件保存失败"
	}
	if ch.conn != nil {
		_ = s.onGetMail(ch, &protocol.C2M_GetMail{Page: gmMailPushPage(len(ss.mails))})
	}
	return "completed", map[string]any{"mailId": strconvI64(mail.Id), "items": len(mail.Items)}, "", ""
}

type gmGrantItem struct {
	ItemID int32 `json:"itemId"`
	Count  int32 `json:"count"`
}
type gmItemsPayload struct {
	Items []gmGrantItem `json:"items"`
}

func (s *Server) gmGrantItems(playerID int64, raw json.RawMessage) (string, map[string]any, string, string) {
	var payload gmItemsPayload
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Items) == 0 || len(payload.Items) > 100 {
		return "rejected", nil, "invalid_payload", "物品参数错误"
	}
	grants := make([]bagGrant, 0, len(payload.Items))
	for _, item := range payload.Items {
		if item.ItemID <= 0 || item.Count <= 0 || item.Count > 1000000000 || !gmItemExists(item.ItemID) {
			return "rejected", nil, "invalid_item", "物品不存在或数量无效"
		}
		grants = append(grants, bagGrant{itemID: item.ItemID, count: item.Count, source: "GM 管理后台"})
	}
	ch, release, err := s.gmResolveSession(playerID)
	if err != nil {
		return "rejected", nil, "player_not_found", "角色不存在"
	}
	defer release()
	ss := ch.session
	ss.battleMu.Lock()
	defer ss.battleMu.Unlock()
	staged, _, ok := stageBagGrants(ss, grants)
	if !ok {
		return "rejected", nil, "bag_full", "背包空间不足"
	}
	ss.bag = staged
	if err := s.persistGMSession(ch); err != nil {
		return "rejected", nil, "persist_failed", "物品保存失败"
	}
	if ch.conn != nil {
		s.pushBagSnapshot(ch)
		s.pushMoney(ch)
	}
	return "completed", map[string]any{"items": payload.Items}, "", ""
}

func gmItemExists(itemID int32) bool {
	if tables == nil {
		return false
	}
	_, equip := tables.equipBase[int64(itemID)]
	_, goods := tables.goodsBase[int64(itemID)]
	_, material := tables.materialBase[int64(itemID)]
	return equip || goods || material
}

func (s *Server) persistGMSession(ch *channel) error {
	if ch == nil || ch.session == nil || s == nil || s.store == nil {
		return errors.New("store unavailable")
	}
	return s.saveGMOffline(ch.session)
}

func (s *Server) saveGMOffline(ss *session) error {
	if ss == nil || s == nil || s.store == nil {
		return errors.New("store unavailable")
	}
	if err := s.store.SavePlayerState(ss.playerID, ss.jobID, ss.level, ss.exp, ss.energy, ss.mapID, ss.x, ss.y,
		ss.charPoint, ss.skillPoint, ss.strAdd, ss.qukAdd, ss.spiAdd, ss.wimAdd, ss.phyAdd, ss.staAdd,
		ss.autoBattleEnabled, ss.coin, ss.yuanBao, ss.voucher, ss.storeCoin, ss.trans, ss.skinID, ss.titleID,
		ss.familyID, ss.familyContribute, ss.personalContribute, playerRelations{skills: ss.skills, skillOrder: ss.skillOrder,
			autoSkills: ss.autoSkills, tasks: ss.tasks, killCounts: ss.killCount, bag: ss.bag, worn: ss.worn, store: ss.store,
			starSoul: ss.starSoul, pet: ss.pet, mails: ss.mails, friends: ss.friends, signin: ss.signin, mainUI: ss.mainUISlots,
			itemBuffs: ss.itemBuffs}, ss.starSoul); err != nil {
		return err
	}
	if err := s.store.SavePlayerCurrencies(ss.playerID, ss.honor, ss.pvpCurrency); err != nil {
		return err
	}
	if err := s.store.SavePlayerStorePages(ss.playerID, storePageCount(ss)); err != nil {
		return err
	}
	ss.socialMu.Lock()
	from := cloneRequestIDs(ss.friendReqFrom)
	to := cloneRequestIDs(ss.friendReqTo)
	ss.socialMu.Unlock()
	return s.store.SaveFriendRequests(ss.playerID, from, to)
}

// truncateGM 按 rune 截断到 max 个字符并去掉首尾空白。
//
// 必须按 rune 而不是字节：GM 发件人名常常是中文，len() 数的是字节，value[:max]
// 会切在多字节字符中间，产生非法 UTF-8。该值要写进 player_mails.sender_name
// （utf8mb4），MySQL 会直接以 "Incorrect string value" 拒绝，整封邮件保存失败。
// 另外要先 TrimSpace 再截断，否则首尾空白会白占掉字符额度；截断后可能落在
// 名字中间的空格上，所以末尾再 trim 一次。
func truncateGM(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > max {
		runes = runes[:max]
	}
	return strings.TrimSpace(string(runes))
}

// gmMailPushPage 返回 GM 发信后应当推给客户端的邮件页码。
//
// ss.mails 按插入序排列，mailPageInfo 又是从左往右切页（第 1 页 = 最旧的
// mailPageSize 封），而 GM 新邮件 append 在末尾 —— 所以只要角色手上已有
// >= mailPageSize 封邮件，固定推 Page:1 推到的就是最旧那页，客户端界面上
// 完全看不到刚发的邮件（邮件确实已落库，玩家翻到最后一页才能发现）。
// 新邮件永远在最后一页，因此这里推总页数。
//
// total 允许大于 onGetMail 内部 pruneExpiredMails 之后的真实封数：页码超过
// 总页数时 mailPageInfo 会把它钳回最后一页，结果依然正确。
func gmMailPushPage(total int) int32 {
	if total <= 0 {
		return 1
	}
	return int32((total + mailPageSize - 1) / mailPageSize)
}

func commandPayloadHash(raw json.RawMessage) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

func strconvI64(value int64) string { return strconv.FormatInt(value, 10) }
