package graphstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

const maxTransferArchiveBytes int64 = 64 << 20
const maxTransferArchiveRows = 10000
const transferArchiveAge = 7 * 24 * time.Hour

type TransferUsage struct {
	Bytes int64 `json:"bytes"`
	Rows  int   `json:"rows"`
	Limit int64 `json:"limit"`
}

func readTransferUsage(tx *bolt.Tx) (TransferUsage, error) {
	usage := TransferUsage{Limit: maxTransferArchiveBytes}
	if data := tx.Bucket([]byte("settings")).Get([]byte("transfer-usage")); data != nil {
		if err := json.Unmarshal(data, &usage); err != nil {
			return usage, err
		}
	}
	if usage.Bytes < 0 || usage.Rows < 0 {
		return usage, errors.New("invalid transfer archive accounting")
	}
	usage.Limit = maxTransferArchiveBytes
	return usage, nil
}
func putTransferUsage(tx *bolt.Tx, usage TransferUsage) error {
	data, _ := json.Marshal(usage)
	return tx.Bucket([]byte("settings")).Put([]byte("transfer-usage"), data)
}
func transferOrderKey(id uint64) []byte              { return []byte(fmt.Sprintf("%020d", id)) }
func transferArchiveSize(id, key, data []byte) int64 { return int64(len(id) + 2*len(key) + len(data)) }
func initializeTransferArchive(tx *bolt.Tx) error {
	if tx.Bucket([]byte("settings")).Get([]byte("transfer-usage")) != nil {
		return nil
	}
	if tx.Bucket([]byte("transfer-order")) != nil {
		if err := tx.DeleteBucket([]byte("transfer-order")); err != nil {
			return err
		}
	}
	order, err := tx.CreateBucket([]byte("transfer-order"))
	if err != nil {
		return err
	}
	usage := TransferUsage{Limit: maxTransferArchiveBytes}
	if err := tx.Bucket([]byte("transfers")).ForEach(func(k, data []byte) error {
		var ref struct {
			ID uint64 `json:"id"`
		}
		if err := json.Unmarshal(data, &ref); err != nil {
			return err
		}
		if ref.ID == 0 {
			return errors.New("transfer record has no sequence identity")
		}
		id := transferOrderKey(ref.ID)
		if err := order.Put(id, k); err != nil {
			return err
		}
		usage.Rows++
		usage.Bytes += transferArchiveSize(id, k, data)
		return nil
	}); err != nil {
		return err
	}
	return putTransferUsage(tx, usage)
}
func pruneTransfers(tx *bolt.Tx, now time.Time, incoming int64) (TransferUsage, error) {
	usage, err := readTransferUsage(tx)
	if err != nil {
		return usage, err
	}
	order, records := tx.Bucket([]byte("transfer-order")), tx.Bucket([]byte("transfers"))
	cursor := order.Cursor()
	for id, k := cursor.First(); id != nil; id, k = cursor.Next() {
		data := records.Get(k)
		if data == nil {
			return usage, errors.New("transfer archive index differs")
		}
		var record struct {
			At string `json:"at"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return usage, err
		}
		at, err := time.Parse(time.RFC3339Nano, record.At)
		if err != nil {
			return usage, err
		}
		nextRows := usage.Rows
		if incoming > 0 {
			nextRows++
		}
		if now.Sub(at) < transferArchiveAge && usage.Bytes+incoming <= maxTransferArchiveBytes && nextRows <= maxTransferArchiveRows {
			break
		}
		usage.Bytes -= transferArchiveSize(id, k, data)
		usage.Rows--
		if err := records.Delete(k); err != nil {
			return usage, err
		}
		if err := cursor.Delete(); err != nil {
			return usage, err
		}
	}
	return usage, putTransferUsage(tx, usage)
}
func (s *Store) PruneTransfers(now time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error { _, err := pruneTransfers(tx, now, 0); return err })
}
