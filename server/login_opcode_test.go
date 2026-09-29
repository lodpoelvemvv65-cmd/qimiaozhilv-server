package main

import (
	"net"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"mhqserver/protocol"
)

func dispatchAndReadFrame(t *testing.T, opcode uint16, req proto.Message, ss *session) (uint16, []byte) {
	t.Helper()

	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	done := make(chan struct{})
	go func() {
		(&Server{}).handleOpcode(&channel{id: 1, conn: server, session: ss}, opcode, body)
		close(done)
	}()

	responseOpcode, responseBody, ok := readFrame(client)
	if !ok {
		t.Fatal("response frame was not received")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request dispatch did not finish")
	}
	return responseOpcode, responseBody
}

func TestInventoryErrorResponsesUseExpectedOpcode(t *testing.T) {
	tests := []struct {
		name           string
		requestOpcode  uint16
		responseOpcode uint16
		rpcID          int32
		request        proto.Message
		response       proto.Message
		businessReject bool
	}{
		{
			name:           "put on missing item",
			requestOpcode:  protocol.OpC2M_PutOn,
			responseOpcode: protocol.OpM2C_PutOn,
			rpcID:          101,
			request:        &protocol.C2M_PutOn{RpcId: 101, Index: 99},
			response:       &protocol.M2C_PutOn{},
			businessReject: true,
		},
		{
			name:           "take off empty slot",
			requestOpcode:  protocol.OpC2M_Takeoff,
			responseOpcode: protocol.OpM2C_Takeoff,
			rpcID:          102,
			request:        &protocol.C2M_Takeoff{RpcId: 102, Index: 1},
			response:       &protocol.M2C_Takeoff{},
			businessReject: true,
		},
		{
			name:           "use missing goods",
			requestOpcode:  protocol.OpC2M_UseGoods,
			responseOpcode: protocol.OpM2C_UseGoods,
			rpcID:          103,
			request:        &protocol.C2M_UseGoods{RpcId: 103, Index: 99},
			response:       &protocol.M2C_UseGoods{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := &session{bag: make(map[int32]*bagItem), worn: make(map[int32]*bagItem)}
			gotOpcode, body := dispatchAndReadFrame(t, tc.requestOpcode, tc.request, ss)
			if gotOpcode != tc.responseOpcode {
				t.Fatalf("response opcode = %d, want %d", gotOpcode, tc.responseOpcode)
			}
			if err := proto.Unmarshal(body, tc.response); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			fields := tc.response.ProtoReflect()
			rpcField := fields.Descriptor().Fields().ByName("RpcId")
			if rpcField == nil {
				t.Fatal("response descriptor has no RpcId field")
			}
			if int32(fields.Get(rpcField).Int()) != tc.rpcID {
				t.Fatalf("response RpcId = %d, want %d", fields.Get(rpcField).Int(), tc.rpcID)
			}
			errorField := fields.Descriptor().Fields().ByName("Error")
			messageField := fields.Descriptor().Fields().ByName("Message")
			if errorField == nil || messageField == nil {
				t.Fatal("response descriptor lacks Error/Message")
			}
			if tc.businessReject {
				if fields.Get(errorField).Int() != 0 || fields.Get(messageField).String() == "" {
					t.Fatal("put-on business reject must use Error=0 with a Message")
				}
			} else if fields.Get(errorField).Int() == 0 {
				t.Fatal("invalid inventory operation did not return an error")
			}
		})
	}
}

func TestButlerRPCsAreDispatchedAndRejectWithoutDisconnect(t *testing.T) {
	oldTables := tables
	tables = &datatables{manulEquip: map[int64]map[string]interface{}{}, equipBase: map[int64]map[string]interface{}{}, strengthen: map[int64]map[string]interface{}{}, equipAffix: map[int64]map[string]interface{}{}}
	t.Cleanup(func() { tables = oldTables })
	tests := []struct {
		requestOpcode, responseOpcode uint16
		request, response             proto.Message
	}{
		{protocol.OpC2M_MakeMunalEquip, protocol.OpM2C_MakeMunalEquip, &protocol.C2M_MakeMunalEquip{RpcId: 201, Type: 99}, &protocol.M2C_MakeMunalEquip{}},
		{protocol.OpC2M_StrengthEquip, protocol.OpM2C_StrengthEquip, &protocol.C2M_StrengthEquip{RpcId: 202, BagIndex: 99, PlusItemIndex: -1}, &protocol.M2C_StrengthEquip{}},
		{protocol.OpC2M_RefreshEquipMainAttribute, protocol.OpM2C_RefreshEquipMainAttribute, &protocol.C2M_RefreshEquipMainAttribute{RpcId: 203, BagIndex: 99}, &protocol.M2C_RefreshEquipMainAttribute{}},
		{protocol.OpC2M_RefreshEquipAffix, protocol.OpM2C_RefreshEquipAffix, &protocol.C2M_RefreshEquipAffix{RpcId: 204, BagIndex: 99}, &protocol.M2C_RefreshEquipAffix{}},
		{protocol.OpC2M_GetAllTrialCopyReword, protocol.OpM2C_GetAllTrialCopyReword, &protocol.C2M_GetAllTrialCopyReword{RpcId: 205}, &protocol.M2C_GetAllTrialCopyReword{}},
		{protocol.OpC2M_AddMapCoinCount, protocol.OpM2C_AddMapCoinCount, &protocol.C2M_AddMapCoinCount{RpcId: 206}, &protocol.M2C_AddMapCoinCount{}},
	}
	for _, tc := range tests {
		ss := newSession()
		ss.playerID = 7
		ss.bag = make(map[int32]*bagItem)
		ss.worn = make(map[int32]*bagItem)
		opcode, body := dispatchAndReadFrame(t, tc.requestOpcode, tc.request, ss)
		if opcode != tc.responseOpcode {
			t.Fatalf("request %d response opcode = %d, want %d", tc.requestOpcode, opcode, tc.responseOpcode)
		}
		if err := proto.Unmarshal(body, tc.response); err != nil {
			t.Fatal(err)
		}
		message := tc.response.ProtoReflect()
		errorField := message.Descriptor().Fields().ByName("Error")
		messageField := message.Descriptor().Fields().ByName("Message")
		if errorField == nil || messageField == nil || message.Get(errorField).Int() != 0 || message.Get(messageField).String() == "" {
			t.Fatalf("request %d response must be Error=0 with business message: %v", tc.requestOpcode, tc.response)
		}
	}
}

func TestGetBagDispatchSendsExpectedResponseAndPushOpcodes(t *testing.T) {
	req := &protocol.C2M_GetBag{RpcId: 104}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	ss := &session{playerID: 7, bag: make(map[int32]*bagItem), worn: make(map[int32]*bagItem)}
	done := make(chan struct{})
	go func() {
		(&Server{}).handleOpcode(&channel{id: 1, conn: server, session: ss}, protocol.OpC2M_GetBag, body)
		close(done)
	}()

	responseOpcode, responseBody, ok := readFrame(client)
	if !ok {
		t.Fatal("get bag response frame was not received")
	}
	if responseOpcode != protocol.OpM2C_GetBag {
		t.Fatalf("get bag response opcode = %d, want %d", responseOpcode, protocol.OpM2C_GetBag)
	}
	var response protocol.M2C_GetBag
	if err := proto.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("unmarshal get bag response: %v", err)
	}
	if response.RpcId != req.RpcId {
		t.Fatalf("get bag RpcId = %d, want %d", response.RpcId, req.RpcId)
	}

	pushOpcode, _, ok := readFrame(client)
	if !ok {
		t.Fatal("get bag update push was not received")
	}
	if pushOpcode != protocol.OpM2C_SendBag {
		t.Fatalf("get bag push opcode = %d, want %d", pushOpcode, protocol.OpM2C_SendBag)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("get bag dispatch did not finish")
	}
}

func TestBeachExitAfterLayerTenReturnsMainCity(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ss := newSession()
	ss.playerID = 7
	ss.mapID = 1000610
	ss.resetMovement(-1.8, -0.84)

	result := make(chan *protocol.M2C_RequestEnterMap, 1)
	go func() {
		resp := (&Server{}).onRequestEnterMap(&channel{id: 1, conn: server, session: ss},
			&protocol.C2M_RequestEnterMap{RpcId: 9, MapId: 1000611})
		result <- resp.(*protocol.M2C_RequestEnterMap)
	}()

	opcode, body, ok := readFrame(client)
	if !ok || opcode != protocol.OpM2C_ChangeMap {
		t.Fatalf("first push opcode = %d, ok=%v", opcode, ok)
	}
	var change protocol.M2C_ChangeMap
	if err := proto.Unmarshal(body, &change); err != nil {
		t.Fatal(err)
	}
	// wireMapID(10004) = 1000401: the wire protocol encodes maps as
	// sceneId*100+layer, so the city's logical id 10004 is sent as its valid
	// layer-1 id (1000401) to avoid the client decoding scene 100.
	if change.MapId != 1000401 {
		t.Fatalf("layer 10 exit changed to map %d, want 1000401", change.MapId)
	}
	// changeMap 现在先推 20036 UnitsInMap（同场景玩家同步）再推 StartupTransPoint。
	if opcode, _, ok = readFrame(client); !ok || opcode != protocol.OpM2C_UnitsInMap {
		t.Fatalf("second push opcode = %d, ok=%v (want UnitsInMap)", opcode, ok)
	}
	if opcode, _, ok = readFrame(client); !ok || opcode != protocol.OpM2C_StartupTransPoint {
		t.Fatalf("third push opcode = %d, ok=%v", opcode, ok)
	}
	resp := <-result
	if resp.Error != 0 || resp.Message != "" || ss.mapID != 10004 {
		t.Fatalf("unexpected map response=%+v sessionMap=%d", resp, ss.mapID)
	}
}
