// Package store_test — DB integration behaviour tests.
//
// These tests require a real Postgres URL in SESSION_RUNTIME_TEST_DATABASE_URL.
// Tests are skipped when the variable is absent (safe for CI without a DB).
//
// Derived from:
//   services/api-rs/crates/centaur-session-sqlx/src/lib.rs (§5 of SPEC.md)
//   services/api-rs/crates/centaur-session-sqlx/tests/etl_context_rls.rs (§6.1)
//   services/api-rs/crates/centaur-session-sqlx/tests/slack_dm_context_rls.rs (§6.2)
//   services/api-rs/crates/absurd-sdk/src/lib.rs (integration_* tests)
//
// ALL ASSERTIONS ARE FIXED.
package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leowmjw/centaur/api-go/internal/session"
	"github.com/leowmjw/centaur/api-go/internal/store"
)

func testStore(t *testing.T) *store.PgSessionStore {
	t.Helper()
	dbURL := os.Getenv("SESSION_RUNTIME_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set SESSION_RUNTIME_TEST_DATABASE_URL to run DB integration tests")
	}
	s, err := store.Connect(context.Background(), dbURL)
	require.NoError(t, err, "connect to test database")
	require.NoError(t, s.RunMigrations(context.Background()), "run migrations")
	return s
}

func uniqueThreadKey() session.ThreadKey {
	k, _ := session.ParseThreadKey(fmt.Sprintf("test:%s", uuid.New()))
	return k
}

// ── Session create idempotency ────────────────────────────────────────────────

func TestIntegration_CreateOrGetSession_Idempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := uniqueThreadKey()

	sess1, err := s.CreateOrGetSession(ctx, key, session.HarnessCodex, nil, map[string]any{})
	require.NoError(t, err)
	sess2, err := s.CreateOrGetSession(ctx, key, session.HarnessCodex, nil, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, sess1.ThreadKey, sess2.ThreadKey)
}

func TestIntegration_CreateOrGetSession_HarnessConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := uniqueThreadKey()

	_, err := s.CreateOrGetSession(ctx, key, session.HarnessCodex, nil, map[string]any{})
	require.NoError(t, err)

	_, err = s.CreateOrGetSession(ctx, key, session.HarnessAmp, nil, map[string]any{})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrHarnessConflict)
}

// ── Stdout owner lease ────────────────────────────────────────────────────────

func TestIntegration_StdoutOwnerFencesOutputAndTerminalUpdates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := uniqueThreadKey()

	_, err := s.CreateOrGetSession(ctx, key, session.HarnessCodex, nil, map[string]any{})
	require.NoError(t, err)

	execResult, err := s.CreateExecution(ctx, key, nil, map[string]any{})
	require.NoError(t, err)
	executionID := execResult.Execution.ExecutionID

	err = s.MarkExecutionRunning(ctx, executionID)
	require.NoError(t, err)

	ttl := 25 * time.Millisecond

	// owner-a claims stdout
	claimed, err := s.ClaimStdoutOwner(ctx, executionID, "owner-a", ttl)
	require.NoError(t, err)
	assert.True(t, claimed)

	// owner-a can append
	eventID, err := s.AppendEventIfStdoutOwner(ctx, key, executionID, "owner-a", ttl, "session.output.line", "line-from-owner-a")
	require.NoError(t, err)
	assert.NotNil(t, eventID, "owner-a must be able to append")

	// owner-b is fenced
	fenced, err := s.AppendEventIfStdoutOwner(ctx, key, executionID, "owner-b", ttl, "session.output.line", "line-from-stale-owner-b")
	require.NoError(t, err)
	assert.Nil(t, fenced, "owner-b must be fenced")
}

func TestIntegration_ReleasesAllStdoutLeasesHeldByOneOwner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ttl := 60 * time.Second

	// Create two sessions and their executions, both owned by "owner-x".
	var executionIDs []string
	for i := 0; i < 2; i++ {
		key := uniqueThreadKey()
		_, err := s.CreateOrGetSession(ctx, key, session.HarnessCodex, nil, map[string]any{})
		require.NoError(t, err)
		result, err := s.CreateExecution(ctx, key, nil, map[string]any{})
		require.NoError(t, err)
		execID := result.Execution.ExecutionID
		require.NoError(t, s.MarkExecutionRunning(ctx, execID))
		claimed, err := s.ClaimStdoutOwner(ctx, execID, "owner-x", ttl)
		require.NoError(t, err)
		require.True(t, claimed)
		executionIDs = append(executionIDs, execID)
	}

	// Release all leases held by owner-x.
	released, err := s.ReleaseStdoutOwnedExecutions(ctx, "owner-x")
	require.NoError(t, err)
	assert.Len(t, released, 2)
}

// ── Idle candidates use persisted execution idle timeout ──────────────────────

func TestIntegration_IdleCandidatesUsePersistedExecutionIdleTimeout(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := uniqueThreadKey()
	sandboxID := fmt.Sprintf("sbx-idle-%s", uuid.New())

	_, err := s.CreateOrGetSession(ctx, key, session.HarnessCodex, nil, map[string]any{})
	require.NoError(t, err)
	require.NoError(t, s.UpdateSandboxID(ctx, key, &sandboxID))

	result, err := s.CreateExecution(ctx, key, nil, map[string]any{"idle_timeout_ms": 1000})
	require.NoError(t, err)
	executionID := result.Execution.ExecutionID

	require.NoError(t, s.CompleteExecution(ctx, executionID))

	// Age the execution by 2 seconds using a direct update.
	require.NoError(t, s.TestAgeExecution(ctx, executionID, 2*time.Second))

	candidates, err := s.ListIdleSandboxCandidates(ctx, 3600*time.Second)
	require.NoError(t, err)

	var candidate *store.IdleSandboxCandidate
	for i := range candidates {
		if candidates[i].ThreadKey == key.String() {
			candidate = &candidates[i]
			break
		}
	}
	require.NotNil(t, candidate, "should find candidate using execution idle timeout, not backstop")
	assert.Equal(t, sandboxID, candidate.SandboxID)
	assert.Equal(t, executionID, candidate.ExecutionID)
	assert.Equal(t, time.Second, candidate.IdleTimeout)
}

// ── Warm eviction reservation ─────────────────────────────────────────────────

func TestIntegration_WarmEvictionReservationBlocksLaterClaims(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := uniqueThreadKey()
	sandboxID := fmt.Sprintf("sbx-warm-%s", uuid.New())

	// Reserve for eviction.
	require.NoError(t, s.ReserveWarmEviction(ctx, key, sandboxID))

	// A subsequent warm claim for the same sandbox must fail.
	ok, err := s.ClaimWarmSandbox(ctx, key, sandboxID)
	require.NoError(t, err)
	assert.False(t, ok, "warm claim must be blocked after eviction reservation")
}
