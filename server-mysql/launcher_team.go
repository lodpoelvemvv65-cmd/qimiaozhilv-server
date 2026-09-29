package main

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"mhqserver/protocol"
)

const (
	launcherTeamRequestOpcode   uint16 = 65000
	launcherTeamResponseOpcode  uint16 = 65001
	launcherTeamRequestVersion         = 1
	launcherTeamPlanTTL                = 2 * time.Minute
	launcherTeamSweepInterval          = time.Second
	launcherTeamMaxRequestBytes        = 8 * 1024
)

type launcherTeamCredential struct {
	Account  string
	Password string
}

type launcherTeamPlanRequest struct {
	Version       int
	LeaderAccount string
	Accounts      []launcherTeamCredential
}

type launcherTeamPlanResponse struct {
	OK               bool
	Message          string
	ExpiresInSeconds int64
}

type launcherTeamPlan struct {
	ID        uint64
	LeaderID  int64
	Members   []int64
	ExpiresAt time.Time
	Applying  bool
}

type launcherTeamSnapshot struct {
	LeaderID int64
	Members  []int64
}

func (s *Server) handleLauncherTeamPlan(ch *channel, body []byte) {
	defer clear(body)
	response := launcherTeamPlanResponse{Message: "自动组队请求无效"}
	request, err := decodeLauncherTeamPlanRequest(body)
	if err == nil {
		response = s.submitLauncherTeamPlan(request, time.Now())
		redactLauncherTeamRequest(request)
	}
	out := encodeLauncherTeamPlanResponse(response)
	s.SendToChannel(ch, packOuter(launcherTeamResponseOpcode, out))
}

func decodeLauncherTeamPlanRequest(body []byte) (*launcherTeamPlanRequest, error) {
	if len(body) == 0 || len(body) > launcherTeamMaxRequestBytes {
		return nil, errors.New("invalid launcher team request length")
	}
	var request launcherTeamPlanRequest
	versionSeen, leaderSeen := false, false
	for len(body) > 0 {
		field, wireType, consumed := protowire.ConsumeTag(body)
		if consumed < 0 {
			return nil, protowire.ParseError(consumed)
		}
		body = body[consumed:]
		switch field {
		case 1:
			if wireType != protowire.VarintType || versionSeen {
				return nil, errors.New("invalid launcher team version field")
			}
			value, n := protowire.ConsumeVarint(body)
			if n < 0 || value > uint64(^uint(0)>>1) {
				return nil, errors.New("invalid launcher team version value")
			}
			request.Version, versionSeen = int(value), true
			body = body[n:]
		case 2:
			if wireType != protowire.BytesType || leaderSeen {
				return nil, errors.New("invalid launcher team leader field")
			}
			value, n := protowire.ConsumeString(body)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			request.LeaderAccount, leaderSeen = value, true
			body = body[n:]
		case 3:
			if wireType != protowire.BytesType {
				return nil, errors.New("invalid launcher team account field")
			}
			value, n := protowire.ConsumeBytes(body)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			credential, err := decodeLauncherTeamCredential(value)
			if err != nil {
				return nil, err
			}
			request.Accounts = append(request.Accounts, credential)
			body = body[n:]
		default:
			return nil, errors.New("launcher team request has an unknown field")
		}
	}
	if !versionSeen || !leaderSeen {
		return nil, errors.New("launcher team request is missing required fields")
	}
	return &request, nil
}

func decodeLauncherTeamCredential(body []byte) (launcherTeamCredential, error) {
	var credential launcherTeamCredential
	accountSeen, passwordSeen := false, false
	for len(body) > 0 {
		field, wireType, consumed := protowire.ConsumeTag(body)
		if consumed < 0 {
			return credential, protowire.ParseError(consumed)
		}
		body = body[consumed:]
		if wireType != protowire.BytesType {
			return credential, errors.New("invalid launcher credential field type")
		}
		value, n := protowire.ConsumeString(body)
		if n < 0 {
			return credential, protowire.ParseError(n)
		}
		body = body[n:]
		switch field {
		case 1:
			if accountSeen {
				return credential, errors.New("duplicate launcher account field")
			}
			credential.Account, accountSeen = value, true
		case 2:
			if passwordSeen {
				return credential, errors.New("duplicate launcher password field")
			}
			credential.Password, passwordSeen = value, true
		default:
			return credential, errors.New("launcher credential has an unknown field")
		}
	}
	if !accountSeen || !passwordSeen {
		return credential, errors.New("launcher credential is missing required fields")
	}
	return credential, nil
}

func encodeLauncherTeamPlanResponse(response launcherTeamPlanResponse) []byte {
	var out []byte
	if response.OK {
		out = protowire.AppendTag(out, 1, protowire.VarintType)
		out = protowire.AppendVarint(out, 1)
	}
	if response.Message != "" {
		out = protowire.AppendTag(out, 2, protowire.BytesType)
		out = protowire.AppendString(out, response.Message)
	}
	if response.ExpiresInSeconds > 0 {
		out = protowire.AppendTag(out, 3, protowire.VarintType)
		out = protowire.AppendVarint(out, uint64(response.ExpiresInSeconds))
	}
	return out
}

func (s *Server) submitLauncherTeamPlan(request *launcherTeamPlanRequest, now time.Time) launcherTeamPlanResponse {
	if request == nil || request.Version != launcherTeamRequestVersion ||
		len(request.Accounts) < 2 || len(request.Accounts) > maxTeamMembers {
		return launcherTeamPlanResponse{Message: "自动组队必须选择 2 到 5 个账号"}
	}
	if s.store == nil {
		return launcherTeamPlanResponse{Message: "服务器内部错误"}
	}

	seen := make(map[string]struct{}, len(request.Accounts))
	playersByAccount := make(map[string]int64, len(request.Accounts))
	credentialsValid := true
	for _, credential := range request.Accounts {
		if credential.Account == "" || len(credential.Account) > 64 ||
			credential.Password == "" || len(credential.Password) > 128 {
			credentialsValid = false
			continue
		}
		if _, duplicate := seen[credential.Account]; duplicate {
			return launcherTeamPlanResponse{Message: "自动组队账号不能重复"}
		}
		seen[credential.Account] = struct{}{}

		accountID, expectedPassword, err := s.store.FindAccount(credential.Account)
		if errors.Is(err, sql.ErrNoRows) {
			credentialsValid = false
			continue
		}
		if err != nil {
			log.Printf("launcher team account lookup failed: %v", err)
			return launcherTeamPlanResponse{Message: "服务器内部错误"}
		}
		if subtle.ConstantTimeCompare([]byte(expectedPassword), []byte(credential.Password)) != 1 {
			credentialsValid = false
			continue
		}
		player, err := s.store.FirstPlayer(accountID)
		if errors.Is(err, sql.ErrNoRows) {
			return launcherTeamPlanResponse{Message: "所选账号尚未创建角色"}
		}
		if err != nil {
			log.Printf("launcher team player lookup failed: %v", err)
			return launcherTeamPlanResponse{Message: "服务器内部错误"}
		}
		playersByAccount[credential.Account] = player.ID
	}
	if !credentialsValid || len(playersByAccount) != len(request.Accounts) {
		return launcherTeamPlanResponse{Message: "账号或密码错误"}
	}
	leaderID := playersByAccount[request.LeaderAccount]
	if leaderID <= 0 {
		return launcherTeamPlanResponse{Message: "队长必须包含在入队账号中"}
	}

	members := make([]int64, 0, len(request.Accounts))
	members = append(members, leaderID)
	for _, credential := range request.Accounts {
		playerID := playersByAccount[credential.Account]
		if playerID != leaderID {
			members = append(members, playerID)
		}
	}

	s.launcherTeamMu.Lock()
	if s.launcherTeamPlans == nil {
		s.launcherTeamPlans = make(map[uint64]*launcherTeamPlan)
	}
	for id, existing := range s.launcherTeamPlans {
		if teamMembersOverlap(existing.Members, members) {
			delete(s.launcherTeamPlans, id)
		}
	}
	s.launcherTeamSeq++
	plan := &launcherTeamPlan{
		ID: s.launcherTeamSeq, LeaderID: leaderID, Members: members,
		ExpiresAt: now.Add(launcherTeamPlanTTL),
	}
	s.launcherTeamPlans[plan.ID] = plan
	s.launcherTeamMu.Unlock()

	log.Printf("launcher team plan accepted id=%d leader=%d members=%v", plan.ID, leaderID, members)
	return launcherTeamPlanResponse{
		OK: true, Message: "自动组队计划已提交，有效期 2 分钟",
		ExpiresInSeconds: int64(launcherTeamPlanTTL / time.Second),
	}
}

func teamMembersOverlap(left, right []int64) bool {
	for _, leftID := range left {
		if teamContains(right, leftID) {
			return true
		}
	}
	return false
}

func (s *Server) launcherTeamPlanLoop() {
	ticker := time.NewTicker(launcherTeamSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		if s.closed.Load() {
			return
		}
		configStateMu.RLock()
		s.tryApplyLauncherTeamPlans(time.Now())
		configStateMu.RUnlock()
	}
}

func (s *Server) tryApplyLauncherTeamPlans(now time.Time) {
	s.launcherTeamMu.Lock()
	candidates := make([]*launcherTeamPlan, 0, len(s.launcherTeamPlans))
	for id, plan := range s.launcherTeamPlans {
		if !now.Before(plan.ExpiresAt) {
			delete(s.launcherTeamPlans, id)
			continue
		}
		if plan.Applying || !s.launcherTeamMembersOnline(plan) {
			continue
		}
		plan.Applying = true
		candidates = append(candidates, plan)
	}
	s.launcherTeamMu.Unlock()

	for _, plan := range candidates {
		s.launcherTeamMu.Lock()
		current := s.launcherTeamPlans[plan.ID]
		if current != plan {
			s.launcherTeamMu.Unlock()
			continue
		}
		applied := s.applyLauncherTeamPlan(plan)
		if applied {
			delete(s.launcherTeamPlans, plan.ID)
		} else {
			plan.Applying = false
		}
		s.launcherTeamMu.Unlock()
	}
}

func (s *Server) launcherTeamMembersOnline(plan *launcherTeamPlan) bool {
	if plan == nil {
		return false
	}
	for _, playerID := range plan.Members {
		if s.findChannelByPlayerID(playerID) == nil {
			return false
		}
	}
	return true
}

func (s *Server) applyLauncherTeamPlan(plan *launcherTeamPlan) bool {
	channels := make(map[int64]*channel, len(plan.Members))
	for _, playerID := range plan.Members {
		member := s.findChannelByPlayerID(playerID)
		if member == nil || sessionHasBattle(member) || isTrialMap(member.session.mapID) ||
			isActivityStageMap(member.session.mapID) {
			return false
		}
		channels[playerID] = member
	}
	leader := channels[plan.LeaderID]
	if leader == nil {
		return false
	}

	selected := make(map[int64]struct{}, len(plan.Members))
	for _, playerID := range plan.Members {
		selected[playerID] = struct{}{}
	}
	oldSnapshots := make([]launcherTeamSnapshot, 0)
	teamMu.Lock()
	for teamID, existing := range teams {
		remaining := make([]int64, 0, len(existing.Members))
		for _, playerID := range existing.Members {
			if _, remove := selected[playerID]; !remove {
				remaining = append(remaining, playerID)
			}
		}
		if len(remaining) == len(existing.Members) {
			continue
		}
		if len(remaining) == 0 {
			delete(teams, teamID)
			continue
		}
		existing.Members = remaining
		if !teamContains(remaining, existing.LeaderId) {
			existing.LeaderId = remaining[0]
		}
		oldSnapshots = append(oldSnapshots, launcherTeamSnapshot{
			LeaderID: existing.LeaderId,
			Members:  append([]int64(nil), remaining...),
		})
	}
	teamSeq++
	teamID := teamSeq
	newMembers := append([]int64(nil), plan.Members...)
	teams[teamID] = &teamState{LeaderId: plan.LeaderID, Members: newMembers}
	for _, playerID := range newMembers {
		channels[playerID].session.teamID = teamID
	}
	teamMu.Unlock()

	for _, snapshot := range oldSnapshots {
		s.sendTeamSnapshot(snapshot.LeaderID, snapshot.Members)
	}
	for _, playerID := range newMembers {
		member := channels[playerID]
		if playerID == plan.LeaderID || sameMapSession(leader.session, member.session) {
			continue
		}
		s.changeMap(member, leader.session.mapID, leader.session.x, leader.session.y)
	}
	s.sendTeamSnapshot(plan.LeaderID, newMembers)
	for _, member := range channels {
		s.sendPush(member, protocol.OpM2C_SendTip, &protocol.M2C_SendTip{
			Message: "自动组队完成", ActorId: member.session.playerID,
		})
	}
	log.Printf("launcher team plan applied id=%d team=%d leader=%d members=%v",
		plan.ID, teamID, plan.LeaderID, newMembers)
	return true
}

func redactLauncherTeamRequest(request *launcherTeamPlanRequest) {
	if request == nil {
		return
	}
	for index := range request.Accounts {
		request.Accounts[index].Password = strings.Repeat("\x00", len(request.Accounts[index].Password))
	}
}
