// Package runtime_test — terminal output parsing & stdout state tests.
//
// Derived from:
//   services/api-rs/crates/centaur-session-runtime/src/lib.rs (§2 of SPEC.md)
//
// ALL ASSERTIONS ARE FIXED.
package runtime_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leowmjw/centaur/api-go/internal/runtime"
)

// ── TerminalOutput detection ──────────────────────────────────────────────────

func TestTerminalOutput_TurnCompletedWithoutAnswerTextIsTerminal(t *testing.T) {
	event := `{"type":"turn.completed","turn":{"id":"turn-1","status":"completed"}}`
	result := runtime.ParseTerminalOutput(event, "")
	require.NotNil(t, result)
	require.IsType(t, runtime.TerminalCompleted{}, *result)
	c := (*result).(runtime.TerminalCompleted)
	assert.Equal(t, "turn_completed", c.Reason)
	assert.Nil(t, c.ResultText)
}

func TestTerminalOutput_TurnCompletedAfterAnswerTextIsTerminal(t *testing.T) {
	event := `{"method":"turn/completed","params":{"turn":{"id":"turn-1","status":"completed"}}}`
	result := runtime.ParseTerminalOutput(event, "Final answer")
	require.NotNil(t, result)
	c := (*result).(runtime.TerminalCompleted)
	assert.Equal(t, "turn_completed", c.Reason)
	require.NotNil(t, c.ResultText)
	assert.Equal(t, "Final answer", *c.ResultText)
}

func TestTerminalOutput_TurnCompletedUsesCompletedAgentMessageWhenEmpty(t *testing.T) {
	// When turn.completed has no answer text but a prior item.completed agentMessage
	// set the final buffer, the buffer text should become result_text.
	completedMsg := `{"type":"item.completed","item":{"id":"msg-final","type":"agentMessage","phase":"final_answer","text":"1. No new findings.\n\n2. No writes were used."}}`
	terminal := `{"type":"turn.completed","turn":{"id":"turn-1","status":"completed"}}`

	// Parse the completed message to get the final text.
	update := runtime.ParseFinalAnswerTextUpdate(completedMsg)
	require.NotNil(t, update)
	require.IsType(t, runtime.FinalAnswerReplace{}, *update)
	finalText := (*update).(runtime.FinalAnswerReplace).Text

	result := runtime.ParseTerminalOutput(terminal, finalText)
	require.NotNil(t, result)
	c := (*result).(runtime.TerminalCompleted)
	require.NotNil(t, c.ResultText)
	assert.Equal(t, "1. No new findings.\n\n2. No writes were used.", *c.ResultText)
}

func TestTerminalOutput_InterruptedTurnWithoutAnswerIsCancelled(t *testing.T) {
	event := `{"type":"turn.completed","turn":{"id":"turn-1","status":"interrupted"}}`
	result := runtime.ParseTerminalOutput(event, "")
	require.NotNil(t, result)
	require.IsType(t, runtime.TerminalCancelled{}, *result)
	c := (*result).(runtime.TerminalCancelled)
	assert.Equal(t, "turn_interrupted", c.Reason)
}

func TestTerminalOutput_InterruptedTurnAfterAnswerStaysTerminal(t *testing.T) {
	event := `{"method":"turn/completed","params":{"turn":{"id":"turn-1","status":"interrupted"}}}`
	result := runtime.ParseTerminalOutput(event, "Final answer")
	require.NotNil(t, result)
	require.IsType(t, runtime.TerminalCompleted{}, *result)
	c := (*result).(runtime.TerminalCompleted)
	assert.Equal(t, "turn_completed", c.Reason)
	require.NotNil(t, c.ResultText)
	assert.Equal(t, "Final answer", *c.ResultText)
}

func TestTerminalOutput_ResultEventCompletesEvenWithoutPriorDelta(t *testing.T) {
	event := `{"type":"result","result":{"text":"Final answer"}}`
	result := runtime.ParseTerminalOutput(event, "")
	require.NotNil(t, result)
	c := (*result).(runtime.TerminalCompleted)
	assert.Equal(t, "result", c.Reason)
	require.NotNil(t, c.ResultText)
	assert.Equal(t, "Final answer", *c.ResultText)
}

func TestTerminalOutput_TurnDoneCarriesTerminalResultText(t *testing.T) {
	event := `{"type":"turn.done","result":"Final answer"}`
	result := runtime.ParseTerminalOutput(event, "")
	require.NotNil(t, result)
	c := (*result).(runtime.TerminalCompleted)
	assert.Equal(t, "turn_done", c.Reason)
	require.NotNil(t, c.ResultText)
	assert.Equal(t, "Final answer", *c.ResultText)
}

func TestTerminalOutput_TurnFailedIsTerminalFailure(t *testing.T) {
	event := `{"type":"turn.failed","error":"sandbox exited"}`
	result := runtime.ParseTerminalOutput(event, "")
	require.NotNil(t, result)
	require.IsType(t, runtime.TerminalFailed{}, *result)
	f := (*result).(runtime.TerminalFailed)
	assert.Equal(t, "sandbox exited", f.Error)
}

func TestTerminalOutput_NestedTerminalTextIsNormalized(t *testing.T) {
	event := `{"result":{"message":{"content":[{"type":"text","text":"Final answer"}]}}}`
	text := runtime.ExtractTerminalPayloadText(event)
	assert.Equal(t, "Final answer", text)
}

// ── FinalAnswerTextUpdate ─────────────────────────────────────────────────────

func TestFinalAnswerTextUpdate_DeltaIsAppend(t *testing.T) {
	line := `{"method":"item/agentMessage/delta","params":{"turnId":"turn-1","delta":"Hello"}}`
	update := runtime.ParseFinalAnswerTextUpdate(line)
	require.NotNil(t, update)
	require.IsType(t, runtime.FinalAnswerAppend{}, *update)
	assert.Equal(t, "Hello", (*update).(runtime.FinalAnswerAppend).Text)
}

func TestFinalAnswerTextUpdate_CompletedAgentMessageIsReplace(t *testing.T) {
	line := `{"type":"item.completed","item":{"id":"msg-1","type":"agentMessage","phase":"final_answer","text":"Full answer"}}`
	update := runtime.ParseFinalAnswerTextUpdate(line)
	require.NotNil(t, update)
	require.IsType(t, runtime.FinalAnswerReplace{}, *update)
	assert.Equal(t, "Full answer", (*update).(runtime.FinalAnswerReplace).Text)
}

// ── Stdout source classification ──────────────────────────────────────────────

func TestSandboxOutputSource_CodexAppServerHasMethodKey(t *testing.T) {
	line := `{"method":"item/agentMessage/delta","params":{"turnId":"turn-1","itemId":"item-1"}}`
	assert.Equal(t, "codex_app_server", runtime.SandboxOutputSource(line))
	assert.Equal(t, "item/agentMessage/delta", runtime.SandboxOutputEventType(line))
}

func TestSandboxOutputSource_HarnessLine(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"redacted"}]}}`
	assert.Equal(t, "harness", runtime.SandboxOutputSource(line))
}

func TestSandboxOutputSource_SandboxLine(t *testing.T) {
	line := `{"type":"custom.wrapper.event"}`
	assert.Equal(t, "sandbox", runtime.SandboxOutputSource(line))
}

// ── First-token detection ─────────────────────────────────────────────────────

func TestFirstToken_TurnStartedIsNotFirstToken(t *testing.T) {
	assert.False(t, runtime.ShouldRecordFirstToken("exe-1", `{"type":"turn.started","turn_id":"turn-1"}`))
}

func TestFirstToken_DeltaIsFirstToken(t *testing.T) {
	assert.True(t, runtime.ShouldRecordFirstToken("exe-1", `{"type":"item.agentMessage.delta","turnId":"turn-1","itemId":"msg-1","delta":"Hello"}`))
}

func TestFirstToken_ResultIsFirstToken(t *testing.T) {
	assert.True(t, runtime.ShouldRecordFirstToken("exe-2", `{"type":"result","result":{"text":"Done"}}`))
}

// ── Terminal failure class ────────────────────────────────────────────────────

func TestTerminalFailureClass_SandboxIO(t *testing.T) {
	assert.Equal(t, "sandbox_io", runtime.TerminalFailureClass("sandbox stdout closed before terminal output"))
}

func TestTerminalFailureClass_Orphaned(t *testing.T) {
	assert.Equal(t, "orphaned", runtime.TerminalFailureClass("execution orphaned by control plane restart"))
}

func TestTerminalFailureClass_Harness(t *testing.T) {
	assert.Equal(t, "harness", runtime.TerminalFailureClass("turn failed: model error"))
}

func TestTerminalFailureClass_Unknown(t *testing.T) {
	assert.Equal(t, "unknown", runtime.TerminalFailureClass("something completely different"))
}

// ── Duration helpers ──────────────────────────────────────────────────────────

func TestDurationMillis_ConvertsMilliseconds(t *testing.T) {
	assert.Equal(t, uint64(3_000), runtime.DurationMillisU64(3_000_000_000)) // 3 seconds in ns
}

// ── Output line redaction ─────────────────────────────────────────────────────

func TestRedactSensitiveText_RedactsBearerToken(t *testing.T) {
	line := `{"type":"item.completed","item":{"aggregatedOutput":"Authorization: ******"}}`
	redacted := runtime.RedactSensitiveText(line)

	assert.NotContains(t, redacted, "sbx1.threadpayload.signature")
	assert.NotContains(t, redacted, "sbx1.otherpayload.othersig")
	assert.NotContains(t, redacted, "xoxb-1234567890-abcdef")
	assert.Contains(t, redacted, "Authorization: ******")
	assert.Contains(t, redacted, "SANDBOX_TOKEN=[REDACTED_TOKEN]")
	assert.Contains(t, redacted, "SLACK_BOT_TOKEN=[REDACTED_TOKEN]")
}

// ── Harness thread ID extraction ──────────────────────────────────────────────

func TestHarnessThreadID_ExtractsThreadID(t *testing.T) {
	assert.Equal(t, "codex-thread-1", runtime.HarnessThreadIDFromOutputLine(`{"type":"thread.started","thread_id":"codex-thread-1"}`))
}

func TestHarnessThreadID_ExtractsThreadIdCamelCase(t *testing.T) {
	assert.Equal(t, "codex-thread-2", runtime.HarnessThreadIDFromOutputLine(`{"type":"thread.started","threadId":"codex-thread-2"}`))
}

func TestHarnessThreadID_ReturnsEmptyForOtherEvents(t *testing.T) {
	assert.Equal(t, "", runtime.HarnessThreadIDFromOutputLine(`{"type":"turn.started","turn_id":"turn-1"}`))
}
