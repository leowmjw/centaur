package sandbox

import "testing"

func TestPodSandboxStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pod  k8sPod
		want Status
	}{
		{
			name: "pending pod is creating",
			pod:  k8sPod{Status: k8sPodStatus{Phase: "Pending"}},
			want: StatusCreating,
		},
		{
			name: "running ready pod is running",
			pod: k8sPod{
				Status: k8sPodStatus{
					Phase: "Running",
					ContainerStatuses: []k8sContainerStatus{{
						Ready: true,
					}},
				},
			},
			want: StatusRunning,
		},
		{
			name: "running unready pod is created",
			pod: k8sPod{
				Status: k8sPodStatus{
					Phase: "Running",
					ContainerStatuses: []k8sContainerStatus{{
						Ready: false,
					}},
				},
			},
			want: StatusCreated,
		},
		{
			name: "paused pod is suspended",
			pod: k8sPod{
				Metadata: k8sPodMetadata{
					Annotations: map[string]string{k8sPausedAnnotation: "2026-07-24T00:00:00Z"},
				},
				Status: k8sPodStatus{Phase: "Running"},
			},
			want: StatusSuspended,
		},
		{
			name: "failed pod is gone",
			pod:  k8sPod{Status: k8sPodStatus{Phase: "Failed"}},
			want: StatusGone,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := podSandboxStatus(tt.pod); got != tt.want {
				t.Fatalf("podSandboxStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodFailureMessage(t *testing.T) {
	t.Parallel()

	waiting := k8sPod{
		Status: k8sPodStatus{
			ContainerStatuses: []k8sContainerStatus{{
				State: k8sContainerState{
					Waiting: &k8sContainerWaiting{
						Reason:  "RunContainerError",
						Message: "exec: not found",
					},
				},
			}},
		},
	}
	if got := podFailureMessage(waiting); got != "RunContainerError: exec: not found" {
		t.Fatalf("waiting failure = %q", got)
	}

	terminated := k8sPod{
		Status: k8sPodStatus{
			ContainerStatuses: []k8sContainerStatus{{
				State: k8sContainerState{
					Terminated: &k8sContainerTerminated{
						ExitCode: 127,
					},
				},
			}},
		},
	}
	if got := podFailureMessage(terminated); got != "container terminated with exit code 127" {
		t.Fatalf("terminated failure = %q", got)
	}
}
