package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInitialScanAlignsFilesChangedWhileServerWasDown 走的是真正的首扫入口
// scanConfigFiles(initial=true)，不是直接调 initialConfigSync。
//
// 这一层单独测是有必要的：首扫里每个文件都是第一次看到，会先落进「未知路径」
// 分支；把对齐逻辑写在那个分支之外（第一版就是这样）会让 initialConfigSync
// 永远不被调用，而所有直接调用它的测试仍然全绿。只有驱动首扫才能发现。
func TestInitialScanAlignsFilesChangedWhileServerWasDown(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "config-file-sync-wiring"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	dir := t.TempDir()
	path := filepath.Join(dir, "SyncScan.yaml")
	if err := os.WriteFile(path, []byte("- - 1001\n  - _id: 1001\n    NeedCoin: 200000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 重启时 states 一定是空 map：这就是「服务端停机期间文件被改过」的现场。
	initialScan := func() {
		t.Helper()
		if err := scanConfigFiles(ctx, dir, store.db, nil, map[string]watchedConfigFile{},
			"test", "initial", 0, time.Now(), true); err != nil {
			t.Fatal(err)
		}
	}

	initialScan()
	published, err := readPublishedConfig(ctx, store.db, "SyncScan")
	if err != nil {
		t.Fatalf("initial scan did not align the file: %v", err)
	}
	if !configTreeContains(published, int64(200000)) {
		t.Fatalf("initial scan published the wrong content: %#v", published)
	}
	revision := testConfigRevision(t, store)

	// 再重启一次，文件没动：不能产生新 revision（否则每次重启都刷审计表）。
	initialScan()
	if got := testConfigRevision(t, store); got != revision {
		t.Errorf("restart with an unchanged file created a new revision: %d -> %d", revision, got)
	}

	// 停机期间改了文件，再启动：必须对齐。
	if err := os.WriteFile(path, []byte("- - 1001\n  - _id: 1001\n    NeedCoin: 400000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initialScan()
	published, err = readPublishedConfig(ctx, store.db, "SyncScan")
	if err != nil {
		t.Fatal(err)
	}
	if !configTreeContains(published, int64(400000)) {
		t.Fatalf("file changed while stopped was not aligned: %#v", published)
	}
}

func TestConfigTreeEqualComparesNestedStructures(t *testing.T) {
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"version": int64(1),
			"affix": map[string]interface{}{
				"empty_percent": int64(20),
				"tiers": []interface{}{
					map[string]interface{}{"quality": int64(1), "tier": int64(2)},
					map[string]interface{}{"quality": int64(2), "tier": int64(2)},
				},
				"items":  []interface{}{},
				"unused": nil,
			},
		}
	}
	if !configTreeEqual(base(), base()) {
		t.Fatal("identical trees compare unequal")
	}

	changedValue := base()
	changedValue["affix"].(map[string]interface{})["empty_percent"] = int64(30)
	if configTreeEqual(base(), changedValue) {
		t.Error("changed scalar compared equal")
	}

	missingKey := base()
	delete(missingKey["affix"].(map[string]interface{}), "empty_percent")
	if configTreeEqual(base(), missingKey) {
		t.Error("tree with a removed key compared equal")
	}

	extraKey := base()
	extraKey["affix"].(map[string]interface{})["surprise"] = int64(1)
	if configTreeEqual(base(), extraKey) {
		t.Error("tree with an added key compared equal")
	}

	reordered := base()
	reordered["affix"].(map[string]interface{})["tiers"] = []interface{}{
		map[string]interface{}{"quality": int64(2), "tier": int64(2)},
		map[string]interface{}{"quality": int64(1), "tier": int64(2)},
	}
	if configTreeEqual(base(), reordered) {
		t.Error("reordered array compared equal")
	}

	shorter := base()
	shorter["affix"].(map[string]interface{})["tiers"] = []interface{}{
		map[string]interface{}{"quality": int64(1), "tier": int64(2)},
	}
	if configTreeEqual(base(), shorter) {
		t.Error("array with a dropped element compared equal")
	}

	if !configTreeEqual([]interface{}{int64(1), "a"}, []interface{}{int64(1), "a"}) {
		t.Error("identical arrays compare unequal")
	}
	if configTreeEqual([]interface{}{int64(1)}, []interface{}{int64(1), int64(2)}) {
		t.Error("arrays of different length compared equal")
	}
}

func TestConfigTreeEqualTreatsWholeFloatAsItsInteger(t *testing.T) {
	// MySQL 侧可能把 20 存成 float（历史上用小数发布过），文件侧是整数 20。
	// 这种纯类型差异不该触发无谓的重发。
	if !configTreeEqual(int64(20), float64(20)) {
		t.Error("int64 20 and float64 20 compared unequal")
	}
	if !configTreeEqual(float64(20), int64(20)) {
		t.Error("float64 20 and int64 20 compared unequal")
	}
	if !configTreeEqual(map[string]interface{}{"a": int64(1)}, map[string]interface{}{"a": float64(1)}) {
		t.Error("nested whole float compared unequal to its integer")
	}
	// 真小数和截断后的整数不能算相等。
	if configTreeEqual(float64(0.1), int64(0)) {
		t.Error("0.1 compared equal to 0")
	}
	if configTreeEqual(int64(20), float64(20.5)) {
		t.Error("20 compared equal to 20.5")
	}
	// 类型不同又不兼容的标量一律不等。
	if configTreeEqual("20", int64(20)) {
		t.Error("string 20 compared equal to int 20")
	}
	if configTreeEqual(true, int64(1)) {
		t.Error("bool true compared equal to int 1")
	}
	if configTreeEqual(nil, int64(0)) {
		t.Error("nil compared equal to int 0")
	}
	if !configTreeEqual(nil, nil) {
		t.Error("nil compared unequal to nil")
	}
}

func TestReadPublishedConfigRoundTripsMapRoot(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "config-file-sync-map-root"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	value := map[string]interface{}{
		"version": int64(1),
		"affix": map[string]interface{}{
			"empty_percent":              int64(20),
			"six_dimension_percent":      int64(2),
			"six_dimension_full_percent": 0.1,
			"debug":                      true,
			"label":                      "洗词缀",
			"empty":                      []interface{}{},
			"nothing":                    nil,
			"tiers": []interface{}{
				map[string]interface{}{"quality": int64(1), "tier": int64(2)},
				map[string]interface{}{"quality": int64(6), "tier": int64(4)},
			},
		},
	}
	if _, err := replaceRuntimeConfig(ctx, store.db, "SyncRoundTrip", value, "test", "round trip"); err != nil {
		t.Fatal(err)
	}
	published, err := readPublishedConfig(ctx, store.db, "SyncRoundTrip")
	if err != nil {
		t.Fatal(err)
	}
	if !configTreeEqual(published, value) {
		t.Fatalf("published tree did not round-trip:\n got %#v\nwant %#v", published, value)
	}
}

func TestReadPublishedConfigRoundTripsDatatableRoot(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "config-file-sync-table-root"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	value := []interface{}{
		[]interface{}{int64(1001), map[string]interface{}{
			"_id":      int64(1001),
			"NeedCoin": int64(200000),
			"JobType":  int64(3),
			"Materials": []interface{}{
				map[string]interface{}{"Id": int64(20001), "Count": int64(5)},
			},
		}},
		[]interface{}{int64(1002), map[string]interface{}{"_id": int64(1002), "NeedCoin": int64(1)}},
	}
	if _, err := replaceRuntimeConfig(ctx, store.db, "SyncTable", value, "test", "round trip"); err != nil {
		t.Fatal(err)
	}
	published, err := readPublishedConfig(ctx, store.db, "SyncTable")
	if err != nil {
		t.Fatal(err)
	}
	if !configTreeEqual(published, value) {
		t.Fatalf("published table did not round-trip:\n got %#v\nwant %#v", published, value)
	}
}

func TestConfigFileMatchesPublishedDetectsValueChange(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "config-file-sync-matches"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	value := map[string]interface{}{"version": int64(1), "value": int64(5)}
	if _, err := replaceRuntimeConfig(ctx, store.db, "SyncMatches", value, "test", "seed"); err != nil {
		t.Fatal(err)
	}

	same, err := configFileMatchesPublished(ctx, store.db, "SyncMatches", value)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Error("identical content reported as changed")
	}

	changed := map[string]interface{}{"version": int64(1), "value": int64(7)}
	same, err = configFileMatchesPublished(ctx, store.db, "SyncMatches", changed)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Error("changed content reported as unchanged")
	}

	// 从来没发布过的配置（AffixWash / town_idle_exp 都踩过）必须判为「不同」。
	same, err = configFileMatchesPublished(ctx, store.db, "SyncNeverPublished", value)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Error("config missing from MySQL reported as unchanged")
	}
}

func TestInitialConfigSyncPublishesOnlyWhenContentDiffers(t *testing.T) {
	store, err := OpenStore(mysqlTestDSN(t, "config-file-sync-initial"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	dir := t.TempDir()
	path := filepath.Join(dir, "SyncInitial.yaml")
	writeFile := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := "- - 1001\n  - _id: 1001\n    NeedCoin: 200000\n"
	writeFile(first)

	// 第一次：MySQL 里没有，必须发布。
	if err := initialConfigSync(ctx, store.db, nil, path, "test", "initial"); err != nil {
		t.Fatal(err)
	}
	published, err := readPublishedConfig(ctx, store.db, "SyncInitial")
	if err != nil {
		t.Fatalf("first sync did not publish: %v", err)
	}
	if !configTreeContains(published, int64(200000)) {
		t.Fatalf("first sync published the wrong content: %#v", published)
	}
	revisionAfterFirst := testConfigRevision(t, store)

	// 第二次：文件没变，必须跳过，不能再产生 revision。
	if err := initialConfigSync(ctx, store.db, nil, path, "test", "initial"); err != nil {
		t.Fatal(err)
	}
	if got := testConfigRevision(t, store); got != revisionAfterFirst {
		t.Errorf("unchanged file created a new revision: %d -> %d", revisionAfterFirst, got)
	}

	// 第三次：内容变了（模拟「服务端没跑的时候改的 YAML」），必须重新发布。
	writeFile("- - 1001\n  - _id: 1001\n    NeedCoin: 300000\n")
	if err := initialConfigSync(ctx, store.db, nil, path, "test", "initial"); err != nil {
		t.Fatal(err)
	}
	published, err = readPublishedConfig(ctx, store.db, "SyncInitial")
	if err != nil {
		t.Fatal(err)
	}
	if !configTreeContains(published, int64(300000)) {
		t.Fatalf("changed file was not republished: %#v", published)
	}
	if got := testConfigRevision(t, store); got == revisionAfterFirst {
		t.Error("changed file did not create a new revision")
	}
}

func TestInitialConfigSyncMatchesRealOperationFile(t *testing.T) {
	// 关键覆盖：YAML 解码出来的树，形状必须和 MySQL 还原出来的树**完全一致**，
	// 否则 initialConfigSync 会把同一份配置判成「变了」，每次重启都刷一条 revision
	// ——那就把首扫不发布想避免的问题原样搬回来了。
	// 数据表形状（根是 [id, 对象] 数组）由
	// TestInitialConfigSyncPublishesOnlyWhenContentDiffers 覆盖；
	// 这里用仓库里真实发布的 AffixWash.yaml 覆盖「根是对象」的形状，
	// 它正是当年停机改配置静默失效时踩到的那一类。
	body, err := os.ReadFile(filepath.Join("config", "operations", "AffixWash.yaml"))
	if err != nil {
		t.Skipf("operation file unavailable: %v", err)
	}
	store, err := OpenStore(mysqlTestDSN(t, "config-file-sync-real-affix"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "AffixWash.yaml")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := decodeRuntimeConfigSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := value.(map[string]interface{}); !ok {
		t.Fatalf("AffixWash.yaml did not decode to an object root: %T", value)
	}

	if err := initialConfigSync(ctx, store.db, nil, path, "test", "initial"); err != nil {
		t.Fatal(err)
	}
	published, err := readPublishedConfig(ctx, store.db, "AffixWash")
	if err != nil {
		t.Fatal(err)
	}
	if !configTreeEqual(published, value) {
		t.Fatalf("object-root config did not round-trip through MySQL:\n got %#v\nwant %#v", published, value)
	}
	revision := testConfigRevision(t, store)

	// 同一个文件再来一次：必须判定为一致，不产生新 revision。
	if err := initialConfigSync(ctx, store.db, nil, path, "test", "initial"); err != nil {
		t.Fatal(err)
	}
	if got := testConfigRevision(t, store); got != revision {
		t.Errorf("unchanged object-root config created a new revision: %d -> %d", revision, got)
	}
}

func TestInitialConfigSyncPublishesInvalidFileSoTheErrorIsLoud(t *testing.T) {
	// 文件读不出来时仍然走发布路径：publishConfigFile 先校验、把原因打进日志，
	// 校验不过不会碰 MySQL。这里断言「有报错」而不是「静默跳过」。
	store, err := OpenStore(mysqlTestDSN(t, "config-file-sync-invalid"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "SyncInvalid.yaml")
	if err := os.WriteFile(path, []byte(":\n\t- not: valid: yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initialConfigSync(ctx, store.db, nil, path, "test", "initial"); err == nil {
		t.Fatal("invalid YAML was accepted silently")
	}
	if _, err := readPublishedConfig(ctx, store.db, "SyncInvalid"); err == nil {
		t.Error("invalid YAML reached MySQL")
	}
}

// TestInitialScanAttemptsAlignment 是不需要 MySQL 的接线守卫。
//
// 首扫里每个文件都是第一次看到，会先落进「未知路径」分支。对齐逻辑必须写在那个
// 分支里；写在分支之外（第一版就是这样）initialConfigSync 永远不会被调用，而所有
// 直接调用它的测试仍然全绿，上面那个 DB 版接线测试在没设 MHQ_TEST_MYSQL_DSN 时
// 又被跳过（发布脚本的 go test ./... 正是这种情况）。
//
// 这里不碰数据库，所以只能看日志：db 传 nil 时对齐必然失败，失败原因由
// scanConfigFiles 打成 "config watcher initial sync ... failed"。这行日志就是
// 「首扫尝试过对齐」的观测点。别人改了这行日志、或把对齐挪到别处，这里要一起改。
func TestInitialScanAttemptsAlignment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SyncWiring.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previous)

	if err := scanConfigFiles(context.Background(), dir, nil, nil, map[string]watchedConfigFile{},
		"test", "initial", 0, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged.String(), "config watcher initial sync") {
		t.Fatalf("首扫没有尝试对齐文件：initialConfigSync 从首扫分支里到不了，"+
			"服务端停机期间改的 YAML 会永远发布不出去。log=%q", logged.String())
	}
}

// configTreeContains 在还原出来的树里找某个整数，用来断言发布的内容。
func configTreeContains(value interface{}, want int64) bool {
	switch v := value.(type) {
	case map[string]interface{}:
		for _, child := range v {
			if configTreeContains(child, want) {
				return true
			}
		}
	case []interface{}:
		for _, child := range v {
			if configTreeContains(child, want) {
				return true
			}
		}
	case int64:
		return v == want
	}
	return false
}

// testConfigRevision 复用 config_reload.go 的 latestConfigRevision，只补错误处理。
func testConfigRevision(t *testing.T, store *Store) int64 {
	t.Helper()
	revision, err := latestConfigRevision(store.db)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}
