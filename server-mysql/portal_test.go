package main

import "testing"

func TestMainCitySpawnFollowsConfiguredPortalOrder(t *testing.T) {
	firstX, firstY := mainCitySpawn(1)
	if firstX != -14.50656+portalOffset || firstY != -1.474469 {
		t.Fatalf("first city portal spawn = (%.5f, %.5f), want screen-left portal", firstX, firstY)
	}

	secondX, secondY := mainCitySpawn(2)
	if secondX != 14.50656-portalOffset || secondY != -1.474469 {
		t.Fatalf("second city portal spawn = (%.5f, %.5f), want screen-right portal", secondX, secondY)
	}

	returnX, returnY := mainCityReturnSpawn()
	if returnX != firstX || returnY != firstY {
		t.Fatalf("city return spawn = (%.5f, %.5f), want first portal (%.5f, %.5f)",
			returnX, returnY, firstX, firstY)
	}
}

func TestTrialSpawnUsesMapCentreInsteadOfEntryPortal(t *testing.T) {
	loadOnlineTablesForTest(t)
	x, y := sceneSpawn(10009)
	if x != 0 {
		t.Fatalf("trial spawn x = %.5f, want map centre 0", x)
	}
	if y != -1.259455 {
		t.Fatalf("trial spawn y = %.5f, want configured safe y -1.259455", y)
	}
}

func TestRightPortalClientTargetSetIsAcceptedFromMainCity(t *testing.T) {
	loadOnlineTablesForTest(t)
	var targets []int32
	for sceneID := int32(10011); sceneID <= 10026; sceneID++ {
		targets = append(targets, sceneID*100+1)
	}
	for sceneID := int32(10052); sceneID <= 10067; sceneID++ {
		targets = append(targets, sceneID*100+1)
	}
	for _, sceneID := range []int32{10009, 10036, 10037, 10038, 10030, 10031, 10032, 10033, 10034, 10035} {
		targets = append(targets, sceneID*100+1)
	}
	for layer := int32(1); layer <= 25; layer++ {
		targets = append(targets, 1001000+layer)
	}
	for _, target := range targets {
		if !validMapTransition(10004, target) {
			t.Errorf("right portal target %d was rejected from main city", target)
			continue
		}
		if sceneID := target / 100; sceneID != 10030 && sceneID != 10031 && sceneID != 10032 && sceneID != 10036 && sceneID != 10037 && sceneID != 10038 {
			x, y := sceneSpawn(sceneID)
			if x == 0 && y == 0 {
				t.Errorf("right portal target %d has no configured spawn", target)
			}
		}
	}
}

func TestRightPortalDoesNotExposeInternalSceneIds(t *testing.T) {
	loadOnlineTablesForTest(t)
	for _, sceneID := range []int32{10045, 10046, 10047, 10048, 10049, 10050, 10051} {
		if knownScene(sceneID) {
			t.Errorf("internal scene %d unexpectedly exposed as direct portal target", sceneID)
		}
	}
}
