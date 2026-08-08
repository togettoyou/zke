package resourcewatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

type fakeRequester struct {
	request *agentv1.ResourceWatchRequest
	trailer *agentv1.ResourceWatchTrailer
}

func (fake *fakeRequester) RequestResourceWatch(
	_ context.Context, _ string, request *agentv1.ResourceWatchRequest, sink agentprotocol.ResourceWatchSink,
) (*agentv1.ResourceWatchResponse, *agentv1.ResourceWatchTrailer, error) {
	fake.request = request
	response := &agentv1.ResourceWatchResponse{Result: agentv1.ResultCode_RESULT_CODE_OK,
		KubernetesStatusCode: 200, ContentType: "application/json", ResourceVersion: "20"}
	if err := sink.Start(response); err != nil {
		return nil, nil, err
	}
	if err := sink.Event(&agentv1.ResourceWatchEvent{Type: agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED,
		Object: []byte(`{"kind":"Event"}`), ResourceVersion: "21"}); err != nil {
		return nil, nil, err
	}
	trailer := fake.trailer
	if trailer == nil {
		trailer = &agentv1.ResourceWatchTrailer{Result: agentv1.ResultCode_RESULT_CODE_OK,
			EventsSent: 1, BytesSent: 16, LastResourceVersion: "21"}
	}
	return response, trailer, nil
}

type discardSink struct{}

func (*discardSink) Start(*agentv1.ResourceWatchResponse) error { return nil }
func (*discardSink) Event(*agentv1.ResourceWatchEvent) error    { return nil }

func TestServiceBuildsFixedEventWatchAndSelectors(t *testing.T) {
	requester := &fakeRequester{}
	result, err := NewService(requester).Stream(context.Background(), Input{
		ClusterID: "00000000-0000-4000-8000-000000000003", Namespace: "default",
		IncludeInitial: true, Follow: true, AllowBookmarks: true, InitialLimit: 100,
		ResourceUID: "pod-uid", EventType: "Warning",
	}, &discardSink{})
	if err != nil {
		t.Fatal(err)
	}
	if requester.request.GetResource().GetResource() != "events" || requester.request.GetResource().GetGroup() != "" ||
		!strings.Contains(requester.request.GetFieldSelector(), "involvedObject.uid=pod-uid") ||
		!strings.Contains(requester.request.GetFieldSelector(), "type=Warning") || !requester.request.GetFollow() ||
		result.LastResourceVersion != "21" {
		t.Fatalf("request=%+v result=%+v", requester.request, result)
	}
}

func TestServiceAllowsOnlyAnExactNodeSnapshotAcrossNamespaces(t *testing.T) {
	requester := &fakeRequester{}
	_, err := NewService(requester).Stream(context.Background(), Input{
		ClusterID:      "00000000-0000-4000-8000-000000000003",
		IncludeInitial: true,
		InitialLimit:   50,
		ResourceUID:    "node-uid",
		ResourceKind:   "Node",
	}, &discardSink{})
	if err != nil {
		t.Fatal(err)
	}
	if requester.request.GetNamespace() != "" ||
		!agentprotocol.IsNodeEventFieldSelector(requester.request.GetFieldSelector()) {
		t.Fatalf("unexpected Node Event request: %+v", requester.request)
	}
	for _, input := range []Input{
		{
			ClusterID: "00000000-0000-4000-8000-000000000003", IncludeInitial: true, InitialLimit: 50,
			ResourceUID: "node-uid",
		},
		{
			ClusterID: "00000000-0000-4000-8000-000000000003", IncludeInitial: true, Follow: true, InitialLimit: 50,
			ResourceUID: "node-uid", ResourceKind: "Node",
		},
	} {
		if _, err := NewService(&fakeRequester{}).Stream(
			context.Background(), input, &discardSink{},
		); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("unsafe cross-Namespace input accepted: %+v err=%v", input, err)
		}
	}
}

func TestServiceMapsExpiredResourceVersion(t *testing.T) {
	requester := &fakeRequester{trailer: &agentv1.ResourceWatchTrailer{
		Result: agentv1.ResultCode_RESULT_CODE_CONFLICT, KubernetesStatusCode: 410,
		Reason: "Expired", Message: "expired", EventsSent: 1, BytesSent: 16, LastResourceVersion: "21",
	}}
	_, err := NewService(requester).Stream(context.Background(), Input{
		ClusterID: "00000000-0000-4000-8000-000000000003", Namespace: "default",
		IncludeInitial: true, InitialLimit: 100,
	}, &discardSink{})
	if !errors.Is(err, ErrResourceVersionExpired) {
		t.Fatalf("error=%v", err)
	}
}
