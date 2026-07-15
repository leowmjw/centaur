# Centaur api-go — Behaviour Specification

This document captures every observable behaviour that the Go + Temporal
implementation must reproduce.  It is derived mechanically from the unit,
integration, and E2E tests in `services/api-rs`.  All section numbers are
referenced from the Go test files in this directory.

---

## 1. Wire Types (`internal/session`)

### 1.1 ThreadKey

A `ThreadKey` is a UTF-8 string with these invariants:

| Rule | Example violation |
|------|-------------------|
| Must not be empty | `""` |
| Must not exceed 512 bytes | (a 513-byte string) |
| Must contain exactly one `:` that splits a non-empty namespace from a non-empty id | `"not-namespaced"` |
| Must not start with `{` or `[` (raw JSON guard) | `"{\"thread\":\"x\"}"` |
| Must not contain ASCII control characters (0x00–0x1F, 0x7F) | `"chat:\x00"` |

Valid examples: `"chat:C123:1780000000.000000"`, `"slack:T123:C123:1780000000.000000"`,
`"cli:local"`, `"mcp:test"`.

### 1.2 HarnessType

Three supported values with exact JSON/wire serialisation:

| Go constant | Wire value |
|-------------|-----------|
| `HarnessCodex` | `"codex"` |
| `HarnessAmp` | `"amp"` |
| `HarnessClaudeCode` | `"claudecode"` |

Any other value must be rejected on deserialisation.  `"claude-code"` (with
hyphen) is **not** a valid value.

### 1.3 SessionStatus

`active`, `idle`, `executing`, `failed`, `archived` (snake_case JSON).

### 1.4 ExecutionStatus

`queued`, `running`, `completed`, `failed`, `cancelled` (snake_case JSON).

### 1.5 MessageRole

`user`, `assistant`, `system`, `tool` (lowercase JSON).

### 1.6 SandboxRepoCacheAccess

`none`, `public`, `all` (lowercase).  Default is `all`.  Only `public` and
`all` are considered "enabled" (`repo_cache_enabled() == true`).

---

## 2. Session Runtime Behaviours (`internal/runtime`)

### 2.1 Terminal output detection

The runtime reads NDJSON lines from the sandbox stdout and detects terminal
events.  Given the input line JSON, the expected `TerminalOutput` is:

| Input event shape | Prior answer text | Result |
|-------------------|-------------------|--------|
| `{"type":"turn.completed","turn":{"id":"t","status":"completed"}}` | `""` | `Completed{reason:"turn_completed", result_text:nil}` |
| `{"method":"turn/completed","params":{"turn":{"id":"t","status":"completed"}}}` | `"Final answer"` | `Completed{reason:"turn_completed", result_text:"Final answer"}` |
| `{"type":"turn.completed","turn":{"id":"t","status":"interrupted"}}` | `""` | `Cancelled{reason:"turn_interrupted"}` |
| `{"method":"turn/completed","params":{"turn":{"id":"t","status":"interrupted"}}}` | `"Final answer"` | `Completed{reason:"turn_completed", result_text:"Final answer"}` |
| `{"type":"result","result":{"text":"Final answer"}}` | `""` | `Completed{reason:"result", result_text:"Final answer"}` |
| `{"type":"turn.done","result":"Final answer"}` | `""` | `Completed{reason:"turn_done", result_text:"Final answer"}` |
| `{"type":"turn.failed","error":"sandbox exited"}` | `""` | `Failed{error:"sandbox exited"}` |

#### Final answer text accumulation

`item.agentMessage.delta` lines with a `delta` key append to the running
answer buffer (`FinalAnswerTextUpdate.Append`).

`item.completed` lines whose `item.type == "agentMessage"` and
`item.phase == "final_answer"` replace the buffer with `item.text`
(`FinalAnswerTextUpdate.Replace`).

When a `turn.completed` terminal is encountered with an empty buffer and a
prior `item.completed` message set the buffer, that buffer text is used as
`result_text`.

#### Nested result text normalisation

`{"result":{"message":{"content":[{"type":"text","text":"X"}]}}}` normalises
to `"X"`.

### 2.2 Stdout output source classification

| Event shape | `source` | `event_type` |
|-------------|---------|--------------|
| Has `method` key | `"codex_app_server"` | the method string |
| Has `type` key, value is `"assistant"` or contains `"agentMessage"` | `"harness"` | type string |
| Has `type` key, other values | `"sandbox"` | type string |

### 2.3 First-token detection

`ShouldRecordFirstToken(executionID, line)` returns `true` when the line
contains `delta` or `result` content:

- `{"type":"turn.started","turn_id":"t"}` → false
- `{"type":"item.agentMessage.delta",...,"delta":"Hello"}` → true
- `{"type":"result","result":{"text":"Done"}}` → true

### 2.4 Terminal failure class (low-cardinality metric label)

| Error message substring | Class |
|------------------------|-------|
| `"sandbox stdout closed"` | `"sandbox_io"` |
| `"execution orphaned"` | `"orphaned"` |
| `"turn failed:"` | `"harness"` |
| anything else | `"unknown"` |

### 2.5 Execution metadata

`ExecutionMetadata(userMeta, idleTimeoutMS, maxDurationMS)` merges user
metadata with `idle_timeout_ms` and `max_duration_ms` keys.

`IdleTimeoutFromExecution(execution)` reads `metadata.idle_timeout_ms` as
`time.Duration` (milliseconds).  Returns `(duration, true)` when the key is
present and numeric.

### 2.6 Output line redaction

Output lines containing bearer tokens, sandbox tokens, or `SLACK_BOT_TOKEN`
values must have those values replaced with `[REDACTED_TOKEN]`.

Patterns to redact:
- `Authorization: ****** — replace token
- `SANDBOX_TOKEN=<token>` — replace token (format `sbx1.<payload>.<sig>`)
- `SLACK_BOT_TOKEN=<token>` — replace token (format `xoxb-...`)

### 2.7 Harness thread ID extraction

`HarnessThreadIDFromOutputLine(line)` reads `thread_id` or `threadId` from
`{"type":"thread.started",...}` events.  Returns empty string for other event
types.

### 2.8 Steering input lines

`SteeringInputLines(threadKey, messages, messageIDs)` returns one NDJSON line
per **user-role** message.  Assistant/system/tool messages are skipped.

Each line is a JSON object with:
- `"type": "user"`
- `"thread_key": <threadKey>`
- `"trace_metadata": {"action":"steer_active_execution","message_id":<id>}`
- `"message": {"content": <parts>}`

### 2.9 Input line session context enrichment

`InputLineWithSessionContext(threadKey, trace, rawLine)` enriches JSON objects:

- Adds `thread_key` (does **not** override an existing `thread_key` in rawLine).
- Adds `trace_id` (does **not** override existing).
- Adds `traceparent` when available in the trace context.
- For Slack thread keys (`slack:T:C:ts`), adds
  `session_context.platform = "slack"`,
  `session_context.slack = {team_id, channel_id, thread_ts}`.
- Merges into an existing `session_context` object without overwriting existing keys.
- Non-JSON lines are returned unchanged.

### 2.10 Thread trace ID determinism

`ThreadTraceID(threadKey)` returns a canonical UUID v5 that is:
- Stable for the same thread key across calls.
- Different for different thread keys.
- A valid UUID string parseable by `uuid.Parse`.

### 2.11 Sandbox capability application

Given a `SandboxSpec` and `SandboxCapabilities`:

**Public repo cache:**
- Sets `capabilities.repo_cache = "public"` on the spec.
- For Bind mounts targeting `/home/agent/github`, appends `/public` to `source_path`.
- For NamedVolume mounts targeting `/home/agent/github`, sets `sub_path = "public"`.
- Filters `CENTAUR_SKILL_DIRS` to only paths listed in `CENTAUR_PUBLIC_SKILL_DIRS`.
- Removes `CENTAUR_PUBLIC_SKILL_DIRS` env var.

**None repo cache:**
- Sets `capabilities.repo_cache = "none"`.
- Removes all mounts targeting `/home/agent/github`.
- Removes `CENTAUR_SKILL_DIRS` and `CENTAUR_PUBLIC_SKILL_DIRS` env vars.

### 2.12 Sandbox workload specs

The workload `spec(threadKey, harnessType, personaCtx)` must:
- Set args to `["harness-server", <harness-cli-name>]` where the harness name mapping is:
  - `codex` → `"codex"`, `claudecode` → `"claude-code"`, `amp` → `"amp"`.
- Set `command = nil` (image entrypoint must not be overridden).
- Set label `centaur.ai/component = "session-sandbox"`.
- Set label `centaur.ai/harness = <wire-value>`.
- Set `CENTAUR_THREAD_KEY` env var.
- Not inject `CODEX_CONTINUE_THREAD_ID` or `AMP_CONTINUE_THREAD_ID` on a fresh spec.

The warm spec (`warmSpec()`) must:
- Not set `CENTAUR_THREAD_KEY`.
- Use the configured default harness for its args.

Persona context application:
- Replaces `AGENT_PERSONA` with `persona.id`.
- Sets `CENTAUR_PERSONA_ID`, `CENTAUR_PERSONA_PROMPT_HASH`, `CENTAUR_PERSONA_SOURCE_REF`.
- Warm spec must not have persona env vars.

### 2.13 Idle pause guard

`ShouldPauseIdleSandbox(session, latestExecution, pauseExecutionID, sandboxID)` returns
`true` only when:
- `latestExecution.execution_id == pauseExecutionID`
- `latestExecution.status == Completed`
- `session.sandbox_id == sandboxID`

Returns `false` for running executions, newer executions, or sandbox ID mismatch.

### 2.14 Event stream attach guard

`ShouldAttachSessionPipe(sandboxStatus)` returns `true` only for `Running`.

### 2.15 Existing sandbox action

| Sandbox status | Action |
|---------------|--------|
| `Running` | `Reuse` |
| `Suspended`, `Created` | `ResumeOrReplace` |
| `Stopped`, `Gone`, `Unknown` | `Replace` |

### 2.16 Steering startup error classification

`IsTransientSteeringStartupError(err)` returns `true` for `NotReady` and
`NotFound` sandbox errors.  Returns `false` for IO errors and store errors.

---

## 3. HTTP API Contract (`cmd/api`)

All paths are prefixed with `/api`.

### 3.1 Session endpoints

#### POST /api/session/{thread_key}
Create or retrieve a session.

Request body:
```json
{"harness_type": "codex|amp|claudecode", "metadata": {}}
```

Conflict rules:
- If the session already exists with a different `harness_type`, return `409 Conflict`.
- If the session already exists with a different `persona_id`, return `409 Conflict`.

Response: `Session` object.

#### POST /api/session/{thread_key}/messages
Append one or more messages to the session transcript.

Request body:
```json
[{"role":"user","parts":[...],"metadata":{}}]
```

Idempotent by `client_message_id` (if provided).

#### POST /api/session/{thread_key}/execute
Create and claim an execution.

Idempotent by `idempotency_key` (returns existing execution if already created).

#### GET /api/session/{thread_key}/events
Server-sent event stream.  Parameters:
- `after_event_id` (optional): only emit events with `event_id > after_event_id`.

Each SSE event has `data: <JSON event>`.  The stream ends with a terminal event
(`execution.completed`, `execution.failed`, `execution.cancelled`).

### 3.2 Health/readiness

| Path | When healthy |
|------|-------------|
| `GET /healthz` | Always `{"ok":true}` |
| `GET /readyz` | `{"ok":true,"ready":true}` once the runtime is initialised |

Calls to session routes while not ready must return `503 Service Unavailable`.

### 3.3 Metrics

`GET /metrics` must expose Prometheus metrics including:
- `http_requests_total{method, path, status}` for each handled request.

### 3.4 Harness wire values

The three harness types must round-trip through the session create endpoint
with exact wire values: `"codex"`, `"amp"`, `"claudecode"`.

---

## 4. Workflows API Contract

### 4.1 Workflow management endpoints

#### GET /api/workflows
Lists all known workflow names and their current state (running, cancelled, etc.)

#### POST /api/workflows/{workflow_name}/runs
Creates a new workflow run.

#### GET /api/workflows/{workflow_name}/runs/{run_id}
Returns run status.

### 4.2 Workflow reconciliation behaviour

When workflow definitions are **added** to `WORKFLOW_DIRS`, the runtime must
schedule them on the next reconcile tick.

When workflow definitions are **removed** from `WORKFLOW_DIRS`, the runtime
must cancel running instances after `WORKFLOW_REAP_REMOVED_AFTER_TICKS`
consecutive absence detections.

---

## 5. Session Store Behaviours (`internal/store`)

### 5.1 Idle sandbox candidate selection

`ListIdleSandboxCandidates(backstop Duration)` returns sessions whose latest
terminal execution has been complete longer than their idle timeout.

The idle timeout is read from `execution.metadata.idle_timeout_ms` (milliseconds).
When that key is missing or non-numeric, the caller-supplied `backstop` is used.

When the persisted idle timeout has **not** yet elapsed (even if `backstop`
has), the candidate is **not** returned.

### 5.2 Stdout owner lease

`ClaimStdoutOwner(executionID, ownerID, ttl)` grants exclusive ownership.
Only one owner may append events at a time.

`AppendEventIfStdoutOwner(threadKey, executionID, ownerID, ttl, eventType, payload)`
returns `(eventID, nil)` when the caller is the current owner, and
`(nil, nil)` (fence) when another owner holds the lease.

`ReleaseStdoutOwnedExecutions(ownerID)` releases all leases held by the
specified owner (used on shutdown handoff).

### 5.3 Warm eviction reservation

`ReserveWarmEviction(threadKey, sandboxID)` blocks a subsequent warm-pool
claim for the same sandbox.  A later `ClaimWarmSandbox` for the same sandbox
must fail after the reservation is made.

### 5.4 Notification payload

Postgres NOTIFY on channel `centaur_session_events` sends JSON:
```json
{"thread_key":"<key>","event_id":<i64>}
```

### 5.5 Session create idempotency

`CreateOrGetSession(threadKey, harnessType, personaID, metadata)`:
- Creates the row if absent.
- Returns the existing row if present.
- Returns `ErrHarnessConflict` if existing harness differs.
- Returns `ErrPersonaConflict` if existing persona differs.

---

## 6. Database Row-Level Security Behaviours (`internal/store`)

### 6.1 ETL context RLS

When the Postgres session variable `app.slack_channel_id` is set to `C_ALPHA`,
only rows with `channel_id = 'C_ALPHA'` are visible from `centaur_slack_reader`.

When the variable is set to `""` or not set, no rows are visible.

The `centaur_readonly` role sees all non-private rows regardless of channel setting.

### 6.2 Slack DM context RLS

Only rows for conversations where `user_id` is a **current** member are
visible to `centaur_slack_reader` with `app.slack_user_id = '<user>'`.

Former members (membership ended) see no rows.

---

## 7. Sandbox Lifecycle Behaviours (`internal/sandbox`)

### 7.1 Create → Stop lifecycle

After `Create(spec)` succeeds, calling `Stop(id)` must transition the sandbox
to `Stopped` status.  `ListObserved()` must not return the sandbox after stop.

### 7.2 Pause / Resume

`Pause(id)` on a Running sandbox must transition it to `Suspended`.
`Resume(id)` must restore it to `Running`.

### 7.3 Unexpected shutdown / drift detection

When a sandbox process exits unexpectedly, `Status(id)` must return `Gone`
or `Unknown("...")`; it must not continue to report `Running`.

### 7.4 Missing sandbox consistency

Operations on a non-existent sandbox ID must return `ErrNotFound`, not a
nil error or a panic.

### 7.5 IO round-trip

Bytes written to `stdin` must be readable from `stdout` (for the echo test
process used in the local sandbox implementation).

### 7.6 Stdin drop closes write half

When the `SandboxWrite` handle is closed/dropped, the sandbox process should
observe EOF on its stdin.

### 7.7 Pause blocks IO until resume

While a sandbox is `Suspended`, read and write operations must block.  After
`Resume`, they must proceed.

### 7.8 Reconnect / multi-observer

A second call to `Open(id)` on a running sandbox must return a new pair of
`(SandboxRead, SandboxWrite)` handles that observe the same output stream.

### 7.9 Failed create cleanup

When `Create(spec)` fails partway through, any partially-created Kubernetes
resources must be cleaned up.  A subsequent `ListObserved()` must not include
the failed sandbox.

---

## 8. Orphan Reap Behaviours (`internal/runtime`)

### 8.1 Two consecutive passes required

An unreferenced running sandbox is **not** reaped on the first observation.
It is added to a pending set.  On the **second** consecutive pass where it is
still unreferenced and running, it is reaped.

### 8.2 Reference rescue

If a sandbox appears in the pending set but becomes referenced before the
second pass, it is removed from the pending set without being reaped.

### 8.3 Terminal sandboxes excluded

Sandboxes in `Created`, `Stopped`, or `Gone` status are never candidates for
orphan reaping.

### 8.4 Failed stop retry

When `Stop()` returns an error, the sandbox stays in the pending set for
retry on the next pass.

### 8.5 Vanished pending orphan

When a sandbox disappears from `ListObserved()` while in the pending set, it
is silently removed from the pending set.

---

## 9. Title Generation Behaviours (`internal/runtime`)

### 9.1 Source preference

Given a user message with multiple text parts:
- Prefers the text after a bot mention (strips the `<@Uxxxx>` prefix) over
  Slack thread context.
- Uses the first meaningful message from `# Slack Thread Context` when the
  direct ask is a low-signal wakeup (e.g. just an emoji or a bare mention).

### 9.2 Low-signal wakeup detection

The following are considered low-signal:
- Only an `<@Uxxxx>` mention with no subsequent text.
- A single emoji or short reaction such as `:thread:`.

### 9.3 Title sanitisation

`SanitiseSessionTitle(raw)`:
- Strips leading/trailing quotes and trailing punctuation.
- Truncates to 50 characters at a word boundary (no mid-word cut).
- Returns empty string for blank input.

### 9.4 OpenAI Responses API shape

The title generator reads `output_text` from the top-level JSON or recursively
from `output[*].content[*].text`.

---

## 10. Persona Registry Behaviours (`internal/runtime`)

### 10.1 Default persona validation

Constructing a registry with a `defaultPersonaID` that is not in the persona
set must return an error.

### 10.2 Public source root restriction

When `publicSourceRoots` is configured, a persona whose `sourceRoot` is not
in that set must **not** be returned for `Public` repo-cache access.

The default persona is not applied for `Public` access if it lives in a
non-public source root.

---

## 11. Workflow Runtime Behaviours (`workflows/`)

### 11.1 Schedule normalisation

Interval schedules must normalise their delivery metadata (next delivery time
calculated from the configured interval starting at `t0`).

Cron schedules must respect the configured timezone.

### 11.2 Stale workflow cancellation

A workflow that disappears from `WORKFLOW_DIRS` must be cancelled after
`reap_removed_after_ticks` consecutive absences.

A workflow that reappears before the threshold resets the counter.

A threshold of `0` disables the reaping selection entirely.

### 11.3 Worker concurrency

`ParseWorkerConcurrency(override)` uses the override when set, otherwise reads
`GOMAXPROCS`.

---

## Appendix A — Full API lifecycle test cases (from centaur-api-integration-test)

These are the end-to-end assertions the API integration test must pass:

1. **Health** — `GET /healthz` returns `{"ok":true}`.
2. **Readiness** — `GET /readyz` returns `{"ok":true,"ready":true}`.
3. **Harness wire values** — Each harness type round-trips through
   `POST /api/session/{thread}` with its exact wire value.
4. **Session turn** — A complete create/append/execute/events cycle:
   a. Create session with `harness_type: "codex"`.
   b. Append user message.
   c. Execute (idempotency key must de-duplicate).
   d. Consume event stream until terminal event.
   e. Terminal event must contain non-empty `result_text`.
5. **Workflows API** — Add a workflow, wait for it to be scheduled; remove it,
   verify it is cancelled.
6. **Metrics** — `GET /metrics` returns HTTP request counters.

---

## Appendix B — Sandbox E2E test cases (from centaur-sandbox-e2e)

These require the sandbox infrastructure to be running:

1. `create_stop_cleans_up` — create → stop → verify gone.
2. `pause_resume_restores_running` — create → pause → verify suspended → resume → verify running.
3. `unexpected_shutdown_reports_drift` — kill process → verify status is Gone/Unknown.
4. `missing_sandbox_operations_are_consistent` — all ops on missing ID return NotFound.
5. `byte_io_round_trips` — write to stdin, read from stdout.
6. `stdin_drop_closes_write_half` — close write handle, process observes EOF.
7. `pause_blocks_read_write_until_resume` — pause, attempt IO (must block), resume, IO proceeds.
8. `reconnect_can_observe_and_stop` — open second handle on running sandbox, observe output.
9. `failed_create_cleans_up_observed_resources` — force-fail create, verify no observed resources.
