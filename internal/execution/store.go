package execution

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/atomicfile"
)

const directoryName = "executions"

// Store persists execution journals beneath the town runtime directory.
type Store struct {
	root string
}

func NewStore(townRoot string) *Store {
	return &Store{root: filepath.Join(townRoot, ".runtime", directoryName)}
}

func (s *Store) Create(req CreateRequest) (*Record, error) {
	var result *Record
	err := s.withLock(func() error {
		if existing, _, err := s.loadUnlocked(req.WorkID); err == nil {
			fp, fingerprintErr := createFingerprint(req)
			if fingerprintErr != nil {
				return fingerprintErr
			}
			if ok, matchErr := receiptMatches(existing, req.IdempotencyKey, "create", fp); matchErr != nil {
				return matchErr
			} else if ok {
				result = cloneRecord(existing)
				return nil
			}
			return ErrAlreadyExists
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		record, _, err := NewRecord(req)
		if err != nil {
			return err
		}
		if err := s.appendAndSnapshot(record, "create", req.IdempotencyKey, req.Actor); err != nil {
			return err
		}
		result = cloneRecord(record)
		return nil
	})
	return result, err
}

func (s *Store) Apply(workID string, command Command) (*Record, error) {
	var result *Record
	err := s.withLock(func() error {
		record, _, err := s.loadUnlocked(workID)
		if err != nil {
			return err
		}
		next, changed, err := Apply(record, command)
		if err != nil {
			return err
		}
		if changed {
			if err := s.appendAndSnapshot(next, command.Operation, command.IdempotencyKey, command.Actor); err != nil {
				return err
			}
		}
		result = cloneRecord(next)
		return nil
	})
	return result, err
}

// Load replays the authoritative journal and repairs a stale snapshot.
func (s *Store) Load(workID string) (*Record, error) {
	var result *Record
	err := s.withLock(func() error {
		record, _, err := s.loadUnlocked(workID)
		if err != nil {
			return err
		}
		if err := atomicfile.EnsureDirAndWriteJSON(s.snapshotPath(workID), record); err != nil {
			return fmt.Errorf("repairing execution snapshot: %w", err)
		}
		result = cloneRecord(record)
		return nil
	})
	return result, err
}

func (s *Store) Events(workID string) ([]Event, error) {
	var result []Event
	err := s.withLock(func() error {
		_, events, err := s.loadUnlocked(workID)
		if err != nil {
			return err
		}
		result = events
		return nil
	})
	return result, err
}

func (s *Store) Verify(workID string) (*Verification, error) {
	events, err := s.Events(workID)
	if err != nil {
		return nil, err
	}
	last := events[len(events)-1]
	return &Verification{WorkID: workID, Events: uint64(len(events)), LastHash: last.Hash, Revision: last.Record.Revision}, nil
}

// List replays every journal so a crash between journal sync and snapshot
// update cannot hide a record.
func (s *Store) List() ([]Record, error) {
	var records []Record
	err := s.withLock(func() error {
		paths, err := filepath.Glob(filepath.Join(s.root, "journals", "*.jsonl"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			record, _, err := readJournal(path, "")
			if err != nil {
				return err
			}
			records = append(records, *record)
		}
		sort.Slice(records, func(i, j int) bool { return records[i].WorkID < records[j].WorkID })
		return nil
	})
	return records, err
}

func (s *Store) withLock(fn func() error) error {
	if err := os.MkdirAll(s.root, 0755); err != nil {
		return fmt.Errorf("creating execution directory: %w", err)
	}
	lock := flock.New(filepath.Join(s.root, "store.lock"))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("locking execution store: %w", err)
	}
	defer lock.Unlock() //nolint:errcheck
	return fn()
}

func (s *Store) loadUnlocked(workID string) (*Record, []Event, error) {
	if err := validateID("work id", workID); err != nil {
		return nil, nil, err
	}
	return readJournal(s.journalPath(workID), workID)
}

func (s *Store) appendAndSnapshot(record *Record, operation, idempotencyKey, actor string) error {
	journalPath := s.journalPath(record.WorkID)
	if err := os.MkdirAll(filepath.Dir(journalPath), 0755); err != nil {
		return fmt.Errorf("creating execution journal directory: %w", err)
	}
	_, prior, err := readJournal(journalPath, record.WorkID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	event := Event{
		SchemaVersion:  SchemaVersion,
		Sequence:       uint64(len(prior) + 1),
		Timestamp:      record.UpdatedAt,
		WorkID:         record.WorkID,
		Operation:      operation,
		IdempotencyKey: idempotencyKey,
		Actor:          actor,
		Record:         *cloneRecord(record),
	}
	if len(prior) > 0 {
		event.PreviousHash = prior[len(prior)-1].Hash
	}
	hash, err := hashEvent(event)
	if err != nil {
		return err
	}
	event.Hash = hash
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling execution event: %w", err)
	}
	file, err := os.OpenFile(journalPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec
	if err != nil {
		return fmt.Errorf("opening execution journal: %w", err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing execution journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("syncing execution journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing execution journal: %w", err)
	}
	if err := atomicfile.EnsureDirAndWriteJSON(s.snapshotPath(record.WorkID), record); err != nil {
		return fmt.Errorf("writing execution snapshot: %w", err)
	}
	return nil
}

func readJournal(path, expectedWorkID string) (*Record, []Event, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("opening execution journal: %w", err)
	}
	defer file.Close() //nolint:errcheck

	var events []Event
	reader := bufio.NewReader(file)
	lineNumber := 0
	previousHash := ""
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			lineNumber++
			var event Event
			if err := json.Unmarshal(line, &event); err != nil {
				return nil, nil, fmt.Errorf("parsing execution journal line %d: %w", lineNumber, err)
			}
			if expectedWorkID != "" && event.WorkID != expectedWorkID {
				return nil, nil, fmt.Errorf("execution journal work id mismatch: expected %q, got %q", expectedWorkID, event.WorkID)
			}
			if event.Sequence != uint64(lineNumber) || event.PreviousHash != previousHash {
				return nil, nil, fmt.Errorf("execution journal chain broken at line %d", lineNumber)
			}
			hash, err := hashEvent(event)
			if err != nil {
				return nil, nil, err
			}
			if event.Hash != hash {
				return nil, nil, fmt.Errorf("execution journal hash mismatch at line %d", lineNumber)
			}
			previousHash = event.Hash
			events = append(events, event)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, nil, fmt.Errorf("reading execution journal: %w", readErr)
		}
	}
	if len(events) == 0 {
		return nil, nil, ErrNotFound
	}
	return cloneRecord(&events[len(events)-1].Record), events, nil
}

func hashEvent(event Event) (string, error) {
	event.Hash = ""
	data, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshaling execution event for hashing: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) journalPath(workID string) string {
	return filepath.Join(s.root, "journals", storageName(workID)+".jsonl")
}

func (s *Store) snapshotPath(workID string) string {
	return filepath.Join(s.root, "records", storageName(workID)+".json")
}

func storageName(workID string) string {
	sum := sha256.Sum256([]byte(workID))
	return hex.EncodeToString(sum[:])
}
