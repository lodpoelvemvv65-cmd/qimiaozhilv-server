package main

import (
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

// quizCardCount is retained as a compatibility alias for older tests/tools.
// Runtime handlers read the hot-reloadable Gameplay value instead.
const quizCardCount = 10

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

func resetQuizRound(ss *session) {
	ss.quizRunScore = 0
	ss.quizRunTimeMS = 0
	ss.quizStreak = 0
	ss.quizAnswerCount = 0
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
	resetQuizRound(ss)
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
	// 本轮题数已答满：客户端只把「Message 非空」当作结束信号
	// （ET.QuestAnswerUI::<OnClickAnswer>d__7 拿到非空 Message 时只弹一次提示就
	// 停止请求下一题，ConfigId 被忽略），所以这里必须回结束提示。继续下发 ConfigId
	// 会让界面永远出新题，玩家无法结束答题；直接回 Error 又会让
	// Session.Call 抛异常，界面卡在最后一题上。
	if ss.quizAnswerCount >= gameplayQuizQuestionCount() {
		resp.Message = "本轮答题已结束"
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
	// 本轮答满后界面仍停在最后一题，玩家可能再点一次选项。此时必须回与
	// 20306 相同的结束提示（Error=0），否则 Session.Call 会把业务错误抛成断线。
	if ss.quizAnswerCount >= gameplayQuizQuestionCount() {
		resp.Message = "本轮答题已结束"
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
	if ss.quizAnswerCount >= gameplayQuizQuestionCount() {
		ss.signin.QuizCompleted = true
		for taskID, state := range ss.tasks {
			row, ok := taskByID(taskID)
			if state == taskStateRunning && ok && int32(num(row["TargetType"])) == taskTargetQuiz {
				ss.signin.TaskQuizProgress[taskID] = true
			}
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
	records, err := s.store.ListQuizScores(100)
	if err != nil {
		resp.Error, resp.Message = errBadParam, "答题排行读取失败"
		return resp
	}
	for _, record := range records {
		resp.InfoList = append(resp.InfoList, &protocol.QuestScordInfo{
			Id:    record.ID,
			Scord: record.Score,
			Name:  record.Name,
			Level: record.Level,
			Time:  record.TimeMS,
		})
	}
	return resp
}
