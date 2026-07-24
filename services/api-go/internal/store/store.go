package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/leowmjw/centaur/api-go/internal/session"
)

var ErrHarnessConflict = errors.New("session already exists with a different harness type")

type IdleSandboxCandidateRow struct {
	ThreadKey   string
	SandboxID   string
	ExecutionID string
	CompletedAt time.Time
	Metadata    map[string]any
}

type IdleSandboxCandidate struct {
	ThreadKey   string
	SandboxID   string
	ExecutionID string
	IdleTimeout time.Duration
}

type SessionEventNotification struct {
	ThreadKey string `json:"thread_key"`
	EventID   int64  `json:"event_id"`
}

type CreateExecutionResult struct {
	Execution *session.Execution
}

type PgSessionStore struct {
	mu               sync.Mutex
	sessions         map[string]*session.Session
	executions       map[string]*storedExecution
	warmReservations map[string]map[string]bool
	eventCounter     int64
}

type storedExecution struct {
	threadKey   string
	execution   *session.Execution
	completedAt time.Time
	stdoutOwner string
	stdoutLease time.Time
}

func Connect(_ context.Context, _ string) (*PgSessionStore, error) {
	return &PgSessionStore{
		sessions:         make(map[string]*session.Session),
		executions:       make(map[string]*storedExecution),
		warmReservations: make(map[string]map[string]bool),
	}, nil
}

func (s *PgSessionStore) RunMigrations(context.Context) error { return nil }

func (s *PgSessionStore) CreateOrGetSession(_ context.Context, key session.ThreadKey, harness session.HarnessType, sandboxID *string, metadata map[string]any) (*session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.sessions[key.String()]; ok {
		if existing.HarnessType != harness {
			return nil, ErrHarnessConflict
		}
		return cloneSession(existing), nil
	}

	sess := &session.Session{
		ThreadKey:   key,
		HarnessType: harness,
		SandboxID:   cloneStringPtr(sandboxID),
		Metadata:    cloneMap(metadata),
	}
	s.sessions[key.String()] = sess
	return cloneSession(sess), nil
}

func (s *PgSessionStore) CreateExecution(_ context.Context, key session.ThreadKey, _ *string, metadata map[string]any) (*CreateExecutionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	executionID := "exe-" + uuid.NewString()
	exec := &session.Execution{
		ExecutionID: executionID,
		Status:      session.ExecutionQueued,
		Metadata:    cloneMap(metadata),
	}
	s.executions[executionID] = &storedExecution{threadKey: key.String(), execution: exec}
	return &CreateExecutionResult{Execution: cloneExecution(exec)}, nil
}

func (s *PgSessionStore) MarkExecutionRunning(_ context.Context, executionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exe, ok := s.executions[executionID]; ok {
		exe.execution.Status = session.ExecutionRunning
		return nil
	}
	return fmt.Errorf("execution %s not found", executionID)
}

func (s *PgSessionStore) ClaimStdoutOwner(_ context.Context, executionID, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exe, ok := s.executions[executionID]
	if !ok {
		return false, fmt.Errorf("execution %s not found", executionID)
	}
	now := time.Now()
	if exe.stdoutOwner == "" || exe.stdoutOwner == owner || now.After(exe.stdoutLease) {
		exe.stdoutOwner = owner
		exe.stdoutLease = now.Add(ttl)
		return true, nil
	}
	return false, nil
}

func (s *PgSessionStore) AppendEventIfStdoutOwner(_ context.Context, _ session.ThreadKey, executionID, owner string, ttl time.Duration, _ string, _ string) (*int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exe, ok := s.executions[executionID]
	if !ok {
		return nil, fmt.Errorf("execution %s not found", executionID)
	}
	if exe.stdoutOwner != owner || time.Now().After(exe.stdoutLease) {
		return nil, nil
	}
	exe.stdoutLease = time.Now().Add(ttl)
	eventID := atomic.AddInt64(&s.eventCounter, 1)
	return &eventID, nil
}

func (s *PgSessionStore) ReleaseStdoutOwnedExecutions(_ context.Context, owner string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var released []string
	for executionID, exe := range s.executions {
		if exe.stdoutOwner == owner {
			released = append(released, executionID)
			exe.stdoutOwner = ""
			exe.stdoutLease = time.Time{}
		}
	}
	return released, nil
}

func (s *PgSessionStore) UpdateSandboxID(_ context.Context, key session.ThreadKey, sandboxID *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[key.String()]
	if !ok {
		return fmt.Errorf("session %s not found", key)
	}
	sess.SandboxID = cloneStringPtr(sandboxID)
	return nil
}

func (s *PgSessionStore) CompleteExecution(_ context.Context, executionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	exe, ok := s.executions[executionID]
	if !ok {
		return fmt.Errorf("execution %s not found", executionID)
	}
	exe.execution.Status = session.ExecutionCompleted
	exe.completedAt = time.Now().UTC()
	return nil
}

func (s *PgSessionStore) TestAgeExecution(_ context.Context, executionID string, age time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	exe, ok := s.executions[executionID]
	if !ok {
		return fmt.Errorf("execution %s not found", executionID)
	}
	exe.completedAt = time.Now().UTC().Add(-age)
	return nil
}

func (s *PgSessionStore) ListIdleSandboxCandidates(_ context.Context, backstop time.Duration) ([]IdleSandboxCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	var candidates []IdleSandboxCandidate
	for _, exe := range s.executions {
		if exe.execution.Status != session.ExecutionCompleted || exe.completedAt.IsZero() {
			continue
		}
		sess, ok := s.sessions[exe.threadKey]
		if !ok || sess.SandboxID == nil || *sess.SandboxID == "" {
			continue
		}
		candidate, err := IdleCandidateFromRow(IdleSandboxCandidateRow{
			ThreadKey:   exe.threadKey,
			SandboxID:   *sess.SandboxID,
			ExecutionID: exe.execution.ExecutionID,
			CompletedAt: exe.completedAt,
			Metadata:    cloneMap(exe.execution.Metadata),
		}, backstop, now)
		if err != nil {
			return nil, err
		}
		if candidate != nil {
			candidates = append(candidates, *candidate)
		}
	}
	return candidates, nil
}

func (s *PgSessionStore) ReserveWarmEviction(_ context.Context, key session.ThreadKey, sandboxID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.warmReservations[key.String()] == nil {
		s.warmReservations[key.String()] = make(map[string]bool)
	}
	s.warmReservations[key.String()][sandboxID] = true
	return nil
}

func (s *PgSessionStore) ClaimWarmSandbox(_ context.Context, key session.ThreadKey, sandboxID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.warmReservations[key.String()][sandboxID], nil
}

func IdleCandidateFromRow(row IdleSandboxCandidateRow, backstop time.Duration, now time.Time) (*IdleSandboxCandidate, error) {
	idleTimeout, ok := parseIdleTimeout(row.Metadata)
	if !ok {
		idleTimeout = backstop
	}
	if now.Sub(row.CompletedAt) < idleTimeout {
		return nil, nil
	}
	return &IdleSandboxCandidate{
		ThreadKey:   row.ThreadKey,
		SandboxID:   row.SandboxID,
		ExecutionID: row.ExecutionID,
		IdleTimeout: idleTimeout,
	}, nil
}

func ParseSessionEventNotification(raw string) (*SessionEventNotification, error) {
	var notification SessionEventNotification
	if err := json.Unmarshal([]byte(raw), &notification); err != nil {
		return nil, err
	}
	return &notification, nil
}

func parseIdleTimeout(metadata map[string]any) (time.Duration, bool) {
	if metadata == nil {
		return 0, false
	}
	raw, ok := metadata["idle_timeout_ms"]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return time.Duration(v) * time.Millisecond, true
	case float32:
		return time.Duration(v) * time.Millisecond, true
	case int:
		return time.Duration(v) * time.Millisecond, true
	case int64:
		return time.Duration(v) * time.Millisecond, true
	case uint64:
		return time.Duration(v) * time.Millisecond, true
	default:
		return 0, false
	}
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneStringPtr(src *string) *string {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func cloneSession(src *session.Session) *session.Session {
	if src == nil {
		return nil
	}
	return &session.Session{
		ThreadKey:   src.ThreadKey,
		HarnessType: src.HarnessType,
		SandboxID:   cloneStringPtr(src.SandboxID),
		Metadata:    cloneMap(src.Metadata),
	}
}

func cloneExecution(src *session.Execution) *session.Execution {
	if src == nil {
		return nil
	}
	return &session.Execution{
		ExecutionID: src.ExecutionID,
		Status:      src.Status,
		Metadata:    cloneMap(src.Metadata),
	}
}
