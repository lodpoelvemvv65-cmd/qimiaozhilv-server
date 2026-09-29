package main

import (
	"strings"
	"testing"

	"mhqserver/protocol"
)

func TestEnergyPurchaseUsesClientCopperPriceAndClampsMaximum(t *testing.T) {
	ss := newSession()
	ss.playerID, ss.coin, ss.energy = 7, energyPurchasePrice+1, energyMaximum-energyPurchaseAmount/2
	ch := &channel{id: 1, conn: &recordingConn{}, session: ss}
	server := &Server{}

	price := server.onGetAddEnergyPrice(ch, &protocol.C2M_GetAddEnergyPrice{RpcId: 1}).(*protocol.M2C_GetAddEnergyPrice)
	if int64(price.Price) != energyPurchasePrice || price.Energy != energyPurchaseAmount {
		t.Fatalf("price=%+v, want copper price=%d energy=%d", price, energyPurchasePrice, energyPurchaseAmount)
	}
	resp := server.onAddEnergy(ch, &protocol.C2M_AddEnergy{RpcId: 2}).(*protocol.M2C_AddEnergy)
	if resp.Error != 0 || resp.Message != "" || ss.energy != energyMaximum || ss.coin != 1 {
		t.Fatalf("purchase response=%+v energy=%d coin=%d", resp, ss.energy, ss.coin)
	}

	full := server.onAddEnergy(ch, &protocol.C2M_AddEnergy{RpcId: 3}).(*protocol.M2C_AddEnergy)
	if full.Error != 0 || !strings.Contains(full.Message, "体力") || ss.coin != 1 || ss.energy != energyMaximum {
		t.Fatalf("full-energy response=%+v energy=%d coin=%d", full, ss.energy, ss.coin)
	}
}
