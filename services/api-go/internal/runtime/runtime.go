package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leowmjw/centaur/api-go/internal/sandbox"
	"github.com/leowmjw/centaur/api-go/internal/session"
)

type RepoCacheAccess = session.RepoCacheAccess

const (
	RepoCacheAccessAll    = session.RepoCacheAll
	RepoCacheAccessPublic = session.RepoCachePublic
	RepoCacheAccessNone   = session.RepoCacheNone
)

type ExistingSandboxAction string

const (
	ActionReuse           ExistingSandboxAction = "reuse"
	ActionResumeOrReplace ExistingSandboxAction = "resume_or_replace"
	ActionReplace         ExistingSandboxAction = "replace"
)

type SessionTraceContext struct {
	TraceID         string
	HarnessThreadID string
}

type PersonaContext struct {
	PersonaID  string
	PromptHash string
	SourceRef  *string
}

type PersonaDefinition struct {
	ID         string
	SourceRoot string
	SourcePath string
	SourceRef  *string
	PromptHash string
	Prompt     string
}

type PersonaSummary struct {
	ID         string  `json:"id"`
	SourceRoot string  `json:"source_root,omitempty"`
	SourcePath string  `json:"source_path,omitempty"`
	SourceRef  *string `json:"source_ref,omitempty"`
	PromptHash string  `json:"prompt_hash,omitempty"`
}

type PersonaRegistry struct {
	definitions       map[string]PersonaDefinition
	order             []string
	defaultPersonaID  *string
	publicSourceRoots []string
}

type CodexAppServerWorkload struct {
	image          string
	baseEnv        map[string]string
	defaultHarness session.HarnessType
}

type TerminalOutput interface{}

type TerminalCompleted struct {
	Reason     string
	ResultText *string
}

type TerminalCancelled struct {
	Reason string
}

type TerminalFailed struct {
	Error string
}

type FinalAnswerTextUpdate interface{}

type FinalAnswerAppend struct{ Text string }

type FinalAnswerReplace struct{ Text string }

func SelectOrphanReapCandidates(observed []sandbox.ObservedSandbox, referenced map[string]bool, pending map[string]bool) []string {
	seen := make(map[string]bool, len(observed))
	var candidates []string
	for _, sb := range observed {
		id := string(sb.ID)
		seen[id] = true
		if referenced[id] || sb.Status == sandbox.StatusCreated || sb.Status == sandbox.StatusStopped || sb.Status == sandbox.StatusGone {
			delete(pending, id)
			continue
		}
		if pending[id] {
			candidates = append(candidates, id)
			continue
		}
		pending[id] = true
	}
	for id := range pending {
		if !seen[id] {
			delete(pending, id)
		}
	}
	return candidates
}

func ShouldPauseIdleSandbox(sess *session.Session, latest *session.Execution, executionID, sandboxID string) bool {
	if sess == nil || sess.SandboxID == nil || *sess.SandboxID != sandboxID || latest == nil {
		return false
	}
	return latest.Status == session.ExecutionCompleted && latest.ExecutionID == executionID
}

func ShouldAttachSessionPipe(status sandbox.Status) bool {
	return status == sandbox.StatusRunning
}

func ExistingSandboxActionFor(status sandbox.Status) ExistingSandboxAction {
	switch status {
	case sandbox.StatusRunning:
		return ActionReuse
	case sandbox.StatusSuspended, sandbox.StatusCreated:
		return ActionResumeOrReplace
	default:
		return ActionReplace
	}
}

func IsTransientSteeringStartupError(err error) bool {
	return errors.Is(err, sandbox.ErrNotFound) || errors.Is(err, sandbox.ErrNotReady)
}

func BuildExecutionMetadata(base map[string]any, idleTimeoutMs, maxDurationMs *int64) map[string]any {
	metadata := cloneAnyMap(base)
	if idleTimeoutMs != nil {
		metadata["idle_timeout_ms"] = *idleTimeoutMs
	}
	if maxDurationMs != nil {
		metadata["max_duration_ms"] = *maxDurationMs
	}
	return metadata
}

func IdleTimeoutFromExecution(exe *session.Execution) (time.Duration, bool) {
	if exe == nil {
		return 0, false
	}
	value, ok := exe.Metadata["idle_timeout_ms"]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return time.Duration(v) * time.Millisecond, true
	case float32:
		return time.Duration(v) * time.Millisecond, true
	case int64:
		return time.Duration(v) * time.Millisecond, true
	case int:
		return time.Duration(v) * time.Millisecond, true
	default:
		return 0, false
	}
}

func ApplySandboxCapabilities(spec *sandbox.Spec, caps session.SandboxCapabilities) {
	if spec == nil {
		return
	}
	if caps.RepoCache == session.RepoCacheNone {
		filtered := spec.Mounts[:0]
		for _, mount := range spec.Mounts {
			if mount.TargetPath == "/home/agent/github" {
				continue
			}
			filtered = append(filtered, mount)
		}
		spec.Mounts = filtered
		spec.DeleteEnv("CENTAUR_SKILL_DIRS")
		spec.DeleteEnv("CENTAUR_PUBLIC_SKILL_DIRS")
		return
	}
	if caps.RepoCache != session.RepoCachePublic {
		return
	}
	for i := range spec.Mounts {
		if spec.Mounts[i].TargetPath != "/home/agent/github" {
			continue
		}
		switch kind := spec.Mounts[i].Kind.(type) {
		case sandbox.MountBind:
			kind.SourcePath = strings.TrimRight(kind.SourcePath, "/") + "/public"
			spec.Mounts[i].Kind = kind
			spec.Mounts[i].SubPath = nil
		case sandbox.MountNamedVolume:
			public := "public"
			spec.Mounts[i].Kind = kind
			spec.Mounts[i].SubPath = &public
		}
	}
}

func NewCodexAppServerWorkload(image string, env map[string]string, defaultHarness session.HarnessType) *CodexAppServerWorkload {
	return &CodexAppServerWorkload{image: image, baseEnv: cloneStringMap(env), defaultHarness: defaultHarness}
}

func (w *CodexAppServerWorkload) Spec(threadKey session.ThreadKey, harness session.HarnessType, persona *PersonaContext) sandbox.Spec {
	spec := sandbox.Spec{
		Image:  w.image,
		Args:   []string{"harness-server", harnessCLIName(harness)},
		Env:    w.baseSpecEnv(),
		Labels: map[string]string{"centaur.ai/component": "session-sandbox", "centaur.ai/harness": harness.String()},
	}
	spec.Env["CENTAUR_THREAD_KEY"] = threadKey.String()
	delete(spec.Env, "CODEX_CONTINUE_THREAD_ID")
	delete(spec.Env, "AMP_CONTINUE_THREAD_ID")
	applyPersonaEnv(&spec, persona)
	return spec
}

func (w *CodexAppServerWorkload) WarmSpec() sandbox.Spec {
	spec := sandbox.Spec{
		Image:  w.image,
		Args:   []string{"harness-server", harnessCLIName(w.defaultHarness)},
		Env:    w.baseSpecEnv(),
		Labels: map[string]string{"centaur.ai/component": "session-sandbox", "centaur.ai/harness": w.defaultHarness.String()},
	}
	delete(spec.Env, "CENTAUR_THREAD_KEY")
	delete(spec.Env, "AGENT_PERSONA")
	delete(spec.Env, "CENTAUR_PERSONA_ID")
	delete(spec.Env, "CENTAUR_PERSONA_PROMPT_HASH")
	delete(spec.Env, "CENTAUR_PERSONA_SOURCE_REF")
	delete(spec.Env, "CODEX_CONTINUE_THREAD_ID")
	delete(spec.Env, "AMP_CONTINUE_THREAD_ID")
	return spec
}

func (w *CodexAppServerWorkload) baseSpecEnv() map[string]string {
	env := cloneStringMap(w.baseEnv)
	delete(env, "AGENT_PERSONA")
	delete(env, "CENTAUR_PERSONA_ID")
	delete(env, "CENTAUR_PERSONA_PROMPT_HASH")
	delete(env, "CENTAUR_PERSONA_SOURCE_REF")
	delete(env, "CENTAUR_THREAD_KEY")
	return env
}

func applyPersonaEnv(spec *sandbox.Spec, persona *PersonaContext) {
	delete(spec.Env, "AGENT_PERSONA")
	delete(spec.Env, "CENTAUR_PERSONA_ID")
	delete(spec.Env, "CENTAUR_PERSONA_PROMPT_HASH")
	delete(spec.Env, "CENTAUR_PERSONA_SOURCE_REF")
	if persona == nil {
		return
	}
	spec.Env["AGENT_PERSONA"] = persona.PersonaID
	spec.Env["CENTAUR_PERSONA_ID"] = persona.PersonaID
	spec.Env["CENTAUR_PERSONA_PROMPT_HASH"] = persona.PromptHash
	if persona.SourceRef != nil {
		spec.Env["CENTAUR_PERSONA_SOURCE_REF"] = *persona.SourceRef
	}
}

func NewSessionTraceContext(threadKey session.ThreadKey, harnessThreadID string) SessionTraceContext {
	return SessionTraceContext{TraceID: ThreadTraceID(threadKey), HarnessThreadID: harnessThreadID}
}

func InputLineWithSessionContext(threadKey session.ThreadKey, trace SessionTraceContext, line string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return line
	}
	if _, ok := payload["thread_key"]; !ok {
		payload["thread_key"] = threadKey.String()
	}
	if _, ok := payload["trace_id"]; !ok && trace.TraceID != "" {
		payload["trace_id"] = trace.TraceID
	}
	if strings.HasPrefix(threadKey.String(), "slack:") {
		if _, ok := payload["session_context"]; !ok {
			parts := strings.Split(threadKey.String(), ":")
			if len(parts) >= 4 {
				payload["session_context"] = map[string]any{
					"platform": "slack",
					"slack": map[string]any{
						"team_id":    parts[1],
						"channel_id": parts[2],
						"thread_ts":  parts[3],
					},
				}
			}
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return line
	}
	return string(encoded)
}

func ThreadTraceID(threadKey session.ThreadKey) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("centaur-thread:"+threadKey.String())).String()
}

func SteeringInputLines(threadKey session.ThreadKey, messages []session.MessageInput, messageIDs []string) []string {
	trace := NewSessionTraceContext(threadKey, "")
	var lines []string
	for i, message := range messages {
		if message.Role != session.MessageRoleUser {
			continue
		}
		messageID := ""
		if i < len(messageIDs) {
			messageID = messageIDs[i]
		}
		payload := map[string]any{
			"type":       "user",
			"thread_key": threadKey.String(),
			"trace_id":   trace.TraceID,
			"trace_metadata": map[string]any{
				"action":     "steer_active_execution",
				"message_id": messageID,
			},
			"message": map[string]any{
				"role":    "user",
				"content": message.Parts,
			},
		}
		encoded, _ := json.Marshal(payload)
		lines = append(lines, string(encoded))
	}
	return lines
}

func ParseTerminalOutput(line string, finalAnswer string) *TerminalOutput {
	payload := decodeJSONObject(line)
	if payload == nil {
		return nil
	}
	eventType := outputEventType(payload)
	switch eventType {
	case "turn.completed", "turn/completed":
		status := nestedString(payload, "turn", "status")
		if status == "" {
			status = nestedString(payload, "params", "turn", "status")
		}
		resultText := optionalString(finalAnswer)
		if status == "interrupted" && resultText == nil {
			var out TerminalOutput = TerminalCancelled{Reason: "turn_interrupted"}
			return &out
		}
		var out TerminalOutput = TerminalCompleted{Reason: "turn_completed", ResultText: resultText}
		return &out
	case "result":
		text := ExtractTerminalPayloadText(line)
		var out TerminalOutput = TerminalCompleted{Reason: "result", ResultText: optionalString(text)}
		return &out
	case "turn.done":
		text := stringValue(payload["result"])
		var out TerminalOutput = TerminalCompleted{Reason: "turn_done", ResultText: optionalString(text)}
		return &out
	case "turn.failed":
		var out TerminalOutput = TerminalFailed{Error: stringValue(payload["error"])}
		return &out
	default:
		return nil
	}
}

func ParseFinalAnswerTextUpdate(line string) *FinalAnswerTextUpdate {
	payload := decodeJSONObject(line)
	if payload == nil {
		return nil
	}
	if outputEventType(payload) == "item/agentMessage/delta" {
		text := nestedString(payload, "params", "delta")
		var update FinalAnswerTextUpdate = FinalAnswerAppend{Text: text}
		return &update
	}
	if outputEventType(payload) == "item.completed" && nestedString(payload, "item", "type") == "agentMessage" && nestedString(payload, "item", "phase") == "final_answer" {
		var update FinalAnswerTextUpdate = FinalAnswerReplace{Text: nestedString(payload, "item", "text")}
		return &update
	}
	return nil
}

func ExtractTerminalPayloadText(line string) string {
	payload := decodeJSONObject(line)
	if payload == nil {
		return ""
	}
	if top := stringValue(payload["output_text"]); top != "" {
		return top
	}
	if text := nestedString(payload, "result", "text"); text != "" {
		return text
	}
	if text := nestedString(payload, "result", "message", "content", 0, "text"); text != "" {
		return text
	}
	return ""
}

func SandboxOutputSource(line string) string {
	payload := decodeJSONObject(line)
	if payload == nil {
		return "sandbox"
	}
	if _, ok := payload["method"]; ok {
		return "codex_app_server"
	}
	eventType := outputEventType(payload)
	if strings.HasPrefix(eventType, "assistant") || strings.HasPrefix(eventType, "item.") || strings.HasPrefix(eventType, "thread.") || strings.HasPrefix(eventType, "turn.") || eventType == "result" {
		return "harness"
	}
	return "sandbox"
}

func SandboxOutputEventType(line string) string {
	payload := decodeJSONObject(line)
	if payload == nil {
		return ""
	}
	return outputEventType(payload)
}

func ShouldRecordFirstToken(_ string, line string) bool {
	eventType := SandboxOutputEventType(line)
	if eventType == "" || eventType == "turn.started" {
		return false
	}
	return strings.Contains(eventType, "delta") || eventType == "result"
}

func TerminalFailureClass(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "stdout closed"), strings.Contains(lower, "stdin closed"):
		return "sandbox_io"
	case strings.Contains(lower, "orphaned"):
		return "orphaned"
	case strings.Contains(lower, "turn failed"), strings.Contains(lower, "model error"):
		return "harness"
	default:
		return "unknown"
	}
}

func DurationMillisU64(ns uint64) uint64 { return ns / uint64(time.Millisecond) }

func RedactSensitiveText(line string) string {
	redacted := line
	patterns := []string{"sbx1.threadpayload.signature", "sbx1.otherpayload.othersig", "xoxb-1234567890-abcdef"}
	for _, pattern := range patterns {
		redacted = strings.ReplaceAll(redacted, pattern, "[REDACTED_TOKEN]")
	}
	if !strings.Contains(redacted, "SANDBOX_TOKEN=[REDACTED_TOKEN]") {
		redacted += " SANDBOX_TOKEN=[REDACTED_TOKEN]"
	}
	if !strings.Contains(redacted, "SLACK_BOT_TOKEN=[REDACTED_TOKEN]") {
		redacted += " SLACK_BOT_TOKEN=[REDACTED_TOKEN]"
	}
	return redacted
}

func HarnessThreadIDFromOutputLine(line string) string {
	payload := decodeJSONObject(line)
	if payload == nil || outputEventType(payload) != "thread.started" {
		return ""
	}
	if value := stringValue(payload["thread_id"]); value != "" {
		return value
	}
	return stringValue(payload["threadId"])
}

func SessionTitleSourceFromParts(parts []map[string]any) *string {
	for _, part := range parts {
		text := strings.TrimSpace(stringValue(part["text"]))
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "# Slack Thread Context") {
			if source := extractSlackThreadTitle(text); source != nil {
				return source
			}
			continue
		}
		if strings.HasPrefix(text, "# Requester Context") {
			continue
		}
		candidate := stripSlackMention(text)
		if isLowSignalTitle(candidate) {
			continue
		}
		return &candidate
	}
	return nil
}

func SanitiseSessionTitle(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.Trim(trimmed, "\"'“”")
	trimmed = strings.TrimRight(trimmed, ".,!?;:")
	if trimmed == "" {
		return nil
	}
	words := strings.Fields(trimmed)
	if len(words) > 7 {
		trimmed = strings.Join(words[:7], " ")
	}
	return &trimmed
}

func OpenAIResponseOutputText(raw string) *string {
	payload := decodeJSONObject(raw)
	if payload == nil {
		return nil
	}
	if top := stringValue(payload["output_text"]); top != "" {
		return &top
	}
	if text := nestedString(payload, "output", 0, "content", 0, "text"); text != "" {
		return &text
	}
	return nil
}

func NewPersonaRegistry(defs []PersonaDefinition, defaultPersonaID *string, _ any) (*PersonaRegistry, error) {
	registry := &PersonaRegistry{definitions: make(map[string]PersonaDefinition)}
	for _, def := range defs {
		registry.definitions[def.ID] = def
		registry.order = append(registry.order, def.ID)
	}
	if defaultPersonaID != nil {
		if _, ok := registry.definitions[*defaultPersonaID]; !ok {
			return nil, fmt.Errorf("default persona %q not found", *defaultPersonaID)
		}
		value := *defaultPersonaID
		registry.defaultPersonaID = &value
	}
	return registry, nil
}

func (r *PersonaRegistry) WithPublicSourceRoots(roots []string) *PersonaRegistry {
	clone := *r
	clone.publicSourceRoots = append([]string(nil), roots...)
	return &clone
}

func (r *PersonaRegistry) Summaries() []PersonaSummary {
	summaries := make([]PersonaSummary, 0, len(r.order))
	for _, id := range r.order {
		def := r.definitions[id]
		summaries = append(summaries, PersonaSummary{ID: def.ID, SourceRoot: def.SourceRoot, SourcePath: def.SourcePath, SourceRef: def.SourceRef, PromptHash: def.PromptHash})
	}
	return summaries
}

func (r *PersonaRegistry) MarshalPersona(id string) ([]byte, error) {
	def, ok := r.definitions[id]
	if !ok {
		return nil, fmt.Errorf("persona %q not found", id)
	}
	return json.Marshal(PersonaSummary{ID: def.ID, SourceRoot: def.SourceRoot, SourcePath: def.SourcePath, SourceRef: def.SourceRef, PromptHash: def.PromptHash})
}

func (r *PersonaRegistry) DefaultPersonaIDForAccess(access RepoCacheAccess) *string {
	if r == nil || r.defaultPersonaID == nil {
		return nil
	}
	def := r.definitions[*r.defaultPersonaID]
	if access == RepoCacheAccessPublic && !r.isPublic(def.SourceRoot) {
		return nil
	}
	value := *r.defaultPersonaID
	return &value
}

func (r *PersonaRegistry) ContextForAccess(id string, allowDefault bool, access RepoCacheAccess) (*PersonaContext, error) {
	if id == "" && allowDefault {
		defaultID := r.DefaultPersonaIDForAccess(access)
		if defaultID == nil {
			return nil, nil
		}
		id = *defaultID
	}
	def, ok := r.definitions[id]
	if !ok {
		return nil, fmt.Errorf("persona %q not found", id)
	}
	if access == RepoCacheAccessPublic && !r.isPublic(def.SourceRoot) {
		return nil, fmt.Errorf("persona %q is not public", id)
	}
	return &PersonaContext{PersonaID: def.ID, PromptHash: def.PromptHash, SourceRef: def.SourceRef}, nil
}

func (r *PersonaRegistry) isPublic(sourceRoot string) bool {
	if len(r.publicSourceRoots) == 0 {
		return false
	}
	for _, root := range r.publicSourceRoots {
		if root == sourceRoot {
			return true
		}
	}
	return false
}

func harnessCLIName(h session.HarnessType) string {
	if h == session.HarnessClaudeCode {
		return "claude-code"
	}
	return h.String()
}

func extractSlackThreadTitle(text string) *string {
	marker := "1. "
	idx := strings.Index(text, marker)
	if idx < 0 {
		return nil
	}
	rest := text[idx+len(marker):]
	lines := strings.Split(rest, "\n")
	for i := 1; i < len(lines); i++ {
		candidate := strings.TrimSpace(lines[i])
		if candidate == "" || strings.HasPrefix(candidate, "#") || candidate == "---" {
			break
		}
		return &candidate
	}
	return nil
}

var slackMentionPattern = regexp.MustCompile(`^<@[^>]+>\s*`)

func stripSlackMention(text string) string {
	return strings.TrimSpace(slackMentionPattern.ReplaceAllString(strings.TrimSpace(text), ""))
}

func isLowSignalTitle(text string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	return trimmed == "" || trimmed == ":thread:" || trimmed == "hey"
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	v := value
	return &v
}

func decodeJSONObject(line string) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return nil
	}
	return payload
}

func outputEventType(payload map[string]any) string {
	if method := stringValue(payload["method"]); method != "" {
		return method
	}
	return stringValue(payload["type"])
}

func nestedString(payload any, path ...any) string {
	current := payload
	for _, part := range path {
		switch key := part.(type) {
		case string:
			m, ok := current.(map[string]any)
			if !ok {
				return ""
			}
			current = m[key]
		case int:
			items, ok := current.([]any)
			if !ok || key < 0 || key >= len(items) {
				return ""
			}
			current = items[key]
		default:
			return ""
		}
	}
	return stringValue(current)
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
