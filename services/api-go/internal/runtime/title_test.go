// Package runtime_test — session title generation and persona registry tests.
//
// Derived from:
//   services/api-rs/crates/centaur-session-runtime/src/title_generator.rs
//   services/api-rs/crates/centaur-session-runtime/src/lib.rs (persona tests)
//   (§9, §10 of SPEC.md)
//
// ALL ASSERTIONS ARE FIXED.
package runtime_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leowmjw/centaur/api-go/internal/runtime"
)

// ── Title source selection ────────────────────────────────────────────────────

func TestTitleSource_PrefersUserAskOverSlackContext(t *testing.T) {
	parts := []map[string]any{
		{"type": "text", "text": "# Requester Context\n\nThe Slack user who prompted this turn is Alice."},
		{"type": "text", "text": "<@U123> please fix the memory leak in the worker"},
	}

	source := runtime.SessionTitleSourceFromParts(parts)
	require.NotNil(t, source)
	assert.Equal(t, "please fix the memory leak in the worker", *source)
}

func TestTitleSource_UsesFirstSlackThreadMessage(t *testing.T) {
	parts := []map[string]any{
		{
			"type": "text",
			"text": "# Slack Thread Context\n\nEarlier messages in this Slack thread, in chronological order:\n\n1. Alice:\n   Planning to replace the billing export job with a streaming worker because the nightly batch keeps timing out\n\n# Current Request\n\nThe user message follows in the next content block.\n---",
		},
		{"type": "text", "text": "<@U123> investigate this"},
	}

	source := runtime.SessionTitleSourceFromParts(parts)
	require.NotNil(t, source)
	assert.Equal(t,
		"Planning to replace the billing export job with a streaming worker because the nightly batch keeps timing out",
		*source)
}

func TestTitleSource_SkipsLowSignalWakeups(t *testing.T) {
	// Bare emoji is low-signal.
	parts := []map[string]any{
		{"type": "text", "text": "<@U123> Hey"},
		{"type": "text", "text": ":thread:"},
	}
	assert.Nil(t, runtime.SessionTitleSourceFromParts(parts))

	// Meaningful follow-up question is not low-signal.
	parts2 := []map[string]any{
		{"type": "text", "text": "<@U123> Hey"},
		{"type": "text", "text": "Can you investigate queue stalls?"},
	}
	source := runtime.SessionTitleSourceFromParts(parts2)
	require.NotNil(t, source)
	assert.Equal(t, "Can you investigate queue stalls?", *source)
}

// ── Title sanitisation ────────────────────────────────────────────────────────

func TestSanitiseTitle_KeepsModelWording(t *testing.T) {
	result := runtime.SanitiseSessionTitle("Memory leak in worker queue needs investigation immediately")
	require.NotNil(t, result)
	assert.Equal(t, "Memory leak in worker queue needs investigation", *result)
}

func TestSanitiseTitle_StripsQuotesAndTrailingPunctuation(t *testing.T) {
	result := runtime.SanitiseSessionTitle(`"Fix worker memory leak."`)
	require.NotNil(t, result)
	assert.Equal(t, "Fix worker memory leak", *result)
}

func TestSanitiseTitle_ReturnsNilForBlankInput(t *testing.T) {
	assert.Nil(t, runtime.SanitiseSessionTitle(""))
	assert.Nil(t, runtime.SanitiseSessionTitle("   "))
}

// ── OpenAI Responses API shape ────────────────────────────────────────────────

func TestOpenAIResponseOutputText_TopLevelOutputText(t *testing.T) {
	result := runtime.OpenAIResponseOutputText(`{"output_text":"Fix worker memory leak"}`)
	require.NotNil(t, result)
	assert.Equal(t, "Fix worker memory leak", *result)
}

func TestOpenAIResponseOutputText_NestedOutputArray(t *testing.T) {
	result := runtime.OpenAIResponseOutputText(
		`{"output":[{"content":[{"type":"output_text","text":"Add Tempo Explorer filter"}]}]}`,
	)
	require.NotNil(t, result)
	assert.Equal(t, "Add Tempo Explorer filter", *result)
}

// ── Persona registry ──────────────────────────────────────────────────────────

func TestPersonaRegistry_ValidatesDefaultAndSummarizesWithoutPrompt(t *testing.T) {
	registry, err := runtime.NewPersonaRegistry([]runtime.PersonaDefinition{
		{
			ID:         "eng",
			SourceRoot: "/repo/tools",
			SourcePath: "/repo/tools/personas/eng",
			SourceRef:  ptr("abc123"),
			PromptHash: "sha256:prompt",
			Prompt:     "secret prompt",
		},
	}, ptr("eng"), nil)
	require.NoError(t, err)

	summaries := registry.Summaries()
	require.Len(t, summaries, 1)
	assert.Equal(t, "eng", summaries[0].ID)

	// The Prompt field must not appear in serialised form.
	raw, err := registry.MarshalPersona("eng")
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "secret prompt")

	// Unknown default persona must fail.
	_, err = runtime.NewPersonaRegistry(nil, ptr("missing"), nil)
	require.Error(t, err)
}

func TestPersonaRegistry_LimitsPublicAccessToPublicSourceRoots(t *testing.T) {
	registry, err := runtime.NewPersonaRegistry(
		[]runtime.PersonaDefinition{
			{ID: "private", SourceRoot: "/repo/private/tools", PromptHash: "sha256:private", Prompt: "private"},
			{ID: "public", SourceRoot: "/repo/public/tools", PromptHash: "sha256:public", Prompt: "public"},
		},
		ptr("private"),
		nil,
	)
	require.NoError(t, err)
	registry = registry.WithPublicSourceRoots([]string{"/repo/public/tools"})

	// All access — private persona is the default.
	id := registry.DefaultPersonaIDForAccess(runtime.RepoCacheAccessAll)
	require.NotNil(t, id)
	assert.Equal(t, "private", *id)

	// Public access — private is NOT the default.
	id = registry.DefaultPersonaIDForAccess(runtime.RepoCacheAccessPublic)
	assert.Nil(t, id)

	// Public access — private persona returns an error.
	_, err = registry.ContextForAccess("private", false, runtime.RepoCacheAccessPublic)
	require.Error(t, err)

	// Public access — public persona works.
	ctx, err := registry.ContextForAccess("public", false, runtime.RepoCacheAccessPublic)
	require.NoError(t, err)
	assert.Equal(t, "public", ctx.PersonaID)
}
