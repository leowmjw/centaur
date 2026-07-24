// Package sandbox defines the Backend interface that sandbox implementations
// must satisfy.  The implementing agent must provide concrete implementations
// for the local and agent-k8s backends.
//
// This file contains only type declarations and the interface — no logic.
package sandbox

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned when an operation targets a sandbox that does not exist.
var ErrNotFound = errors.New("sandbox not found")

// ID is an opaque sandbox identifier in the format "asbx-<uuid>"
// (e.g. "asbx-01234567-89ab-cdef-0123-456789abcdef").
type ID string

// Status models the observable state of a sandbox process.
type Status int

const (
	StatusUnknown   Status = iota
	StatusCreating         // transient: pod/process is being provisioned
	StatusCreated          // pod/process provisioned but not yet accepting IO
	StatusRunning          // process is alive and accepting IO
	StatusSuspended        // process is paused (SIGSTOP or equivalent)
	StatusStopped          // process has exited cleanly via Stop
	StatusGone             // process has exited or was force-killed unexpectedly
)

func (s Status) String() string {
	switch s {
	case StatusCreating:
		return "creating"
	case StatusCreated:
		return "created"
	case StatusRunning:
		return "running"
	case StatusSuspended:
		return "suspended"
	case StatusStopped:
		return "stopped"
	case StatusGone:
		return "gone"
	default:
		return "unknown"
	}
}

// ObservedSandbox is a summary of an observed sandbox returned by ListObserved.
type ObservedSandbox struct {
	ID     ID
	Status Status
}

// Spec describes the configuration required to create a sandbox.
type Spec struct {
	Image   string
	Command []string
	Env     map[string]string
}

// Backend is the contract that all sandbox implementations must satisfy.
type Backend interface {
	// Name returns a human-readable implementation name for test output.
	Name() string

	// EchoSpec returns a minimal spec that runs an echo/cat process.
	EchoSpec() Spec

	// FailingSpec returns a spec that is guaranteed to fail at create time.
	FailingSpec() Spec

	// Create provisions a new sandbox and returns its ID.
	Create(ctx context.Context, spec Spec) (ID, error)

	// Status returns the current observable status of the sandbox.
	Status(ctx context.Context, id ID) (Status, error)

	// Stop terminates the sandbox and frees its resources.
	Stop(ctx context.Context, id ID) error

	// Pause suspends the sandbox process.
	Pause(ctx context.Context, id ID) error

	// Resume resumes a suspended sandbox process.
	Resume(ctx context.Context, id ID) error

	// Open returns a read handle and a write handle for the sandbox's stdio.
	Open(ctx context.Context, id ID) (r io.ReadCloser, w io.WriteCloser, err error)

	// ListObserved returns all sandboxes currently known to this backend.
	ListObserved(ctx context.Context) ([]ObservedSandbox, error)

	// ForceKillForTest kills the sandbox's process without going through Stop.
	// This is a test-only escape hatch to simulate unexpected shutdown.
	ForceKillForTest(ctx context.Context, id ID) error
}

// NewLocalBackend creates the local process-based sandbox backend.
func NewLocalBackend() Backend {
	return newLocalBackend()
}

// NewK8sBackend creates the Kubernetes sandbox backend.
func NewK8sBackend(kubeContext, namespace, image string) (Backend, error) {
	return newK8sBackend(kubeContext, namespace, image)
}
