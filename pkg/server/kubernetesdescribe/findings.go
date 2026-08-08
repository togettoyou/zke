package kubernetesdescribe

import (
	"strings"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

// The findings this package derives for a Pod.
//
// Each one answers a question an operator asks out loud — why is it not
// scheduled, why is it not pulling, why does it keep dying — and each is read
// from state Kubernetes reported rather than inferred from it. A container
// killed with exit code 137 is reported as the exit code it is, not as an OOM
// kill: 137 is SIGKILL, and only the `OOMKilled` reason says which SIGKILL it
// was.
const (
	FindingPodUnschedulable     = "PodUnschedulable"
	FindingImagePullFailure     = "ImagePullFailure"
	FindingContainerConfigError = "ContainerConfigError"
	FindingCrashLoopBackOff     = "CrashLoopBackOff"
	FindingContainerTerminated  = "ContainerTerminated"
	FindingOOMKilled            = "OOMKilled"
	FindingVolumeMountFailure   = "VolumeMountFailure"
	FindingProbeFailure         = "ProbeFailure"
)

// Container waiting reasons that mean the image never arrived.
var imagePullWaitingReasons = map[string]struct{}{
	"ErrImagePull":              {},
	"ImagePullBackOff":          {},
	"InvalidImageName":          {},
	"ImageInspectError":         {},
	"RegistryUnavailable":       {},
	"ErrImageNeverPull":         {},
	"SignatureValidationFailed": {},
}

// Container waiting reasons that mean the container could not be built out of
// what the Pod asked for: a missing ConfigMap or Secret key, a bad reference.
var containerConfigWaitingReasons = map[string]struct{}{
	"CreateContainerConfigError": {},
	"CreateContainerError":       {},
}

// Event reasons that mean a volume never became usable. These are reported
// against the Pod rather than a container: the kubelet fails the mount before
// any container starts, so no container is at fault and none is named.
var volumeFailureEventReasons = map[string]struct{}{
	"FailedMount":        {},
	"FailedAttachVolume": {},
	"FailedMapVolume":    {},
}

const (
	scheduledConditionType      = "PodScheduled"
	readyConditionType          = "Ready"
	conditionStatusFalse        = "False"
	conditionStatusTrue         = "True"
	schedulingFailedEventReason = "FailedScheduling"
	unhealthyEventReason        = "Unhealthy"
	waitingContainerState       = "waiting"
	terminatedContainerState    = "terminated"
	crashLoopBackOffReason      = "CrashLoopBackOff"
	completedTerminationReason  = "Completed"
	oomKilledReason             = "OOMKilled"
	runningPodPhase             = "Running"
	eventTypeWarning            = "Warning"
)

// podFindings reads the Pod's own status and the Events naming it, and returns
// what the two together support.
//
// Object-level findings come first and containers keep spec order, so the same
// Pod always produces the same list: a diagnosis that reshuffles between two
// refreshes reads as new information when nothing changed.
func podFindings(
	pod kubernetesresource.PodDetail,
	events []Event,
) []Finding {
	findings := make([]Finding, 0, 4)
	if finding, found := unschedulableFinding(pod, events); found {
		findings = append(findings, finding)
	}
	if finding, found := volumeFailureFinding(events); found {
		findings = append(findings, finding)
	}
	containers := make([]kubernetesresource.PodContainer, 0,
		len(pod.InitContainers)+len(pod.Containers)+len(pod.EphemeralContainers))
	containers = append(containers, pod.InitContainers...)
	containers = append(containers, pod.Containers...)
	containers = append(containers, pod.EphemeralContainers...)
	for _, container := range containers {
		findings = append(findings, containerFindings(container, events)...)
	}
	return append(findings, probeFindings(pod, events)...)
}

// unschedulableFinding reports a Pod the scheduler could not place.
//
// The condition says that it failed; the scheduler's own Event says what ran
// out, and that message ("0/5 nodes are available: 3 Insufficient cpu …") is
// the whole reason an operator opens describe. The condition usually repeats
// it, so the Event is a fallback rather than a duplicate.
func unschedulableFinding(
	pod kubernetesresource.PodDetail,
	events []Event,
) (Finding, bool) {
	condition, found := podCondition(pod, scheduledConditionType)
	if !found || condition.Status != conditionStatusFalse {
		return Finding{}, false
	}
	finding := Finding{
		Code:     FindingPodUnschedulable,
		Severity: SeverityWarning,
		Reason:   condition.Reason,
		Message:  condition.Message,
		Evidence: []Evidence{
			{Kind: EvidenceCondition, Name: scheduledConditionType},
		},
	}
	if event, ok := latestEvent(events, func(candidate Event) bool {
		return candidate.Reason == schedulingFailedEventReason
	}); ok {
		if finding.Message == "" {
			finding.Message = event.Message
		}
		finding.Evidence = append(
			finding.Evidence,
			Evidence{Kind: EvidenceEvent, Name: event.UID},
		)
	}
	return finding, true
}

func volumeFailureFinding(events []Event) (Finding, bool) {
	event, found := latestEvent(events, func(candidate Event) bool {
		_, matches := volumeFailureEventReasons[candidate.Reason]
		return matches
	})
	if !found {
		return Finding{}, false
	}
	return Finding{
		Code:     FindingVolumeMountFailure,
		Severity: SeverityWarning,
		Reason:   event.Reason,
		Message:  event.Message,
		Evidence: []Evidence{{Kind: EvidenceEvent, Name: event.UID}},
	}, true
}

func containerFindings(
	container kubernetesresource.PodContainer,
	events []Event,
) []Finding {
	findings := make([]Finding, 0, 2)
	state := container.State
	if state.Type == waitingContainerState {
		switch {
		case containsReason(imagePullWaitingReasons, state.Reason):
			findings = append(findings, containerFinding(
				FindingImagePullFailure, container, state, events,
			))
		case containsReason(containerConfigWaitingReasons, state.Reason):
			findings = append(findings, containerFinding(
				FindingContainerConfigError, container, state, events,
			))
		case state.Reason == crashLoopBackOffReason:
			finding := containerFinding(
				FindingCrashLoopBackOff, container, state, events,
			)
			// What the container did before the back-off is the part that says
			// why: the waiting state only says Kubernetes has stopped trying for
			// the moment.
			if container.LastState != nil {
				finding.ExitCode = container.LastState.ExitCode
				if container.LastState.Message != "" {
					finding.Message = container.LastState.Message
				}
			}
			findings = append(findings, finding)
		}
	}
	if state.Type == terminatedContainerState &&
		state.Reason != "" &&
		state.Reason != completedTerminationReason {
		code := FindingContainerTerminated
		if state.Reason == oomKilledReason {
			code = FindingOOMKilled
		}
		finding := containerFinding(code, container, state, events)
		finding.ExitCode = state.ExitCode
		findings = append(findings, finding)
	}
	// An OOM kill is reported even when the container is running again: the
	// restart that followed it is exactly what hides it from the current state,
	// and "it came back" is not the same as "it was fine".
	if container.LastState != nil &&
		container.LastState.Reason == oomKilledReason &&
		!hasFinding(findings, FindingOOMKilled) {
		finding := containerFinding(
			FindingOOMKilled, container, *container.LastState, events,
		)
		finding.ExitCode = container.LastState.ExitCode
		findings = append(findings, finding)
	}
	return findings
}

func containerFinding(
	code string,
	container kubernetesresource.PodContainer,
	state kubernetesresource.PodContainerState,
	events []Event,
) Finding {
	finding := Finding{
		Code:     code,
		Severity: SeverityWarning,
		Scope:    container.Name,
		Reason:   state.Reason,
		Message:  state.Message,
		Evidence: []Evidence{
			{Kind: EvidenceContainerState, Name: container.Name},
		},
	}
	// The kubelet scopes its Events to a container through the involved
	// object's field path, so this attaches the Event that reported this
	// container rather than the newest Event about the Pod.
	if event, found := latestEvent(events, func(candidate Event) bool {
		return candidate.Container == container.Name &&
			candidate.Type == eventTypeWarning
	}); found {
		if finding.Message == "" {
			finding.Message = event.Message
		}
		finding.Evidence = append(
			finding.Evidence,
			Evidence{Kind: EvidenceEvent, Name: event.UID},
		)
	}
	return finding
}

// probeFindings reports probes that are failing now.
//
// A probe that failed once during startup and then passed is ordinary, and a
// describe that reported it would cry wolf on every healthy Pod that took a
// moment to warm up. So this fires only for a running Pod that is not Ready,
// which is the case where a failing probe is the thing keeping it out of
// service.
func probeFindings(
	pod kubernetesresource.PodDetail,
	events []Event,
) []Finding {
	if pod.Phase != runningPodPhase {
		return nil
	}
	condition, found := podCondition(pod, readyConditionType)
	if !found || condition.Status == conditionStatusTrue {
		return nil
	}
	findings := make([]Finding, 0, 1)
	reported := make(map[string]struct{}, 1)
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Reason != unhealthyEventReason {
			continue
		}
		if _, seen := reported[event.Container]; seen {
			continue
		}
		reported[event.Container] = struct{}{}
		findings = append(findings, Finding{
			Code:     FindingProbeFailure,
			Severity: SeverityWarning,
			Scope:    event.Container,
			Reason:   event.Reason,
			Message:  event.Message,
			Evidence: []Evidence{{Kind: EvidenceEvent, Name: event.UID}},
		})
	}
	return findings
}

func podCondition(
	pod kubernetesresource.PodDetail,
	conditionType string,
) (kubernetesresource.PodCondition, bool) {
	for _, condition := range pod.Conditions {
		if condition.Type == conditionType {
			return condition, true
		}
	}
	return kubernetesresource.PodCondition{}, false
}

// latestEvent returns the most recent match. Events arrive oldest first, so
// this walks backwards: the newest FailedScheduling describes the Cluster as it
// is now, and the ones behind it describe a Cluster that has since changed.
func latestEvent(events []Event, matches func(Event) bool) (Event, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		if matches(events[index]) {
			return events[index], true
		}
	}
	return Event{}, false
}

func containsReason(reasons map[string]struct{}, reason string) bool {
	_, found := reasons[reason]
	return found
}

func hasFinding(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

// eventContainer reads the container name out of an Event's field path.
//
// The kubelet writes `spec.containers{app}`, and that brace is the only thing
// tying "Back-off restarting failed container" to the container it was about.
func eventContainer(fieldPath string) string {
	open := strings.Index(fieldPath, "{")
	if open < 0 || !strings.HasSuffix(fieldPath, "}") {
		return ""
	}
	return fieldPath[open+1 : len(fieldPath)-1]
}
