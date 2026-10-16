package conversation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xfey/coconet/packages/contracts"
)

const Format = 1
const BlockBytes = 128 << 10
const MaxEntryBytes = 16 << 20

// Entry preserves a stable cumulative source anchor. A present Visible value
// (including an empty string) is authoritative; nil denotes legacy evidence.
type Entry struct {
	Turn      contracts.NormalizedTurn `json:"turn"`
	Visible   *string                  `json:"visible,omitempty"`
	Inherited bool                     `json:"inherited,omitempty"`
	// Ranges retain native visibility validation during ingestion. Visible is
	// the portable rendered view; neither requires an older snapshot row.
	Ranges []contracts.TextRange `json:"ranges,omitempty"`
}

type Block = contracts.ConversationBlock
type Manifest = contracts.ConversationManifest

type Prepared struct {
	Manifest Manifest
	Objects  map[string][]byte
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func manifestHash(m Manifest) string {
	m.SHA256 = ""
	data, _ := json.Marshal(m)
	return Digest(data)
}

// Build shares all complete prefix blocks across append-only observations.
// A manifest contains no snapshot IDs or base-version links.
func Build(entries []Entry) (Prepared, error) {
	result := Prepared{Manifest: Manifest{Format: Format, Blocks: []Block{}}, Objects: map[string][]byte{}}
	var block bytes.Buffer
	first, count := 0, 0
	flush := func() {
		if count == 0 {
			return
		}
		data := bytes.Clone(block.Bytes())
		hash := Digest(data)
		result.Objects[hash] = data
		result.Manifest.Blocks = append(result.Manifest.Blocks, Block{SHA256: hash, Size: len(data), First: first, Count: count})
		first += count
		count = 0
		block.Reset()
	}
	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		if entry.Turn.TurnID == "" || entry.Turn.Ordinal != i || seen[entry.Turn.TurnID] {
			return Prepared{}, errors.New("conversation entry identity or ordinal is invalid")
		}
		seen[entry.Turn.TurnID] = true
		data, err := json.Marshal(entry)
		if err != nil {
			return Prepared{}, err
		}
		if len(data)+1 > MaxEntryBytes {
			return Prepared{}, errors.New("conversation entry exceeds content limit")
		}
		if block.Len()+len(data)+1 > BlockBytes {
			flush()
		}
		block.Write(data)
		block.WriteByte('\n')
		count++
	}
	flush()
	result.Manifest.Count = len(entries)
	result.Manifest.SHA256 = manifestHash(result.Manifest)
	return result, nil
}

func Validate(m Manifest) error {
	if m.Format != Format || m.Count < 0 || m.Blocks == nil || m.SHA256 != manifestHash(m) {
		return errors.New("conversation manifest format or hash is invalid")
	}
	count := 0
	for _, b := range m.Blocks {
		hash, err := hex.DecodeString(b.SHA256)
		if err != nil || len(hash) != sha256.Size || b.First != count || b.Count < 1 || b.Size < 1 || b.Size > MaxEntryBytes || b.Count > b.Size {
			return errors.New("conversation block descriptor is invalid")
		}
		if b.Count > m.Count-count {
			return errors.New("conversation block count exceeds manifest")
		}
		count += b.Count
	}
	if count != m.Count {
		return errors.New("conversation manifest count is inconsistent")
	}
	return nil
}

// Read fetches only blocks overlapping [start,end), and verifies complete block
// hashes before returning any entries. The callback must bound object reads by Size.
func Read(m Manifest, start, end int, fetch func(Block) ([]byte, error)) ([]Entry, error) {
	if err := Validate(m); err != nil {
		return nil, err
	}
	if start < 0 || end < start || end > m.Count {
		return nil, errors.New("invalid conversation range")
	}
	result := make([]Entry, 0, end-start)
	seen := map[string]bool{}
	for _, b := range m.Blocks {
		if b.First >= end || b.First+b.Count <= start {
			continue
		}
		data, err := fetch(b)
		if err != nil {
			return nil, err
		}
		if len(data) != b.Size || Digest(data) != b.SHA256 {
			return nil, errors.New("conversation block failed integrity verification")
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			return nil, errors.New("conversation block is incomplete")
		}
		lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
		if len(lines) != b.Count {
			return nil, errors.New("conversation block count is inconsistent")
		}
		for offset, line := range lines {
			var entry Entry
			if err := json.Unmarshal(line, &entry); err != nil {
				return nil, fmt.Errorf("decode conversation entry: %w", err)
			}
			ordinal := b.First + offset
			if entry.Turn.Ordinal != ordinal || entry.Turn.TurnID == "" || seen[entry.Turn.TurnID] {
				return nil, errors.New("conversation entry identity is inconsistent")
			}
			seen[entry.Turn.TurnID] = true
			if ordinal >= start && ordinal < end {
				result = append(result, entry)
			}
		}
	}
	return result, nil
}

// Walk verifies one block at a time and visits its entries without retaining
// the full conversation in memory. Used when materializing local downloads.
func Walk(m Manifest, fetch func(Block) ([]byte, error), visit func(Entry) error) error {
	if err := Validate(m); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, b := range m.Blocks {
		data, err := fetch(b)
		if err != nil {
			return err
		}
		if len(data) != b.Size || Digest(data) != b.SHA256 || data[len(data)-1] != '\n' {
			return errors.New("conversation block failed integrity verification")
		}
		lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
		if len(lines) != b.Count {
			return errors.New("conversation block count is inconsistent")
		}
		for i, line := range lines {
			var entry Entry
			if err := json.Unmarshal(line, &entry); err != nil {
				return err
			}
			if entry.Turn.Ordinal != b.First+i || entry.Turn.TurnID == "" || seen[entry.Turn.TurnID] {
				return errors.New("conversation entry identity is inconsistent")
			}
			seen[entry.Turn.TurnID] = true
			if err := visit(entry); err != nil {
				return err
			}
		}
	}
	return nil
}
