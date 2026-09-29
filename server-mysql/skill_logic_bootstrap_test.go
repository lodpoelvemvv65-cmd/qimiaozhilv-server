package main

import (
	"os"
	"path/filepath"
)

func skillLogicFixturePath() string {
	// 仓库内自带 testdata 夹具，保证不依赖游戏客户端也能跑 `go test`；
	// 如果本地存在客户端，也允许回退到客户端解包目录。
	for _, path := range []string{
		filepath.Join("testdata", "SkillLogicConfig"),
		filepath.Join("..", "client-test", "梦幻奇遇记_Data", "StreamingAssets", "yoo", "_extracted", "SkillLogicConfig"),
		filepath.Join("..", "client-127.0.0.1", "梦幻奇遇记_Data", "StreamingAssets", "yoo", "_extracted", "SkillLogicConfig"),
	} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join("testdata", "SkillLogicConfig")
}

// Unit tests that exercise legacy, database-independent combat fixtures need
// the online catalog. Production startup never executes this file and loads
// the same catalog exclusively from MySQL.
func init() {
	catalog, err := LoadSkillLogicCatalog(skillLogicFixturePath())
	if err != nil {
		panic(err)
	}
	skillLogicCatalog = catalog
}
