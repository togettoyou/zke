package kubernetesresource

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// Kubernetes' own record of which revision a ReplicaSet holds.
	deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"
	// The conventional note about what a revision changed. `kubectl` writes it;
	// ZKE only reads it back, and never as anything but a label for the row.
	changeCauseAnnotation = "kubernetes.io/change-cause"
	// Added by the Deployment controller to the template of every ReplicaSet it
	// stamps out. It identifies the ReplicaSet, not the Deployment, so a rollback
	// must not carry it back up.
	podTemplateHashLabel = "pod-template-hash"
	// The strategic-merge directive a StatefulSet or DaemonSet controller writes
	// into the template it records. It is an instruction to a patch, not a field
	// of a Pod template.
	strategicMergeDirective = "$patch"
	// One page is the whole history: `revisionHistoryLimit` defaults to 10 and a
	// cluster keeping five hundred revisions of one workload is not a list this
	// endpoint should try to page through.
	maxWorkloadRevisionListLimit = MaxResourceListLimit
)

var (
	// The three controllers Kubernetes keeps a revision history for. A Job runs
	// once and a CronJob stamps out Jobs; neither has anything to roll back to.
	ErrWorkloadRevisionsUnsupported = errors.New("workload type has no revision history")
	ErrWorkloadRevisionNotFound     = errors.New("Kubernetes workload revision not found")
	// The selected revision's Pod template is the one already running, so the
	// rollback is refused instead of writing an update that changes nothing and
	// leaves an audit record saying it did.
	ErrWorkloadRevisionUnchanged = errors.New("workload already runs the selected revision")
)

// Where each controller's history lives.
//
// A Deployment's revisions are the ReplicaSets it owns; a StatefulSet's and a
// DaemonSet's are ControllerRevisions. Both are read-only here — ZKE never
// creates, edits or prunes a revision object, which stays the controller's job.
var workloadRevisionIdentities = map[WorkloadResource]ResourceIdentity{
	WorkloadDeployments:  {Group: "apps", Version: "v1", Resource: "replicasets"},
	WorkloadStatefulSets: {Group: "apps", Version: "v1", Resource: "controllerrevisions"},
	WorkloadDaemonSets:   {Group: "apps", Version: "v1", Resource: "controllerrevisions"},
}

type ListWorkloadRevisionsInput struct {
	ClusterID string
	Namespace string
	Resource  WorkloadResource
	Name      string
}

type WorkloadRevisionPage struct {
	Revisions []WorkloadRevision `json:"revisions"`
	// True when the Cluster holds more revision objects than one page returns.
	// The newest revisions are not guaranteed to be the ones that came back, so
	// a truncated history is reported rather than presented as the whole of it.
	Truncated bool `json:"truncated"`
}

// WorkloadRevision is one recorded Pod template of a workload.
//
// `name` and `uid` identify the object the record was read from — a ReplicaSet
// or a ControllerRevision — which is what makes a revision inspectable in the
// cluster. `current` means this revision's template is the one the workload runs
// now, so rolling back to it would change nothing.
type WorkloadRevision struct {
	Revision          int64                       `json:"revision"`
	Name              string                      `json:"name"`
	UID               string                      `json:"uid"`
	CreationTimestamp time.Time                   `json:"creation_timestamp"`
	Current           bool                        `json:"current"`
	ChangeCause       string                      `json:"change_cause,omitempty"`
	Images            []string                    `json:"images"`
	Containers        []WorkloadRevisionContainer `json:"containers"`
	InitContainers    []WorkloadRevisionContainer `json:"init_containers"`
}

type WorkloadRevisionContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// RollbackWorkloadInput requests a rollback of one workload to one recorded revision.
//
// The preconditions are the ones an update carries, and for the same reason:
// the revision list was read against one version of the object, and a rollback
// submitted against a newer one is a decision made about something else.
type RollbackWorkloadInput struct {
	WorkloadMutationInput
	Revision        int64
	UID             string
	ResourceVersion string
}

// WorkloadResourceSupportsRevisions reports whether the type keeps a history.
func WorkloadResourceSupportsRevisions(resource WorkloadResource) bool {
	_, exists := workloadRevisionIdentities[resource]
	return exists
}

// ListWorkloadRevisions returns the recorded Pod templates of one workload,
// newest revision first.
func (service *Service) ListWorkloadRevisions(
	ctx context.Context,
	input ListWorkloadRevisionsInput,
) (WorkloadRevisionPage, error) {
	if !WorkloadResourceSupportsRevisions(input.Resource) {
		return WorkloadRevisionPage{}, ErrWorkloadRevisionsUnsupported
	}
	if !validWorkloadTarget(input.Namespace, input.Name, input.Resource) {
		return WorkloadRevisionPage{}, ErrInvalidInput
	}
	workload, err := service.readWorkloadRevisionTarget(
		ctx,
		input.ClusterID,
		input.Namespace,
		input.Resource,
		input.Name,
	)
	if err != nil {
		return WorkloadRevisionPage{}, err
	}
	records, truncated, err := service.readWorkloadRevisionRecords(
		ctx,
		input.ClusterID,
		input.Namespace,
		input.Resource,
		workload,
	)
	if err != nil {
		return WorkloadRevisionPage{}, err
	}
	revisions := make([]WorkloadRevision, 0, len(records))
	for _, record := range records {
		revisions = append(revisions, WorkloadRevision{
			Revision:          record.revision,
			Name:              record.name,
			UID:               record.uid,
			CreationTimestamp: record.creationTimestamp,
			Current:           workloadTemplatesEqual(record.template, workload.template),
			ChangeCause:       record.changeCause,
			Images:            podImages(record.template.Spec),
			Containers:        workloadRevisionContainers(record.template.Spec.Containers),
			InitContainers:    workloadRevisionContainers(record.template.Spec.InitContainers),
		})
	}
	return WorkloadRevisionPage{Revisions: revisions, Truncated: truncated}, nil
}

// RollbackWorkload restores the Pod template one recorded revision holds.
//
// Only `spec.template` is written. For these three controllers a revision is
// created exactly when the Pod template changes, so the template is the whole of
// what the revision recorded: replicas, update strategy and the object's own
// labels and annotations were never part of it and are left as they are. That
// also keeps a rollback from undoing a scale or a quota change nobody asked
// about.
func (service *Service) RollbackWorkload(
	ctx context.Context,
	input RollbackWorkloadInput,
) (WorkloadDetail, error) {
	identity, exists := WorkloadResourceIdentity(input.Resource)
	if !exists {
		return WorkloadDetail{}, ErrInvalidInput
	}
	if !WorkloadResourceSupportsRevisions(input.Resource) {
		return WorkloadDetail{}, ErrWorkloadRevisionsUnsupported
	}
	if !validWorkloadTarget(input.Namespace, input.Name, input.Resource) ||
		!validWorkloadMutationIdentity(input.UID, input.ResourceVersion) ||
		input.Revision < 1 {
		return WorkloadDetail{}, ErrInvalidInput
	}
	workload, err := service.readWorkloadRevisionTarget(
		ctx,
		input.ClusterID,
		input.Namespace,
		input.Resource,
		input.Name,
	)
	if err != nil {
		return WorkloadDetail{}, err
	}
	if workload.uid != input.UID ||
		workload.resourceVersion != input.ResourceVersion {
		return WorkloadDetail{}, ErrUpstreamConflict
	}
	records, _, err := service.readWorkloadRevisionRecords(
		ctx,
		input.ClusterID,
		input.Namespace,
		input.Resource,
		workload,
	)
	if err != nil {
		return WorkloadDetail{}, err
	}
	target, found := findWorkloadRevision(records, input.Revision)
	if !found {
		return WorkloadDetail{}, ErrWorkloadRevisionNotFound
	}
	if workloadTemplatesEqual(target.template, workload.template) {
		return WorkloadDetail{}, ErrWorkloadRevisionUnchanged
	}
	updated, ok := runtime.DeepCopyJSONValue(workload.object).(map[string]any)
	if !ok {
		return WorkloadDetail{}, ErrInvalidResponse
	}
	// The recorded template is written back as it was recorded, field for field.
	// Decoding it into this Server's compiled Pod types first would silently drop
	// anything a newer Kubernetes has added to a Pod spec, and a rollback that
	// quietly removes fields is the one thing it must not do.
	if unstructured.SetNestedField(
		updated,
		target.rawTemplate,
		"spec", "template",
	) != nil {
		return WorkloadDetail{}, ErrInvalidResponse
	}
	result, err := service.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: input.ClusterID,
		Resource:  identity,
		Namespace: input.Namespace,
		Name:      input.Name,
		Object:    updated,
		Options: MutationOptions{
			DryRun: input.DryRun,
		},
		Confirm:        input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return WorkloadDetail{}, err
	}
	return workloadDetail(result, input.Resource, input.Namespace, input.Name)
}

// The workload a revision list or a rollback is about, read once.
type workloadRevisionTarget struct {
	object          map[string]any
	uid             string
	resourceVersion string
	// The Pod selector, as a label selector string. Revision objects carry the
	// template's labels, so this is what narrows the list to one workload's
	// history before the ownership check narrows it to exactly that workload's.
	selector string
	template corev1.PodTemplateSpec
}

func (service *Service) readWorkloadRevisionTarget(
	ctx context.Context,
	clusterID string,
	namespace string,
	resource WorkloadResource,
	name string,
) (workloadRevisionTarget, error) {
	identity, exists := WorkloadResourceIdentity(resource)
	if !exists {
		return workloadRevisionTarget{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID,
		Resource:  identity,
		Namespace: namespace,
		Name:      name,
	})
	if err != nil {
		return workloadRevisionTarget{}, err
	}
	var (
		selector *metav1.LabelSelector
		template corev1.PodTemplateSpec
	)
	switch resource {
	case WorkloadDeployments:
		var workload appsv1.Deployment
		if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &workload) != nil {
			return workloadRevisionTarget{}, ErrInvalidResponse
		}
		selector, template = workload.Spec.Selector, workload.Spec.Template
	case WorkloadStatefulSets:
		var workload appsv1.StatefulSet
		if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &workload) != nil {
			return workloadRevisionTarget{}, ErrInvalidResponse
		}
		selector, template = workload.Spec.Selector, workload.Spec.Template
	case WorkloadDaemonSets:
		var workload appsv1.DaemonSet
		if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &workload) != nil {
			return workloadRevisionTarget{}, ErrInvalidResponse
		}
		selector, template = workload.Spec.Selector, workload.Spec.Template
	default:
		return workloadRevisionTarget{}, ErrWorkloadRevisionsUnsupported
	}
	current := &unstructured.Unstructured{Object: object}
	// A controller with no selector cannot own a revision either, and asking the
	// API Server for every ReplicaSet in the Namespace is not the fallback.
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil || selector == nil || parsed.Empty() ||
		string(current.GetUID()) == "" {
		return workloadRevisionTarget{}, ErrInvalidResponse
	}
	return workloadRevisionTarget{
		object:          object,
		uid:             string(current.GetUID()),
		resourceVersion: current.GetResourceVersion(),
		selector:        parsed.String(),
		template:        template,
	}, nil
}

// One revision object, reduced to what a list row and a rollback need.
type workloadRevisionRecord struct {
	revision          int64
	name              string
	uid               string
	creationTimestamp time.Time
	changeCause       string
	// The template as recorded, for the write.
	rawTemplate map[string]any
	// The same template as Kubernetes types, for comparison and for the summary.
	template corev1.PodTemplateSpec
}

func (service *Service) readWorkloadRevisionRecords(
	ctx context.Context,
	clusterID string,
	namespace string,
	resource WorkloadResource,
	workload workloadRevisionTarget,
) ([]workloadRevisionRecord, bool, error) {
	identity, exists := workloadRevisionIdentities[resource]
	if !exists {
		return nil, false, ErrWorkloadRevisionsUnsupported
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID:     clusterID,
		Resource:      identity,
		Namespace:     namespace,
		Limit:         maxWorkloadRevisionListLimit,
		LabelSelector: workload.selector,
	})
	if err != nil {
		return nil, false, err
	}
	records := make([]workloadRevisionRecord, 0, len(page.Items))
	for _, item := range page.Items {
		record, ok := workloadRevisionRecordOf(item, resource, workload.uid)
		if !ok {
			continue
		}
		records = append(records, record)
	}
	// Newest first, and by creation time when two objects claim one revision —
	// a collision the controllers avoid but nothing in the API prevents.
	sort.SliceStable(records, func(left, right int) bool {
		if records[left].revision != records[right].revision {
			return records[left].revision > records[right].revision
		}
		return records[left].creationTimestamp.After(records[right].creationTimestamp)
	})
	return records, page.ContinueToken != "", nil
}

// Reads one revision object, or reports that it is not one of this workload's.
//
// The label selector already narrowed the list to templates that match, which is
// not the same as belonging: two controllers in a Namespace can select the same
// Pods. The owner reference is what settles it, and it is matched on UID so that
// a same-named controller recreated since cannot lend its history to this one.
func workloadRevisionRecordOf(
	item map[string]any,
	resource WorkloadResource,
	ownerUID string,
) (workloadRevisionRecord, bool) {
	object := &unstructured.Unstructured{Object: item}
	if !ownedByWorkload(object.GetOwnerReferences(), ownerUID) {
		return workloadRevisionRecord{}, false
	}
	revision, rawTemplate, ok := workloadRevisionTemplate(item, resource)
	if !ok {
		return workloadRevisionRecord{}, false
	}
	var template corev1.PodTemplateSpec
	if runtime.DefaultUnstructuredConverter.FromUnstructured(
		rawTemplate,
		&template,
	) != nil {
		return workloadRevisionRecord{}, false
	}
	return workloadRevisionRecord{
		revision:          revision,
		name:              object.GetName(),
		uid:               string(object.GetUID()),
		creationTimestamp: object.GetCreationTimestamp().Time,
		changeCause:       object.GetAnnotations()[changeCauseAnnotation],
		rawTemplate:       rawTemplate,
		template:          template,
	}, true
}

// The revision number and the recorded template, per history object.
//
// A ReplicaSet carries the template in its own spec and the revision in an
// annotation the Deployment controller writes. A ControllerRevision carries a
// strategic-merge patch of its owner, whose only modeled part is the template.
func workloadRevisionTemplate(
	item map[string]any,
	resource WorkloadResource,
) (int64, map[string]any, bool) {
	if resource == WorkloadDeployments {
		object := &unstructured.Unstructured{Object: item}
		revision, err := strconv.ParseInt(
			object.GetAnnotations()[deploymentRevisionAnnotation],
			10,
			64,
		)
		if err != nil || revision < 1 {
			return 0, nil, false
		}
		template, found, err := unstructured.NestedMap(item, "spec", "template")
		if err != nil || !found {
			return 0, nil, false
		}
		// The Deployment controller adds this label to the ReplicaSet's template,
		// and the Deployment's own selector does not carry it. Writing it back
		// would put a stale ReplicaSet's identity on the object that owns it.
		unstructured.RemoveNestedField(template, "metadata", "labels", podTemplateHashLabel)
		return revision, template, true
	}
	revision, found, err := unstructured.NestedInt64(item, "revision")
	if err != nil || !found || revision < 1 {
		return 0, nil, false
	}
	template, found, err := unstructured.NestedMap(item, "data", "spec", "template")
	if err != nil || !found {
		return 0, nil, false
	}
	// An instruction to whoever applies the patch, not a field of a Pod template.
	// ZKE replaces the template outright, so the directive has already been
	// carried out by the time it would be written.
	delete(template, strategicMergeDirective)
	return revision, template, true
}

func ownedByWorkload(owners []metav1.OwnerReference, ownerUID string) bool {
	if ownerUID == "" {
		return false
	}
	for _, owner := range owners {
		if string(owner.UID) == ownerUID &&
			owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func findWorkloadRevision(
	records []workloadRevisionRecord,
	revision int64,
) (workloadRevisionRecord, bool) {
	for _, record := range records {
		if record.revision == revision {
			return record, true
		}
	}
	return workloadRevisionRecord{}, false
}

// Whether two Pod templates describe the same Pod.
//
// Semantic rather than structural: `1` and `1000m` are the same CPU request, and
// a revision that differs from the running template only in how a quantity was
// written is not a revision anyone can roll back to. The ReplicaSet hash is
// dropped from both sides — it identifies the ReplicaSet, never the Pod.
func workloadTemplatesEqual(left corev1.PodTemplateSpec, right corev1.PodTemplateSpec) bool {
	return apiequality.Semantic.DeepEqual(
		withoutPodTemplateHash(&left),
		withoutPodTemplateHash(&right),
	)
}

func withoutPodTemplateHash(template *corev1.PodTemplateSpec) *corev1.PodTemplateSpec {
	copied := template.DeepCopy()
	delete(copied.Labels, podTemplateHashLabel)
	return copied
}

func workloadRevisionContainers(
	containers []corev1.Container,
) []WorkloadRevisionContainer {
	result := make([]WorkloadRevisionContainer, 0, len(containers))
	for _, container := range containers {
		result = append(result, WorkloadRevisionContainer{
			Name:  container.Name,
			Image: container.Image,
		})
	}
	return result
}
