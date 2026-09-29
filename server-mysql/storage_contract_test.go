package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionStorageContract(t *testing.T) {
	forbidden := []string{"encoding/" + "json", "modernc.org/" + "sqlite", "mattn/go-" + "sqlite", "datatable_json"}
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == "protocol" || strings.HasPrefix(path, "protocol"+string(filepath.Separator)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, needle := range forbidden {
			if strings.Contains(string(data), needle) {
				t.Errorf("production file %s contains forbidden storage dependency %q", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	module, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(module)), "sqlite") {
		t.Fatal("go.mod contains a SQLite dependency")
	}
}
