package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
)

var (
	ErrNotFound = errors.New("sandbox not found")
	ErrNotReady = errors.New("sandbox not ready")
	ErrIO       = errors.New("sandbox io error")
)

type ID string

type Status int

const (
	StatusUnknown Status = iota
	StatusCreating
	StatusCreated
	StatusRunning
	StatusSuspended
	StatusStopped
	StatusGone
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

type ObservedSandbox struct {
	ID     ID
	Status Status
}

type Mount struct {
	Kind       any
	TargetPath string
	SubPath    *string
}

type MountBind struct {
	SourcePath string
}

type MountNamedVolume struct {
	Name string
}

type MountEmptyDir struct{}

type Spec struct {
	Image   string
	Command []string
	Args    []string
	Env     map[string]string
	Labels  map[string]string
	Mounts  []Mount
}

func NewSpec(image string) *Spec {
	return &Spec{Image: image, Env: map[string]string{}, Labels: map[string]string{}}
}

func (s *Spec) WithMount(m Mount) *Spec {
	s.Mounts = append(s.Mounts, m)
	return s
}

func (s *Spec) WithEnv(key, value string) *Spec {
	if s.Env == nil {
		s.Env = make(map[string]string)
	}
	s.Env[key] = value
	return s
}

func (s Spec) EnvValue(key string) string {
	if s.Env == nil {
		return ""
	}
	return s.Env[key]
}

func (s *Spec) DeleteEnv(key string) {
	if s == nil || s.Env == nil {
		return
	}
	delete(s.Env, key)
}

type Backend interface {
	Name() string
	EchoSpec() Spec
	FailingSpec() Spec
	Create(ctx context.Context, spec Spec) (ID, error)
	Status(ctx context.Context, id ID) (Status, error)
	Stop(ctx context.Context, id ID) error
	Pause(ctx context.Context, id ID) error
	Resume(ctx context.Context, id ID) error
	Open(ctx context.Context, id ID) (r io.ReadCloser, w io.WriteCloser, err error)
	ListObserved(ctx context.Context) ([]ObservedSandbox, error)
	ForceKillForTest(ctx context.Context, id ID) error
}

func NewLocalBackend() Backend {
	return newLocalBackend()
}

func NewK8sBackend(kubeContext, namespace, image string) (Backend, error) {
	return newK8sBackend(kubeContext, namespace, image)
}

func NewNotReadyError(message string) error {
	return fmt.Errorf("%s: %w", message, ErrNotReady)
}

func NewNotFoundError(id string) error {
	return fmt.Errorf("sandbox %s: %w", id, ErrNotFound)
}

func NewIOError(message string) error {
	return fmt.Errorf("%s: %w", message, ErrIO)
}
