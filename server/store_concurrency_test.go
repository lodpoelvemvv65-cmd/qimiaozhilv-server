package main

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreConcurrentAccountCreationDoesNotReturnBusy(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const accountCount = 32
	start := make(chan struct{})
	errs := make(chan error, accountCount)
	var wg sync.WaitGroup
	for i := 0; i < accountCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := store.CreateAccount(fmt.Sprintf("parallel-%02d", index), "password")
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent CreateAccount failed: %v", err)
		}
	}
	for i := 0; i < accountCount; i++ {
		if _, _, err := store.FindAccount(fmt.Sprintf("parallel-%02d", i)); err != nil {
			t.Fatalf("account %d was not persisted: %v", i, err)
		}
	}
}
