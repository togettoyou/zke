package kubernetesdescribe

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// What one workload describe is allowed to cost.
//
// A describe is a page load, not a report: it runs under the ordinary operation
// timeout and every object it reaches is a round trip to the Agent. These bound
// the fan-out, and the ordering rules below make sure the objects that survive
// the cut are the ones that failed.
const (
	// Pods carried in the answer. Enough to see whether one replica is unhappy or
	// all of them are; a Deployment of fifty broken replicas is fifty copies of
	// one sentence.
	maxRelatedPods = 10
	// Intermediate controllers carried in the answer: for a Deployment mid-rollout
	// the new ReplicaSet and the one it is replacing.
	maxRelatedControllers = 2
	// Claims carried in the answer and read from the Cluster. A workload template
	// can technically reference more, but a diagnosis must not turn one page load
	// into an unbounded number of Agent round trips.
	maxRelatedPersistentVolumeClaims = 10
	// Related objects whose own Events are read. Each is a round trip, and
	// replicas that fail all fail the same way, so the first few say everything
	// the rest would.
	maxRelatedEventReads = 4
	// One page is the whole of it: a workload with more than this many Pods in one
	// Namespace is not a list a describe should page through.
	relatedListLimit = kubernetesresource.MaxResourceListLimit
)

type WorkloadInput struct {
	ClusterID string
	Resource  kubernetesresource.WorkloadResource
	Namespace string
	Name      string
}

// Where each workload's Pods actually live.
//
// Deployments and CronJobs do not own Pods: a Deployment owns ReplicaSets which
// own the Pods, and a CronJob owns Jobs which own theirs. That indirection is
// not a detail — it is where a rejected creation is reported, because the
// ReplicaSet is the object that tried and failed to make the Pod.
var workloadControllerIdentities = map[kubernetesresource.WorkloadResource]kubernetesresource.ResourceIdentity{
	kubernetesresource.WorkloadDeployments: {
		Group: "apps", Version: "v1", Resource: "replicasets",
	},
	kubernetesresource.WorkloadCronJobs: {
		Group: "batch", Version: "v1", Resource: "jobs",
	},
}

// DescribeWorkload answers "why is this workload not running".
//
// The workload itself is rarely the answer. It reports that it is short of
// replicas; the reason is in the ReplicaSet that could not create them or in the
// Pods that were created and did not start. So this walks down the ownership
// chain the workload's type defines, derives each Pod's findings by the Pod
// rules, and merges the Events of everything it touched into one timeline.
//
// A CronJob stops at its Jobs. Its Pods belong to those Jobs, and a Job knows
// how to describe its own — following the chain a second level here would
// duplicate that with a weaker claim to which Pods are whose.
func (service *Service) DescribeWorkload(
	ctx context.Context,
	input WorkloadInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	workload, err := service.resources.GetWorkload(
		ctx,
		input.ClusterID,
		input.Namespace,
		input.Resource,
		input.Name,
	)
	if err != nil {
		return Result{}, err
	}
	if workload.UID == "" {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	result := Result{
		Target: Target{
			APIVersion:      workload.APIVersion,
			Kind:            workload.Kind,
			Namespace:       workload.Namespace,
			Name:            workload.Name,
			UID:             workload.UID,
			ResourceVersion: workload.ResourceVersion,
		},
		Family:   FamilyWorkload,
		Workload: &workload,
		Related: &Related{
			Controllers:            []RelatedObject{},
			Pods:                   []RelatedObject{},
			PersistentVolumeClaims: []RelatedObject{},
		},
		Findings:         []Finding{},
		DegradedSections: []string{},
	}

	// The ownership walk. A failure here loses the related objects but not the
	// workload: its own conditions and Events still answer part of the question,
	// and a describe that refused to render because one list call failed would
	// answer none of it.
	controllers, pods, degraded := service.relatedObjects(ctx, input, workload)
	result.Related.Controllers = controllers.related
	result.Related.Pods = pods.related
	result.Related.Truncated = controllers.truncated || pods.truncated
	result.DegradedSections = append(result.DegradedSections, degraded...)
	claims, claimsDegraded := service.relatedPersistentVolumeClaims(
		ctx, input, workload,
	)
	result.Related.PersistentVolumeClaims = claims.related
	result.Related.Truncated = result.Related.Truncated || claims.truncated
	result.DegradedSections = append(result.DegradedSections, claimsDegraded...)

	// Controllers and Pending claims keep a share of the bounded Event budget:
	// FailedCreate exists only on a ReplicaSet, and a PVC's reason exists only on
	// that claim. Pod status already carries most container failures, so Pods use
	// the remainder.
	events, eventsDegraded := service.workloadEvents(
		ctx,
		input.ClusterID,
		input.Namespace,
		result.Target,
		append(
			append(append([]RelatedObject{}, controllers.related...), claims.related...),
			pods.related...,
		),
	)
	result.Events = events
	result.DegradedSections = append(result.DegradedSections, eventsDegraded...)
	pods.refineWithEvents(events.Items)
	claims.refineWithEvents(events.Items)
	result.Findings = workloadFindings(workload, events.Items)
	return result, nil
}

// The objects one level of the walk produced, and whether the Cluster held more.
//
// `details` is kept alongside the Pod rows so that the findings can be derived a
// second time once the Events are in: a Pod's status alone says `Unschedulable`,
// and the scheduler's Event says which resource ran out.
type relatedGroup struct {
	related   []RelatedObject
	details   []kubernetesresource.PodDetail
	truncated bool
}

type persistentVolumeClaimGroup struct {
	related   []RelatedObject
	details   []kubernetesresource.StorageResourceDetail
	truncated bool
}

func (group *persistentVolumeClaimGroup) refineWithEvents(events []Event) {
	if len(group.details) != len(group.related) {
		return
	}
	byObject := make(map[string][]Event, len(group.related))
	for _, event := range events {
		if event.Regarding.UID != "" {
			byObject[event.Regarding.UID] = append(
				byObject[event.Regarding.UID], event,
			)
		}
	}
	for index, object := range group.related {
		own := byObject[object.UID]
		if len(own) == 0 {
			continue
		}
		findings := persistentVolumeClaimFindings(group.details[index], own)
		group.related[index].Findings = findings
		group.related[index].Ready =
			group.details[index].PersistentVolumeClaim != nil &&
				group.details[index].PersistentVolumeClaim.Phase == "Bound" &&
				len(findings) == 0
	}
}

// refineWithEvents re-derives each Pod's findings from the Events that turned
// out to be about it. Pods whose Events were not read keep what their own status
// supported.
func (group *relatedGroup) refineWithEvents(events []Event) {
	if len(group.details) != len(group.related) {
		return
	}
	byObject := make(map[string][]Event, len(group.related))
	for _, event := range events {
		if event.Regarding.UID == "" {
			continue
		}
		byObject[event.Regarding.UID] = append(byObject[event.Regarding.UID], event)
	}
	for index, object := range group.related {
		own := byObject[object.UID]
		if len(own) == 0 {
			continue
		}
		findings := podFindings(group.details[index], own)
		group.related[index].Findings = findings
		group.related[index].Ready = group.details[index].Ready && len(findings) == 0
	}
}

func (service *Service) relatedObjects(
	ctx context.Context,
	input WorkloadInput,
	workload kubernetesresource.WorkloadDetail,
) (relatedGroup, relatedGroup, []string) {
	selector, hasSelector := workloadPodSelector(workload)
	degraded := make([]string, 0, 2)
	controllers := relatedGroup{related: []RelatedObject{}}
	pods := relatedGroup{related: []RelatedObject{}}
	var indirectOwners map[string]struct{}

	identity, indirect := workloadControllerIdentities[input.Resource]
	if indirect {
		// A CronJob has no Pod selector at all, so its Jobs are found by ownership
		// alone; a Deployment's ReplicaSets carry its template's labels, which
		// narrows the list before ownership settles it.
		listSelector := ""
		if input.Resource == kubernetesresource.WorkloadDeployments {
			if !hasSelector {
				return controllers, pods, append(degraded, "related")
			}
			listSelector = selector
		}
		owned, truncated, err := service.ownedObjects(
			ctx,
			input.ClusterID,
			input.Namespace,
			identity,
			listSelector,
			map[string]struct{}{workload.UID: {}},
		)
		if err != nil {
			return controllers, pods, append(degraded, "related")
		}
		controllers.related = controllerObjects(owned)
		controllers.truncated = truncated || len(owned) > maxRelatedControllers
		indirectOwners = make(map[string]struct{}, len(owned))
		for _, controller := range owned {
			if uid := string(controller.GetUID()); uid != "" {
				indirectOwners[uid] = struct{}{}
			}
		}
	}

	// A CronJob's Pods are its Jobs' Pods, and each of those Jobs describes them
	// better than this could.
	if input.Resource == kubernetesresource.WorkloadCronJobs {
		return controllers, pods, degraded
	}
	if !hasSelector {
		return controllers, pods, append(degraded, "related")
	}
	// Whose Pods count as this workload's: the ones its ReplicaSets control when
	// there is a ReplicaSet in between, and the ones it controls itself otherwise.
	owners := map[string]struct{}{workload.UID: {}}
	if indirect {
		owners = indirectOwners
		if len(owners) == 0 {
			return controllers, pods, degraded
		}
	}
	podDetails, truncated, err := service.resources.ListPodDetails(
		ctx,
		kubernetesresource.ListPodsInput{
			ClusterID:     input.ClusterID,
			Namespace:     input.Namespace,
			Limit:         relatedListLimit,
			LabelSelector: selector,
		},
	)
	if err != nil {
		return controllers, pods, append(degraded, "related")
	}
	return controllers, podObjects(podDetails, owners, truncated), degraded
}

// relatedPersistentVolumeClaims reads the claims referenced by the workload's
// Pod template. Claims are fetched concurrently but the fan-out is bounded, and
// the answer keeps template order so a refresh does not reshuffle the page.
func (service *Service) relatedPersistentVolumeClaims(
	ctx context.Context,
	input WorkloadInput,
	workload kubernetesresource.WorkloadDetail,
) (persistentVolumeClaimGroup, []string) {
	names := make([]string, 0, len(workload.Volumes))
	seen := make(map[string]struct{}, len(workload.Volumes))
	for _, volume := range workload.Volumes {
		if volume.PersistentVolumeClaim == nil ||
			volume.PersistentVolumeClaim.ClaimName == "" {
			continue
		}
		name := volume.PersistentVolumeClaim.ClaimName
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	group := persistentVolumeClaimGroup{related: []RelatedObject{}}
	if len(names) == 0 {
		return group, nil
	}
	if len(names) > maxRelatedPersistentVolumeClaims {
		names = names[:maxRelatedPersistentVolumeClaims]
		group.truncated = true
	}
	type readResult struct {
		detail kubernetesresource.StorageResourceDetail
		err    error
	}
	results := make([]readResult, len(names))
	var waitGroup sync.WaitGroup
	for index, name := range names {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			detail, err := service.resources.GetStorageResource(
				ctx,
				input.ClusterID,
				input.Namespace,
				kubernetesresource.StoragePersistentVolumeClaims,
				name,
			)
			results[index] = readResult{detail: detail, err: err}
		}()
	}
	waitGroup.Wait()
	degraded := false
	for _, result := range results {
		if result.err != nil || result.detail.PersistentVolumeClaim == nil {
			degraded = true
			continue
		}
		claim := result.detail
		findings := persistentVolumeClaimFindings(claim, nil)
		group.related = append(group.related, RelatedObject{
			Kind:      claim.Kind,
			Name:      claim.Name,
			UID:       claim.UID,
			Namespace: claim.Namespace,
			Status:    claim.PersistentVolumeClaim.Phase,
			Ready: claim.PersistentVolumeClaim.Phase == "Bound" &&
				len(findings) == 0,
			Findings: findings,
		})
		group.details = append(group.details, claim)
	}
	order := make([]int, len(group.related))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		return !group.related[order[left]].Ready && group.related[order[right]].Ready
	})
	orderedRelated := make([]RelatedObject, 0, len(group.related))
	orderedDetails := make([]kubernetesresource.StorageResourceDetail, 0, len(group.details))
	for _, index := range order {
		orderedRelated = append(orderedRelated, group.related[index])
		orderedDetails = append(orderedDetails, group.details[index])
	}
	group.related, group.details = orderedRelated, orderedDetails
	if degraded {
		return group, []string{"related.persistent_volume_claims"}
	}
	return group, nil
}

// ownedObjects lists one resource and keeps what the given owners control.
//
// The label selector narrows the list to objects stamped from the same template,
// which is not the same as belonging: two controllers in a Namespace can select
// the same Pods. The controller owner reference settles it, matched on UID so
// that a same-named controller recreated since cannot lend its children to this
// one.
func (service *Service) ownedObjects(
	ctx context.Context,
	clusterID string,
	namespace string,
	identity kubernetesresource.ResourceIdentity,
	labelSelector string,
	owners map[string]struct{},
) ([]unstructured.Unstructured, bool, error) {
	page, err := service.resources.ListResources(
		ctx,
		kubernetesresource.ListResourcesInput{
			ClusterID:     clusterID,
			Resource:      identity,
			Namespace:     namespace,
			Limit:         relatedListLimit,
			LabelSelector: labelSelector,
		},
	)
	if err != nil {
		return nil, false, err
	}
	owned := make([]unstructured.Unstructured, 0, len(page.Items))
	for _, item := range page.Items {
		object := unstructured.Unstructured{Object: item}
		if !controlledBy(object.GetOwnerReferences(), owners) {
			continue
		}
		owned = append(owned, object)
	}
	return owned, page.ContinueToken != "", nil
}

func controlledBy(
	owners []metav1.OwnerReference,
	uids map[string]struct{},
) bool {
	for _, owner := range owners {
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		if _, owned := uids[string(owner.UID)]; owned {
			return true
		}
	}
	return false
}

// controllerObjects reduces the intermediate controllers to rows, newest first.
//
// Newest first because that is the one being rolled out: during a rollout the
// ReplicaSet that cannot create Pods is the new one, and the old one is working
// perfectly right up until it is scaled away.
func controllerObjects(
	objects []unstructured.Unstructured,
) []RelatedObject {
	sort.SliceStable(objects, func(left, right int) bool {
		newer := objects[left].GetCreationTimestamp()
		older := objects[right].GetCreationTimestamp()
		return older.Before(&newer)
	})
	related := make([]RelatedObject, 0, len(objects))
	for index, object := range objects {
		if index == maxRelatedControllers {
			break
		}
		desired, ready := controllerReplicaCounts(object)
		related = append(related, RelatedObject{
			Kind:      object.GetKind(),
			Name:      object.GetName(),
			UID:       string(object.GetUID()),
			Namespace: object.GetNamespace(),
			Status:    fmt.Sprintf("%d/%d", ready, desired),
			// A ReplicaSet scaled to zero is finished rather than unhealthy: the
			// old one always ends a rollout that way.
			Ready:    ready >= desired,
			Findings: []Finding{},
		})
	}
	return related
}

// controllerReplicaCounts reads how many Pods the controller wanted and how many
// are ready, from whichever fields its own type uses.
func controllerReplicaCounts(object unstructured.Unstructured) (int64, int64) {
	if desired, found, err := unstructured.NestedInt64(
		object.Object, "spec", "replicas",
	); err == nil && found {
		ready, _, _ := unstructured.NestedInt64(
			object.Object, "status", "readyReplicas",
		)
		return desired, ready
	}
	// A Job counts completions rather than replicas, and a Job with no declared
	// completions is done when one Pod succeeds.
	desired, found, err := unstructured.NestedInt64(
		object.Object, "spec", "completions",
	)
	if err != nil || !found {
		desired = 1
	}
	succeeded, _, _ := unstructured.NestedInt64(
		object.Object, "status", "succeeded",
	)
	return desired, succeeded
}

// podObjects reduces the owned Pods to rows carrying their own findings,
// unhealthy first.
//
// The order is the whole point of the cut: a Deployment with three healthy
// replicas and one that will not start must not answer with the three that are
// fine. Within each half the Pods keep the order the Cluster listed them in,
// which is by name.
func podObjects(
	pods []kubernetesresource.PodDetail,
	owners map[string]struct{},
	listTruncated bool,
) relatedGroup {
	owned := make([]kubernetesresource.PodDetail, 0, len(pods))
	for _, pod := range pods {
		if podControlledBy(pod, owners) {
			owned = append(owned, pod)
		}
	}
	related := make([]RelatedObject, 0, len(owned))
	for _, pod := range owned {
		// Derived from status alone here. Only a few of these Pods have their
		// Events read — one round trip each — and those findings are refined once
		// the Events are in.
		findings := podFindings(pod, nil)
		related = append(related, RelatedObject{
			Kind:      "Pod",
			Name:      pod.Name,
			UID:       pod.UID,
			Namespace: pod.Namespace,
			Status:    podStatusText(pod),
			Ready:     pod.Ready && len(findings) == 0,
			Findings:  findings,
		})
	}
	// Sorted together so a row and its detail keep the same index.
	order := make([]int, len(related))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		return !related[order[left]].Ready && related[order[right]].Ready
	})
	group := relatedGroup{
		related:   make([]RelatedObject, 0, len(related)),
		details:   make([]kubernetesresource.PodDetail, 0, len(related)),
		truncated: listTruncated,
	}
	for _, index := range order {
		if len(group.related) == maxRelatedPods {
			group.truncated = true
			break
		}
		group.related = append(group.related, related[index])
		group.details = append(group.details, owned[index])
	}
	return group
}

func podControlledBy(
	pod kubernetesresource.PodDetail,
	owners map[string]struct{},
) bool {
	for _, owner := range pod.OwnerReferences {
		if !owner.Controller {
			continue
		}
		if _, owned := owners[owner.UID]; owned {
			return true
		}
	}
	return false
}

func podStatusText(pod kubernetesresource.PodDetail) string {
	if pod.DeletionTimestamp != nil {
		return "Terminating"
	}
	if pod.Reason != "" {
		return pod.Reason
	}
	return pod.Phase
}

// workloadEvents merges the Events of the workload and of the objects under it
// into one timeline.
//
// It is one timeline rather than one list per object because the story is one
// story: the ReplicaSet was created, it failed to create a Pod, the Pod that did
// exist could not pull its image. Each Event says which object it is about.
//
// Reads are bounded and run together: the workload always, then the related
// objects in the order they are already sorted, which puts the ones that failed
// first.
func (service *Service) workloadEvents(
	ctx context.Context,
	clusterID string,
	namespace string,
	target Target,
	related []RelatedObject,
) (Events, []string) {
	if service.events == nil {
		return Events{Items: []Event{}, Omitted: EventsOmittedUnavailable},
			[]string{"events"}
	}
	subjects := make([]string, 0, maxRelatedEventReads+1)
	subjects = append(subjects, target.UID)
	for _, object := range related {
		if len(subjects) > maxRelatedEventReads {
			break
		}
		if object.UID != "" {
			subjects = append(subjects, object.UID)
		}
	}
	type readResult struct {
		items     []Event
		truncated bool
		err       error
	}
	results := make([]readResult, len(subjects))
	var waitGroup sync.WaitGroup
	for index, uid := range subjects {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			items, truncated, err := service.readObjectEvents(
				ctx, clusterID, namespace, uid,
			)
			results[index] = readResult{items: items, truncated: truncated, err: err}
		}()
	}
	waitGroup.Wait()

	// The workload's own read failing is the one that makes the section
	// unusable; a related object's read failing only shortens the timeline, and
	// the workload's half is still worth showing.
	if results[0].err != nil {
		return Events{Items: []Event{}, Omitted: EventsOmittedUnavailable},
			[]string{"events"}
	}
	merged := make([]Event, 0, len(results[0].items))
	truncated := false
	degraded := []string{}
	for _, result := range results {
		if result.err != nil {
			truncated = true
			degraded = []string{"events.related"}
			continue
		}
		merged = append(merged, result.items...)
		truncated = truncated || result.truncated
	}
	sort.SliceStable(merged, func(left, right int) bool {
		return eventOrder(merged[left]).Before(eventOrder(merged[right]))
	})
	// The cut is made after merging so the newest lines of the whole story
	// survive rather than the newest lines of whichever object was read first.
	if uint32(len(merged)) > service.eventLimit {
		merged = merged[uint32(len(merged))-service.eventLimit:]
		truncated = true
	}
	return Events{Items: merged, Truncated: truncated}, degraded
}

// workloadPodSelector rebuilds the workload's Pod selector as a selector string.
//
// A controller with no selector selects nothing, and asking the API Server for
// every Pod in the Namespace is not the fallback: that would attach other
// workloads' failures to this one.
func workloadPodSelector(
	workload kubernetesresource.WorkloadDetail,
) (string, bool) {
	if workload.Selector == nil {
		return "", false
	}
	requirements := make(
		[]metav1.LabelSelectorRequirement,
		0,
		len(workload.Selector.MatchExpressions),
	)
	for _, requirement := range workload.Selector.MatchExpressions {
		requirements = append(requirements, metav1.LabelSelectorRequirement{
			Key:      requirement.Key,
			Operator: metav1.LabelSelectorOperator(requirement.Operator),
			Values:   requirement.Values,
		})
	}
	parsed, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels:      workload.Selector.MatchLabels,
		MatchExpressions: requirements,
	})
	if err != nil || parsed.Empty() {
		return "", false
	}
	return parsed.String(), true
}
