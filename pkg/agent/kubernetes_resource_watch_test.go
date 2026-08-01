package agent

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
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

func TestGenericResourcePathRejectsKubernetesEvents(t *testing.T) {
	request := &agentv1.ResourceRequest{Verb: agentv1.ResourceVerb_RESOURCE_VERB_LIST,
		Resource: &agentv1.GroupVersionResource{Version: "v1", Resource: "events"}}
	if allowedKubernetesResourceRequest(request) {
		t.Fatal("generic Resource path allowed Kubernetes Events")
	}
}
