package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
)

type localBackend struct {
	mu        sync.Mutex
	nextID    uint64
	sandboxes map[ID]*localSandbox
}

type localSandbox struct {
	id            ID
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	status        Status
	stopRequested bool
	done          chan struct{}

	mu          sync.Mutex
	subscribers map[int]chan []byte
	nextSubID   int
	stdinClosed bool
}

func newLocalBackend() Backend {
	return &localBackend{
		sandboxes: make(map[ID]*localSandbox),
	}
}

func (b *localBackend) Name() string {
	return "local"
}

func (b *localBackend) EchoSpec() Spec {
	return Spec{
		Command: []string{"cat"},
	}
}

func (b *localBackend) FailingSpec() Spec {
	return Spec{
		Command: []string{"/definitely-not-a-real-command"},
	}
}

func (b *localBackend) Create(_ context.Context, spec Spec) (ID, error) {
	command, err := localCommand(spec)
	if err != nil {
		return "", err
	}

	stdin, err := command.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("open stdin pipe: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("open stdout pipe: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("open stderr pipe: %w", err)
	}

	if err := command.Start(); err != nil {
		return "", fmt.Errorf("start sandbox process: %w", err)
	}

	id := b.nextSandboxID()
	sb := &localSandbox{
		id:          id,
		cmd:         command,
		stdin:       stdin,
		status:      StatusRunning,
		done:        make(chan struct{}),
		subscribers: make(map[int]chan []byte),
	}

	b.mu.Lock()
	b.sandboxes[id] = sb
	b.mu.Unlock()

	go b.broadcastStdout(sb, stdout)
	go drainReader(stderr)
	go b.waitForExit(sb)

	return id, nil
}

func (b *localBackend) Status(_ context.Context, id ID) (Status, error) {
	sb, err := b.lookup(id)
	if err != nil {
		return StatusUnknown, err
	}

	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.status, nil
}

func (b *localBackend) Stop(_ context.Context, id ID) error {
	sb, err := b.remove(id)
	if err != nil {
		return err
	}

	sb.mu.Lock()
	sb.stopRequested = true
	process := sb.cmd.Process
	done := sb.done
	sb.mu.Unlock()

	if process != nil {
		_ = process.Kill()
	}
	<-done
	return nil
}

func (b *localBackend) Pause(_ context.Context, id ID) error {
	sb, err := b.lookup(id)
	if err != nil {
		return err
	}

	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.status == StatusSuspended {
		return nil
	}
	if sb.status != StatusRunning {
		return fmt.Errorf("sandbox %s is %s", id, sb.status)
	}
	if err := signalProcess(sb.cmd, syscall.SIGSTOP); err != nil {
		return err
	}
	sb.status = StatusSuspended
	return nil
}

func (b *localBackend) Resume(_ context.Context, id ID) error {
	sb, err := b.lookup(id)
	if err != nil {
		return err
	}

	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.status == StatusRunning {
		return nil
	}
	if sb.status != StatusSuspended {
		return fmt.Errorf("sandbox %s is %s", id, sb.status)
	}
	if err := signalProcess(sb.cmd, syscall.SIGCONT); err != nil {
		return err
	}
	sb.status = StatusRunning
	return nil
}

func (b *localBackend) Open(_ context.Context, id ID) (io.ReadCloser, io.WriteCloser, error) {
	sb, err := b.lookup(id)
	if err != nil {
		return nil, nil, err
	}

	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.status != StatusRunning {
		return nil, nil, fmt.Errorf("sandbox %s is %s", id, sb.status)
	}

	reader, ch := newSubscriptionReader()
	subID := sb.nextSubID
	sb.nextSubID++
	sb.subscribers[subID] = ch

	return &localSubscriber{
			ReadCloser: reader,
			onClose: func() {
				b.unsubscribe(sb, subID)
			},
		},
		&localWriter{sandbox: sb},
		nil
}

func (b *localBackend) ListObserved(ctx context.Context) ([]ObservedSandbox, error) {
	b.mu.Lock()
	ids := make([]ID, 0, len(b.sandboxes))
	for id := range b.sandboxes {
		ids = append(ids, id)
	}
	b.mu.Unlock()

	slices.Sort(ids)
	observed := make([]ObservedSandbox, 0, len(ids))
	for _, id := range ids {
		status, err := b.Status(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		observed = append(observed, ObservedSandbox{ID: id, Status: status})
	}
	return observed, nil
}

func (b *localBackend) ForceKillForTest(_ context.Context, id ID) error {
	sb, err := b.lookup(id)
	if err != nil {
		return err
	}

	sb.mu.Lock()
	defer sb.mu.Unlock()
	if err := signalProcess(sb.cmd, syscall.SIGKILL); err != nil {
		return err
	}
	sb.status = StatusGone
	return nil
}

func (b *localBackend) nextSandboxID() ID {
	next := atomic.AddUint64(&b.nextID, 1)
	return ID(fmt.Sprintf("asbx-local-%d", next))
}

func (b *localBackend) lookup(id ID) (*localSandbox, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sb, ok := b.sandboxes[id]
	if !ok {
		return nil, ErrNotFound
	}
	return sb, nil
}

func (b *localBackend) remove(id ID) (*localSandbox, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sb, ok := b.sandboxes[id]
	if !ok {
		return nil, ErrNotFound
	}
	delete(b.sandboxes, id)
	return sb, nil
}

func (b *localBackend) broadcastStdout(sb *localSandbox, stdout io.ReadCloser) {
	defer stdout.Close()

	buf := make([]byte, 32*1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			sb.mu.Lock()
			for _, ch := range sb.subscribers {
				ch <- payload
			}
			sb.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (b *localBackend) waitForExit(sb *localSandbox) {
	_ = sb.cmd.Wait()

	sb.mu.Lock()
	if sb.stopRequested {
		sb.status = StatusStopped
	} else if sb.status != StatusStopped {
		sb.status = StatusGone
	}
	for id, ch := range sb.subscribers {
		close(ch)
		delete(sb.subscribers, id)
	}
	sb.mu.Unlock()

	close(sb.done)
}

func (b *localBackend) unsubscribe(sb *localSandbox, subID int) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	ch, ok := sb.subscribers[subID]
	if !ok {
		return
	}
	delete(sb.subscribers, subID)
	close(ch)
}

func localCommand(spec Spec) (*exec.Cmd, error) {
	var command []string
	switch {
	case len(spec.Command) > 0:
		command = append(command, spec.Command...)
	case spec.Image != "":
		command = []string{spec.Image}
	default:
		return nil, fmt.Errorf("sandbox spec must set command or image")
	}

	cmd := exec.Command(command[0], command[1:]...)
	for key, value := range spec.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd, nil
}

func signalProcess(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil {
		return fmt.Errorf("sandbox process is not running")
	}
	if err := cmd.Process.Signal(signal); err != nil {
		return fmt.Errorf("signal sandbox process: %w", err)
	}
	return nil
}

func drainReader(r io.ReadCloser) {
	defer r.Close()
	_, _ = io.Copy(io.Discard, r)
}

type localWriter struct {
	sandbox *localSandbox
}

func (w *localWriter) Write(p []byte) (int, error) {
	w.sandbox.mu.Lock()
	stdin := w.sandbox.stdin
	closed := w.sandbox.stdinClosed
	w.sandbox.mu.Unlock()

	if closed || stdin == nil {
		return 0, io.ErrClosedPipe
	}
	return stdin.Write(p)
}

func (w *localWriter) Close() error {
	w.sandbox.mu.Lock()
	defer w.sandbox.mu.Unlock()
	if w.sandbox.stdinClosed || w.sandbox.stdin == nil {
		return nil
	}
	w.sandbox.stdinClosed = true
	err := w.sandbox.stdin.Close()
	w.sandbox.stdin = nil
	return err
}

type localSubscriber struct {
	io.ReadCloser
	onClose func()
}

func (r *localSubscriber) Close() error {
	if r.onClose != nil {
		r.onClose()
		r.onClose = nil
	}
	if r.ReadCloser != nil {
		return r.ReadCloser.Close()
	}
	return nil
}

func newSubscriptionReader() (io.ReadCloser, chan []byte) {
	pr, pw := io.Pipe()
	ch := make(chan []byte, 128)
	go func() {
		defer pw.Close()
		for chunk := range ch {
			if _, err := pw.Write(chunk); err != nil {
				return
			}
		}
	}()
	return pr, ch
}
