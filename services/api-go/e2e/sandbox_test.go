// Package e2e — sandbox lifecycle end-to-end tests.
//
// These tests require a sandbox backend.  Set SANDBOX_E2E_IMPLS to control
// which implementations to test:
//   SANDBOX_E2E_IMPLS=local        — local process-based backend only
//   SANDBOX_E2E_IMPLS=agent-k8s    — Kubernetes backend only
//   SANDBOX_E2E_IMPLS=all          — both
//
// Additional env vars for agent-k8s:
//   SANDBOX_E2E_K8S_CONTEXT    — kubectl context (default: kind-centaur-api-rs-e2e)
//   SANDBOX_E2E_K8S_NAMESPACE  — Kubernetes namespace (default: centaur-sandbox-e2e)
//   SANDBOX_E2E_K8S_IMAGE      — agent image (default: centaur-agent:latest)
//
// Tests are skipped when SANDBOX_E2E_IMPLS is not set.
//
// Derived from:
//   services/api-rs/crates/centaur-sandbox-e2e/tests/ (Appendix B of SPEC.md)
//
// ALL ASSERTIONS ARE FIXED.
package e2e_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leowmjw/centaur/api-go/internal/sandbox"
)

// impls returns the sandbox implementations requested by SANDBOX_E2E_IMPLS.
// Returns nil (test is skipped) when the variable is not set.
func impls(t *testing.T) []sandbox.Backend {
	t.Helper()
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("SANDBOX_E2E_IMPLS")))
	if raw == "" {
		t.Skip("set SANDBOX_E2E_IMPLS to run sandbox E2E tests")
	}
	var out []sandbox.Backend
	if raw == "all" || strings.Contains(raw, "local") {
		out = append(out, sandbox.NewLocalBackend())
	}
	if raw == "all" || strings.Contains(raw, "agent-k8s") {
		out = append(out, newK8sBackend(t))
	}
	return out
}

// newK8sBackend constructs the Kubernetes sandbox backend from env vars.
func newK8sBackend(t *testing.T) sandbox.Backend {
	t.Helper()
	ctx := os.Getenv("SANDBOX_E2E_K8S_CONTEXT")
	if ctx == "" {
		ctx = "kind-centaur-api-rs-e2e"
	}
	ns := os.Getenv("SANDBOX_E2E_K8S_NAMESPACE")
	if ns == "" {
		ns = "centaur-sandbox-e2e"
	}
	image := os.Getenv("SANDBOX_E2E_K8S_IMAGE")
	if image == "" {
		image = "centaur-agent:latest"
	}
	b, err := sandbox.NewK8sBackend(ctx, ns, image)
	require.NoError(t, err, "create k8s backend")
	return b
}

// echoSpec returns a minimal SandboxSpec that runs an echo/cat process.
func echoSpec(backend sandbox.Backend) sandbox.Spec {
	return backend.EchoSpec()
}

// ── 1. Create → Stop lifecycle ────────────────────────────────────────────────

func TestSandbox_CreateStopCleansUp(t *testing.T) {
	for _, b := range impls(t) {
		t.Run(b.Name(), func(t *testing.T) {
			ctx := context.Background()
			id, err := b.Create(ctx, echoSpec(b))
			require.NoError(t, err)
			t.Cleanup(func() { b.Stop(ctx, id) }) //nolint:errcheck

			// Must be Running after create.
			require.Eventually(t, func() bool {
				st, _ := b.Status(ctx, id)
				return st == sandbox.StatusRunning
			}, 30*time.Second, 500*time.Millisecond)

			require.NoError(t, b.Stop(ctx, id))

			// Must not appear in ListObserved after stop.
			observed, err := b.ListObserved(ctx)
			require.NoError(t, err)
			for _, o := range observed {
				assert.NotEqual(t, id, o.ID, "stopped sandbox must not appear in observed list")
			}
		})
	}
}

// ── 2. Pause / Resume ─────────────────────────────────────────────────────────

func TestSandbox_PauseResumeRestoresRunning(t *testing.T) {
	for _, b := range impls(t) {
		t.Run(b.Name(), func(t *testing.T) {
			ctx := context.Background()
			id, err := b.Create(ctx, echoSpec(b))
			require.NoError(t, err)
			t.Cleanup(func() { b.Stop(ctx, id) }) //nolint:errcheck

			waitForStatus(t, b, id, sandbox.StatusRunning)

			require.NoError(t, b.Pause(ctx, id))
			waitForStatus(t, b, id, sandbox.StatusSuspended)

			require.NoError(t, b.Resume(ctx, id))
			waitForStatus(t, b, id, sandbox.StatusRunning)
		})
	}
}

// ── 3. Unexpected shutdown reports drift ──────────────────────────────────────

func TestSandbox_UnexpectedShutdownReportsDrift(t *testing.T) {
	for _, b := range impls(t) {
		t.Run(b.Name(), func(t *testing.T) {
			ctx := context.Background()
			id, err := b.Create(ctx, echoSpec(b))
			require.NoError(t, err)

			waitForStatus(t, b, id, sandbox.StatusRunning)

			// Kill the underlying process without using the Stop API.
			require.NoError(t, b.ForceKillForTest(ctx, id))

			// Status must eventually report Gone or Unknown — not Running.
			require.Eventually(t, func() bool {
				st, _ := b.Status(ctx, id)
				return st == sandbox.StatusGone || st == sandbox.StatusUnknown
			}, 15*time.Second, 500*time.Millisecond)
		})
	}
}

// ── 4. Missing sandbox operations are consistent ──────────────────────────────

func TestSandbox_MissingSandboxOperationsAreConsistent(t *testing.T) {
	for _, b := range impls(t) {
		t.Run(b.Name(), func(t *testing.T) {
			ctx := context.Background()
			bogus := sandbox.ID("asbx-does-not-exist")

			_, err := b.Status(ctx, bogus)
			require.ErrorIs(t, err, sandbox.ErrNotFound)

			err = b.Stop(ctx, bogus)
			require.ErrorIs(t, err, sandbox.ErrNotFound)

			err = b.Pause(ctx, bogus)
			require.ErrorIs(t, err, sandbox.ErrNotFound)

			err = b.Resume(ctx, bogus)
			require.ErrorIs(t, err, sandbox.ErrNotFound)
		})
	}
}

// ── 5. Byte IO round-trip ─────────────────────────────────────────────────────

func TestSandbox_ByteIORoundTrips(t *testing.T) {
	for _, b := range impls(t) {
		t.Run(b.Name(), func(t *testing.T) {
			ctx := context.Background()
			id, err := b.Create(ctx, echoSpec(b))
			require.NoError(t, err)
			t.Cleanup(func() { b.Stop(ctx, id) }) //nolint:errcheck

			waitForStatus(t, b, id, sandbox.StatusRunning)

			r, w, err := b.Open(ctx, id)
			require.NoError(t, err)
			defer r.Close()
			defer w.Close()

			msg := []byte("hello sandbox\n")
			_, err = w.Write(msg)
			require.NoError(t, err)

			buf := make([]byte, len(msg))
			_, err = io.ReadFull(r, buf)
			require.NoError(t, err)
			assert.Equal(t, msg, buf)
		})
	}
}

// ── 6. Stdin drop closes write half ──────────────────────────────────────────

func TestSandbox_StdinDropClosesWriteHalf(t *testing.T) {
	for _, b := range impls(t) {
		t.Run(b.Name(), func(t *testing.T) {
			ctx := context.Background()
			id, err := b.Create(ctx, echoSpec(b))
			require.NoError(t, err)
			t.Cleanup(func() { b.Stop(ctx, id) }) //nolint:errcheck

			waitForStatus(t, b, id, sandbox.StatusRunning)

			r, w, err := b.Open(ctx, id)
			require.NoError(t, err)
			defer r.Close()

			// Close the write handle.
			require.NoError(t, w.Close())

			// A subsequent read must return io.EOF (sandbox saw EOF on stdin).
			done := make(chan error, 1)
			go func() {
				buf := make([]byte, 64)
				_, readErr := r.Read(buf)
				done <- readErr
			}()

			select {
			case err := <-done:
				assert.ErrorIs(t, err, io.EOF)
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for EOF after stdin close")
			}
		})
	}
}

// ── 7. Pause blocks IO until resume ──────────────────────────────────────────

func TestSandbox_PauseBlocksReadWriteUntilResume(t *testing.T) {
	for _, b := range impls(t) {
		t.Run(b.Name(), func(t *testing.T) {
			ctx := context.Background()
			id, err := b.Create(ctx, echoSpec(b))
			require.NoError(t, err)
			t.Cleanup(func() { b.Stop(ctx, id) }) //nolint:errcheck

			waitForStatus(t, b, id, sandbox.StatusRunning)

			r, w, err := b.Open(ctx, id)
			require.NoError(t, err)
			defer r.Close()
			defer w.Close()

			require.NoError(t, b.Pause(ctx, id))
			waitForStatus(t, b, id, sandbox.StatusSuspended)

			// Write and read must block.
			blocked := make(chan struct{})
			go func() {
				w.Write([]byte("data\n")) //nolint:errcheck
				r.Read(make([]byte, 64)) //nolint:errcheck
				close(blocked)
			}()

			select {
			case <-blocked:
				t.Fatal("IO must block while sandbox is suspended")
			case <-time.After(300 * time.Millisecond):
				// Expected: IO is still blocking.
			}

			require.NoError(t, b.Resume(ctx, id))

			// After resume IO must proceed.
			select {
			case <-blocked:
				// OK
			case <-time.After(10 * time.Second):
				t.Fatal("IO must unblock after resume")
			}
		})
	}
}

// ── 8. Reconnect / multi-observer ─────────────────────────────────────────────

func TestSandbox_ReconnectCanObserveAndStop(t *testing.T) {
	for _, b := range impls(t) {
		t.Run(b.Name(), func(t *testing.T) {
			ctx := context.Background()
			id, err := b.Create(ctx, echoSpec(b))
			require.NoError(t, err)
			t.Cleanup(func() { b.Stop(ctx, id) }) //nolint:errcheck

			waitForStatus(t, b, id, sandbox.StatusRunning)

			// First observer.
			r1, w1, err := b.Open(ctx, id)
			require.NoError(t, err)
			defer r1.Close()
			defer w1.Close()

			// Second observer on the same running sandbox.
			r2, _, err := b.Open(ctx, id)
			require.NoError(t, err)
			defer r2.Close()

			// Write on first, read on second.
			_, err = w1.Write([]byte("reconnect\n"))
			require.NoError(t, err)

			buf := make([]byte, len("reconnect\n"))
			_, err = io.ReadFull(r2, buf)
			require.NoError(t, err)
			assert.Equal(t, "reconnect\n", string(buf))
		})
	}
}

// ── 9. Failed create cleans up observed resources ─────────────────────────────

func TestSandbox_FailedCreateCleansUpObservedResources(t *testing.T) {
	for _, b := range impls(t) {
		t.Run(b.Name(), func(t *testing.T) {
			ctx := context.Background()

			// Use a spec that is designed to fail at creation time.
			_, err := b.Create(ctx, b.FailingSpec())
			require.Error(t, err, "create must fail for FailingSpec")

			// No partial resource must appear in the observed list.
			observed, err := b.ListObserved(ctx)
			require.NoError(t, err)
			assert.Empty(t, observed, "failed create must not leave any observed resources")
		})
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func waitForStatus(t *testing.T, b sandbox.Backend, id sandbox.ID, want sandbox.Status) {
	t.Helper()
	ctx := context.Background()
	require.Eventually(t, func() bool {
		st, err := b.Status(ctx, id)
		return err == nil && st == want
	}, 30*time.Second, 250*time.Millisecond, "waiting for status %s", want)
}
