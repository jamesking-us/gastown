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
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
)

var ErrControllerLeaseHeld = errors.New("execution controller lease is held")

type ControllerLease struct {
	SchemaVersion  int                          `json:"schema_version"`
	ControllerID   string                       `json:"controller_id,omitempty"`
	Epoch          uint64                       `json:"epoch"`
	Revision       uint64                       `json:"revision"`
	AcquiredAt     time.Time                    `json:"acquired_at"`
	UpdatedAt      time.Time                    `json:"updated_at"`
	LeaseExpiresAt time.Time                    `json:"lease_expires_at"`
	Receipts       map[string]ControllerReceipt `json:"receipts,omitempty"`
}

type ControllerReceipt struct {
	Operation    string `json:"operation"`
	Fingerprint  string `json:"fingerprint"`
	Revision     uint64 `json:"revision"`
	ControllerID string `json:"controller_id"`
	Epoch        uint64 `json:"epoch"`
}

type ControllerCommand struct {
	Operation      string    `json:"operation"`
	ControllerID   string    `json:"controller_id"`
	ExpectedEpoch  uint64    `json:"expected_epoch,omitempty"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
	IdempotencyKey string    `json:"idempotency_key"`
	Actor          string    `json:"actor,omitempty"`
	At             time.Time `json:"at"`
}

type ControllerEvent struct {
	SchemaVersion  int             `json:"schema_version"`
	Sequence       uint64          `json:"sequence"`
	Timestamp      time.Time       `json:"timestamp"`
	Operation      string          `json:"operation"`
	IdempotencyKey string          `json:"idempotency_key"`
	Actor          string          `json:"actor,omitempty"`
	PreviousHash   string          `json:"previous_hash,omitempty"`
	Hash           string          `json:"hash"`
	Lease          ControllerLease `json:"lease"`
}

type ControllerVerification struct {
	Events   uint64 `json:"events"`
	Epoch    uint64 `json:"epoch"`
	Revision uint64 `json:"revision"`
	LastHash string `json:"last_hash"`
}

func (lease ControllerLease) Active(at time.Time) bool {
	at = normalizeTime(at)
	return lease.ControllerID != "" && lease.LeaseExpiresAt.After(at)
}

func (s *Store) ApplyController(command ControllerCommand) (*ControllerLease, error) {
	var result *ControllerLease
	err := s.withLock(func() error {
		current, events, err := readControllerJournal(s.controllerJournalPath())
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if errors.Is(err, ErrNotFound) {
			current = nil
			events = nil
		}
		next, changed, err := applyController(current, command)
		if err != nil {
			return err
		}
		if changed {
			if err := s.appendControllerAndSnapshot(next, command, events); err != nil {
				return err
			}
		}
		result = cloneController(next)
		return nil
	})
	return result, err
}

func (s *Store) LoadController() (*ControllerLease, error) {
	var result *ControllerLease
	err := s.withLock(func() error {
		lease, _, err := readControllerJournal(s.controllerJournalPath())
		if err != nil {
			return err
		}
		result = cloneController(lease)
		return atomicfile.EnsureDirAndWriteJSON(s.controllerSnapshotPath(), result)
	})
	return result, err
}

func (s *Store) ControllerEvents() ([]ControllerEvent, error) {
	var result []ControllerEvent
	err := s.withLock(func() error {
		_, events, err := readControllerJournal(s.controllerJournalPath())
		if err != nil {
			return err
		}
		result = append([]ControllerEvent(nil), events...)
		return nil
	})
	return result, err
}

func (s *Store) VerifyController() (*ControllerVerification, error) {
	events, err := s.ControllerEvents()
	if err != nil {
		return nil, err
	}
	last := events[len(events)-1]
	return &ControllerVerification{
		Events: uint64(len(events)), Epoch: last.Lease.Epoch,
		Revision: last.Lease.Revision, LastHash: last.Hash,
	}, nil
}

func applyController(current *ControllerLease, command ControllerCommand) (*ControllerLease, bool, error) {
	command.Operation = strings.ToLower(strings.TrimSpace(command.Operation))
	command.ControllerID = strings.TrimSpace(command.ControllerID)
	command.At = normalizeTime(command.At)
	command.LeaseExpiresAt = command.LeaseExpiresAt.UTC()
	if err := validateID("controller id", command.ControllerID); err != nil {
		return nil, false, err
	}
	if err := validateID("idempotency key", command.IdempotencyKey); err != nil {
		return nil, false, err
	}
	fp, err := controllerFingerprint(command)
	if err != nil {
		return nil, false, err
	}
	if current != nil {
		if receipt, ok := current.Receipts[command.IdempotencyKey]; ok {
			if receipt.Operation != "controller-"+command.Operation || receipt.Fingerprint != fp {
				return nil, false, fmt.Errorf("%w: %q", ErrIdempotencyConflict, command.IdempotencyKey)
			}
			if current.Epoch != receipt.Epoch ||
				(command.Operation != "release" && current.ControllerID != receipt.ControllerID) ||
				(command.Operation == "release" && current.ControllerID != "") {
				return nil, false, fmt.Errorf("%w: idempotent controller result has been superseded", ErrFenceMismatch)
			}
			return cloneController(current), false, nil
		}
	}

	next := cloneController(current)
	if next == nil {
		next = &ControllerLease{SchemaVersion: SchemaVersion, Receipts: make(map[string]ControllerReceipt)}
	}
	switch command.Operation {
	case "acquire":
		if !command.LeaseExpiresAt.After(command.At) {
			return nil, false, fmt.Errorf("controller lease expiry must be after command time")
		}
		if current != nil && current.Active(command.At) {
			return nil, false, fmt.Errorf("%w by %s through %s", ErrControllerLeaseHeld, current.ControllerID, current.LeaseExpiresAt.Format(time.RFC3339))
		}
		next.Epoch++
		next.ControllerID = command.ControllerID
		next.AcquiredAt = command.At
		next.LeaseExpiresAt = command.LeaseExpiresAt
	case "renew":
		if current == nil || !current.Active(command.At) || current.ControllerID != command.ControllerID || current.Epoch != command.ExpectedEpoch {
			return nil, false, fmt.Errorf("%w: active controller is %s epoch %d", ErrFenceMismatch, next.ControllerID, next.Epoch)
		}
		if !command.LeaseExpiresAt.After(current.LeaseExpiresAt) {
			return nil, false, fmt.Errorf("renewal must extend the current controller lease")
		}
		next.LeaseExpiresAt = command.LeaseExpiresAt
	case "release":
		if current == nil || current.ControllerID != command.ControllerID || current.Epoch != command.ExpectedEpoch {
			return nil, false, fmt.Errorf("%w: active controller is %s epoch %d", ErrFenceMismatch, next.ControllerID, next.Epoch)
		}
		next.ControllerID = ""
		next.LeaseExpiresAt = command.At
	default:
		return nil, false, fmt.Errorf("unsupported controller operation %q", command.Operation)
	}
	next.Revision++
	next.UpdatedAt = command.At
	if next.Receipts == nil {
		next.Receipts = make(map[string]ControllerReceipt)
	}
	next.Receipts[command.IdempotencyKey] = ControllerReceipt{
		Operation: "controller-" + command.Operation, Fingerprint: fp, Revision: next.Revision,
		ControllerID: command.ControllerID, Epoch: next.Epoch,
	}
	return next, true, nil
}

func controllerFingerprint(command ControllerCommand) (string, error) {
	command.At = time.Time{}
	return fingerprint(command)
}

func cloneController(value *ControllerLease) *ControllerLease {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Receipts = make(map[string]ControllerReceipt, len(value.Receipts))
	for key, receipt := range value.Receipts {
		copy.Receipts[key] = receipt
	}
	return &copy
}

func (s *Store) appendControllerAndSnapshot(lease *ControllerLease, command ControllerCommand, prior []ControllerEvent) error {
	path := s.controllerJournalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating controller journal directory: %w", err)
	}
	event := ControllerEvent{
		SchemaVersion: SchemaVersion, Sequence: uint64(len(prior) + 1), Timestamp: lease.UpdatedAt,
		Operation: command.Operation, IdempotencyKey: command.IdempotencyKey, Actor: command.Actor,
		Lease: *cloneController(lease),
	}
	if len(prior) > 0 {
		event.PreviousHash = prior[len(prior)-1].Hash
	}
	hash, err := hashControllerEvent(event)
	if err != nil {
		return err
	}
	event.Hash = hash
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling controller event: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec
	if err != nil {
		return fmt.Errorf("opening controller journal: %w", err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing controller journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("syncing controller journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing controller journal: %w", err)
	}
	if err := atomicfile.EnsureDirAndWriteJSON(s.controllerSnapshotPath(), lease); err != nil {
		return fmt.Errorf("writing controller snapshot: %w", err)
	}
	return nil
}

func readControllerJournal(path string) (*ControllerLease, []ControllerEvent, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("opening controller journal: %w", err)
	}
	defer file.Close() //nolint:errcheck

	var events []ControllerEvent
	reader := bufio.NewReader(file)
	previousHash := ""
	for lineNumber := 1; ; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var event ControllerEvent
			if err := json.Unmarshal(line, &event); err != nil {
				return nil, nil, fmt.Errorf("parsing controller journal line %d: %w", lineNumber, err)
			}
			if event.Sequence != uint64(lineNumber) || event.PreviousHash != previousHash {
				return nil, nil, fmt.Errorf("controller journal chain broken at line %d", lineNumber)
			}
			hash, err := hashControllerEvent(event)
			if err != nil {
				return nil, nil, err
			}
			if event.Hash != hash {
				return nil, nil, fmt.Errorf("controller journal hash mismatch at line %d", lineNumber)
			}
			previousHash = event.Hash
			events = append(events, event)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, nil, fmt.Errorf("reading controller journal: %w", readErr)
		}
	}
	if len(events) == 0 {
		return nil, nil, ErrNotFound
	}
	return cloneController(&events[len(events)-1].Lease), events, nil
}

func hashControllerEvent(event ControllerEvent) (string, error) {
	event.Hash = ""
	data, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshaling controller event for hashing: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) controllerJournalPath() string {
	return filepath.Join(s.root, "controller", "journal.jsonl")
}

func (s *Store) controllerSnapshotPath() string {
	return filepath.Join(s.root, "controller", "record.json")
}
