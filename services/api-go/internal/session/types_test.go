// Package session defines the core durable control-plane wire types.
//
// This file contains behaviour tests derived from:
//   services/api-rs/crates/centaur-session-core/src/lib.rs (§1 of SPEC.md)
//
// ALL ASSERTIONS ARE FIXED — the implementing agent must make these pass by
// writing the production code, not by changing the test bodies.
package session_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leowmjw/centaur/api-go/internal/session"
)

// ── ThreadKey ────────────────────────────────────────────────────────────────

func TestThreadKey_AcceptsNamespacedValues(t *testing.T) {
	key, err := session.ParseThreadKey("chat:C123:1780000000.000000")
	require.NoError(t, err)
	assert.Equal(t, "chat:C123:1780000000.000000", key.String())
}

func TestThreadKey_AcceptsSlackNamespacedValues(t *testing.T) {
	key, err := session.ParseThreadKey("slack:T123:C123:1780000000.000000")
	require.NoError(t, err)
	assert.Equal(t, "slack:T123:C123:1780000000.000000", key.String())
}

func TestThreadKey_AcceptsSimpleNamespace(t *testing.T) {
	_, err := session.ParseThreadKey("cli:local")
	require.NoError(t, err)
}

func TestThreadKey_RejectsMissingNamespace(t *testing.T) {
	_, err := session.ParseThreadKey("not-namespaced")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespaced")
}

func TestThreadKey_RejectsEmptyString(t *testing.T) {
	_, err := session.ParseThreadKey("")
	require.Error(t, err)
}

func TestThreadKey_RejectsRawJSON(t *testing.T) {
	_, err := session.ParseThreadKey(`{"thread":"x"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON")
}

func TestThreadKey_RejectsRawJSONArray(t *testing.T) {
	_, err := session.ParseThreadKey(`["thread","x"]`)
	require.Error(t, err)
}

func TestThreadKey_RejectsControlCharacters(t *testing.T) {
	_, err := session.ParseThreadKey("chat:\x00bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control")
}

func TestThreadKey_RejectsTooLong(t *testing.T) {
	longKey := "x:" + strings.Repeat("a", 512)
	_, err := session.ParseThreadKey(longKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "512")
}

func TestThreadKey_RejectsEmptyNamespacePart(t *testing.T) {
	_, err := session.ParseThreadKey(":id")
	require.Error(t, err)
}

func TestThreadKey_RejectsEmptyIDPart(t *testing.T) {
	_, err := session.ParseThreadKey("ns:")
	require.Error(t, err)
}

// ── HarnessType ──────────────────────────────────────────────────────────────

func TestHarnessType_AcceptsSupportedValues(t *testing.T) {
	cases := []struct {
		wire string
		want session.HarnessType
	}{
		{"codex", session.HarnessCodex},
		{"amp", session.HarnessAmp},
		{"claudecode", session.HarnessClaudeCode},
	}
	for _, tc := range cases {
		t.Run(tc.wire, func(t *testing.T) {
			got, err := session.ParseHarnessType(tc.wire)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestHarnessType_SerializesAsWireValue(t *testing.T) {
	cases := []struct {
		harness  session.HarnessType
		wantJSON string
	}{
		{session.HarnessCodex, `"codex"`},
		{session.HarnessAmp, `"amp"`},
		{session.HarnessClaudeCode, `"claudecode"`},
	}
	for _, tc := range cases {
		t.Run(tc.wantJSON, func(t *testing.T) {
			assert.Equal(t, tc.wantJSON[1:len(tc.wantJSON)-1], tc.harness.String())
		})
	}
}

func TestHarnessType_RejectsHyphenatedClaudeCode(t *testing.T) {
	// "claude-code" (with hyphen) is NOT a valid value — only "claudecode" is.
	_, err := session.ParseHarnessType("claude-code")
	require.Error(t, err)
}

func TestHarnessType_RejectsUnknownValues(t *testing.T) {
	_, err := session.ParseHarnessType("gpt-4")
	require.Error(t, err)
}

// ── SandboxRepoCacheAccess ───────────────────────────────────────────────────

func TestSandboxRepoCacheAccess_EnabledOnlyForPublicAndAll(t *testing.T) {
	assert.False(t, session.RepoCacheNone.Enabled())
	assert.True(t, session.RepoCachePublic.Enabled())
	assert.True(t, session.RepoCacheAll.Enabled())
}

func TestSandboxRepoCacheAccess_DefaultIsAll(t *testing.T) {
	var caps session.SandboxCapabilities
	assert.Equal(t, session.RepoCacheAll, caps.RepoCache)
}

// ── ExecutionStatus ──────────────────────────────────────────────────────────

func TestExecutionStatus_SerializesAsSnakeCase(t *testing.T) {
	cases := []struct {
		status session.ExecutionStatus
		wire   string
	}{
		{session.ExecutionQueued, "queued"},
		{session.ExecutionRunning, "running"},
		{session.ExecutionCompleted, "completed"},
		{session.ExecutionFailed, "failed"},
		{session.ExecutionCancelled, "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.wire, func(t *testing.T) {
			assert.Equal(t, tc.wire, tc.status.String())
		})
	}
}
