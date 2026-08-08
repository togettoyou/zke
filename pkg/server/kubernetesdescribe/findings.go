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
	FindingPVCPending           = "PVCPending"
)

const (
	FindingServiceNoEndpoints         = "ServiceNoEndpoints"
	FindingServiceNoReadyEndpoints    = "ServiceNoReadyEndpoints"
	FindingServiceLoadBalancerPending = "ServiceLoadBalancerPending"
)

func serviceFindings(
	service kubernetesresource.NetworkingResourceDetail,
	endpoints *ServiceEndpoints,
) []Finding {
	if service.Service == nil {
		return []Finding{}
	}
	findings := make([]Finding, 0, 2)
	view := service.Service
	if view.Spec.Type == "LoadBalancer" && len(view.LoadBalancerIngress) == 0 {
		findings = append(findings, Finding{
			Code: FindingServiceLoadBalancerPending, Severity: SeverityWarning,
			Evidence: []Evidence{{Kind: EvidenceObjectStatus, Name: "status.loadBalancer.ingress"}},
		})
	}
	if view.Spec.Type == "ExternalName" || endpoints == nil || endpoints.Truncated {
		return findings
	}
	code := ""
	if endpoints.Endpoints == 0 {
		code = FindingServiceNoEndpoints
	} else if endpoints.ReadyEndpoints == 0 {
		code = FindingServiceNoReadyEndpoints
	}
	if code != "" {
		findings = append(findings, Finding{
			Code: code, Severity: SeverityWarning,
			Evidence: []Evidence{{Kind: EvidenceObjectStatus, Name: "endpoints.ready"}},
		})
	}
	return findings
}

// The findings derived for a workload itself.
//
// They are deliberately few. A workload that is short of replicas is a symptom,
// and its cause is on the Pods that did not start or on the controller that
// could not create them — which is why a workload describe carries those objects
// and their findings rather than restating the shortfall as a diagnosis of its
// own.
const (
	FindingWorkloadProgressStalled = "WorkloadProgressStalled"
	FindingReplicaCreateRejected   = "ReplicaCreateRejected"
	FindingWorkloadFailed          = "WorkloadFailed"
)

// Node findings keep controller-reported health separate from scheduler
// capacity signals. Conditions say the Node is unhealthy; request ratios say
// new Pods are likely to have little room even when current utilization is low.
const (
	FindingNodeNotReady           = "NodeNotReady"
	FindingNodeMemoryPressure     = "NodeMemoryPressure"
	FindingNodeDiskPressure       = "NodeDiskPressure"
	FindingNodePIDPressure        = "NodePIDPressure"
	FindingNodeNetworkUnavailable = "NodeNetworkUnavailable"
	FindingNodeSchedulingDisabled = "NodeSchedulingDisabled"
	FindingNodeCPURequestsHigh    = "NodeCPURequestsHigh"
	FindingNodeMemoryRequestsHigh = "NodeMemoryRequestsHigh"
	FindingNodePodCapacityHigh    = "NodePodCapacityHigh"
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

// PVC Event reasons that explain why a claim is still Pending. Kubernetes can
// legitimately wait for a Pod to be scheduled before binding, while a failed
// provisioner or binding decision is actionable immediately; both belong next
// to the Pending phase rather than being guessed from it.
var pendingPVCEventReasons = map[string]struct{}{
	"WaitForFirstConsumer": {},
	"ProvisioningFailed":   {},
	"FailedBinding":        {},
}

const (
	progressingConditionType    = "Progressing"
	replicaFailureConditionType = "ReplicaFailure"
	failedConditionType         = "Failed"
	createFailedEventReason     = "FailedCreate"
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

func nodeConditionFindings(
	node kubernetesresource.NodeDetail,
) []Finding {
	findings := make([]Finding, 0, 6)
	readyConditionFound := false
	for _, condition := range node.Conditions {
		code := ""
		reported := false
		switch condition.Type {
		case "Ready":
			readyConditionFound = true
			code = FindingNodeNotReady
			reported = condition.Status != conditionStatusTrue
		case "MemoryPressure":
			code = FindingNodeMemoryPressure
			reported = condition.Status == conditionStatusTrue
		case "DiskPressure":
			code = FindingNodeDiskPressure
			reported = condition.Status == conditionStatusTrue
		case "PIDPressure":
			code = FindingNodePIDPressure
			reported = condition.Status == conditionStatusTrue
		case "NetworkUnavailable":
			code = FindingNodeNetworkUnavailable
			reported = condition.Status == conditionStatusTrue
		}
		if !reported {
			continue
		}
		findings = append(findings, Finding{
			Code:     code,
			Severity: SeverityWarning,
			Reason:   condition.Reason,
			Message:  condition.Message,
			Evidence: []Evidence{{
				Kind: EvidenceCondition,
				Name: condition.Type,
			}},
		})
	}
	if !readyConditionFound {
		findings = append(findings, Finding{
			Code:     FindingNodeNotReady,
			Severity: SeverityWarning,
			Evidence: []Evidence{{
				Kind: EvidenceObjectStatus,
				Name: "status",
			}},
		})
	}
	if node.Unschedulable {
		findings = append(findings, Finding{
			Code:     FindingNodeSchedulingDisabled,
			Severity: SeverityWarning,
			Evidence: []Evidence{{
				Kind: EvidenceObjectStatus,
				Name: "spec.unschedulable",
			}},
		})
	}
	return findings
}

func nodeResourceFindings(resources NodeResources) []Finding {
	if resources.Truncated {
		return []Finding{}
	}
	findings := make([]Finding, 0, 3)
	checks := []struct {
		code        string
		evidence    string
		requested   int64
		allocatable int64
	}{
		{FindingNodeCPURequestsHigh, "resources.cpu", resources.CPURequestedMillis, resources.CPUAllocatableMillis},
		{FindingNodeMemoryRequestsHigh, "resources.memory", resources.MemoryRequestedBytes, resources.MemoryAllocatableBytes},
		{FindingNodePodCapacityHigh, "resources.pods", resources.NonTerminalPods, resources.PodAllocatable},
	}
	for _, check := range checks {
		if check.allocatable <= 0 || check.requested < ninetyPercent(check.allocatable) {
			continue
		}
		findings = append(findings, Finding{
			Code:     check.code,
			Severity: SeverityWarning,
			Evidence: []Evidence{{
				Kind: EvidenceObjectStatus,
				Name: check.evidence,
			}},
		})
	}
	return findings
}

// ninetyPercent rounds the boundary up without multiplying a potentially
// huge quantity by nine first.
func ninetyPercent(value int64) int64 {
	return value/10*9 + (value%10*9+9)/10
}

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

// workloadFindings reads a workload's own conditions and the Events of it and
// the objects under it.
//
// Everything here is about the workload as a controller: whether it gave up
// making progress, whether something refused the Pods it tried to create,
// whether it finished by failing. Why an individual Pod did not run is a Pod
// finding, and it stays on that Pod.
func workloadFindings(
	workload kubernetesresource.WorkloadDetail,
	events []Event,
) []Finding {
	findings := make([]Finding, 0, 2)
	if finding, found := replicaFailureFinding(workload, events); found {
		findings = append(findings, finding)
	}
	if condition, found := workloadCondition(
		workload, progressingConditionType,
	); found && condition.Status == conditionStatusFalse {
		// `Progressing=False` is the Deployment controller reporting that it has
		// stopped waiting: the rollout passed its progress deadline. The reason
		// says which deadline and the message names the ReplicaSet involved.
		findings = append(findings, Finding{
			Code:     FindingWorkloadProgressStalled,
			Severity: SeverityWarning,
			Reason:   condition.Reason,
			Message:  condition.Message,
			Evidence: []Evidence{
				{Kind: EvidenceCondition, Name: progressingConditionType},
			},
		})
	}
	if condition, found := workloadCondition(
		workload, failedConditionType,
	); found && condition.Status == conditionStatusTrue {
		// A Job that exhausted its backoff limit or ran past its deadline. The
		// reason is the Kubernetes one — `BackoffLimitExceeded`,
		// `DeadlineExceeded` — and the Pods carry what actually went wrong.
		findings = append(findings, Finding{
			Code:     FindingWorkloadFailed,
			Severity: SeverityWarning,
			Reason:   condition.Reason,
			Message:  condition.Message,
			Evidence: []Evidence{
				{Kind: EvidenceCondition, Name: failedConditionType},
			},
		})
	}
	return findings
}

// persistentVolumeClaimFindings reports a referenced claim that has not bound.
// The phase is the stable fact; when the PVC's own Event was inside the bounded
// event window, its reason and message explain whether binding is waiting for a
// consumer or failed at the provisioner.
func persistentVolumeClaimFindings(
	claim kubernetesresource.StorageResourceDetail,
	events []Event,
) []Finding {
	if claim.PersistentVolumeClaim == nil ||
		claim.PersistentVolumeClaim.Phase != "Pending" {
		return []Finding{}
	}
	finding := Finding{
		Code:     FindingPVCPending,
		Severity: SeverityWarning,
		Reason:   "Pending",
		Evidence: []Evidence{{
			Kind: EvidenceObjectStatus,
			Name: "phase",
		}},
	}
	if event, found := latestEvent(events, func(candidate Event) bool {
		_, matches := pendingPVCEventReasons[candidate.Reason]
		return matches
	}); found {
		finding.Reason = event.Reason
		finding.Message = event.Message
		finding.Evidence = append(finding.Evidence, Evidence{
			Kind: EvidenceEvent,
			Name: event.UID,
		})
	} else if claim.PersistentVolumeClaimDetail != nil {
		for _, condition := range claim.PersistentVolumeClaimDetail.Conditions {
			if condition.Status != conditionStatusTrue {
				continue
			}
			finding.Reason = condition.Reason
			finding.Message = condition.Message
			finding.Evidence = append(finding.Evidence, Evidence{
				Kind: EvidenceCondition,
				Name: condition.Type,
			})
			break
		}
	}
	return []Finding{finding}
}

// replicaFailureFinding reports Pods that were refused before they existed.
//
// This is the failure a Pod-level rule can never see, because there is no Pod:
// a ResourceQuota, a Pod Security admission policy or a missing ServiceAccount
// makes the creation itself fail, and the only record is the controller's
// condition and its `FailedCreate` Event. For a Deployment that Event is on the
// ReplicaSet rather than the Deployment, which is why the workload describe
// reads the Events of the controllers under it as well.
func replicaFailureFinding(
	workload kubernetesresource.WorkloadDetail,
	events []Event,
) (Finding, bool) {
	finding := Finding{
		Code:     FindingReplicaCreateRejected,
		Severity: SeverityWarning,
		Evidence: []Evidence{},
	}
	condition, found := workloadCondition(workload, replicaFailureConditionType)
	if found && condition.Status == conditionStatusTrue {
		finding.Reason, finding.Message = condition.Reason, condition.Message
		finding.Evidence = append(finding.Evidence, Evidence{
			Kind: EvidenceCondition, Name: replicaFailureConditionType,
		})
	}
	event, hasEvent := latestEvent(events, func(candidate Event) bool {
		return candidate.Reason == createFailedEventReason
	})
	if hasEvent {
		if finding.Message == "" {
			finding.Reason, finding.Message = event.Reason, event.Message
		}
		finding.Evidence = append(finding.Evidence, Evidence{
			Kind: EvidenceEvent, Name: event.UID,
		})
	}
	if len(finding.Evidence) == 0 {
		return Finding{}, false
	}
	return finding, true
}

func workloadCondition(
	workload kubernetesresource.WorkloadDetail,
	conditionType string,
) (kubernetesresource.WorkloadCondition, bool) {
	for _, condition := range workload.Conditions {
		if condition.Type == conditionType {
			return condition, true
		}
	}
	return kubernetesresource.WorkloadCondition{}, false
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
