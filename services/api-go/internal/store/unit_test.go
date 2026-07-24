// Package store_test — idle-candidate & notification payload unit tests.
//
// Derived from:
//   services/api-rs/crates/centaur-session-sqlx/src/lib.rs (§5 of SPEC.md)
//
// ALL ASSERTIONS ARE FIXED.
package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leowmjw/centaur/api-go/internal/store"
)

// ── Idle candidate from row ───────────────────────────────────────────────────

func TestIdleCandidate_UsesPersistedTimeoutDeadline(t *testing.T) {
	now := time.Now().UTC()
	// Execution completed 2 seconds ago; idle_timeout_ms = 1000 ms → already elapsed.
	row := store.IdleSandboxCandidateRow{
		ThreadKey:   "test:idle-row",
		SandboxID:   "sbx-idle-row",
		ExecutionID: "exe-idle-row",
		CompletedAt: now.Add(-2 * time.Second),
		Metadata:    map[string]any{"idle_timeout_ms": float64(1000)},
	}

	candidate, err := store.IdleCandidateFromRow(row, 3600*time.Second, now)
	require.NoError(t, err)
	require.NotNil(t, candidate)
	assert.Equal(t, time.Second, candidate.IdleTimeout)
}

func TestIdleCandidate_WaitsForPersistedTimeoutEvenWhenBackstopElapsed(t *testing.T) {
	now := time.Now().UTC()
	// idle_timeout_ms = 10000 ms; only 2 s have passed → not yet elapsed.
	row := store.IdleSandboxCandidateRow{
		ThreadKey:   "test:idle-row",
		SandboxID:   "sbx-idle-row",
		ExecutionID: "exe-idle-row",
		CompletedAt: now.Add(-2 * time.Second),
		Metadata:    map[string]any{"idle_timeout_ms": float64(10_000)},
	}

	// backstop = 1 s (already elapsed), but persisted timeout = 10 s (not yet elapsed).
	candidate, err := store.IdleCandidateFromRow(row, time.Second, now)
	require.NoError(t, err)
	assert.Nil(t, candidate, "must not return candidate when persisted timeout has not elapsed")
}

func TestIdleCandidate_FallsBackToBackstopForMissingOrInvalidTimeout(t *testing.T) {
	now := time.Now().UTC()
	row := store.IdleSandboxCandidateRow{
		ThreadKey:   "test:idle-row",
		SandboxID:   "sbx-idle-row",
		ExecutionID: "exe-idle-row",
		CompletedAt: now.Add(-2 * time.Second),
		Metadata:    map[string]any{"idle_timeout_ms": "not-a-number"},
	}

	candidate, err := store.IdleCandidateFromRow(row, time.Second, now)
	require.NoError(t, err)
	require.NotNil(t, candidate, "backstop elapsed — must return candidate")
	assert.Equal(t, time.Second, candidate.IdleTimeout)
}

// ── Notification payload ──────────────────────────────────────────────────────

func TestSessionEventNotification_ParsesPayload(t *testing.T) {
	notif, err := store.ParseSessionEventNotification(`{"thread_key":"cli:test","event_id":42}`)
	require.NoError(t, err)
	assert.Equal(t, "cli:test", notif.ThreadKey)
	assert.Equal(t, int64(42), notif.EventID)
}
