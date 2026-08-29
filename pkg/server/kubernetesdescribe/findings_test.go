package kubernetesdescribe

import (
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

func waitingContainer(
	name string,
	reason string,
	message string,
) kubernetesresource.PodContainer {
	return kubernetesresource.PodContainer{
		Name: name,
		State: kubernetesresource.PodContainerState{
			Type:    waitingContainerState,
			Reason:  reason,
			Message: message,
		},
	}
}

func terminatedState(
	reason string,
	exitCode int32,
) kubernetesresource.PodContainerState {
	code := exitCode
	return kubernetesresource.PodContainerState{
		Type:     terminatedContainerState,
		Reason:   reason,
		ExitCode: &code,
	}
}

func describedEvent(uid, reason, message, container string) Event {
	return describedEventAt(uid, reason, message, container, describedEventBase)
}

var describedEventBase = time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)

// describedEventAt is for the tests that are about which Event is the newest.
// They have to say so with the timestamp: recency is read from `last_seen`, not
// from where an Event happens to sit in the slice.
func describedEventAt(uid, reason, message, container string, seen time.Time) Event {
	return Event{
		UID:       uid,
		Type:      eventTypeWarning,
		Reason:    reason,
		Message:   message,
		Container: container,
		LastSeen:  &seen,
	}
}

func codesOf(findings []Finding) []string {
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	return codes
}

func TestPodFindingsReadTheStateKubernetesReported(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		pod    kubernetesresource.PodDetail
		events []Event
		codes  []string
	}{
		{
			name: "a running Pod that is Ready reports nothing",
			pod: kubernetesresource.PodDetail{
				PodSummary: kubernetesresource.PodSummary{Phase: "Running", Ready: true},
				Containers: []kubernetesresource.PodContainer{{
					Name:  "server",
					Ready: true,
					State: kubernetesresource.PodContainerState{Type: "running"},
				}},
				Conditions: []kubernetesresource.PodCondition{
					{Type: "PodScheduled", Status: "True"},
					{Type: "Ready", Status: "True"},
				},
			},
			codes: []string{},
		},
		{
			name: "a Pod the scheduler could not place",
			pod: kubernetesresource.PodDetail{
				PodSummary: kubernetesresource.PodSummary{Phase: "Pending"},
				Conditions: []kubernetesresource.PodCondition{{
					Type:    "PodScheduled",
					Status:  "False",
					Reason:  "Unschedulable",
					Message: "0/5 nodes are available: 3 Insufficient cpu.",
				}},
			},
			codes: []string{FindingPodUnschedulable},
		},
		{
			name: "an image that never arrived",
			pod: kubernetesresource.PodDetail{
				PodSummary: kubernetesresource.PodSummary{Phase: "Pending"},
				Containers: []kubernetesresource.PodContainer{waitingContainer(
					"server",
					"ImagePullBackOff",
					"Back-off pulling image \"registry.example.internal/inference:v3\"",
				)},
			},
			codes: []string{FindingImagePullFailure},
		},
		{
			name: "a container built from configuration that is not there",
			pod: kubernetesresource.PodDetail{
				PodSummary: kubernetesresource.PodSummary{Phase: "Pending"},
				Containers: []kubernetesresource.PodContainer{waitingContainer(
					"server",
					"CreateContainerConfigError",
					"secret \"model-credentials\" not found",
				)},
			},
			codes: []string{FindingContainerConfigError},
		},
		{
			name: "a container that keeps dying",
			pod: kubernetesresource.PodDetail{
				PodSummary: kubernetesresource.PodSummary{Phase: "Running"},
				Containers: []kubernetesresource.PodContainer{{
					Name: "server",
					State: kubernetesresource.PodContainerState{
						Type:    waitingContainerState,
						Reason:  crashLoopBackOffReason,
						Message: "back-off 5m0s restarting failed container",
					},
					LastState: func() *kubernetesresource.PodContainerState {
						state := terminatedState("Error", 1)
						return &state
					}(),
				}},
				Conditions: []kubernetesresource.PodCondition{
					{Type: "Ready", Status: "False"},
				},
			},
			codes: []string{FindingCrashLoopBackOff},
		},
		{
			name: "an init container failure keeps the init container's name",
			pod: kubernetesresource.PodDetail{
				PodSummary: kubernetesresource.PodSummary{Phase: "Pending"},
				InitContainers: []kubernetesresource.PodContainer{{
					Name:  "fetch-model",
					State: terminatedState("Error", 2),
				}},
				Containers: []kubernetesresource.PodContainer{waitingContainer(
					"server", "PodInitializing", "",
				)},
			},
			codes: []string{FindingContainerTerminated},
		},
		{
			name: "a completed init container is not a failure",
			pod: kubernetesresource.PodDetail{
				PodSummary: kubernetesresource.PodSummary{Phase: "Running"},
				InitContainers: []kubernetesresource.PodContainer{{
					Name:  "fetch-model",
					State: terminatedState("Completed", 0),
				}},
				Conditions: []kubernetesresource.PodCondition{
					{Type: "Ready", Status: "True"},
				},
			},
			codes: []string{},
		},
		{
			name: "a volume that never mounted",
			pod: kubernetesresource.PodDetail{
				PodSummary: kubernetesresource.PodSummary{Phase: "Pending"},
				Containers: []kubernetesresource.PodContainer{waitingContainer(
					"server", "ContainerCreating", "",
				)},
			},
			events: []Event{describedEvent(
				"event-a",
				"FailedMount",
				"Unable to attach or mount volumes: unmounted volumes=[model-cache]",
				"",
			)},
			codes: []string{FindingVolumeMountFailure},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			findings := podFindings(testCase.pod, testCase.events)
			codes := codesOf(findings)
			if len(codes) != len(testCase.codes) {
				t.Fatalf("findings = %v, want %v", codes, testCase.codes)
			}
			for index, code := range testCase.codes {
				if codes[index] != code {
					t.Fatalf("findings = %v, want %v", codes, testCase.codes)
				}
			}
		})
	}
}

// 137 is SIGKILL. Reporting it as an OOM kill would be the Server explaining
// something the Cluster did not say, and an operator who then went looking at
// memory limits would be looking in the wrong place.
func TestPodFindingsDoNotInferAnOOMKillFromAnExitCode(t *testing.T) {
	t.Parallel()

	pod := kubernetesresource.PodDetail{
		PodSummary: kubernetesresource.PodSummary{Phase: "Running"},
		Containers: []kubernetesresource.PodContainer{{
			Name:  "server",
			State: terminatedState("Error", 137),
		}},
	}

	findings := podFindings(pod, nil)
	if len(findings) != 1 ||
		findings[0].Code != FindingContainerTerminated ||
		findings[0].ExitCode == nil ||
		*findings[0].ExitCode != 137 {
		t.Fatalf("unexpected findings: %+v", findings)
	}
}

// A container the kubelet killed for memory and then restarted looks healthy in
// its current state. The kill is the answer to "why did it restart", so it is
// reported from the previous state.
func TestPodFindingsReportAnOOMKillFromThePreviousState(t *testing.T) {
	t.Parallel()

	previous := kubernetesresource.PodContainerState{
		Type:     terminatedContainerState,
		Reason:   oomKilledReason,
		ExitCode: func() *int32 { code := int32(137); return &code }(),
	}
	pod := kubernetesresource.PodDetail{
		PodSummary: kubernetesresource.PodSummary{Phase: "Running", Ready: true},
		Containers: []kubernetesresource.PodContainer{{
			Name:         "server",
			Ready:        true,
			RestartCount: 3,
			State:        kubernetesresource.PodContainerState{Type: "running"},
			LastState:    &previous,
		}},
		Conditions: []kubernetesresource.PodCondition{
			{Type: "Ready", Status: "True"},
		},
	}

	findings := podFindings(pod, nil)
	if len(findings) != 1 ||
		findings[0].Code != FindingOOMKilled ||
		findings[0].Scope != "server" {
		t.Fatalf("unexpected findings: %+v", findings)
	}
}

func TestUnschedulableFindingFallsBackToTheSchedulerEvent(t *testing.T) {
	t.Parallel()

	pod := kubernetesresource.PodDetail{
		PodSummary: kubernetesresource.PodSummary{Phase: "Pending"},
		Conditions: []kubernetesresource.PodCondition{{
			Type:   "PodScheduled",
			Status: "False",
			Reason: "Unschedulable",
		}},
	}
	// Newest first, the order the describe hands them over in — so a finding
	// that reached for the last element rather than the latest timestamp would
	// quote the stale one and this test would say so.
	events := []Event{
		describedEventAt(
			"event-b",
			"FailedScheduling",
			"0/5 nodes are available: 2 Insufficient nvidia.com/gpu.",
			"",
			describedEventBase.Add(time.Minute),
		),
		describedEventAt(
			"event-a",
			"FailedScheduling",
			"0/3 nodes are available",
			"",
			describedEventBase,
		),
	}

	findings := podFindings(pod, events)
	if len(findings) != 1 {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	// The newest Event describes the Cluster as it is now.
	if findings[0].Message != "0/5 nodes are available: 2 Insufficient nvidia.com/gpu." {
		t.Fatalf("unexpected message: %q", findings[0].Message)
	}
	if len(findings[0].Evidence) != 2 ||
		findings[0].Evidence[0].Kind != EvidenceCondition ||
		findings[0].Evidence[1].Kind != EvidenceEvent ||
		findings[0].Evidence[1].Name != "event-b" {
		t.Fatalf("unexpected evidence: %+v", findings[0].Evidence)
	}
}

func TestContainerFindingCitesTheEventAboutThatContainer(t *testing.T) {
	t.Parallel()

	pod := kubernetesresource.PodDetail{
		PodSummary: kubernetesresource.PodSummary{Phase: "Pending"},
		Containers: []kubernetesresource.PodContainer{
			waitingContainer("server", "ImagePullBackOff", ""),
			waitingContainer("sidecar", "ContainerCreating", ""),
		},
	}
	events := []Event{
		describedEvent("event-a", "Failed", "Failed to pull image for sidecar", "sidecar"),
		describedEvent("event-b", "Failed", "Failed to pull image \"inference:v3\": not found", "server"),
	}

	findings := podFindings(pod, events)
	if len(findings) != 1 || findings[0].Scope != "server" {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	if findings[0].Message != "Failed to pull image \"inference:v3\": not found" {
		t.Fatalf("unexpected message: %q", findings[0].Message)
	}
	if len(findings[0].Evidence) != 2 ||
		findings[0].Evidence[1].Name != "event-b" {
		t.Fatalf("unexpected evidence: %+v", findings[0].Evidence)
	}
}

func TestProbeFindingsOnlyFireWhileTheProbeIsKeepingThePodOutOfService(t *testing.T) {
	t.Parallel()

	events := []Event{describedEvent(
		"event-a",
		"Unhealthy",
		"Readiness probe failed: HTTP probe failed with statuscode: 503",
		"server",
	)}
	running := kubernetesresource.PodDetail{
		PodSummary: kubernetesresource.PodSummary{Phase: "Running"},
		Containers: []kubernetesresource.PodContainer{{
			Name:  "server",
			State: kubernetesresource.PodContainerState{Type: "running"},
		}},
		Conditions: []kubernetesresource.PodCondition{
			{Type: "Ready", Status: "False"},
		},
	}

	findings := podFindings(running, events)
	if len(findings) != 1 ||
		findings[0].Code != FindingProbeFailure ||
		findings[0].Scope != "server" {
		t.Fatalf("unexpected findings: %+v", findings)
	}

	// The same Events, once the Pod passed its probe: a blip during startup is
	// not a finding.
	ready := running
	ready.Conditions = []kubernetesresource.PodCondition{
		{Type: "Ready", Status: "True"},
	}
	if findings := podFindings(ready, events); len(findings) != 0 {
		t.Fatalf("unexpected findings for a Ready Pod: %+v", findings)
	}

	// And once it finished: a Job's Pod is not Ready because it is done.
	succeeded := running
	succeeded.Phase = "Succeeded"
	if findings := podFindings(succeeded, events); len(findings) != 0 {
		t.Fatalf("unexpected findings for a finished Pod: %+v", findings)
	}
}

func TestEventContainerReadsTheKubeletFieldPath(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"spec.containers{server}":    "server",
		"spec.initContainers{fetch}": "fetch",
		"spec.containers":            "",
		"":                           "",
		"spec.containers{unfinished": "",
	}
	for fieldPath, want := range cases {
		if got := eventContainer(fieldPath); got != want {
			t.Fatalf("eventContainer(%q) = %q, want %q", fieldPath, got, want)
		}
	}
}
