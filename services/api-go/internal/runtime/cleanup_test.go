// Package runtime_test — orphan-reap behaviour tests.
//
// Derived from:
//   services/api-rs/crates/centaur-session-runtime/src/cleanup.rs (§8 of SPEC.md)
//
// ALL ASSERTIONS ARE FIXED.
package runtime_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leowmjw/centaur/api-go/internal/runtime"
	"github.com/leowmjw/centaur/api-go/internal/sandbox"
)

// ── Orphan reap ───────────────────────────────────────────────────────────────

// SelectOrphanReapCandidates takes observed sandboxes, referenced sandbox IDs,
// and the mutable pending-orphan set.  It returns sandbox IDs to stop and
// updates the pending set in place.

func TestOrphanReap_RequiresTwoConsecutivePasses(t *testing.T) {
	observed := []sandbox.ObservedSandbox{
		{ID: "asbx-1", Status: sandbox.StatusRunning},
	}
	referenced := map[string]bool{}
	pending := map[string]bool{}

	// First pass — the sandbox is seen as unreferenced but not yet reaped.
	candidates := runtime.SelectOrphanReapCandidates(observed, referenced, pending)
	assert.Empty(t, candidates, "first pass must not reap")

	// Second pass — now it is a confirmed orphan and should be reaped.
	candidates = runtime.SelectOrphanReapCandidates(observed, referenced, pending)
	assert.Equal(t, []string{"asbx-1"}, candidates)
}

func TestOrphanReap_ReferencedSandboxRescuesPendingOrphan(t *testing.T) {
	observed := []sandbox.ObservedSandbox{
		{ID: "asbx-1", Status: sandbox.StatusRunning},
	}
	referenced := map[string]bool{}
	pending := map[string]bool{}

	// First pass — adds to pending.
	runtime.SelectOrphanReapCandidates(observed, referenced, pending)

	// Second pass — sandbox is now referenced.
	referenced["asbx-1"] = true
	candidates := runtime.SelectOrphanReapCandidates(observed, referenced, pending)
	assert.Empty(t, candidates)
	assert.Empty(t, pending)
}

func TestOrphanReap_CreatedAndTerminalSandboxesAreNotReaped(t *testing.T) {
	observed := []sandbox.ObservedSandbox{
		{ID: "asbx-created", Status: sandbox.StatusCreated},
		{ID: "asbx-stopped", Status: sandbox.StatusStopped},
		{ID: "asbx-gone", Status: sandbox.StatusGone},
	}
	referenced := map[string]bool{}
	pending := map[string]bool{
		"asbx-created": true,
		"asbx-stopped": true,
		"asbx-gone":    true,
	}

	candidates := runtime.SelectOrphanReapCandidates(observed, referenced, pending)
	assert.Empty(t, candidates)
	assert.Empty(t, pending)
}

func TestOrphanReap_FailedStopStaysPendingForRetry(t *testing.T) {
	observed := []sandbox.ObservedSandbox{
		{ID: "asbx-1", Status: sandbox.StatusRunning},
	}
	referenced := map[string]bool{}
	// Start with asbx-1 already in the pending set (simulating a previous pass).
	pending := map[string]bool{"asbx-1": true}

	candidates := runtime.SelectOrphanReapCandidates(observed, referenced, pending)
	assert.Equal(t, []string{"asbx-1"}, candidates)
	// Must still be in the pending set so a retry can occur.
	assert.True(t, pending["asbx-1"])
}

func TestOrphanReap_VanishedPendingOrphanIsDropped(t *testing.T) {
	// Empty observed — the sandbox has gone away.
	observed := []sandbox.ObservedSandbox{}
	referenced := map[string]bool{}
	pending := map[string]bool{"asbx-1": true}

	candidates := runtime.SelectOrphanReapCandidates(observed, referenced, pending)
	assert.Empty(t, candidates)
	assert.Empty(t, pending)
}
