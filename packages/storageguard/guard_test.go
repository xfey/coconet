package storageguard

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func testGuard(t *testing.T) *Guard {
	t.Helper()
	root := t.TempDir()
	paths := []string{filepath.Join(root, "authority.db"), filepath.Join(root, "relay.db")}
	for _, p := range paths {
		if err := os.WriteFile(p, []byte("database"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	g, err := New(paths, 256<<20, 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	g.space = func(string) (int64, error) { return 1 << 30, nil }
	return g
}

func TestAdmissionCombinesStoresAndAllowsReclamation(t *testing.T) {
	g := testGuard(t)
	for _, p := range g.Paths {
		if err := os.Truncate(p, 100<<20); err != nil {
			t.Fatal(err)
		}
	}
	called := false
	if err := g.Run(false, func() error { called = true; return nil }); !errors.Is(err, ErrCapacity) || called {
		t.Fatal(err, called)
	}
	if err := g.Run(true, func() error { called = true; return nil }); err != nil || !called {
		t.Fatal(err)
	}
	for _, p := range g.Paths {
		if err := os.Truncate(p, 10<<20); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Run(false, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestLowDiskFailsClosedButPreservesRecoveryReserve(t *testing.T) {
	g := testGuard(t)
	g.space = func(string) (int64, error) { return 100 << 20, nil }
	if err := g.Run(false, func() error { t.Fatal("mutation called"); return nil }); !errors.Is(err, ErrSpace) {
		t.Fatal(err)
	}
	if err := g.Run(true, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	g.space = func(string) (int64, error) { return 0, errors.New("unavailable filesystem") }
	if err := g.Run(false, func() error { t.Fatal("mutation called"); return nil }); err == nil {
		t.Fatal("ignored probe error")
	}
}

func TestConcurrentUploadsCannotOverbookDisk(t *testing.T) {
	g := testGuard(t)
	g.space = func(string) (int64, error) { return g.MinFreeBytes + Headroom + 100, nil }
	var wg sync.WaitGroup
	var mu sync.Mutex
	var releases []func()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := g.Reserve(60)
			if err == nil {
				mu.Lock()
				releases = append(releases, release)
				mu.Unlock()
			} else if !errors.Is(err, ErrSpace) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(releases) != 1 {
		t.Fatal("overbooked", len(releases))
	}
	releases[0]()
	releases[0]()
	release, err := g.Reserve(100)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestSnapshotBoundaryExcludesMutations(t *testing.T) {
	g := testGuard(t)
	entered, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { _ = g.Exclusive(func() error { close(entered); <-finish; return nil }); close(done) }()
	<-entered
	changed := make(chan struct{})
	go func() { _ = g.Run(false, func() error { close(changed); return nil }) }()
	select {
	case <-changed:
		t.Fatal("mutation crossed snapshot boundary")
	default:
	}
	close(finish)
	<-done
	<-changed
}
