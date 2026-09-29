package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func itemUpgradeTestRow(rate float64, failCount int64) map[string]interface{} {
	return map[string]interface{}{
		"_id":           int64(20059),
		"UpgradeItemId": int64(20072),
		"SucceefulRate": rate,
		"UpgradeNeedMaterialArr": []interface{}{
			map[string]interface{}{
				"UpgradeNeedMaterial_Id":    int64(20059),
				"UpgradeNeedMaterial_Count": int64(4),
			},
		},
		"FailCastArr": []interface{}{
			map[string]interface{}{"_Id": int64(20059), "Count": failCount},
		},
	}
}

func withItemUpgradeTables(t *testing.T, row map[string]interface{}) {
	t.Helper()
	withFeatureTables(t, &datatables{
		itemUpgrade: map[int64]map[string]interface{}{20059: row},
		materialBase: map[int64]map[string]interface{}{
			20059: {"_id": int64(20059), "MaxAmount": int64(999)},
			20072: {"_id": int64(20072), "MaxAmount": int64(999)},
		},
	})
}

func TestStageItemUpgradeConsumesSubjectAndExtraMaterials(t *testing.T) {
	row := itemUpgradeTestRow(0.7, 5)
	withItemUpgradeTables(t, row)
	ss := featureTestSession(101)
	ss.bag[6] = &bagItem{ItemId: 20059, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 5}

	bag, success, message := stageItemUpgrade(ss, 6, row, true, func() float64 { return 0.69 })
	if message != "" || !success {
		t.Fatalf("upgrade success=%v message=%q", success, message)
	}
	result := bag[6]
	if result == nil || result.ItemId != 20072 || result.Count != 1 || !result.IsLock {
		t.Fatalf("upgrade result=%+v, want one locked item 20072", result)
	}
	if bagItemCount(&session{bag: bag}, 20059) != 0 {
		t.Fatalf("upgrade retained input items: %+v", bag)
	}
	if original := ss.bag[6]; original == nil || original.Count != 5 {
		t.Fatalf("staging mutated original bag: %+v", original)
	}
}

func TestStageItemUpgradeFailureUsesConfiguredRefund(t *testing.T) {
	row := itemUpgradeTestRow(0.7, 3)
	withItemUpgradeTables(t, row)
	ss := featureTestSession(102)
	ss.bag[2] = &bagItem{ItemId: 20059, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 5}

	bag, success, message := stageItemUpgrade(ss, 2, row, false, func() float64 { return 0.7 })
	if message != "" || success {
		t.Fatalf("upgrade success=%v message=%q", success, message)
	}
	if count := bagItemCount(&session{bag: bag}, 20059); count != 3 {
		t.Fatalf("failure refund count=%d, want 3", count)
	}
	if bagItemCount(&session{bag: bag}, 20072) != 0 {
		t.Fatalf("failed upgrade produced target: %+v", bag)
	}
}

func TestUpgradeValidationDoesNotRaiseRPCError(t *testing.T) {
	row := itemUpgradeTestRow(0.7, 5)
	withItemUpgradeTables(t, row)
	ss := featureTestSession(103)
	ss.bag[4] = &bagItem{ItemId: 20059, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 4}
	ch := &channel{id: 3, conn: &recordingConn{}, session: ss}

	response := (&Server{}).onUpgradeEquip(ch, &protocol.C2M_Upgrade{Index: 4, RpcId: 91}).(*protocol.M2C_Upgrade)
	if response.Error != 0 || response.Message != "进化材料不足" {
		t.Fatalf("validation response=%+v", response)
	}
	if ss.bag[4] == nil || ss.bag[4].Count != 4 {
		t.Fatalf("validation changed bag: %+v", ss.bag)
	}
}

func TestUpgradeFailurePushesBagBeforeMessageResponse(t *testing.T) {
	row := itemUpgradeTestRow(0, 3)
	withItemUpgradeTables(t, row)
	ss := featureTestSession(104)
	ss.bag[1] = &bagItem{ItemId: 20059, ItemType: int32(protocol.ItemType_MaterialsItem), Count: 5}
	conn := &recordingConn{}
	ch := &channel{id: 4, conn: conn, session: ss}

	if response := (&Server{}).onUpgradeEquip(ch, &protocol.C2M_Upgrade{Index: 1, RpcId: 92}); response != nil {
		t.Fatalf("upgrade response=%T, want raw response", response)
	}
	frames := decodeRecordedFrames(t, conn.Bytes())
	if len(frames) != 2 || frames[0].opcode != protocol.OpM2C_SendBag || frames[1].opcode != protocol.OpM2C_Upgrade {
		t.Fatalf("failure opcodes=%v", recordedOpcodes(t, conn.Bytes()))
	}
	var response protocol.M2C_Upgrade
	if err := proto.Unmarshal(frames[1].body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != 0 || response.Message != "进化失败" {
		t.Fatalf("failure response=%+v", &response)
	}
}
