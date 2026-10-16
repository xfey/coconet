package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	bolt "go.etcd.io/bbolt"
)

var recordsBucket = []byte("records")
var recordIndexesBucket = []byte("record_indexes")
var recordCountsBucket = []byte("record_counts")

// The business transaction reads typed records. Backend adapters own bytes,
// indexes and atomic commits; PostgreSQL can adopt the same boundary later.
type recordSource interface {
	get(string) ([]byte, error)
	scan(func(string, []byte) error) error
	count() (int, error)
	indexKeys(string, string) ([]string, error)
	put(string, []byte, []byte) error
	remove(string, []byte) error
}

type recordAccessStats struct {
	DecodedRecords, DecodedBytes, WrittenRecords, WrittenBytes, ScannedKeys int64
	DecodedByCollection                                                     map[string]int64
}

type recordTransaction struct {
	active bool
	err    error
	stats  recordAccessStats
}

func (tx *recordTransaction) fail(err error) {
	if err != nil && tx.err == nil {
		tx.err = err
	}
}

type storedRecords interface {
	bind(string, recordSource, *recordTransaction)
	flush() error
	replace(storedRecords) error
	writeTo(recordSource) error
	validate([]byte) error
}

// A collection is scoped to one transaction, never shared between requests.
// Only accessed records are decoded. Comparing the accessed values at commit
// also preserves existing mutations of nested slices/maps returned by Get.
type recordCollection[T any] struct {
	name     string
	rows     map[string]T
	before   map[string][]byte
	complete bool
	source   recordSource
	tx       *recordTransaction
	changed  map[string]bool
}

func newRecordCollectionWith[T any](rows map[string]T) *recordCollection[T] {
	if rows == nil {
		rows = make(map[string]T)
	}
	return &recordCollection[T]{rows: rows, complete: true}
}

func (c *recordCollection[T]) bind(name string, source recordSource, tx *recordTransaction) {
	c.name = name
	c.rows = make(map[string]T)
	c.before = make(map[string][]byte)
	c.complete, c.source, c.tx = false, source, tx
}

func (c *recordCollection[T]) load(key string, data []byte) {
	c.before[key] = bytes.Clone(data)
	if data == nil {
		return
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		c.tx.fail(fmt.Errorf("decode stored record: %w", err))
		return
	}
	if c.tx.stats.DecodedByCollection == nil {
		c.tx.stats.DecodedByCollection = map[string]int64{}
	}
	c.tx.stats.DecodedByCollection[c.name]++
	c.tx.stats.DecodedRecords++
	c.tx.stats.DecodedBytes += int64(len(data))
	c.rows[key] = value
}

func (c *recordCollection[T]) Lookup(key string) (T, bool) {
	if c == nil {
		var zero T
		return zero, false
	}
	if c.source != nil && !c.complete {
		if _, known := c.before[key]; !known {
			data, err := c.source.get(key)
			c.tx.fail(err)
			if err == nil {
				c.load(key, data)
			}
		}
	}
	value, ok := c.rows[key]
	return value, ok
}

func (c *recordCollection[T]) Get(key string) T { value, _ := c.Lookup(key); return value }

func (c *recordCollection[T]) Has(key string) bool {
	if c == nil {
		return false
	}
	if c.complete || c.source == nil {
		_, ok := c.rows[key]
		return ok
	}
	if _, known := c.before[key]; known {
		_, ok := c.rows[key]
		return ok
	}
	data, err := c.source.get(key)
	c.tx.fail(err)
	return err == nil && data != nil
}

func (c *recordCollection[T]) Set(key string, value T) {
	if c == nil {
		panic("uninitialized record collection")
	}
	_, _ = c.Lookup(key)
	c.rows[key] = value
	if c.changed == nil {
		c.changed = map[string]bool{}
	}
	c.changed[key] = true
}

func (c *recordCollection[T]) Delete(key string) {
	if c == nil {
		return
	}
	_, _ = c.Lookup(key)
	delete(c.rows, key)
	if c.changed == nil {
		c.changed = map[string]bool{}
	}
	c.changed[key] = true
}

func (c *recordCollection[T]) All() map[string]T {
	if c == nil {
		return nil
	}
	if !c.complete && c.source != nil {
		err := c.source.scan(func(key string, data []byte) error {
			if _, known := c.before[key]; !known {
				c.load(key, data)
			}
			return c.tx.err
		})
		c.tx.fail(err)
		if err == nil {
			c.complete = true
		}
	}
	return c.rows
}

func (c *recordCollection[T]) Len() int {
	if c == nil {
		return 0
	}
	if c.complete || c.source == nil {
		return len(c.rows)
	}
	n, err := c.source.count()
	c.tx.fail(err)
	for key, original := range c.before {
		_, exists := c.rows[key]
		if original == nil && exists {
			n++
		}
		if original != nil && !exists {
			n--
		}
	}
	return n
}

// By uses durable secondary keys, then overlays changes made in this same
// transaction so newly created/updated rows are immediately visible.
func (c *recordCollection[T]) By(index, value string) map[string]T {
	result := make(map[string]T)
	if c == nil {
		return result
	}
	if c.source != nil && !c.complete {
		keys, err := c.source.indexKeys(index, value)
		c.tx.fail(err)
		for _, key := range keys {
			_, _ = c.Lookup(key)
		}
	}
	for key, row := range c.rows {
		for _, candidate := range recordIndexValues(key, row)[index] {
			if candidate == value {
				result[key] = row
				break
			}
		}
	}
	return result
}

func (c *recordCollection[T]) MarshalJSON() ([]byte, error) {
	if c == nil {
		return []byte("null"), nil
	}
	rows := c.All()
	if c.tx != nil && c.tx.err != nil {
		return nil, c.tx.err
	}
	return json.Marshal(rows)
}

func (c *recordCollection[T]) UnmarshalJSON(data []byte) error {
	rows := make(map[string]T)
	if err := json.Unmarshal(data, &rows); err != nil {
		return err
	}
	*c = *newRecordCollectionWith(rows)
	return nil
}

func (c *recordCollection[T]) replace(other storedRecords) error {
	rows := c.All()
	for key := range rows {
		delete(rows, key)
	}
	if other != nil {
		typed, ok := other.(*recordCollection[T])
		if !ok {
			return errors.New("record collection type changed")
		}
		for key, value := range typed.All() {
			rows[key] = value
		}
	}
	return nil
}

func (c *recordCollection[T]) flush() error {
	if c.tx.err != nil {
		return c.tx.err
	}
	for key, original := range c.before {
		if original != nil {
			if _, exists := c.rows[key]; !exists {
				if err := c.source.remove(key, original); err != nil {
					return err
				}
				c.tx.stats.WrittenRecords++
			}
		}
	}
	for key, row := range c.rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if bytes.Equal(encoded, c.before[key]) {
			continue
		}
		if err := c.source.put(key, encoded, c.before[key]); err != nil {
			return err
		}
		c.tx.stats.WrittenRecords++
		c.tx.stats.WrittenBytes += int64(len(encoded))
	}
	return nil
}

func (c *recordCollection[T]) validate(data []byte) error {
	var row T
	return json.Unmarshal(data, &row)
}

func (c *recordCollection[T]) writeTo(source recordSource) error {
	for key, row := range c.All() {
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if err := source.put(key, data, nil); err != nil {
			return err
		}
	}
	return nil
}

type boltRecordSource struct {
	tx      *bolt.Tx
	context *recordTransaction
	name    string
}

func (s *boltRecordSource) ready() error {
	if !s.context.active {
		return errors.New("record accessed outside its transaction")
	}
	return s.context.err
}

func (s *boltRecordSource) bucket() *bolt.Bucket {
	return s.tx.Bucket(recordsBucket).Bucket([]byte(s.name))
}

func (s *boltRecordSource) get(key string) ([]byte, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.bucket().Get([]byte(key)), nil
}

func (s *boltRecordSource) scan(visit func(string, []byte) error) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.bucket().ForEach(func(key, value []byte) error {
		if value == nil {
			return errors.New("unexpected nested record bucket")
		}
		s.context.stats.ScannedKeys++
		return visit(string(key), value)
	})
}

func (s *boltRecordSource) count() (int, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	value := s.tx.Bucket(recordCountsBucket).Get([]byte(s.name))
	if len(value) != 8 {
		return 0, errors.New("invalid record count")
	}
	return int(binary.BigEndian.Uint64(value)), nil
}

func (s *boltRecordSource) adjustCount(delta int) error {
	n, err := s.count()
	if err != nil {
		return err
	}
	if n+delta < 0 {
		return errors.New("negative record count")
	}
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], uint64(n+delta))
	return s.tx.Bucket(recordCountsBucket).Put([]byte(s.name), data[:])
}

func recordIndexPrefix(value string) []byte {
	data := make([]byte, 4, 4+len(value))
	binary.BigEndian.PutUint32(data, uint32(len(value)))
	return append(data, value...)
}

func (s *boltRecordSource) indexKeys(index, value string) ([]string, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	keys := []string{}
	root := s.tx.Bucket(recordIndexesBucket).Bucket([]byte(s.name))
	if root == nil {
		return keys, nil
	}
	b := root.Bucket([]byte(index))
	if b == nil {
		return keys, nil
	}
	prefix := recordIndexPrefix(value)
	cursor := b.Cursor()
	for key, _ := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, _ = cursor.Next() {
		keys = append(keys, string(key[len(prefix):]))
		s.context.stats.ScannedKeys++
	}
	return keys, nil
}

func (s *boltRecordSource) changeIndexes(key string, data []byte, add bool) error {
	if data == nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	indexes := rawRecordIndexValues(key, value)
	if len(indexes) == 0 {
		return nil
	}
	root, err := s.tx.Bucket(recordIndexesBucket).CreateBucketIfNotExists([]byte(s.name))
	if err != nil {
		return err
	}
	for index, values := range indexes {
		b, err := root.CreateBucketIfNotExists([]byte(index))
		if err != nil {
			return err
		}
		for _, value := range values {
			if value == "" {
				continue
			}
			indexKey := append(recordIndexPrefix(value), key...)
			if add {
				err = b.Put(indexKey, []byte{})
			} else {
				err = b.Delete(indexKey)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *boltRecordSource) put(key string, data, original []byte) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := s.changeIndexes(key, original, false); err != nil {
		return err
	}
	if err := s.changeIndexes(key, data, true); err != nil {
		return err
	}
	if original == nil {
		if err := s.adjustCount(1); err != nil {
			return err
		}
	}
	return s.bucket().Put([]byte(key), data)
}

func (s *boltRecordSource) remove(key string, original []byte) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := s.changeIndexes(key, original, false); err != nil {
		return err
	}
	if err := s.adjustCount(-1); err != nil {
		return err
	}
	return s.bucket().Delete([]byte(key))
}

func recordIndexValues[T any](key string, value T) map[string][]string {
	result := map[string][]string{}
	v := reflect.ValueOf(value)
	if v.Kind() != reflect.Struct {
		return result
	}
	add := func(object reflect.Value) {
		for field, index := range map[string]string{"ProjectID": "project_id", "ServerSessionID": "server_session_id", "AccountID": "account_id", "DeviceID": "device_id"} {
			f := object.FieldByName(field)
			if f.IsValid() && f.Kind() == reflect.String && f.String() != "" {
				result[index] = append(result[index], f.String())
			}
		}
	}
	add(v)
	for _, field := range []string{"Summary", "Entry", "Batch", "Request", "Account", "Source"} {
		child := v.FieldByName(field)
		if child.IsValid() && child.Kind() == reflect.Struct {
			add(child)
		}
	}
	return result
}

func rawRecordIndexValues(key string, value any) map[string][]string {
	result := map[string][]string{}
	object, ok := value.(map[string]any)
	if !ok {
		return result
	}
	for _, field := range []string{"project_id", "server_session_id", "account_id", "device_id"} {
		if text, ok := object[field].(string); ok && text != "" {
			result[field] = []string{text}
		}
	}
	// Immutable versions, jobs and normalized records expose their source in
	// either the record itself or its summary. Nested ownership is explicit.
	for _, field := range []string{"summary", "entry", "batch", "request", "account", "source"} {
		if child, ok := object[field].(map[string]any); ok {
			for _, id := range []string{"project_id", "server_session_id", "account_id", "device_id"} {
				if text, ok := child[id].(string); ok && text != "" {
					result[id] = append(result[id], text)
				}
			}
		}
	}
	return result
}

func stateRecordCollections(state *databaseState) map[string]storedRecords {
	result := map[string]storedRecords{}
	value := reflect.ValueOf(state).Elem()
	kind := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := kind.Field(i)
		if !field.IsExported() || value.Field(i).Kind() != reflect.Pointer {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		if value.Field(i).IsNil() {
			result[name] = nil
			continue
		}
		if table, ok := value.Field(i).Interface().(storedRecords); ok {
			result[name] = table
		}
	}
	return result
}

func bindRecordState(tx *bolt.Tx, context *recordTransaction) (databaseState, map[string]storedRecords, error) {
	state := newDatabaseState()
	if tx.Bucket(recordsBucket) == nil || tx.Bucket(recordCountsBucket) == nil || tx.Bucket(recordIndexesBucket) == nil {
		return state, nil, errors.New("record storage buckets are missing")
	}
	tables := stateRecordCollections(&state)
	for name, table := range tables {
		if tx.Bucket(recordsBucket).Bucket([]byte(name)) == nil {
			return state, nil, fmt.Errorf("record collection missing: %s", name)
		}
		table.bind(name, &boltRecordSource{tx: tx, context: context, name: name}, context)
	}
	state.recordContext = context
	state.failureSource = &boltRecordSource{tx: tx, context: context, name: "recent_failures"}
	if tx.Bucket(recordsBucket).Bucket([]byte("recent_failures")) == nil {
		return state, nil, errors.New("failure record collection missing")
	}
	return state, tables, nil
}

func flushRecordState(state *databaseState, tables map[string]storedRecords) error {
	if state.recordContext.err != nil {
		return state.recordContext.err
	}
	for name, current := range stateRecordCollections(state) {
		original := tables[name]
		if current != original {
			if err := original.replace(current); err != nil {
				return err
			}
		}
		if err := original.flush(); err != nil {
			return err
		}
	}
	if state.failuresLoaded || state.RecentFailures != nil {
		data, err := json.Marshal(state.RecentFailures)
		if err != nil {
			return err
		}
		original, err := state.failureSource.get("all")
		if err != nil {
			return err
		}
		if !bytes.Equal(data, original) {
			return state.failureSource.put("all", data, original)
		}
	}
	return state.recordContext.err
}

func (state *databaseState) recentFailureRecords() []projectFailureRecord {
	if !state.failuresLoaded && state.failureSource != nil {
		data, err := state.failureSource.get("all")
		if err == nil && data != nil {
			err = json.Unmarshal(data, &state.RecentFailures)
		}
		state.recordContext.fail(err)
		state.failuresLoaded = true
	}
	return state.RecentFailures
}

func writeInitialRecordState(tx *bolt.Tx, state *databaseState) error {
	for _, name := range [][]byte{recordsBucket, recordIndexesBucket, recordCountsBucket} {
		if tx.Bucket(name) != nil {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
		}
		if _, err := tx.CreateBucket(name); err != nil {
			return err
		}
	}
	context := &recordTransaction{active: true}
	defer func() { context.active = false }()
	tables := stateRecordCollections(state)
	for name, table := range tables {
		if _, err := tx.Bucket(recordsBucket).CreateBucket([]byte(name)); err != nil {
			return err
		}
		if err := tx.Bucket(recordCountsBucket).Put([]byte(name), make([]byte, 8)); err != nil {
			return err
		}
		if err := table.writeTo(&boltRecordSource{tx: tx, context: context, name: name}); err != nil {
			return err
		}
	}
	if _, err := tx.Bucket(recordsBucket).CreateBucket([]byte("recent_failures")); err != nil {
		return err
	}
	if err := tx.Bucket(recordCountsBucket).Put([]byte("recent_failures"), make([]byte, 8)); err != nil {
		return err
	}
	data, err := json.Marshal(state.recentFailureRecords())
	if err != nil {
		return err
	}
	return (&boltRecordSource{tx: tx, context: context, name: "recent_failures"}).put("all", data, nil)
}

// Explicit snapshots are for migration, backup validation and diagnostics.
// Request handlers never materialize all collections.
func materializeRecordState(state databaseState) (databaseState, error) {
	state.recentFailureRecords()
	data, err := json.Marshal(state)
	if err != nil {
		return databaseState{}, err
	}
	return decodeStateJSON(data)
}

// Schema migrations may add or remove typed collections. Export the previous
// record layout without binding it to the new collection catalog.
func exportRecordJSON(tx *bolt.Tx) ([]byte, error) {
	root := tx.Bucket(recordsBucket)
	if root == nil {
		return nil, errors.New("record storage is missing")
	}
	document := map[string]json.RawMessage{}
	err := root.ForEach(func(name, value []byte) error {
		if value != nil {
			return errors.New("unexpected record root value")
		}
		bucket := root.Bucket(name)
		if string(name) == "recent_failures" {
			document[string(name)] = bytes.Clone(bucket.Get([]byte("all")))
			return nil
		}
		rows := map[string]json.RawMessage{}
		if err := bucket.ForEach(func(key, data []byte) error {
			if data == nil {
				return errors.New("unexpected nested records")
			}
			rows[string(key)] = bytes.Clone(data)
			return nil
		}); err != nil {
			return err
		}
		data, err := json.Marshal(rows)
		document[string(name)] = data
		return err
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(document)
}

// Restore validates records one at a time, so validation does not retain the
// complete normalized history in memory. Missing and orphan secondary keys are
// both rejected: every expected key must exist and entry counts must match.
func validateRecordSnapshot(tx *bolt.Tx) error {
	context := &recordTransaction{active: true}
	defer func() { context.active = false }()
	_, tables, err := bindRecordState(tx, context)
	if err != nil {
		return err
	}
	tables["recent_failures"] = nil
	for name, table := range tables {
		source := &boltRecordSource{tx: tx, context: context, name: name}
		expected := map[string]int{}
		indexRoot := tx.Bucket(recordIndexesBucket).Bucket([]byte(name))
		count := 0
		err := source.scan(func(key string, data []byte) error {
			count++
			if table == nil {
				if key != "all" {
					return errors.New("invalid failure record key")
				}
				var failures []projectFailureRecord
				if err := json.Unmarshal(data, &failures); err != nil {
					return err
				}
			} else if err := table.validate(data); err != nil {
				return err
			}
			var row any
			if err := json.Unmarshal(data, &row); err != nil {
				return err
			}
			for index, values := range rawRecordIndexValues(key, row) {
				seen := map[string]bool{}
				for _, value := range values {
					if value == "" || seen[value] {
						continue
					}
					seen[value] = true
					expected[index]++
					if indexRoot == nil || indexRoot.Bucket([]byte(index)) == nil || indexRoot.Bucket([]byte(index)).Get(append(recordIndexPrefix(value), key...)) == nil {
						return fmt.Errorf("missing secondary index: %s/%s", name, index)
					}
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("validate collection %s: %w", name, err)
		}
		n, err := source.count()
		if err != nil {
			return err
		}
		if n != count || name == "recent_failures" && count != 1 {
			return fmt.Errorf("record count mismatch: %s", name)
		}
		if indexRoot != nil {
			if err := indexRoot.ForEach(func(index, data []byte) error {
				if data != nil {
					return errors.New("unexpected secondary index value")
				}
				actual := 0
				if err := indexRoot.Bucket(index).ForEach(func(_, value []byte) error {
					if value == nil {
						return errors.New("unexpected nested secondary index")
					}
					actual++
					return nil
				}); err != nil {
					return err
				}
				if actual != expected[string(index)] {
					return fmt.Errorf("secondary index count mismatch: %s/%s", name, index)
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}
	return context.err
}
