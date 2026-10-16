package graphstore

import (
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

const inferenceBudgetWindow = 24 * time.Hour

type InferenceBudget struct {
	Used      int    `json:"used"`
	Limit     int    `json:"limit"`
	ResumesAt string `json:"resumes_at,omitempty"`
}

func readInferenceBudget(tx *bolt.Tx, now time.Time, limit int) (InferenceBudget, []time.Time, error) {
	state := InferenceBudget{Limit: limit}
	if limit < 0 || limit > 100 {
		return state, nil, errors.New("inference budget must be zero (automatic) or within 1–100 attempts per 24 hours")
	}
	if limit == 0 {
		if data := tx.Bucket([]byte("settings")).Get([]byte("inference_attempt_total")); data != nil {
			if err := json.Unmarshal(data, &state.Used); err != nil {
				return state, nil, err
			}
		}
		return state, nil, nil
	}
	var reservations []time.Time
	if data := tx.Bucket([]byte("settings")).Get([]byte("inference_reservations")); data != nil {
		if err := json.Unmarshal(data, &reservations); err != nil {
			return state, nil, err
		}
	}
	active := []time.Time{}
	var previous time.Time
	for _, at := range reservations {
		if at.IsZero() || at.Before(previous) {
			return state, nil, errors.New("invalid inference budget history")
		}
		previous = at
		if at.Add(inferenceBudgetWindow).After(now) {
			active = append(active, at)
		}
	}
	state.Used = len(active)
	if state.Used >= limit {
		state.ResumesAt = active[state.Used-limit].Add(inferenceBudgetWindow).UTC().Format(time.RFC3339Nano)
	}
	return state, active, nil
}

func (s *Store) InferenceBudget(now time.Time, limit int) (InferenceBudget, error) {
	var state InferenceBudget
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		state, _, err = readInferenceBudget(tx, now, limit)
		return err
	})
	return state, err
}

// ReserveInference persists an attempt before invoking the provider. A crash
// can consume an unused reservation, but restarting cannot replenish a quota.
// Authentication failures also consume an attempt, preventing login storms.
func (s *Store) ReserveInference(now time.Time, limit int) (InferenceBudget, bool, error) {
	var state InferenceBudget
	reserved := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		var active []time.Time
		var err error
		state, active, err = readInferenceBudget(tx, now, limit)
		if err != nil || limit > 0 && state.Used >= limit {
			return err
		}
		if limit == 0 {
			state.Used++
			data, err := json.Marshal(state.Used)
			if err != nil {
				return err
			}
			if err = tx.Bucket([]byte("settings")).Put([]byte("inference_attempt_total"), data); err != nil {
				return err
			}
			reserved = true
			return nil
		}
		if len(active) > 0 && now.Before(active[len(active)-1]) {
			now = active[len(active)-1]
		}
		active = append(active, now.UTC())
		data, err := json.Marshal(active)
		if err != nil {
			return err
		}
		if err := tx.Bucket([]byte("settings")).Put([]byte("inference_reservations"), data); err != nil {
			return err
		}
		state.Used++
		if state.Used >= limit {
			state.ResumesAt = active[0].Add(inferenceBudgetWindow).UTC().Format(time.RFC3339Nano)
		}
		reserved = true
		return nil
	})
	return state, reserved, err
}
