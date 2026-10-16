package graphstore

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInferenceBudgetSurvivesRestartAndExpiresWithoutResettingOtherAttempts(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{now, now.Add(time.Hour)} {
		if _, ok, err := s.ReserveInference(at, 2); err != nil || !ok {
			t.Fatal("reservation failed", err)
		}
	}
	s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, ok, err := s.ReserveInference(now.Add(2*time.Hour), 2)
	if err != nil || ok || state.Used != 2 || state.ResumesAt != now.Add(24*time.Hour).Format(time.RFC3339Nano) {
		t.Fatal("restart replenished budget", state, err)
	}
	state, err = s.InferenceBudget(now.Add(2*time.Hour), 1)
	if err != nil || state.ResumesAt != now.Add(25*time.Hour).Format(time.RFC3339Nano) {
		t.Fatal("reduced budget resumed too early", state, err)
	}
	state, ok, err = s.ReserveInference(now.Add(24*time.Hour), 2)
	if err != nil || !ok || state.Used != 2 {
		t.Fatal("rolling expiry lost an active attempt", state, err)
	}
}

func TestInferenceBudgetConcurrentReservationsCannotExceedLimit(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.ReserveInference(time.Now(), 3)
			if err != nil {
				t.Error(err)
			}
			if ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatal("concurrent overspend", accepted.Load())
	}
}

func TestAutomaticInferenceHasNoQuotaAndPersistsAttemptCount(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 125; i++ {
		state, allowed, err := store.ReserveInference(time.Now(), 0)
		if err != nil || !allowed || state.Used != i+1 || state.ResumesAt != "" {
			t.Fatal(state, allowed, err)
		}
	}
	store.Close()
	store, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, allowed, err := store.ReserveInference(time.Now().Add(48*time.Hour), 0)
	if err != nil || !allowed || state.Used != 126 || state.Limit != 0 || state.ResumesAt != "" {
		t.Fatal(state, allowed, err)
	}
}
