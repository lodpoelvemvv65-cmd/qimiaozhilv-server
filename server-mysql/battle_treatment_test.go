package main

import (
	"encoding/binary"
	"testing"
	"time"

	gproto "google.golang.org/protobuf/proto"
	"mhqserver/protocol"
)

func TestTreatmentCritAffectsHealEventAndProtocol(t *testing.T) {
	owner := &session{transBonus: map[int32]float32{1014: 1, 1016: 0.25}}
	battle := &battleState{owner: owner, playerHP: 500, playerMaxHP: 1000, spiAtk: 100}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	player := battle.runtime.Player()
	effect := SkillEffect{
		Kind:   SkillEffectHeal,
		Target: SkillTargetPlan{Kind: SkillTargetSingle, Side: SkillTargetSelf},
		Treatment: &SkillTreatmentPlan{
			Self: SkillFormula{Stat: SkillStatSpiritualAttack, Percent: 100}, CanCrit: true,
		},
	}
	events, err := battle.executeSkillEffect(effect, player, player, player, nil, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != CombatEventHeal || events[0].Amount != 175 || !events[0].IsCrit {
		t.Fatalf("critical treatment events = %+v, want one 175-point critical heal", events)
	}
	conn := &recordingConn{}
	(&Server{}).emitCombatEvents(&channel{conn: conn, session: owner}, battle, events)
	message := firstBattleSkillResult(t, conn.Bytes())
	if message.ChangeHpValue != 175 || !message.IsCrit {
		t.Fatalf("critical treatment protocol = %+v", message)
	}
}

func TestConfiguredTreatmentCritTriggersEventSeven(t *testing.T) {
	catalog := loadTestSkillLogic(t)
	owner := &session{transBonus: map[int32]float32{1014: 1}}
	battle := &battleState{owner: owner, playerHP: 500, playerMaxHP: 1000, spiAtk: 100}
	battle.runtime = NewCombatRuntime(battle, 100, nil)
	plan, err := catalog.Plan(310401, 5)
	if err != nil {
		t.Fatal(err)
	}
	events, err := battle.executeSkillPlan(plan, battle.runtime.Player(), battle.runtime.Player(), func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	criticalHeal, regularHeal := false, false
	for _, event := range events {
		if event.Type != CombatEventHeal {
			continue
		}
		criticalHeal = criticalHeal || (event.IsCrit && event.Amount == 150)
		regularHeal = regularHeal || (!event.IsCrit && event.Amount == 90)
	}
	if !criticalHeal || !regularHeal || battle.playerHP != 740 {
		t.Fatalf("advanced first aid crit hooks: hp=%d events=%+v", battle.playerHP, events)
	}
}

func TestPeriodicTreatmentRollsCriticalAtTick(t *testing.T) {
	clock := &fakeCombatClock{now: time.Unix(5000, 0)}
	owner := &session{transBonus: map[int32]float32{1014: 1}}
	battle := &battleState{owner: owner, playerHP: 500, playerMaxHP: 1000, spiAtk: 100}
	battle.runtime = NewCombatRuntime(battle, 100, clock)
	player := battle.runtime.Player()
	status := &SkillStatusPlan{ModifierID: 9001, DurationSeconds: 7, CanBeCleared: true}
	effect := SkillEffect{Treatment: &SkillTreatmentPlan{CanCrit: true, Periodic: true}}
	applied, err := battle.applyPeriodic(effect, status, player, player, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].IsCrit {
		t.Fatalf("periodic treatment rolled before its tick: %+v", applied)
	}
	clock.Add(gameplayEffectTickInterval())
	ticks := battle.runtime.Tick()
	if len(ticks) != 1 || ticks[0].Type != CombatEventHeal || ticks[0].Amount != 150 || !ticks[0].IsCrit {
		t.Fatalf("periodic critical treatment ticks = %+v", ticks)
	}
}

func firstBattleSkillResult(t *testing.T, data []byte) *protocol.M2C_BattleSkillRet {
	t.Helper()
	for len(data) >= 4 {
		length := int(binary.LittleEndian.Uint16(data[:2]))
		if length+2 > len(data) {
			t.Fatalf("truncated frame: length=%d remaining=%d", length, len(data))
		}
		if binary.LittleEndian.Uint16(data[2:4]) == protocol.OpM2C_BattleSkillRet {
			message := &protocol.M2C_BattleSkillRet{}
			if err := gproto.Unmarshal(data[4:length+2], message); err != nil {
				t.Fatal(err)
			}
			return message
		}
		data = data[length+2:]
	}
	t.Fatal("missing M2C_BattleSkillRet")
	return nil
}
