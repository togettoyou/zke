package kubernetesdescribe

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/resourcewatch"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	deploymentUID = "b2f0a2f4-2b6f-4a9c-8f38-4bb2f4f5d901"
	replicaSetUID = "9b1a4f0e-1c33-4d59-8c1b-6f1a4f2f7c22"
	strangerUID   = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"
)

func testWorkloadDetail() kubernetesresource.WorkloadDetail {
	return kubernetesresource.WorkloadDetail{
		WorkloadSummary: kubernetesresource.WorkloadSummary{
			Resource:        kubernetesresource.WorkloadDeployments,
			APIVersion:      "apps/v1",
			Kind:            "Deployment",
			Namespace:       "model-serving",
			Name:            "inference",
			UID:             deploymentUID,
			ResourceVersion: "204",
			Replicas: &kubernetesresource.WorkloadReplicaStatus{
				Desired: 2, Current: 2, Ready: 0,
			},
		},
		Selector: &kubernetesresource.WorkloadSelector{
			MatchLabels: map[string]string{"app": "inference"},
		},
		Conditions: []kubernetesresource.WorkloadCondition{},
	}
}

func ownedObject(
	kind string,
	name string,
	uid string,
	ownerUID string,
	fields map[string]any,
) map[string]any {
	controller := true
	object := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": "model-serving",
			"uid":       uid,
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"name":       "inference",
				"uid":        ownerUID,
				"controller": controller,
			}},
		},
	}
	for key, value := range fields {
		object[key] = value
	}
	return object
}

func ownedPod(
	name string,
	uid string,
	ownerUID string,
	phase string,
	container kubernetesresource.PodContainer,
) kubernetesresource.PodDetail {
	return kubernetesresource.PodDetail{
		PodSummary: kubernetesresource.PodSummary{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  "model-serving",
			Name:       name,
			UID:        uid,
			Phase:      phase,
			Ready:      phase == "Running",
		},
		OwnerReferences: []kubernetesresource.PodOwnerReference{{
			Kind:       "ReplicaSet",
			Name:       "inference-7d9f",
			UID:        ownerUID,
			Controller: true,
		}},
		Containers: []kubernetesresource.PodContainer{container},
		Conditions: []kubernetesresource.PodCondition{},
	}
}

func objectEvent(
	uid string,
	kind string,
	name string,
	objectUID string,
	reason string,
	message string,
	at time.Time,
) corev1.Event {
	return corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "." + uid,
			Namespace: "model-serving",
			UID:       types.UID("event-" + uid),
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:      kind,
			Name:      name,
			Namespace: "model-serving",
			UID:       types.UID(objectUID),
		},
		Type:          "Warning",
		Reason:        reason,
		Message:       message,
		Count:         1,
		LastTimestamp: metav1.NewTime(at),
	}
}

func storageClaim(name, uid, phase string) kubernetesresource.StorageResourceDetail {
	return kubernetesresource.StorageResourceDetail{
		StorageResourceSummary: kubernetesresource.StorageResourceSummary{
			Resource:   kubernetesresource.StoragePersistentVolumeClaims,
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
			Namespace:  "model-serving",
			Name:       name,
			UID:        uid,
			PersistentVolumeClaim: &kubernetesresource.PersistentVolumeClaimSummary{
				Phase: phase,
			},
		},
		PersistentVolumeClaimDetail: &kubernetesresource.PersistentVolumeClaimDetail{
			Conditions: []kubernetesresource.PersistentVolumeClaimCondition{},
		},
	}
}

// A Deployment's Pods are two hops away, and both hops are checked by ownership
// rather than by the label selector alone.
func TestDescribeWorkloadWalksTheOwnershipChain(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{
		workload: testWorkloadDetail(),
		lists: map[string]kubernetesresource.ResourcePage{
			"replicasets": {Items: []map[string]any{
				ownedObject("ReplicaSet", "inference-7d9f", replicaSetUID, deploymentUID,
					map[string]any{
						"spec":   map[string]any{"replicas": int64(2)},
						"status": map[string]any{"readyReplicas": int64(0)},
					}),
				// Same labels, another owner: selected by the list, not this
				// Deployment's.
				ownedObject("ReplicaSet", "other-6c2a", "rs-other", strangerUID, nil),
			}},
		},
		podDetails: []kubernetesresource.PodDetail{
			ownedPod("inference-7d9f-aaa", "pod-a", replicaSetUID, "Pending",
				waitingContainer("server", "ImagePullBackOff", "Back-off pulling image")),
			ownedPod("stranger-0", "pod-x", strangerUID, "Running",
				kubernetesresource.PodContainer{Name: "server"}),
		},
	}
	events := &fakeEventSource{byUID: map[string][]corev1.Event{}}
	service := NewService(access, events, Config{})

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.Family != FamilyWorkload || result.Workload == nil || result.Related == nil {
		t.Fatalf("unexpected projection: %+v", result)
	}
	if access.listInput["replicasets"].LabelSelector != "app=inference" ||
		access.podListInput.LabelSelector != "app=inference" {
		t.Fatalf("the workload selector was not used: %+v", access.listInput)
	}
	if len(result.Related.Controllers) != 1 ||
		result.Related.Controllers[0].UID != replicaSetUID ||
		result.Related.Controllers[0].Status != "0/2" ||
		result.Related.Controllers[0].Ready {
		t.Fatalf("unexpected controllers: %+v", result.Related.Controllers)
	}
	// The stranger's Pod matched the labels and belongs to another controller.
	if len(result.Related.Pods) != 1 ||
		result.Related.Pods[0].UID != "pod-a" ||
		result.Related.Pods[0].Ready {
		t.Fatalf("unexpected pods: %+v", result.Related.Pods)
	}
	if len(result.Related.Pods[0].Findings) != 1 ||
		result.Related.Pods[0].Findings[0].Code != FindingImagePullFailure {
		t.Fatalf("the Pod's own findings are missing: %+v", result.Related.Pods[0])
	}
	// Nothing on the Deployment itself is wrong; the answer is on the Pod.
	if len(result.Findings) != 0 {
		t.Fatalf("unexpected workload findings: %+v", result.Findings)
	}
}

// The failure a Pod-level rule can never see: the Pod was refused before it
// existed, and the Event is on the ReplicaSet rather than on the Deployment.
func TestDescribeWorkloadReportsCreationRefusedOnTheReplicaSet(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	access := &fakeResourceAccess{
		workload: testWorkloadDetail(),
		lists: map[string]kubernetesresource.ResourcePage{
			"replicasets": {Items: []map[string]any{
				ownedObject("ReplicaSet", "inference-7d9f", replicaSetUID, deploymentUID, nil),
			}},
		},
	}
	events := &fakeEventSource{byUID: map[string][]corev1.Event{
		replicaSetUID: {objectEvent(
			"a", "ReplicaSet", "inference-7d9f", replicaSetUID,
			"FailedCreate",
			`pods "inference-7d9f-" is forbidden: exceeded quota: compute, requested: limits.memory=8Gi`,
			base,
		)},
	}}
	service := NewService(access, events, Config{})

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 ||
		result.Findings[0].Code != FindingReplicaCreateRejected {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
	if result.Findings[0].Message == "" ||
		len(result.Findings[0].Evidence) != 1 ||
		result.Findings[0].Evidence[0].Kind != EvidenceEvent {
		t.Fatalf("unexpected evidence: %+v", result.Findings[0])
	}
	// The timeline says which object each line is about.
	if len(result.Events.Items) != 1 ||
		result.Events.Items[0].Regarding.Kind != "ReplicaSet" ||
		result.Events.Items[0].Regarding.UID != replicaSetUID {
		t.Fatalf("unexpected events: %+v", result.Events.Items)
	}
}

func TestDescribeWorkloadReportsAReferencedPendingPVC(t *testing.T) {
	t.Parallel()

	workload := testWorkloadDetail()
	workload.Volumes = []kubernetesresource.WorkloadVolume{{
		Name: "model-cache",
		PersistentVolumeClaim: &kubernetesresource.WorkloadPersistentVolumeClaimVolume{
			ClaimName: "model-cache",
		},
	}}
	access := &fakeResourceAccess{
		workload: workload,
		lists: map[string]kubernetesresource.ResourcePage{
			"replicasets": {Items: []map[string]any{}},
		},
		claims: map[string]kubernetesresource.StorageResourceDetail{
			"model-cache": storageClaim("model-cache", "pvc-a", "Pending"),
		},
	}
	events := &fakeEventSource{byUID: map[string][]corev1.Event{
		"pvc-a": {objectEvent(
			"a", "PersistentVolumeClaim", "model-cache", "pvc-a",
			"ProvisioningFailed", "storageclass.storage.k8s.io \"fast\" not found",
			time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC),
		)},
	}}
	service := NewService(access, events, Config{})

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Related.PersistentVolumeClaims) != 1 {
		t.Fatalf("unexpected claims: %+v", result.Related.PersistentVolumeClaims)
	}
	claim := result.Related.PersistentVolumeClaims[0]
	if claim.Ready || claim.Status != "Pending" || len(claim.Findings) != 1 ||
		claim.Findings[0].Code != FindingPVCPending ||
		claim.Findings[0].Reason != "ProvisioningFailed" ||
		claim.Findings[0].Message == "" {
		t.Fatalf("unexpected claim finding: %+v", claim)
	}
}

func TestDescribeWorkloadReadsConditionsOfItsOwn(t *testing.T) {
	t.Parallel()

	workload := testWorkloadDetail()
	workload.Conditions = []kubernetesresource.WorkloadCondition{
		{
			Type:    "Progressing",
			Status:  "False",
			Reason:  "ProgressDeadlineExceeded",
			Message: `ReplicaSet "inference-7d9f" has timed out progressing.`,
		},
		{Type: "Available", Status: "False", Reason: "MinimumReplicasUnavailable"},
	}
	service := NewService(
		&fakeResourceAccess{workload: workload},
		&fakeEventSource{byUID: map[string][]corev1.Event{}},
		Config{},
	)

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	// `Available=False` is the shortfall restated, not a cause, and is not a
	// finding of its own.
	if len(result.Findings) != 1 ||
		result.Findings[0].Code != FindingWorkloadProgressStalled ||
		result.Findings[0].Reason != "ProgressDeadlineExceeded" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestDescribeWorkloadReportsAJobThatFailed(t *testing.T) {
	t.Parallel()

	workload := kubernetesresource.WorkloadDetail{
		WorkloadSummary: kubernetesresource.WorkloadSummary{
			Resource:   kubernetesresource.WorkloadJobs,
			APIVersion: "batch/v1",
			Kind:       "Job",
			Namespace:  "model-serving",
			Name:       "import",
			UID:        deploymentUID,
		},
		Selector: &kubernetesresource.WorkloadSelector{
			MatchLabels: map[string]string{"batch.kubernetes.io/controller-uid": deploymentUID},
		},
		Conditions: []kubernetesresource.WorkloadCondition{{
			Type:    "Failed",
			Status:  "True",
			Reason:  "BackoffLimitExceeded",
			Message: "Job has reached the specified backoff limit",
		}},
	}
	access := &fakeResourceAccess{workload: workload}
	service := NewService(
		access,
		&fakeEventSource{byUID: map[string][]corev1.Event{}},
		Config{},
	)

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadJobs,
		Namespace: "model-serving",
		Name:      "import",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A Job owns its Pods directly: no intermediate controller is listed.
	if _, listed := access.listInput["replicasets"]; listed {
		t.Fatal("a Job was walked as if it owned ReplicaSets")
	}
	if len(result.Findings) != 1 ||
		result.Findings[0].Code != FindingWorkloadFailed ||
		result.Findings[0].Reason != "BackoffLimitExceeded" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

// A CronJob has no Pod selector, and its Pods belong to the Jobs it stamps out.
// Following the chain a second level here would guess at which Pods are whose.
func TestDescribeWorkloadStopsAtACronJobsJobs(t *testing.T) {
	t.Parallel()

	workload := kubernetesresource.WorkloadDetail{
		WorkloadSummary: kubernetesresource.WorkloadSummary{
			Resource:   kubernetesresource.WorkloadCronJobs,
			APIVersion: "batch/v1",
			Kind:       "CronJob",
			Namespace:  "model-serving",
			Name:       "nightly",
			UID:        deploymentUID,
		},
	}
	access := &fakeResourceAccess{
		workload: workload,
		lists: map[string]kubernetesresource.ResourcePage{
			"jobs": {Items: []map[string]any{
				ownedObject("Job", "nightly-29", "job-a", deploymentUID, map[string]any{
					"spec":   map[string]any{"completions": int64(1)},
					"status": map[string]any{"succeeded": int64(0)},
				}),
			}},
		},
	}
	service := NewService(
		access,
		&fakeEventSource{byUID: map[string][]corev1.Event{}},
		Config{},
	)

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadCronJobs,
		Namespace: "model-serving",
		Name:      "nightly",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Owned by UID, with no label selector to narrow the list first.
	if access.listInput["jobs"].LabelSelector != "" {
		t.Fatalf("unexpected Job list selector: %+v", access.listInput["jobs"])
	}
	if len(result.Related.Controllers) != 1 ||
		result.Related.Controllers[0].Kind != "Job" ||
		result.Related.Controllers[0].Status != "0/1" {
		t.Fatalf("unexpected controllers: %+v", result.Related.Controllers)
	}
	if len(result.Related.Pods) != 0 {
		t.Fatalf("a CronJob's Pods were guessed at: %+v", result.Related.Pods)
	}
	if access.podListInput.ClusterID != "" {
		t.Fatal("Pods were listed for a CronJob")
	}
}

// The cut has to keep the Pods that failed: a Deployment with nine healthy
// replicas and one that will not start must not answer with the nine.
func TestDescribeWorkloadKeepsTheUnhealthyPodsWhenItTruncates(t *testing.T) {
	t.Parallel()

	pods := make([]kubernetesresource.PodDetail, 0, maxRelatedPods+2)
	for index := range maxRelatedPods + 1 {
		pods = append(pods, ownedPod(
			fmt.Sprintf("inference-7d9f-%02d", index),
			fmt.Sprintf("pod-%02d", index),
			replicaSetUID,
			"Running",
			kubernetesresource.PodContainer{
				Name:  "server",
				Ready: true,
				State: kubernetesresource.PodContainerState{Type: "running"},
			},
		))
	}
	broken := ownedPod("inference-7d9f-zz", "pod-zz", replicaSetUID, "Pending",
		waitingContainer("server", "CreateContainerConfigError", `secret "model-credentials" not found`))
	pods = append(pods, broken)

	access := &fakeResourceAccess{
		workload: testWorkloadDetail(),
		lists: map[string]kubernetesresource.ResourcePage{
			"replicasets": {Items: []map[string]any{
				ownedObject("ReplicaSet", "inference-7d9f", replicaSetUID, deploymentUID, nil),
			}},
		},
		podDetails: pods,
	}
	service := NewService(
		access,
		&fakeEventSource{byUID: map[string][]corev1.Event{}},
		Config{},
	)

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Related.Pods) != maxRelatedPods || !result.Related.Truncated {
		t.Fatalf("unexpected pod window: %d truncated=%v",
			len(result.Related.Pods), result.Related.Truncated)
	}
	if result.Related.Pods[0].UID != "pod-zz" ||
		len(result.Related.Pods[0].Findings) != 1 {
		t.Fatalf("the unhealthy Pod did not survive the cut: %+v", result.Related.Pods[0])
	}
}

func TestDescribeWorkloadMarksAControllerCutAsTruncated(t *testing.T) {
	t.Parallel()

	controllers := make([]map[string]any, 0, maxRelatedControllers+1)
	for index := range maxRelatedControllers + 1 {
		controllers = append(controllers, ownedObject(
			"ReplicaSet",
			fmt.Sprintf("inference-%d", index),
			fmt.Sprintf("rs-%d", index),
			deploymentUID,
			nil,
		))
	}
	service := NewService(&fakeResourceAccess{
		workload: testWorkloadDetail(),
		lists: map[string]kubernetesresource.ResourcePage{
			"replicasets": {Items: controllers},
		},
	}, &fakeEventSource{byUID: map[string][]corev1.Event{}}, Config{})

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Related.Controllers) != maxRelatedControllers ||
		!result.Related.Truncated {
		t.Fatalf("unexpected controller window: %+v", result.Related)
	}
}

// The Events read for a Pod refine that Pod's findings: its status says
// `Unschedulable`, and the scheduler's Event says what ran out.
func TestDescribeWorkloadRefinesPodFindingsWithTheEventsItRead(t *testing.T) {
	t.Parallel()

	pod := ownedPod("inference-7d9f-aaa", "pod-a", replicaSetUID, "Pending",
		kubernetesresource.PodContainer{Name: "server"})
	pod.Conditions = []kubernetesresource.PodCondition{{
		Type:   "PodScheduled",
		Status: "False",
		Reason: "Unschedulable",
	}}
	access := &fakeResourceAccess{
		workload: testWorkloadDetail(),
		lists: map[string]kubernetesresource.ResourcePage{
			"replicasets": {Items: []map[string]any{
				ownedObject("ReplicaSet", "inference-7d9f", replicaSetUID, deploymentUID, nil),
			}},
		},
		podDetails: []kubernetesresource.PodDetail{pod},
	}
	events := &fakeEventSource{byUID: map[string][]corev1.Event{
		"pod-a": {objectEvent(
			"a", "Pod", pod.Name, "pod-a", "FailedScheduling",
			"0/5 nodes are available: 2 Insufficient nvidia.com/gpu.",
			time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC),
		)},
	}}
	service := NewService(access, events, Config{})

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Related.Pods) != 1 ||
		len(result.Related.Pods[0].Findings) != 1 {
		t.Fatalf("unexpected pods: %+v", result.Related.Pods)
	}
	finding := result.Related.Pods[0].Findings[0]
	if finding.Code != FindingPodUnschedulable ||
		finding.Message != "0/5 nodes are available: 2 Insufficient nvidia.com/gpu." {
		t.Fatalf("the finding was not refined with the Event: %+v", finding)
	}
}

// One failed list must not take the whole describe with it: the workload's own
// conditions and Events still answer part of the question.
func TestDescribeWorkloadDegradesWhenTheRelatedListFails(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{
		workload: testWorkloadDetail(),
		listErr:  kubernetesresource.ErrClusterUnavailable,
	}
	service := NewService(
		access,
		&fakeEventSource{byUID: map[string][]corev1.Event{}},
		Config{},
	)

	result, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatalf("a failed related read must not fail the describe: %v", err)
	}
	if len(result.DegradedSections) != 1 || result.DegradedSections[0] != "related" {
		t.Fatalf("unexpected degraded sections: %v", result.DegradedSections)
	}
	if result.Workload == nil {
		t.Fatal("the workload itself was dropped")
	}
}

// The Event fan-out is bounded: replicas that fail all fail the same way, so the
// first few say everything the rest would.
func TestDescribeWorkloadBoundsTheEventFanOut(t *testing.T) {
	t.Parallel()

	pods := make([]kubernetesresource.PodDetail, 0, maxRelatedPods)
	for index := range maxRelatedPods {
		pods = append(pods, ownedPod(
			fmt.Sprintf("inference-7d9f-%02d", index),
			fmt.Sprintf("pod-%02d", index),
			replicaSetUID,
			"Pending",
			waitingContainer("server", "ImagePullBackOff", ""),
		))
	}
	access := &fakeResourceAccess{
		workload: testWorkloadDetail(),
		lists: map[string]kubernetesresource.ResourcePage{
			"replicasets": {Items: []map[string]any{
				ownedObject("ReplicaSet", "inference-7d9f", replicaSetUID, deploymentUID, nil),
			}},
		},
		podDetails: pods,
	}
	events := &fakeEventSource{byUID: map[string][]corev1.Event{}}
	service := NewService(access, events, Config{})

	if _, err := service.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	}); err != nil {
		t.Fatal(err)
	}
	if len(events.requested) != maxRelatedEventReads+1 {
		t.Fatalf("unexpected Event reads: %v", events.requested)
	}
	// The reads run together, so the order they arrive in is not the assertion;
	// what matters is that the workload's own Events are never the ones dropped.
	if !slices.Contains(events.requested, deploymentUID) {
		t.Fatalf("the workload's own Events were not read: %v", events.requested)
	}
	if !slices.Contains(events.requested, replicaSetUID) {
		t.Fatalf("the controller's Events lost their reserved budget: %v", events.requested)
	}
}

// A related object's Event read failing shortens the timeline; the workload's
// own failing empties the section.
func TestDescribeWorkloadSeparatesTheTwoEventFailures(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{
		workload: testWorkloadDetail(),
		lists: map[string]kubernetesresource.ResourcePage{
			"replicasets": {Items: []map[string]any{
				ownedObject("ReplicaSet", "inference-7d9f", replicaSetUID, deploymentUID, nil),
			}},
		},
	}
	partial := NewService(access, &fakeEventSource{
		byUID:   map[string][]corev1.Event{},
		failUID: map[string]error{replicaSetUID: resourcewatch.ErrAgentNotConnected},
	}, Config{})
	result, err := partial.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Events.Omitted != "" ||
		len(result.DegradedSections) != 1 ||
		result.DegradedSections[0] != "events.related" {
		t.Fatalf("unexpected degradation: omitted=%q sections=%v",
			result.Events.Omitted, result.DegradedSections)
	}

	whole := NewService(access, &fakeEventSource{
		byUID:   map[string][]corev1.Event{},
		failUID: map[string]error{deploymentUID: resourcewatch.ErrAgentNotConnected},
	}, Config{})
	result, err = whole.DescribeWorkload(context.Background(), WorkloadInput{
		ClusterID: testClusterID,
		Resource:  kubernetesresource.WorkloadDeployments,
		Namespace: "model-serving",
		Name:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Events.Omitted != EventsOmittedUnavailable ||
		len(result.DegradedSections) != 1 ||
		result.DegradedSections[0] != "events" {
		t.Fatalf("unexpected degradation: omitted=%q sections=%v",
			result.Events.Omitted, result.DegradedSections)
	}
}
