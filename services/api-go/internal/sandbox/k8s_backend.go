package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

const (
	k8sContainerName      = "sandbox"
	k8sManagedByLabel     = "centaur.ai/managed-by"
	k8sManagedByValue     = "api-go"
	k8sPausedAnnotation   = "centaur.ai/paused-at"
	k8sCreatePollInterval = 500 * time.Millisecond
)

var nextK8sSandboxID uint64

type k8sBackend struct {
	kubeContext  string
	namespace    string
	defaultImage string
}

type k8sPodList struct {
	Items []k8sPod `json:"items"`
}

type k8sPod struct {
	Metadata k8sPodMetadata `json:"metadata"`
	Status   k8sPodStatus   `json:"status"`
}

type k8sPodMetadata struct {
	Name              string            `json:"name"`
	Annotations       map[string]string `json:"annotations"`
	DeletionTimestamp *string           `json:"deletionTimestamp"`
}

type k8sPodStatus struct {
	Phase             string               `json:"phase"`
	ContainerStatuses []k8sContainerStatus `json:"containerStatuses"`
}

type k8sContainerStatus struct {
	Ready bool              `json:"ready"`
	State k8sContainerState `json:"state"`
}

type k8sContainerState struct {
	Waiting    *k8sContainerWaiting    `json:"waiting"`
	Running    map[string]any          `json:"running"`
	Terminated *k8sContainerTerminated `json:"terminated"`
}

type k8sContainerWaiting struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type k8sContainerTerminated struct {
	Reason   string `json:"reason"`
	Message  string `json:"message"`
	ExitCode int    `json:"exitCode"`
}

func newK8sBackend(kubeContext, namespace, image string) (Backend, error) {
	if strings.TrimSpace(kubeContext) == "" {
		return nil, fmt.Errorf("kube context is required")
	}
	if strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("default image is required")
	}
	return &k8sBackend{
		kubeContext:  kubeContext,
		namespace:    namespace,
		defaultImage: image,
	}, nil
}

func (b *k8sBackend) Name() string {
	return "agent-k8s"
}

func (b *k8sBackend) EchoSpec() Spec {
	return Spec{
		Image:   b.defaultImage,
		Command: []string{"sh", "-c", "exec cat"},
	}
}

func (b *k8sBackend) FailingSpec() Spec {
	return Spec{
		Image:   b.defaultImage,
		Command: []string{"/definitely-not-a-real-command"},
	}
}

func (b *k8sBackend) Create(ctx context.Context, spec Spec) (ID, error) {
	id := ID(fmt.Sprintf("asbx-k8s-%d", atomic.AddUint64(&nextK8sSandboxID, 1)))
	if err := b.applyPod(ctx, id, spec); err != nil {
		return "", err
	}
	if err := b.waitForRunning(ctx, id); err != nil {
		_ = b.deletePod(context.Background(), id)
		_ = b.waitForMissing(context.Background(), id, 30*time.Second)
		return "", err
	}
	return id, nil
}

func (b *k8sBackend) Status(ctx context.Context, id ID) (Status, error) {
	pod, err := b.getPod(ctx, id)
	if err != nil {
		return StatusUnknown, err
	}
	return podSandboxStatus(pod), nil
}

func (b *k8sBackend) Stop(ctx context.Context, id ID) error {
	if _, err := b.getPod(ctx, id); err != nil {
		return err
	}
	if err := b.deletePod(ctx, id); err != nil {
		return err
	}
	return b.waitForMissing(ctx, id, 30*time.Second)
}

func (b *k8sBackend) Pause(ctx context.Context, id ID) error {
	if _, err := b.getPod(ctx, id); err != nil {
		return err
	}
	if err := b.execInPod(ctx, id, "kill -STOP 1"); err != nil {
		return err
	}
	return b.annotate(ctx, id, fmt.Sprintf("%s=%s", k8sPausedAnnotation, time.Now().UTC().Format(time.RFC3339Nano)))
}

func (b *k8sBackend) Resume(ctx context.Context, id ID) error {
	if _, err := b.getPod(ctx, id); err != nil {
		return err
	}
	if err := b.execInPod(ctx, id, "kill -CONT 1"); err != nil {
		return err
	}
	return b.annotate(ctx, id, k8sPausedAnnotation+"-")
}

func (b *k8sBackend) Open(ctx context.Context, id ID) (io.ReadCloser, io.WriteCloser, error) {
	status, err := b.Status(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if status != StatusRunning {
		return nil, nil, fmt.Errorf("sandbox %s is %s", id, status)
	}

	cmd := exec.CommandContext(ctx, "kubectl",
		"--context", b.kubeContext,
		"--namespace", b.namespace,
		"attach",
		string(id),
		"-c", k8sContainerName,
		"-i",
		"--tty=false",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("attach stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("attach stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("attach stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start kubectl attach: %w", err)
	}
	go drainReader(stderr)
	go func() { _ = cmd.Wait() }()

	return &attachReadCloser{ReadCloser: stdout, process: cmd.Process}, stdin, nil
}

func (b *k8sBackend) ListObserved(ctx context.Context) ([]ObservedSandbox, error) {
	out, err := b.kubectlOutput(ctx, "get", "pods", "-l", fmt.Sprintf("%s=%s", k8sManagedByLabel, k8sManagedByValue), "-o", "json")
	if err != nil {
		return nil, err
	}

	var podList k8sPodList
	if err := json.Unmarshal(out, &podList); err != nil {
		return nil, fmt.Errorf("decode pod list: %w", err)
	}

	observed := make([]ObservedSandbox, 0, len(podList.Items))
	for _, pod := range podList.Items {
		observed = append(observed, ObservedSandbox{
			ID:     ID(pod.Metadata.Name),
			Status: podSandboxStatus(pod),
		})
	}
	slices.SortFunc(observed, func(a, b ObservedSandbox) int {
		return strings.Compare(string(a.ID), string(b.ID))
	})
	return observed, nil
}

func (b *k8sBackend) ForceKillForTest(ctx context.Context, id ID) error {
	if _, err := b.getPod(ctx, id); err != nil {
		return err
	}
	return b.execInPod(ctx, id, "kill -KILL 1")
}

func (b *k8sBackend) applyPod(ctx context.Context, id ID, spec Spec) error {
	image := strings.TrimSpace(spec.Image)
	if image == "" {
		image = b.defaultImage
	}

	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name": id,
			"labels": map[string]string{
				k8sManagedByLabel: "api-go",
			},
		},
		"spec": map[string]any{
			"restartPolicy": "Never",
			"containers": []map[string]any{{
				"name":      k8sContainerName,
				"image":     image,
				"stdin":     true,
				"stdinOnce": true,
				"tty":       false,
			}},
		},
	}

	container := manifest["spec"].(map[string]any)["containers"].([]map[string]any)[0]
	if len(spec.Command) > 0 {
		container["command"] = spec.Command
	}
	if len(spec.Env) > 0 {
		env := make([]map[string]string, 0, len(spec.Env))
		keys := make([]string, 0, len(spec.Env))
		for key := range spec.Env {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			env = append(env, map[string]string{"name": key, "value": spec.Env[key]})
		}
		container["env"] = env
	}

	payload, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal pod manifest: %w", err)
	}
	return b.kubectlInput(ctx, payload, "apply", "-f", "-")
}

func (b *k8sBackend) waitForRunning(ctx context.Context, id ID) error {
	deadline := time.Now().Add(45 * time.Second)
	for {
		pod, err := b.getPod(ctx, id)
		if err != nil {
			return err
		}

		status := podSandboxStatus(pod)
		if status == StatusRunning {
			return nil
		}
		if failure := podFailureMessage(pod); failure != "" {
			return errors.New(failure)
		}
		if status == StatusGone || status == StatusStopped {
			return fmt.Errorf("sandbox %s terminated before becoming running", id)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox %s did not become running before timeout (latest status: %s)", id, status)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(k8sCreatePollInterval):
		}
	}
}

func (b *k8sBackend) waitForMissing(ctx context.Context, id ID, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := b.getPod(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for sandbox %s deletion", id)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(k8sCreatePollInterval):
		}
	}
}

func (b *k8sBackend) getPod(ctx context.Context, id ID) (k8sPod, error) {
	out, err := b.kubectlOutput(ctx, "get", "pod", string(id), "-o", "json")
	if err != nil {
		var zero k8sPod
		if isKubectlNotFound(err) {
			return zero, ErrNotFound
		}
		return zero, err
	}

	var pod k8sPod
	if err := json.Unmarshal(out, &pod); err != nil {
		return k8sPod{}, fmt.Errorf("decode pod: %w", err)
	}
	return pod, nil
}

func (b *k8sBackend) deletePod(ctx context.Context, id ID) error {
	return b.kubectlOutputErr(ctx, "delete", "pod", string(id), "--wait=false")
}

func (b *k8sBackend) execInPod(ctx context.Context, id ID, shellCommand string) error {
	return b.kubectlOutputErr(ctx, "exec", string(id), "-c", k8sContainerName, "--", "sh", "-c", shellCommand)
}

func (b *k8sBackend) annotate(ctx context.Context, id ID, annotation string) error {
	return b.kubectlOutputErr(ctx, "annotate", "pod", string(id), annotation, "--overwrite")
}

func (b *k8sBackend) kubectlOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "kubectl", b.kubectlArgs(args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", kubectlError(err), strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (b *k8sBackend) kubectlInput(ctx context.Context, input []byte, args ...string) error {
	cmd := exec.CommandContext(ctx, "kubectl", b.kubectlArgs(args...)...)
	cmd.Stdin = bytes.NewReader(input)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", kubectlError(err), strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *k8sBackend) kubectlOutputErr(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "kubectl", b.kubectlArgs(args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", kubectlError(err), strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *k8sBackend) kubectlArgs(args ...string) []string {
	base := []string{"--context", b.kubeContext, "--namespace", b.namespace}
	return append(base, args...)
}

func kubectlError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("kubectl exited with %s", exitErr.ProcessState.String())
	}
	return err
}

func isKubectlNotFound(err error) bool {
	return strings.Contains(err.Error(), `pods "`) && strings.Contains(err.Error(), `" not found`)
}

func podSandboxStatus(pod k8sPod) Status {
	if pod.Metadata.DeletionTimestamp != nil {
		return StatusStopped
	}
	if _, paused := pod.Metadata.Annotations[k8sPausedAnnotation]; paused {
		return StatusSuspended
	}

	switch pod.Status.Phase {
	case "Pending":
		return StatusCreating
	case "Running":
		for _, status := range pod.Status.ContainerStatuses {
			if status.Ready {
				return StatusRunning
			}
		}
		return StatusCreated
	case "Succeeded", "Failed":
		return StatusGone
	default:
		return StatusUnknown
	}
}

func podFailureMessage(pod k8sPod) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil {
			reason := status.State.Waiting.Reason
			if reason == "ErrImagePull" || reason == "ImagePullBackOff" || reason == "RunContainerError" || reason == "CreateContainerError" || reason == "CrashLoopBackOff" {
				return strings.TrimSpace(reason + ": " + status.State.Waiting.Message)
			}
		}
		if status.State.Terminated != nil {
			reason := strings.TrimSpace(status.State.Terminated.Reason + ": " + status.State.Terminated.Message)
			if reason != ":" {
				return strings.TrimSpace(reason)
			}
			return fmt.Sprintf("container terminated with exit code %d", status.State.Terminated.ExitCode)
		}
	}
	return ""
}

type attachReadCloser struct {
	io.ReadCloser
	process *os.Process
}

func (r *attachReadCloser) Close() error {
	if r.process != nil {
		_ = r.process.Kill()
	}
	return r.ReadCloser.Close()
}
