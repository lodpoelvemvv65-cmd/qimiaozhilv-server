package main

import (
	"encoding/binary"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

type teamHeadVitalPush struct {
	unitID  int64
	numeric int32
	value   float32
}

// recordedVitals 解析测试连接录到的帧，只留下 SyncUnitAttribute。
func recordedVitals(t *testing.T, data []byte) []teamHeadVitalPush {
	t.Helper()
	var pushes []teamHeadVitalPush
	for len(data) > 0 {
		if len(data) < 4 {
			t.Fatalf("truncated frame: %x", data)
		}
		length := int(binary.LittleEndian.Uint16(data[:2]))
		if length < 2 || len(data) < length+2 {
			t.Fatalf("invalid frame length %d in %x", length, data)
		}
		opcode := binary.LittleEndian.Uint16(data[2:4])
		body := data[4 : length+2]
		data = data[length+2:]
		if opcode != protocol.OpM2C_SyncUnitAttribute {
			continue
		}
		var msg protocol.M2C_SyncUnitAttribute
		if err := proto.Unmarshal(body, &msg); err != nil {
			t.Fatalf("unmarshal SyncUnitAttribute: %v", err)
		}
		pushes = append(pushes, teamHeadVitalPush{unitID: msg.UnitId, numeric: msg.NumericType, value: msg.Value})
	}
	return pushes
}

// vitalsFor 取某个 UnitId 上某个 NumericType 的推送序列。
func vitalsFor(pushes []teamHeadVitalPush, unitID int64, numeric int32) []float32 {
	var values []float32
	for _, push := range pushes {
		if push.unitID == unitID && push.numeric == numeric {
			values = append(values, push.value)
		}
	}
	return values
}

// teamHeadTestParty 造一支三人队伍：收件人 3902（自己），满血的 3904 和 0 血的 3903。
// 返回收件人 channel、队伍 UnitId 列表，以及各成员的连接，方便核对推送内容。
func teamHeadTestParty(t *testing.T) (*Server, *channel, []int64, map[int64]*recordingConn) {
	t.Helper()
	loadOnlineTablesForTest(t)
	server := &Server{conns: map[int64]*channel{}}
	conns := map[int64]*recordingConn{}
	add := func(playerID int64, hurt bool) *channel {
		ss := newSession()
		ss.state = sessInGame
		ss.playerID, ss.name = playerID, "team"
		ss.jobID, ss.level, ss.trans, ss.mapID = 7, 17007, 2, 10004
		ss.bag = make(map[int32]*bagItem)
		ss.worn = make(map[int32]*bagItem)
		ss.hp, ss.mp = ss.playerMaxHp(), ss.playerMaxMp()
		if hurt {
			ss.hp = 0
		}
		conn := &recordingConn{}
		ch := &channel{id: playerID, conn: conn, session: ss}
		server.conns[playerID] = ch
		conns[playerID] = conn
		return ch
	}
	recipient := add(3902, true)
	add(3903, true)
	add(3904, false)
	add(2, false)
	return server, recipient, []int64{2, 3902, 3903, 3904}, conns
}

func TestTeamHeadNudgeDiffersFromExactValue(t *testing.T) {
	for _, test := range []struct {
		name        string
		exact, max  int32
		wantForZero float32
	}{
		{name: "没血抖动到 1", exact: 0, max: 9645998, wantForZero: 1},
		{name: "半血抖动一格", exact: 500, max: 1000},
		{name: "满血不越过上限", exact: 1000, max: 1000},
		{name: "float32 边界内 ±1 可用", exact: 16777215, max: 16777215},
		{name: "float32 边界外只能抖动裸值", exact: 35891804, max: 35891804},
		{name: "上限比血量大 1 时不越界", exact: 11489032, max: 11489033},
	} {
		t.Run(test.name, func(t *testing.T) {
			nudge := teamHeadNudge(test.exact, test.max)
			if nudge == float32(test.exact) {
				t.Fatalf("nudge(%d, %d) = %v 与真值相同，客户端收不到变化事件", test.exact, test.max, nudge)
			}
			if test.exact == 0 && nudge != test.wantForZero {
				t.Fatalf("0 血抖动值 = %v, want %v", nudge, test.wantForZero)
			}
			if test.exact > 0 && test.exact >= test.max && nudge > float32(test.max) {
				t.Fatalf("满血抖动值 %v 越过了上限 %d", nudge, test.max)
			}
		})
	}
}

func TestTeamHeadRebuildSendsExactVitals(t *testing.T) {
	server, recipient, ids, conns := teamHeadTestParty(t)
	server.sendTeamMember(recipient, 2, ids)
	pushes := recordedVitals(t, conns[3902].Bytes())

	for _, member := range []int64{2, 3903, 3904} {
		ss := server.conns[member].session
		for numeric, want := range map[int32]float32{
			1001: float32(ss.battleHP()),
			1002: float32(ss.playerMaxHp()),
			1003: float32(ss.battleMP()),
			1004: float32(ss.playerMaxMp()),
		} {
			values := vitalsFor(pushes, member, numeric)
			if len(values) == 0 {
				t.Fatalf("重建时 UnitId=%d 没有收到 1001-1004 中的 %d", member, numeric)
			}
			if last := values[len(values)-1]; last != want {
				t.Fatalf("UnitId=%d 类型 %d 重建后最后推送 %v, want %v", member, numeric, last, want)
			}
		}
	}
	if got := vitalsFor(pushes, 3903, 1001); got[len(got)-1] != 0 {
		t.Fatalf("0 血成员重建后最后推送 %v, want 0", got[len(got)-1])
	}
}

func TestTeamHeadVitalReloadResendsNudgedExactValues(t *testing.T) {
	server, recipient, ids, conns := teamHeadTestParty(t)
	server.sendTeamMember(recipient, 2, ids)
	before := len(conns[3902].Bytes())

	server.reloadTeamHeadVitals(recipient, ids, recipient.teamHeadReloadGen.Load())
	pushes := recordedVitals(t, conns[3902].Bytes()[before:])

	for _, member := range []int64{2, 3903, 3904} {
		ss := server.conns[member].session
		for numeric, want := range map[int32]float32{
			1001: float32(ss.battleHP()),
			1003: float32(ss.battleMP()),
		} {
			values := vitalsFor(pushes, member, numeric)
			if len(values) != 2 {
				t.Fatalf("UnitId=%d 类型 %d 补发 %d 次, want 2（抖动 + 精确）", member, numeric, len(values))
			}
			if values[0] == want {
				t.Fatalf("UnitId=%d 类型 %d 第一条推送 %v 与真值相同，客户端不会刷新", member, numeric, values[0])
			}
			if values[1] != want {
				t.Fatalf("UnitId=%d 类型 %d 精确值 %v, want %v", member, numeric, values[1], want)
			}
		}
	}
	// 没血的那一格必须停在 0，不能停在抖动出来的 1。
	hp := vitalsFor(pushes, 3903, 1001)
	if hp[0] != 1 || hp[1] != 0 {
		t.Fatalf("0 血成员补发序列 = %v, want [1 0]", hp)
	}
	// 自己的格子由客户端本机数据驱动，服务端不该补发。
	if got := vitalsFor(pushes, 3902, 1001); len(got) != 0 {
		t.Fatalf("给自己补发了 %d 条血量: %v", len(got), got)
	}
}

func TestTeamHeadVitalReloadSkipsStaleGeneration(t *testing.T) {
	server, recipient, ids, conns := teamHeadTestParty(t)
	server.sendTeamMember(recipient, 2, ids)
	gen := recipient.teamHeadReloadGen.Load()
	before := len(conns[3902].Bytes())

	server.reloadTeamHeadVitals(recipient, ids, gen-1)
	if after := len(conns[3902].Bytes()); after != before {
		t.Fatalf("过期代数的补发仍然写了 %d 字节", after-before)
	}
}

func TestTeamHeadVitalReloadSchedulesFollowUpRounds(t *testing.T) {
	original := teamHeadVitalReloadDelays
	teamHeadVitalReloadDelays = []time.Duration{10 * time.Millisecond, 30 * time.Millisecond}
	t.Cleanup(func() { teamHeadVitalReloadDelays = original })

	server, recipient, ids, conns := teamHeadTestParty(t)
	server.sendTeamMember(recipient, 2, ids)
	time.Sleep(300 * time.Millisecond)

	pushes := recordedVitals(t, conns[3902].Bytes())
	// 重建本身一条抖动 + 一条精确，加两轮补发各两条，共 6 条，且必须停在真值上。
	values := vitalsFor(pushes, 3903, 1001)
	if len(values) != 6 {
		t.Fatalf("0 血成员共收到 %d 条血量推送 %v, want 6", len(values), values)
	}
	if values[len(values)-1] != 0 {
		t.Fatalf("补发结束后最后一条血量 %v, want 0", values[len(values)-1])
	}
}
