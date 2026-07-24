// Package runtime_test — sandbox lifecycle decision tests.
//
// Derived from:
//   services/api-rs/crates/centaur-session-runtime/src/lib.rs (§2.13–2.16 of SPEC.md)
//
// ALL ASSERTIONS ARE FIXED.
package runtime_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leowmjw/centaur/api-go/internal/runtime"
	"github.com/leowmjw/centaur/api-go/internal/sandbox"
	"github.com/leowmjw/centaur/api-go/internal/session"
)

// ── Idle pause guard ──────────────────────────────────────────────────────────

func TestShouldPauseIdleSandbox_RequiresLatestCompletedExecution(t *testing.T) {
	sess := &session.Session{SandboxID: ptr("asbx-1")}
	completed := &session.Execution{ExecutionID: "exe-1", Status: session.ExecutionCompleted}
	running := &session.Execution{ExecutionID: "exe-1", Status: session.ExecutionRunning}
	newer := &session.Execution{ExecutionID: "exe-2", Status: session.ExecutionCompleted}

	assert.True(t, runtime.ShouldPauseIdleSandbox(sess, completed, "exe-1", "asbx-1"))
	assert.False(t, runtime.ShouldPauseIdleSandbox(sess, running, "exe-1", "asbx-1"), "running execution must not pause")
	assert.False(t, runtime.ShouldPauseIdleSandbox(sess, newer, "exe-1", "asbx-1"), "newer execution ID must not pause")
	assert.False(t, runtime.ShouldPauseIdleSandbox(sess, completed, "exe-1", "asbx-other"), "sandbox ID mismatch must not pause")
}

// ── Event stream attach guard ─────────────────────────────────────────────────

func TestShouldAttachSessionPipe_OnlyRunning(t *testing.T) {
	assert.True(t, runtime.ShouldAttachSessionPipe(sandbox.StatusRunning))
	assert.False(t, runtime.ShouldAttachSessionPipe(sandbox.StatusCreated))
	assert.False(t, runtime.ShouldAttachSessionPipe(sandbox.StatusSuspended))
	assert.False(t, runtime.ShouldAttachSessionPipe(sandbox.StatusStopped))
	assert.False(t, runtime.ShouldAttachSessionPipe(sandbox.StatusGone))
	assert.False(t, runtime.ShouldAttachSessionPipe(sandbox.StatusUnknown))
}

// ── Existing sandbox action ───────────────────────────────────────────────────

func TestExistingSandboxAction_MapsAllStatuses(t *testing.T) {
	cases := []struct {
		status sandbox.Status
		want   runtime.ExistingSandboxAction
	}{
		{sandbox.StatusRunning, runtime.ActionReuse},
		{sandbox.StatusSuspended, runtime.ActionResumeOrReplace},
		{sandbox.StatusCreated, runtime.ActionResumeOrReplace},
		{sandbox.StatusStopped, runtime.ActionReplace},
		{sandbox.StatusGone, runtime.ActionReplace},
		{sandbox.StatusUnknown, runtime.ActionReplace},
	}
	for _, tc := range cases {
		t.Run(tc.status.String(), func(t *testing.T) {
			assert.Equal(t, tc.want, runtime.ExistingSandboxActionFor(tc.status))
		})
	}
}

// ── Steering startup error classification ─────────────────────────────────────

func TestSteeringStartupError_TransientSandboxErrors(t *testing.T) {
	notReady := sandbox.NewNotReadyError("sandbox starting")
	notFound := sandbox.NewNotFoundError("asbx-1")

	assert.True(t, runtime.IsTransientSteeringStartupError(notReady))
	assert.True(t, runtime.IsTransientSteeringStartupError(notFound))
}

func TestSteeringStartupError_NonTransientErrors(t *testing.T) {
	ioErr := sandbox.NewIOError("stdin closed")
	storeErr := session.NewNotFoundError("cli:test")

	assert.False(t, runtime.IsTransientSteeringStartupError(ioErr))
	assert.False(t, runtime.IsTransientSteeringStartupError(storeErr))
}

// ── Execution metadata ────────────────────────────────────────────────────────

func TestExecutionMetadata_PreservesIdleAndMaxDuration(t *testing.T) {
	meta := runtime.BuildExecutionMetadata(
		map[string]any{"source": "test"},
		ptr(int64(2_000)),
		ptr(int64(5_000)),
	)
	assert.Equal(t, "test", meta["source"])
	assert.EqualValues(t, 2_000, meta["idle_timeout_ms"])
	assert.EqualValues(t, 5_000, meta["max_duration_ms"])
}

func TestExecutionMetadata_IdleTimeoutFromExecution(t *testing.T) {
	exe := &session.Execution{
		Metadata: map[string]any{"idle_timeout_ms": float64(1500)},
	}
	d, ok := runtime.IdleTimeoutFromExecution(exe)
	require.True(t, ok)
	assert.Equal(t, int64(1500), d.Milliseconds())
}

// ── Sandbox repo cache capability application ─────────────────────────────────

func TestSandboxCapabilities_PublicRepoCacheScopesBindMount(t *testing.T) {
	spec := sandbox.NewSpec("mock").
		WithMount(sandbox.Mount{
			Kind:       sandbox.MountBind{SourcePath: "/var/lib/centaur/repos"},
			TargetPath: "/home/agent/github",
		})
	caps := session.SandboxCapabilities{
		RepoCache:             session.RepoCachePublic,
		ObservabilityEnabled: true,
		APIServerEnabled:     true,
	}

	runtime.ApplySandboxCapabilities(spec, caps)

	require.Len(t, spec.Mounts, 1)
	bind, ok := spec.Mounts[0].Kind.(sandbox.MountBind)
	require.True(t, ok)
	assert.Equal(t, "/var/lib/centaur/repos/public", bind.SourcePath)
	assert.Nil(t, spec.Mounts[0].SubPath)
}

func TestSandboxCapabilities_PublicRepoCacheScopesNamedVolumeSubPath(t *testing.T) {
	spec := sandbox.NewSpec("mock").
		WithMount(sandbox.Mount{
			Kind:       sandbox.MountNamedVolume{Name: "centaur-repo-cache"},
			TargetPath: "/home/agent/github",
		})
	caps := session.SandboxCapabilities{
		RepoCache:             session.RepoCachePublic,
		ObservabilityEnabled: true,
		APIServerEnabled:     true,
	}

	runtime.ApplySandboxCapabilities(spec, caps)

	require.Len(t, spec.Mounts, 1)
	require.NotNil(t, spec.Mounts[0].SubPath)
	assert.Equal(t, "public", *spec.Mounts[0].SubPath)
}

func TestSandboxCapabilities_DisabledRepoCacheRemovesRepoMount(t *testing.T) {
	spec := sandbox.NewSpec("mock").
		WithMount(sandbox.Mount{
			Kind:       sandbox.MountBind{SourcePath: "/var/lib/centaur/repos"},
			TargetPath: "/home/agent/github",
		}).
		WithMount(sandbox.Mount{
			Kind:       sandbox.MountEmptyDir{},
			TargetPath: "/workspace",
		}).
		WithEnv("CENTAUR_SKILL_DIRS", "/home/agent/github/acme/private/.agents/skills").
		WithEnv("CENTAUR_PUBLIC_SKILL_DIRS", "/home/agent/github/acme/public/.agents/skills")

	caps := session.SandboxCapabilities{
		RepoCache:             session.RepoCacheNone,
		ObservabilityEnabled: true,
		APIServerEnabled:     true,
	}

	runtime.ApplySandboxCapabilities(spec, caps)

	require.Len(t, spec.Mounts, 1)
	assert.Equal(t, "/workspace", spec.Mounts[0].TargetPath)
	assert.Equal(t, "", spec.EnvValue("CENTAUR_SKILL_DIRS"))
	assert.Equal(t, "", spec.EnvValue("CENTAUR_PUBLIC_SKILL_DIRS"))
}

// ── Workload sandbox spec ─────────────────────────────────────────────────────

func TestWorkloadSpec_PinsHarnessViaContainerArgs(t *testing.T) {
	workload := runtime.NewCodexAppServerWorkload("centaur-agent:latest", nil, session.HarnessCodex)
	threadKey, _ := session.ParseThreadKey("chat:C123:1780000000.000000")

	codexSpec := workload.Spec(threadKey, session.HarnessCodex, nil)
	claudeSpec := workload.Spec(threadKey, session.HarnessClaudeCode, nil)
	ampSpec := workload.Spec(threadKey, session.HarnessAmp, nil)

	assert.Equal(t, []string{"harness-server", "codex"}, codexSpec.Args)
	assert.Equal(t, []string{"harness-server", "claude-code"}, claudeSpec.Args)
	assert.Equal(t, []string{"harness-server", "amp"}, ampSpec.Args)
	assert.Nil(t, codexSpec.Command, "image entrypoint must not be overridden")
}

func TestWorkloadSpec_LabelsSessionSandboxForObservability(t *testing.T) {
	workload := runtime.NewCodexAppServerWorkload("centaur-agent:latest", nil, session.HarnessCodex)
	threadKey, _ := session.ParseThreadKey("chat:C123:1780000000.000000")

	spec := workload.Spec(threadKey, session.HarnessClaudeCode, nil)

	assert.Equal(t, "session-sandbox", spec.Labels["centaur.ai/component"])
	assert.Equal(t, "claudecode", spec.Labels["centaur.ai/harness"])
}

func TestWorkloadSpec_WarmSpecHasNoThreadKey(t *testing.T) {
	workload := runtime.NewCodexAppServerWorkload("centaur-agent:latest", map[string]string{
		"CENTAUR_API_URL": "http://api:8000",
	}, session.HarnessCodex)
	threadKey, _ := session.ParseThreadKey("chat:C123:1780000000.000000")

	claimedSpec := workload.Spec(threadKey, session.HarnessClaudeCode, nil)
	warmSpec := workload.WarmSpec()

	assert.Equal(t, threadKey.String(), claimedSpec.EnvValue("CENTAUR_THREAD_KEY"))
	assert.Equal(t, "", warmSpec.EnvValue("CENTAUR_THREAD_KEY"))
}

func TestWorkloadSpec_WarmSpecUsesDefaultHarness(t *testing.T) {
	workload := runtime.NewCodexAppServerWorkload("centaur-agent:latest", nil, session.HarnessCodex)

	assert.Equal(t, []string{"harness-server", "codex"}, workload.WarmSpec().Args)
}

func TestWorkloadSpec_DoesNotInjectStaleContinueThreadID(t *testing.T) {
	workload := runtime.NewCodexAppServerWorkload("centaur-agent:latest", nil, session.HarnessCodex)
	threadKey, _ := session.ParseThreadKey("chat:C123:1780000000.000000")

	spec := workload.Spec(threadKey, session.HarnessCodex, nil)

	assert.Equal(t, "", spec.EnvValue("CODEX_CONTINUE_THREAD_ID"))
	assert.Equal(t, "", spec.EnvValue("AMP_CONTINUE_THREAD_ID"))
}

func TestWorkloadSpec_ReflectsPersonaContext(t *testing.T) {
	workload := runtime.NewCodexAppServerWorkload("centaur-agent:latest", map[string]string{
		"AGENT_PERSONA": "stale",
	}, session.HarnessCodex)
	threadKey, _ := session.ParseThreadKey("chat:C123:1780000000.000000")
	persona := &runtime.PersonaContext{
		PersonaID:   "eng",
		PromptHash:  "sha256:prompt",
		SourceRef:   ptr("abc123"),
	}

	spec := workload.Spec(threadKey, session.HarnessCodex, persona)

	assert.Equal(t, "eng", spec.EnvValue("AGENT_PERSONA"))
	assert.Equal(t, "eng", spec.EnvValue("CENTAUR_PERSONA_ID"))
	assert.Equal(t, "sha256:prompt", spec.EnvValue("CENTAUR_PERSONA_PROMPT_HASH"))
	assert.Equal(t, "abc123", spec.EnvValue("CENTAUR_PERSONA_SOURCE_REF"))
	assert.Equal(t, "", workload.WarmSpec().EnvValue("AGENT_PERSONA"), "warm spec must not carry persona")
}

// ── Input enrichment ──────────────────────────────────────────────────────────

func TestInputLine_EnrichesJSONObjects(t *testing.T) {
	threadKey, _ := session.ParseThreadKey("chat:C123:1780000000.000000")
	trace := runtime.NewSessionTraceContext(threadKey, "")

	line := runtime.InputLineWithSessionContext(threadKey, trace, `{"type":"user"}`)

	var v map[string]any
	require.NoError(t, unmarshalJSON(line, &v))
	assert.Equal(t, "user", v["type"])
	assert.Equal(t, threadKey.String(), v["thread_key"])
	assert.Equal(t, trace.TraceID, v["trace_id"])
	assert.Nil(t, v["session_context"], "no session_context for non-slack keys")
}

func TestInputLine_AddsSlackThreadContext(t *testing.T) {
	threadKey, _ := session.ParseThreadKey("slack:T123:C123:1780000000.000000")
	trace := runtime.NewSessionTraceContext(threadKey, "")

	line := runtime.InputLineWithSessionContext(threadKey, trace, `{"type":"user"}`)

	var v map[string]any
	require.NoError(t, unmarshalJSON(line, &v))
	sc := v["session_context"].(map[string]any)
	slack := sc["slack"].(map[string]any)
	assert.Equal(t, "slack", sc["platform"])
	assert.Equal(t, "T123", slack["team_id"])
	assert.Equal(t, "C123", slack["channel_id"])
	assert.Equal(t, "1780000000.000000", slack["thread_ts"])
}

func TestInputLine_PreservesExistingFields(t *testing.T) {
	threadKey, _ := session.ParseThreadKey("chat:C123:1780000000.000000")
	trace := runtime.NewSessionTraceContext(threadKey, "")

	line := runtime.InputLineWithSessionContext(threadKey, trace,
		`{"type":"user","thread_key":"chat:existing","trace_id":"caller-trace"}`)

	var v map[string]any
	require.NoError(t, unmarshalJSON(line, &v))
	assert.Equal(t, "chat:existing", v["thread_key"])
	assert.Equal(t, "caller-trace", v["trace_id"])
}

func TestInputLine_PreservesNonJSONLines(t *testing.T) {
	threadKey, _ := session.ParseThreadKey("chat:C123:1780000000.000000")
	trace := runtime.NewSessionTraceContext(threadKey, "")

	line := runtime.InputLineWithSessionContext(threadKey, trace, "raw")
	assert.Equal(t, "raw", line)
}

// ── Thread trace ID determinism ───────────────────────────────────────────────

func TestThreadTraceID_DeterministicPerThread(t *testing.T) {
	k1, _ := session.ParseThreadKey("chat:C123:1780000000.000000")
	k2, _ := session.ParseThreadKey("chat:C456:1780000000.000000")

	id1a := runtime.ThreadTraceID(k1)
	id1b := runtime.ThreadTraceID(k1)
	id2 := runtime.ThreadTraceID(k2)

	assert.Equal(t, id1a, id1b, "same thread key must produce same trace ID")
	assert.NotEqual(t, id1a, id2, "different thread keys must produce different trace IDs")
	// Must be a valid UUID
	_, err := parseUUID(id1a)
	assert.NoError(t, err)
}

// ── Steering input lines ──────────────────────────────────────────────────────

func TestSteeringInputLines_OnlyForwardsUserMessages(t *testing.T) {
	threadKey, _ := session.ParseThreadKey("cli:test-steering")
	messages := []session.MessageInput{
		{
			Role:  session.MessageRoleUser,
			Parts: []map[string]any{{"type": "text", "text": "steer now"}},
		},
		{
			Role:  session.MessageRoleAssistant,
			Parts: []map[string]any{{"type": "text", "text": "do not echo assistant"}},
		},
	}
	messageIDs := []string{"msg-user", "msg-assistant"}

	lines := runtime.SteeringInputLines(threadKey, messages, messageIDs)
	require.Len(t, lines, 1)

	var v map[string]any
	require.NoError(t, unmarshalJSON(lines[0], &v))
	assert.Equal(t, "user", v["type"])
	assert.Equal(t, "cli:test-steering", v["thread_key"])
	trace := v["trace_metadata"].(map[string]any)
	assert.Equal(t, "steer_active_execution", trace["action"])
	assert.Equal(t, "msg-user", trace["message_id"])
	msg := v["message"].(map[string]any)
	content := msg["content"].([]any)
	assert.Equal(t, "steer now", content[0].(map[string]any)["text"])
}

// ── helpers ───────────────────────────────────────────────────────────────────

func ptr[T any](v T) *T { return &v }

func unmarshalJSON(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func parseUUID(s string) ([16]byte, error) {
	u, err := uuid.Parse(s)
	return [16]byte(u), err
}
