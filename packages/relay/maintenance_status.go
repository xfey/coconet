package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

type MaintenanceStatus struct {
	ObservedAt              string `json:"observed_at"`
	TransferReservedBytes   int64  `json:"transfer_reserved_bytes"`
	TransferCapacityBytes   int64  `json:"transfer_capacity_bytes"`
	DueRecords              int    `json:"due_records"`
	DueCountCapped          bool   `json:"due_count_capped"`
	OldestDueAt             string `json:"oldest_due_at,omitempty"`
	HistoryRetentionSeconds int64  `json:"history_retention_seconds"`
	LastAttemptAt           string `json:"last_attempt_at,omitempty"`
	LastSuccessAt           string `json:"last_success_at,omitempty"`
	LastFailed              bool   `json:"last_failed"`
	Processed               int    `json:"processed"`
	HistoryRemoved          int    `json:"history_removed"`
}

func (s *Store) MaintenanceStatus() (MaintenanceStatus, error) {
	s.statsMu.Lock()
	result := s.lastMaintenance
	s.statsMu.Unlock()
	result.ObservedAt = s.now().UTC().Format(time.RFC3339Nano)
	result.TransferCapacityBytes = s.transferCapacity
	result.HistoryRetentionSeconds = int64(s.historyRetention / time.Second)
	err := s.db.View(func(tx *bolt.Tx) error {
		result.TransferReservedBytes = number(tx.Bucket([]byte("transfer-usage")).Get([]byte("\x00")))
		cursor := tx.Bucket([]byte("expiry")).Cursor()
		cutoff := []byte(fmt.Sprintf("%020d", s.now().UnixNano()))
		for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
			if len(k) < 21 {
				return errors.New("invalid expiry index")
			}
			if bytes.Compare(k[:20], cutoff) > 0 {
				break
			}
			if result.DueRecords == 10000 {
				result.DueCountCapped = true
				break
			}
			if result.DueRecords == 0 {
				at, err := strconv.ParseInt(string(k[:20]), 10, 64)
				if err != nil {
					return err
				}
				result.OldestDueAt = time.Unix(0, at).UTC().Format(time.RFC3339Nano)
			}
			result.DueRecords++
		}
		return nil
	})
	return result, err
}

// MaintenanceCycle limits both transaction batches and remote object work.
// RunMaintenance adds a wall-clock deadline; callers can impose a shorter one.
func (s *Store) MaintenanceCycle(ctx context.Context) error {
	started := s.now().UTC().Format(time.RFC3339Nano)
	processed, removed := 0, 0
	var failure error
	for batch := 0; batch < 16; batch++ {
		n, err := s.MaintainTemporary(ctx, 256)
		processed += n
		if err != nil && failure == nil {
			failure = err
		}
		if ctx.Err() != nil {
			break
		}
		status, err := s.MaintenanceStatus()
		if err != nil {
			failure = errors.Join(failure, err)
			break
		}
		if status.DueRecords == 0 {
			break
		}
	}
	if ctx.Err() == nil {
		failure = errors.Join(failure, s.pruneRepairs(256), s.pruneProcessing(256))
		if s.historyRetention > 0 {
			var err error
			removed, err = s.CompactHistory(ctx, s.historyRetention, 1024)
			failure = errors.Join(failure, err)
		}
	}
	failure = errors.Join(failure, ctx.Err())
	s.statsMu.Lock()
	s.lastMaintenance.LastAttemptAt = started
	s.lastMaintenance.Processed = processed
	s.lastMaintenance.HistoryRemoved = removed
	s.lastMaintenance.LastFailed = failure != nil
	if failure == nil {
		s.lastMaintenance.LastSuccessAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	s.statsMu.Unlock()
	return failure
}
