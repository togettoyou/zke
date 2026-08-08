// Package kubernetesdescribe assembles the view an operator opens when a
// workload will not start.
//
// The Console already offers two halves of that answer and makes the operator
// join them by hand: the object's own projection says a container is waiting,
// and the Namespace Event stream — a page away, unfiltered — says why. This
// package does the join on the Server: it reads the object, reads only the
// Events that name that object, and reports the findings the two together
// support.
//
// It adds no capability to the Agent protocol. Every read here is one the
// Server could already make: the family projection through the resource
// service, the Events through the same bounded Resource Watch the Event stream
// uses, in snapshot mode.
package kubernetesdescribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/resourcewatch"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DefaultEventLimit is the Event window one describe carries.
//
// kubectl prints every Event the API server still holds. This is bounded
// because the Server pays for the transfer and because the Events that explain
// a failure are the recent ones: a Pod that has been crash-looping for a day
// has hundreds of identical BackOff Events and none of the ones past the first
// few say anything new.
const DefaultEventLimit uint32 = 50

// ErrInvalidInput is returned for a describe target the Server can reject
// without asking the Cluster.
var ErrInvalidInput = errors.New("invalid Kubernetes describe input")

// Reasons an Event section is empty that are not "there were no Events".
const (
	// The object is Cluster-scoped. Events name a Namespace, and which one holds
	// a Cluster-scoped object's Events is a convention rather than a rule, so
	// this reports the gap instead of guessing at `default` and quietly
	// returning someone else's Events.
	EventsOmittedUnsupportedScope = "unsupported_scope"
	// The Event read failed on its own — the Agent is gone, the Watch capability
	// is missing, the Kubernetes API refused it. The object half of the answer
	// is still worth returning, so the failure is reported in place rather than
	// failing the request.
	EventsOmittedUnavailable = "unavailable"
)

// Families whose findings this package knows how to derive. A family that is
// not modelled still describes: it gets its identity and its own Events, which
// is the part the Console could not assemble before. It gets no findings,
// because a rule that has not been written for a type is not a rule.
const (
	FamilyPod        = "pod"
	FamilyWorkload   = "workload"
	FamilyNode       = "node"
	FamilyStorage    = "storage"
	FamilyNetworking = "networking"
	FamilyGeneric    = "generic"
)

// ResourceAccess is the read half of the Kubernetes resource service. Describe
// never writes.
type ResourceAccess interface {
	GetPod(context.Context, string, string, string) (kubernetesresource.PodDetail, error)
	ListPodDetails(
		context.Context,
		kubernetesresource.ListPodsInput,
	) ([]kubernetesresource.PodDetail, bool, error)
	ListNodePodDetails(
		context.Context,
		kubernetesresource.ListPodsInput,
	) ([]kubernetesresource.NodePodDetail, bool, error)
	GetNode(context.Context, string, string) (kubernetesresource.NodeDetail, error)
	GetNetworkingResource(
		context.Context,
		string,
		string,
		kubernetesresource.NetworkingResource,
		string,
	) (kubernetesresource.NetworkingResourceDetail, error)
	ListNetworkingResources(
		context.Context,
		kubernetesresource.ListNetworkingResourcesInput,
	) (kubernetesresource.NetworkingResourcePage, error)
	GetWorkload(
		context.Context,
		string,
		string,
		kubernetesresource.WorkloadResource,
		string,
	) (kubernetesresource.WorkloadDetail, error)
	GetStorageResource(
		context.Context,
		string,
		string,
		kubernetesresource.StorageResource,
		string,
	) (kubernetesresource.StorageResourceDetail, error)
	ListResources(
		context.Context,
		kubernetesresource.ListResourcesInput,
	) (kubernetesresource.ResourcePage, error)
	GetResource(
		context.Context,
		kubernetesresource.GetResourceInput,
	) (map[string]any, error)
}

// EventSource is the Resource Watch service, used in snapshot mode: no follow,
// initial Events only, filtered to one object.
type EventSource interface {
	Stream(
		context.Context,
		resourcewatch.Input,
		agentprotocol.ResourceWatchSink,
	) (resourcewatch.Result, error)
}

type Config struct {
	// EventLimit bounds the Event snapshot. Zero takes DefaultEventLimit; the
	// protocol's own ceiling still applies above it.
	EventLimit uint32
}

type Service struct {
	resources  ResourceAccess
	events     EventSource
	eventLimit uint32
}

func NewService(
	resources ResourceAccess,
	events EventSource,
	config Config,
) *Service {
	limit := config.EventLimit
	if limit == 0 {
		limit = DefaultEventLimit
	}
	if limit > agentprotocol.MaxResourceWatchInitialEvents {
		limit = agentprotocol.MaxResourceWatchInitialEvents
	}
	return &Service{resources: resources, events: events, eventLimit: limit}
}

type PodInput struct {
	ClusterID string
	Namespace string
	Name      string
}

type ResourceInput struct {
	ClusterID string
	Resource  kubernetesresource.ResourceIdentity
	Namespace string
	Name      string
}

// Target is what was described, resolved from the live object rather than from
// the request: a describe that reported the requested name against another
// object's Events would be worse than no describe at all.
type Target struct {
	APIVersion      string `json:"api_version"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
}

type Result struct {
	Target Target `json:"target"`
	Family string `json:"family"`
	// The family projection, present for the families that have one. Same shape
	// the family's own detail endpoint returns, so the Console renders it with
	// the components it already has.
	Pod              *kubernetesresource.PodDetail                `json:"pod,omitempty"`
	Workload         *kubernetesresource.WorkloadDetail           `json:"workload,omitempty"`
	Node             *kubernetesresource.NodeDetail               `json:"node,omitempty"`
	Storage          *kubernetesresource.StorageResourceDetail    `json:"storage,omitempty"`
	Networking       *kubernetesresource.NetworkingResourceDetail `json:"networking,omitempty"`
	ServiceEndpoints *ServiceEndpoints                            `json:"service_endpoints,omitempty"`
	IngressBackends  *IngressBackends                             `json:"ingress_backends,omitempty"`
	GatewayStatus    *GatewayStatus                               `json:"gateway_status,omitempty"`
	// NodeResources is the scheduler-requested share of a Node's allocatable
	// resources. It is separate from the Node projection because the values come
	// from the Pods assigned to it, not from the Node object itself.
	NodeResources *NodeResources `json:"node_resources,omitempty"`
	// What the described object owns, for the families where the object itself is
	// never the thing that failed: a Deployment that will not come up is a
	// statement about its Pods.
	Related  *Related  `json:"related,omitempty"`
	Events   Events    `json:"events"`
	Findings []Finding `json:"findings"`
	// Sections that were asked for and did not arrive. Empty means the answer is
	// complete; a describe that silently dropped a section would read as
	// "nothing wrong here".
	DegradedSections []string `json:"degraded_sections"`
}

type ServiceEndpoints struct {
	EndpointSlices       int64 `json:"endpoint_slices"`
	Endpoints            int64 `json:"endpoints"`
	ReadyEndpoints       int64 `json:"ready_endpoints"`
	ServingEndpoints     int64 `json:"serving_endpoints"`
	TerminatingEndpoints int64 `json:"terminating_endpoints"`
	Truncated            bool  `json:"truncated"`
}

type IngressBackends struct {
	Items                   []IngressBackend `json:"items"`
	Truncated               bool             `json:"truncated"`
	ServicesTruncated       bool             `json:"services_truncated"`
	EndpointSlicesTruncated bool             `json:"endpoint_slices_truncated"`
}

type IngressBackend struct {
	ServiceName            string    `json:"service_name"`
	PortName               string    `json:"port_name"`
	PortNumber             int32     `json:"port_number"`
	References             []string  `json:"references"`
	ServiceFound           *bool     `json:"service_found,omitempty"`
	PortFound              *bool     `json:"port_found,omitempty"`
	EndpointStateAvailable bool      `json:"endpoint_state_available"`
	EndpointSlices         int64     `json:"endpoint_slices"`
	Endpoints              int64     `json:"endpoints"`
	ReadyEndpoints         int64     `json:"ready_endpoints"`
	Findings               []Finding `json:"findings"`
	endpointPortName       string
}

type GatewayStatus struct {
	Listeners []GatewayListenerStatus `json:"listeners"`
}

type GatewayListenerStatus struct {
	Name           string    `json:"name"`
	AttachedRoutes int32     `json:"attached_routes"`
	Findings       []Finding `json:"findings"`
}

type NodeResources struct {
	CPUAllocatableMillis   int64 `json:"cpu_allocatable_millis"`
	CPURequestedMillis     int64 `json:"cpu_requested_millis"`
	MemoryAllocatableBytes int64 `json:"memory_allocatable_bytes"`
	MemoryRequestedBytes   int64 `json:"memory_requested_bytes"`
	PodAllocatable         int64 `json:"pod_allocatable"`
	NonTerminalPods        int64 `json:"non_terminal_pods"`
	// Truncated means the assigned-Pod list had another page. Requested totals
	// are then lower bounds, so percentage findings must not be derived from them.
	Truncated bool `json:"truncated"`
}

// Related is what stands between a workload and the Pods that run it.
//
// The two groups are kept apart because they answer different questions. A
// Deployment's ReplicaSet is where a rejected creation is reported — a quota or
// an admission policy refuses the Pod before one exists — while the Pods are
// where everything that happens after creation is reported. Reading them as one
// list would put "there is no Pod" next to "this Pod will not start" as if they
// were the same kind of fact.
type Related struct {
	Controllers            []RelatedObject `json:"controllers"`
	Pods                   []RelatedObject `json:"pods"`
	PersistentVolumeClaims []RelatedObject `json:"persistent_volume_claims"`
	// The workload owns or references more objects than this bounded view carries.
	// Each family defines its own order; callers must not infer that omitted
	// objects are healthy.
	Truncated bool `json:"truncated"`
}

type RelatedObject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Namespace string `json:"namespace"`
	// The object's own summary of itself: a Pod's phase, a ReplicaSet's or Job's
	// replica counts.
	Status string `json:"status"`
	Ready  bool   `json:"ready"`
	// Findings derived for this object, by the rules of its own family. A
	// workload's failure is usually one of these rather than anything on the
	// workload itself.
	Findings []Finding `json:"findings"`
}

type Events struct {
	Items []Event `json:"items"`
	// The Cluster held more Events for this object than the window carries.
	Truncated bool `json:"truncated"`
	// Empty when the Events were read. Otherwise one of the EventsOmitted*
	// reasons, and Items is empty.
	Omitted string `json:"omitted,omitempty"`
}

type Event struct {
	UID     string `json:"uid"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Count   int32  `json:"count"`
	Source  string `json:"source"`
	// The container the Event was reported against, when it named one. This is
	// what lets a container's finding cite the Event that explains it instead of
	// the newest Event about the Pod.
	Container string `json:"container,omitempty"`
	// Which object the Event is about. A workload's describe merges the Events of
	// the workload, of the controllers under it and of its Pods into one
	// timeline, and a line saying a container could not start means nothing
	// without saying which Pod it was.
	Regarding EventSubject `json:"regarding"`
	FirstSeen *time.Time   `json:"first_seen,omitempty"`
	LastSeen  *time.Time   `json:"last_seen,omitempty"`
}

type EventSubject struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// Finding is one reason the object is not doing what it was asked to do.
//
// It carries a stable Code and the upstream text verbatim, and no prose of its
// own: the wording an operator reads belongs to the Console, and the Server
// inventing an explanation on top of a Kubernetes message is how a describe
// starts saying things the Cluster never said. Evidence names what the finding
// was read from, so the Console can link to it and a reader can disagree.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// The container the finding is about, empty when it is about the object.
	Scope string `json:"scope,omitempty"`
	// The Kubernetes reason, as reported: `Unschedulable`, `ImagePullBackOff`,
	// `CreateContainerConfigError`.
	Reason string `json:"reason,omitempty"`
	// The upstream message, unedited.
	Message  string     `json:"message,omitempty"`
	ExitCode *int32     `json:"exit_code,omitempty"`
	Evidence []Evidence `json:"evidence"`
}

// SeverityWarning is the only severity findings carry: they are raised for
// problems only, and a describe that also reported healthy state would bury the
// one line that matters.
const SeverityWarning = "warning"

// Evidence kinds.
const (
	EvidenceCondition      = "Condition"
	EvidenceContainerState = "ContainerState"
	EvidenceEvent          = "Event"
	EvidenceObjectStatus   = "ObjectStatus"
)

type Evidence struct {
	Kind string `json:"kind"`
	// The condition type, the container name, or the Event UID.
	Name string `json:"name"`
}

// DescribePod describes one Pod: its detail projection, the Events naming it,
// and the Pod findings those support.
func (service *Service) DescribePod(
	ctx context.Context,
	input PodInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	pod, err := service.resources.GetPod(
		ctx,
		input.ClusterID,
		input.Namespace,
		input.Name,
	)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Target: Target{
			APIVersion:      pod.APIVersion,
			Kind:            pod.Kind,
			Namespace:       pod.Namespace,
			Name:            pod.Name,
			UID:             pod.UID,
			ResourceVersion: pod.ResourceVersion,
		},
		Family:           FamilyPod,
		Pod:              &pod,
		Findings:         []Finding{},
		DegradedSections: []string{},
	}
	result.Events, result.DegradedSections = service.objectEvents(
		ctx,
		input.ClusterID,
		result.Target,
	)
	result.Findings = podFindings(pod, result.Events.Items)
	return result, nil
}

// DescribeResource describes any other object: its identity and its own
// Events. No findings — the rules are written per family, and this is the path
// for the families that have none.
func (service *Service) DescribeResource(
	ctx context.Context,
	input ResourceInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	object, err := service.resources.GetResource(
		ctx,
		kubernetesresource.GetResourceInput{
			ClusterID: input.ClusterID,
			Resource:  input.Resource,
			Namespace: input.Namespace,
			Name:      input.Name,
		},
	)
	if err != nil {
		return Result{}, err
	}
	live := unstructured.Unstructured{Object: object}
	if live.GetKind() == "" || live.GetName() == "" {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	result := Result{
		Target: Target{
			APIVersion:      live.GetAPIVersion(),
			Kind:            live.GetKind(),
			Namespace:       live.GetNamespace(),
			Name:            live.GetName(),
			UID:             string(live.GetUID()),
			ResourceVersion: live.GetResourceVersion(),
		},
		Family:           FamilyGeneric,
		Findings:         []Finding{},
		DegradedSections: []string{},
	}
	result.Events, result.DegradedSections = service.objectEvents(
		ctx,
		input.ClusterID,
		result.Target,
	)
	return result, nil
}

// objectEvents reads the Events naming one object, and reports rather than
// propagates its own failures: the object half of a describe is worth
// returning without them.
func (service *Service) objectEvents(
	ctx context.Context,
	clusterID string,
	target Target,
) (Events, []string) {
	if target.Namespace == "" {
		return Events{Items: []Event{}, Omitted: EventsOmittedUnsupportedScope},
			[]string{}
	}
	items, truncated, err := service.readObjectEvents(
		ctx,
		clusterID,
		target.Namespace,
		target.UID,
	)
	if err != nil {
		return Events{Items: []Event{}, Omitted: EventsOmittedUnavailable},
			[]string{"events"}
	}
	return Events{Items: items, Truncated: truncated}, []string{}
}

// readObjectEvents reads the bounded Event snapshot of exactly one object.
//
// Filtered by UID rather than by name: an object deleted and recreated under the
// same name leaves its predecessor's Events behind, and attaching those to the
// live object is how a describe explains a failure that already ended.
func (service *Service) readObjectEvents(
	ctx context.Context,
	clusterID string,
	namespace string,
	uid string,
) ([]Event, bool, error) {
	return service.readEvents(ctx, clusterID, namespace, uid, "")
}

// readEvents also supports a cluster-scoped Node. That exceptional scope is
// constrained again by Resource Watch validation and by the Agent to an exact
// Node UID snapshot, so it cannot become a namespace-wide Event back door.
func (service *Service) readEvents(
	ctx context.Context,
	clusterID string,
	namespace string,
	uid string,
	kind string,
) ([]Event, bool, error) {
	if service.events == nil || uid == "" || (namespace == "" && kind != "Node") {
		return nil, false, ErrInvalidInput
	}
	collector := &eventCollector{limit: service.eventLimit}
	if _, err := service.events.Stream(ctx, resourcewatch.Input{
		ClusterID:      clusterID,
		Namespace:      namespace,
		IncludeInitial: true,
		Follow:         false,
		InitialLimit:   service.eventLimit,
		ResourceUID:    uid,
		ResourceKind:   kind,
	}, collector); err != nil {
		return nil, false, err
	}
	return collector.collected(), collector.truncated, nil
}

// eventCollector is the snapshot counterpart of the SSE sink: same Resource
// Watch, gathered into a slice instead of written to a client.
type eventCollector struct {
	limit     uint32
	items     []Event
	truncated bool
}

func (collector *eventCollector) Start(
	response *agentv1.ResourceWatchResponse,
) error {
	collector.truncated = response.GetInitialEventsTruncated()
	return nil
}

func (collector *eventCollector) Event(
	frame *agentv1.ResourceWatchEvent,
) error {
	switch frame.GetType() {
	case agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED,
		agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_MODIFIED:
	default:
		// Bookmarks carry no object, and an Event deleted mid-snapshot is not
		// something to report as having happened.
		return nil
	}
	if uint32(len(collector.items)) >= collector.limit {
		collector.truncated = true
		return nil
	}
	var event corev1.Event
	if err := json.Unmarshal(frame.GetObject(), &event); err != nil {
		return fmt.Errorf("decode Kubernetes Event: %w", err)
	}
	collector.items = append(collector.items, describeEvent(event))
	return nil
}

// collected returns the snapshot oldest first, which is the order the failure
// happened in and the order kubectl prints.
func (collector *eventCollector) collected() []Event {
	items := collector.items
	if items == nil {
		items = []Event{}
	}
	sort.SliceStable(items, func(first, second int) bool {
		return eventOrder(items[first]).Before(eventOrder(items[second]))
	})
	return items
}

func eventOrder(event Event) time.Time {
	if event.LastSeen != nil {
		return *event.LastSeen
	}
	if event.FirstSeen != nil {
		return *event.FirstSeen
	}
	return time.Time{}
}

func describeEvent(event corev1.Event) Event {
	result := Event{
		UID:       string(event.UID),
		Name:      event.Name,
		Type:      event.Type,
		Reason:    event.Reason,
		Message:   event.Message,
		Count:     event.Count,
		Source:    eventSource(event),
		Container: eventContainer(event.InvolvedObject.FieldPath),
		Regarding: EventSubject{
			Kind: event.InvolvedObject.Kind,
			Name: event.InvolvedObject.Name,
			UID:  string(event.InvolvedObject.UID),
		},
	}
	if !event.FirstTimestamp.IsZero() {
		value := event.FirstTimestamp.UTC()
		result.FirstSeen = &value
	}
	if !event.LastTimestamp.IsZero() {
		value := event.LastTimestamp.UTC()
		result.LastSeen = &value
	}
	if result.LastSeen == nil && !event.EventTime.IsZero() {
		value := event.EventTime.UTC()
		result.LastSeen = &value
	}
	if result.FirstSeen == nil {
		result.FirstSeen = result.LastSeen
	}
	return result
}

// eventSource says who reported it. Which half of the Event carries that
// depends on which API wrote it: the scheduler and the kubelet report through
// `source.component` on the core API and through `reportingController` on
// events.k8s.io, and an operator reading "why is this Pod stuck" is asking
// exactly that question.
func eventSource(event corev1.Event) string {
	if event.Source.Component != "" {
		return event.Source.Component
	}
	return event.ReportingController
}
