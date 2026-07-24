package session

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const maxThreadKeyLength = 512

type ThreadKey string

func ParseThreadKey(raw string) (ThreadKey, error) {
	if raw == "" {
		return "", errors.New("thread key is required")
	}
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		return "", errors.New("thread key must be namespaced text, not raw JSON")
	}
	if len(raw) > maxThreadKeyLength {
		return "", fmt.Errorf("thread key must be %d characters or fewer", maxThreadKeyLength)
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", errors.New("thread key must not contain control characters")
		}
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 2 {
		return "", errors.New("thread key must be namespaced")
	}
	for _, part := range parts {
		if part == "" {
			return "", errors.New("thread key parts must be non-empty")
		}
	}
	return ThreadKey(raw), nil
}

func (k ThreadKey) String() string { return string(k) }

type HarnessType string

const (
	HarnessCodex      HarnessType = "codex"
	HarnessAmp        HarnessType = "amp"
	HarnessClaudeCode HarnessType = "claudecode"
)

func ParseHarnessType(raw string) (HarnessType, error) {
	switch HarnessType(raw) {
	case HarnessCodex, HarnessAmp, HarnessClaudeCode:
		return HarnessType(raw), nil
	default:
		return "", fmt.Errorf("unsupported harness type %q", raw)
	}
}

func (h HarnessType) String() string { return string(h) }

type RepoCacheAccess string

const (
	RepoCacheAll    RepoCacheAccess = ""
	RepoCachePublic RepoCacheAccess = "public"
	RepoCacheNone   RepoCacheAccess = "none"
)

func (a RepoCacheAccess) Enabled() bool { return a != RepoCacheNone }

type SandboxCapabilities struct {
	RepoCache            RepoCacheAccess
	ObservabilityEnabled bool
	APIServerEnabled     bool
}

type ExecutionStatus string

const (
	ExecutionQueued    ExecutionStatus = "queued"
	ExecutionRunning   ExecutionStatus = "running"
	ExecutionCompleted ExecutionStatus = "completed"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionCancelled ExecutionStatus = "cancelled"
)

func (s ExecutionStatus) String() string { return string(s) }

type Session struct {
	ThreadKey   ThreadKey
	HarnessType HarnessType
	SandboxID   *string
	Metadata    map[string]any
}

type Execution struct {
	ExecutionID string
	Status      ExecutionStatus
	Metadata    map[string]any
}

type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

type MessageInput struct {
	Role  MessageRole
	Parts []map[string]any
}

type notFoundError struct {
	key string
}

func (e *notFoundError) Error() string {
	return fmt.Sprintf("session %s not found", e.key)
}

func NewNotFoundError(key string) error {
	return &notFoundError{key: key}
}
