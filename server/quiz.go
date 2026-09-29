package main

import (
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

const quizCardCount = 60

func quizQuestionIDs() []int32 {
	if tables == nil {
		return nil
	}
	ids := make([]int, 0, len(tables.questConfig))
	for id := range tables.questConfig {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		out = append(out, int32(id))
	}
	return out
}

func nextQuizQuestion(current int32) int32 {
	ids := quizQuestionIDs()
	if len(ids) == 0 {
		return 0
	}
	for _, id := range ids {
		if id > current {
			return id
		}
	}
	return ids[0]
}

func startQuizQuestion(ss *session, questionID int32) {
	ss.quizQuestionID = questionID
	ss.quizStartedAt = time.Now()
	ss.quizAnswered = false
}

func (s *Server) onStartAnswerQuest(ch *channel, req *protocol.C2M_StartAnswerQuest) proto.Message {
	resp := &protocol.M2C_StartAnswerQuest{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	questionID := nextQuizQuestion(0)
	if questionID == 0 {
		resp.Error, resp.Message = errBadParam, "题库为空"
		return resp
	}
	ss := ch.session
	ss.quizRunScore = 0
	ss.quizRunTimeMS = 0
	ss.quizStreak = 0
	ss.quizAnswerCount = 0
	startQuizQuestion(ss, questionID)
	resp.ConfigId = questionID
	return resp
}

func (s *Server) onNextAnswerQuest(ch *channel, req *protocol.C2M_NextAnswerQuest) proto.Message {
	resp := &protocol.M2C_NextAnswerQuest{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	if ss.quizQuestionID == 0 {
		resp.Error, resp.Message = errBadParam, "请先开始答题"
		return resp
	}
	if !ss.quizAnswered {
		resp.Error, resp.Message = errBadParam, "请先回答当前题目"
		return resp
	}
	next := nextQuizQuestion(ss.quizQuestionID)
	if next == 0 {
		resp.Error, resp.Message = errBadParam, "题库为空"
		return resp
	}
	startQuizQuestion(ss, next)
	resp.ConfigId = next
	return resp
}

func (s *Server) onAnswerQuest(ch *channel, req *protocol.C2M_AnswerQuest) proto.Message {
	resp := &protocol.M2C_AnswerQuest{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	ss := ch.session
	row := map[string]interface{}(nil)
	if tables != nil {
		row = tables.questConfig[int64(ss.quizQuestionID)]
	}
	if row == nil || ss.quizStartedAt.IsZero() {
		resp.Error, resp.Message = errBadParam, "请先开始答题"
		return resp
	}
	if ss.quizAnswered {
		resp.Error, resp.Message = errBadParam, "当前题目已经回答"
		return resp
	}
	if req.Answer < 1 || req.Answer > 3 {
		resp.Error, resp.Message = errBadParam, "答案无效"
		return resp
	}
	elapsed := time.Since(ss.quizStartedAt).Milliseconds()
	if elapsed < 1 {
		elapsed = 1
	}
	if elapsed > math.MaxInt32 {
		elapsed = math.MaxInt32
	}
	ss.quizAnswered = true
	ss.quizAnswerCount++
	ss.quizRunTimeMS += elapsed
	score := int32(0)
	if req.Answer == int32(num(row["Answer"])) {
		ss.quizStreak++
		score = 10
		ss.quizRunScore += score
	} else {
		ss.quizStreak = 0
	}
	resp.Time = int32(elapsed)
	resp.ContinueCorrectCount = ss.quizStreak
	resp.Scord = score
	if ss.signin == nil {
		ss.signin = &signinState{}
	}
	ss.signin.ensureTaskProgress()
	ss.signin.QuizScore += score
	ss.signin.QuizTimeMS += elapsed
	ss.signin.QuizCompleted = true
	for taskID, state := range ss.tasks {
		row, ok := taskByID(taskID)
		if state == taskStateRunning && ok && int32(num(row["TargetType"])) == taskTargetQuiz {
			ss.signin.TaskQuizProgress[taskID] = true
		}
	}
	s.saveData(ch)
	s.pushTaskProgressAfterKill(ch)
	return resp
}

func (s *Server) onGetQuestScordInfo(ch *channel, req *protocol.C2M_GetQuestScordInfo) proto.Message {
	resp := &protocol.M2C_GetQuestScordInfo{RpcId: req.RpcId}
	if ch == nil || ch.session == nil || ch.session.playerID == 0 {
		resp.Error, resp.Message = errNotLogged, "请先登录"
		return resp
	}
	players, err := s.store.ListPlayers(0)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "答题排行读取失败"
		return resp
	}
	for _, player := range players {
		if player == nil {
			continue
		}
		state := signinFromJSON(player.Signin)
		if state == nil {
			state = &signinState{}
		}
		resp.InfoList = append(resp.InfoList, &protocol.QuestScordInfo{
			Id:    player.ID,
			Scord: state.QuizScore,
			Name:  player.Name,
			Level: player.Level,
			Time:  state.QuizTimeMS,
		})
	}
	sort.SliceStable(resp.InfoList, func(i, j int) bool {
		if resp.InfoList[i].Scord != resp.InfoList[j].Scord {
			return resp.InfoList[i].Scord > resp.InfoList[j].Scord
		}
		if resp.InfoList[i].Time != resp.InfoList[j].Time {
			return resp.InfoList[i].Time < resp.InfoList[j].Time
		}
		return resp.InfoList[i].Id < resp.InfoList[j].Id
	})
	if len(resp.InfoList) > 100 {
		resp.InfoList = resp.InfoList[:100]
	}
	return resp
}
