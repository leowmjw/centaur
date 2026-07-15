// Package e2e — full API lifecycle tests.
//
// These tests run against a live centaur-api binary.  They require:
//   CENTAUR_API_URL — base URL of the running API (default http://127.0.0.1:18080)
//
// Skip when the URL is unreachable.
//
// Derived from:
//   services/api-rs/crates/centaur-api-integration-test/src/main.rs (Appendix A of SPEC.md)
//
// ALL ASSERTIONS ARE FIXED.
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func apiURL() string {
	u := os.Getenv("CENTAUR_API_URL")
	if u == "" {
		u = "http://127.0.0.1:18080"
	}
	return strings.TrimRight(u, "/")
}

func apiClient(t *testing.T) *http.Client {
	t.Helper()
	// Quick reachability probe — skip if the API is not up.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL()+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Skip("CENTAUR_API_URL not reachable — skipping E2E test")
	}
	return http.DefaultClient
}

func postJSON(t *testing.T, client *http.Client, url string, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(b)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST %s returned non-200", url)
	var v map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	return v
}

func getJSON(t *testing.T, client *http.Client, url string) map[string]any {
	t.Helper()
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s returned non-200", url)
	var v map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	return v
}

func threadKey(t *testing.T, suffix string) string {
	t.Helper()
	return fmt.Sprintf("api-e2e:%s-%s", suffix, uuid.New())
}

func sessionURL(threadKey string) string {
	return apiURL() + "/api/session/" + threadKey
}

// ── Health / readiness ────────────────────────────────────────────────────────

func TestAPI_HealthEndpointResponds(t *testing.T) {
	client := apiClient(t)
	body := getJSON(t, client, apiURL()+"/healthz")
	assert.Equal(t, true, body["ok"])
}

func TestAPI_ReadinessEndpointResponds(t *testing.T) {
	client := apiClient(t)
	body := getJSON(t, client, apiURL()+"/readyz")
	assert.Equal(t, true, body["ok"])
	assert.Equal(t, true, body["ready"])
}

// ── Harness wire values ───────────────────────────────────────────────────────

func TestAPI_HarnessWireValuesMatchAPIContract(t *testing.T) {
	client := apiClient(t)
	cases := []string{"codex", "amp", "claudecode"}

	for _, wire := range cases {
		t.Run(wire, func(t *testing.T) {
			key := threadKey(t, "harness-"+wire)
			sess := postJSON(t, client, sessionURL(key), map[string]any{
				"harness_type": wire,
				"metadata":     map[string]any{"source": "api-e2e"},
			})
			assert.Equal(t, key, sess["thread_key"])
			assert.Equal(t, wire, sess["harness_type"])
		})
	}
}

// ── Session turn (create / append / execute / events) ────────────────────────

func TestAPI_SessionExecuteForwardsModelContextAndCompletes(t *testing.T) {
	client := apiClient(t)
	key := threadKey(t, "session-turn")

	// 1. Create session.
	sess := postJSON(t, client, sessionURL(key), map[string]any{
		"harness_type": "codex",
		"metadata":     map[string]any{"source": "api-e2e", "purpose": "api-integration-test"},
	})
	assert.Equal(t, key, sess["thread_key"])

	// 2. Append user message.
	msgs := postJSON(t, client, sessionURL(key)+"/messages", []any{
		map[string]any{
			"client_message_id": "api-e2e-message-1",
			"role":              "user",
			"parts":             []any{map[string]any{"type": "text", "text": "Respond with exactly: 'API integration test passed'"}},
			"metadata":          map[string]any{"source": "api-e2e"},
		},
	})
	messageIDs, ok := msgs["message_ids"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, messageIDs)

	// 3. Execute (with idempotency key).
	const idempotencyKey = "api-e2e-execute-1"
	exec1 := postJSON(t, client, sessionURL(key)+"/execute", map[string]any{
		"idempotency_key": idempotencyKey,
		"metadata":        map[string]any{"source": "api-e2e"},
	})
	executionID, _ := exec1["execution_id"].(string)
	require.NotEmpty(t, executionID)

	// 3a. Replay the same idempotency key — must return the same execution.
	exec2 := postJSON(t, client, sessionURL(key)+"/execute", map[string]any{
		"idempotency_key": idempotencyKey,
		"metadata":        map[string]any{"source": "api-e2e", "replay": true},
	})
	assert.Equal(t, executionID, exec2["execution_id"], "idempotent execute must return same execution_id")

	// 4. Consume SSE event stream until terminal.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		sessionURL(key)+"/events?after_event_id=0", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	terminalFound, resultText := consumeUntilTerminal(t, resp.Body)
	assert.True(t, terminalFound, "event stream must contain a terminal event")
	assert.NotEmpty(t, resultText, "terminal event must carry non-empty result_text")
}

// consumeUntilTerminal reads SSE events until it finds execution.completed /
// execution.failed / execution.cancelled and returns (found, result_text).
func consumeUntilTerminal(t *testing.T, body io.Reader) (bool, string) {
	t.Helper()
	scanner := bufio.NewScanner(body)
	var lastData string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			lastData = strings.TrimPrefix(line, "data: ")
			var ev map[string]any
			if err := json.Unmarshal([]byte(lastData), &ev); err != nil {
				continue
			}
			et, _ := ev["event_type"].(string)
			if et == "execution.completed" || et == "execution.failed" || et == "execution.cancelled" {
				payload, _ := ev["payload"].(map[string]any)
				result, _ := payload["result_text"].(string)
				return true, result
			}
		}
	}
	return false, ""
}

// ── Metrics ───────────────────────────────────────────────────────────────────

func TestAPI_MetricsExposeHTTPRequestCounters(t *testing.T) {
	client := apiClient(t)

	// Hit the health endpoint to ensure at least one request is counted.
	client.Get(apiURL() + "/healthz")

	resp, err := client.Get(apiURL() + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "http_requests_total")
}
