package agent

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKubernetesResourceWatchClosedChannelIsReconnectableFailure(t *testing.T) {
	watcher := watch.NewFake()
	source := &kubernetesEventSource{watcher: watcher}
	watcher.Stop()
	_, err := source.Next(context.Background())
	var sourceError *agentprotocol.ResourceWatchSourceError
	if !errors.As(err, &sourceError) || sourceError.Reason != "WatchClosed" ||
		sourceError.Result != agentv1.ResultCode_RESULT_CODE_UNAVAILABLE {
		t.Fatalf("error=%v", err)
	}
}

func TestKubernetesResourceWatchOnlyAllowsEventsAndListsInitialSnapshot(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Event{ObjectMeta: metav1.ObjectMeta{
		Name: "scheduled", Namespace: "default", ResourceVersion: "7",
	}, Reason: "Scheduled", Message: "assigned"})
	handler := newKubernetesResourceWatchHandler(client)
	request := &agentv1.ResourceWatchRequest{
		Resource: &agentv1.GroupVersionResource{Version: "v1", Resource: "events"}, Namespace: "default",
		IncludeInitialEvents: true, InitialEventLimit: 100,
	}
	response, source, err := handler(context.Background(), request)
	if err != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK || source == nil {
		t.Fatalf("response=%+v source=%v err=%v", response, source, err)
	}
	event, err := source.Next(context.Background())
	if err != nil || event.GetType() != agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED ||
		event.GetResourceVersion() != response.GetResourceVersion() {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	response, source, err = handler(context.Background(), &agentv1.ResourceWatchRequest{
		Resource: &agentv1.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespace: "default",
		IncludeInitialEvents: true, InitialEventLimit: 100,
	})
	if err != nil || source != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_FORBIDDEN {
		t.Fatalf("unsafe response=%+v source=%v err=%v", response, source, err)
	}
}

func TestKubernetesResourceWatchAllowsOneNodeAcrossNamespaces(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "node-ready", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "worker-a", UID: "node-a"},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "pod-ready", Namespace: "workloads"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-0", UID: "pod-a"},
		},
	)
	handler := newKubernetesResourceWatchHandler(client)
	request := &agentv1.ResourceWatchRequest{
		Resource:             &agentv1.GroupVersionResource{Version: "v1", Resource: "events"},
		FieldSelector:        "involvedObject.kind=Node,involvedObject.uid=node-a",
		IncludeInitialEvents: true,
		InitialEventLimit:    50,
	}
	response, source, err := handler(context.Background(), request)
	if err != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK || source == nil {
		t.Fatalf("response=%+v source=%v err=%v", response, source, err)
	}
	event, err := source.Next(context.Background())
	if err != nil || string(event.GetObject()) == "" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	unsafe := proto.Clone(request).(*agentv1.ResourceWatchRequest)
	unsafe.FieldSelector = "involvedObject.kind=Node"
	response, source, err = handler(context.Background(), unsafe)
	if err != nil || source != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_FORBIDDEN {
		t.Fatalf("unsafe response=%+v source=%v err=%v", response, source, err)
	}
	unsafe = proto.Clone(request).(*agentv1.ResourceWatchRequest)
	unsafe.IncludeInitialEvents = false
	response, source, err = handler(context.Background(), unsafe)
	if err != nil || source != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_FORBIDDEN {
		t.Fatalf("non-snapshot response=%+v source=%v err=%v", response, source, err)
	}
}

// The Cluster-wide event centre is the second caller allowed an empty
// Namespace, and it has to ask for it explicitly. Without the flag the Agent
// keeps refusing anything but the exact Node UID snapshot — the behaviour an
// Agent older than this Server has, and the one that keeps a
// `cluster.event.read` holder from widening a describe into a Cluster-wide
// follow by dropping a selector term.
func TestKubernetesResourceWatchAllowsEveryNamespaceOnlyWhenAsked(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "node-ready", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "worker-a", UID: "node-a"},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "pod-ready", Namespace: "workloads"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-0", UID: "pod-a"},
		},
	)
	handler := newKubernetesResourceWatchHandler(client)
	request := &agentv1.ResourceWatchRequest{
		Resource:             &agentv1.GroupVersionResource{Version: "v1", Resource: "events"},
		IncludeInitialEvents: true,
		Follow:               true,
		InitialEventLimit:    50,
		ClusterEventAccess:   true,
	}
	response, source, err := handler(context.Background(), request)
	if err != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK || source == nil {
		t.Fatalf("response=%+v source=%v err=%v", response, source, err)
	}
	seen := 0
	for range 2 {
		event, nextErr := source.Next(context.Background())
		if nextErr != nil || len(event.GetObject()) == 0 {
			t.Fatalf("event=%+v err=%v", event, nextErr)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("cluster-wide snapshot returned %d events", seen)
	}
	refused := proto.Clone(request).(*agentv1.ResourceWatchRequest)
	refused.ClusterEventAccess = false
	response, source, err = handler(context.Background(), refused)
	if err != nil || source != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_FORBIDDEN ||
		response.GetReason() != "ClusterEventWatchForbidden" {
		t.Fatalf("unflagged response=%+v source=%v err=%v", response, source, err)
	}
}

func TestGenericResourcePathRejectsKubernetesEvents(t *testing.T) {
	request := &agentv1.ResourceRequest{Verb: agentv1.ResourceVerb_RESOURCE_VERB_LIST,
		Resource: &agentv1.GroupVersionResource{Version: "v1", Resource: "events"}}
	if refuseKubernetesResourceRequest(request, "zke-system") == nil {
		t.Fatal("generic Resource path allowed Kubernetes Events")
	}
}
