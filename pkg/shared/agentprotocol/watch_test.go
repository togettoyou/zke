package agentprotocol

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func TestResourceWatchEmptySnapshotKeepsListResourceVersion(t *testing.T) {
	var encoded bytes.Buffer
	request := validResourceWatchRequest()
	if err := relayResourceWatch(context.Background(), &encoded, &testWatchSource{}, request, "42"); err != nil {
		t.Fatal(err)
	}
	frame := &agentv1.ResourceWatchFrame{}
	if err := ReadMessage(&encoded, frame); err != nil {
		t.Fatal(err)
	}
	if frame.GetTrailer().GetLastResourceVersion() != "42" ||
		frame.GetTrailer().GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("trailer=%+v", frame.GetTrailer())
	}
}

type testWatchSource struct{ events []*agentv1.ResourceWatchEvent }

func (source *testWatchSource) Next(context.Context) (*agentv1.ResourceWatchEvent, error) {
	if len(source.events) == 0 {
		return nil, io.EOF
	}
	event := source.events[0]
	source.events = source.events[1:]
	return event, nil
}
func (*testWatchSource) Close() error { return nil }

type testWatchSink struct {
	response *agentv1.ResourceWatchResponse
	events   []*agentv1.ResourceWatchEvent
}

func (sink *testWatchSink) Start(response *agentv1.ResourceWatchResponse) error {
	sink.response = response
	return nil
}
func (sink *testWatchSink) Event(event *agentv1.ResourceWatchEvent) error {
	sink.events = append(sink.events, event)
	return nil
}

func TestRealQUICResourceWatchPreservesEventsAndTrailer(t *testing.T) {
	client, server, stop := openStreamTestConnection(t)
	defer stop()
	streamServer, err := NewStreamServer(StreamServerConfig{
		HeaderTimeout: 200 * time.Millisecond, MaxTimeout: 2 * time.Second,
		Handlers: map[agentv1.StreamKind]StreamHandlerConfig{
			agentv1.StreamKind_STREAM_KIND_RESOURCE_WATCH: {MaxConcurrent: 2, Handle: ResourceWatchStreamHandler(
				func(context.Context, *agentv1.ResourceWatchRequest) (*agentv1.ResourceWatchResponse, ResourceWatchSource, error) {
					return &agentv1.ResourceWatchResponse{Result: agentv1.ResultCode_RESULT_CODE_OK,
							KubernetesStatusCode: 200, ContentType: "application/json", ResourceVersion: "10"},
						&testWatchSource{events: []*agentv1.ResourceWatchEvent{{
							Type:   agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED,
							Object: []byte(`{"kind":"Event"}`), ResourceVersion: "11",
						}}}, nil
				},
			)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveContext, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- streamServer.Serve(serveContext, server) }()
	defer func() {
		cancel()
		_ = client.CloseWithError(0, "done")
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("server did not stop")
		}
	}()
	request := validResourceWatchRequest()
	sink := &testWatchSink{}
	response, trailer, err := DoResourceWatch(context.Background(), client, &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion, Kind: agentv1.StreamKind_STREAM_KIND_RESOURCE_WATCH,
		RequestId: "00000000-0000-4000-8000-000000000031", TimeoutMillis: 1000,
	}, request, sink)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResourceVersion() != "10" || len(sink.events) != 1 || sink.events[0].GetResourceVersion() != "11" ||
		trailer.GetResult() != agentv1.ResultCode_RESULT_CODE_OK || trailer.GetEventsSent() != 1 || trailer.GetLastResourceVersion() != "11" {
		t.Fatalf("response=%+v events=%+v trailer=%+v", response, sink.events, trailer)
	}
}

func TestResourceWatchValidationRequiresBounds(t *testing.T) {
	header := &agentv1.StreamHeader{ProtocolVersion: ProtocolVersion, Kind: agentv1.StreamKind_STREAM_KIND_RESOURCE_WATCH,
		RequestId: "00000000-0000-4000-8000-000000000032", TimeoutMillis: 1000}
	valid := validResourceWatchRequest()
	if err := validateResourceWatchRequest(header, valid); err != nil {
		t.Fatal(err)
	}
	valid.MaxEventBytes = DefaultMaxResourceWatchEventBytes + 1
	if err := validateResourceWatchRequest(header, valid); err == nil {
		t.Fatal("oversized events accepted")
	}
}

func TestResourceWatchValidationBoundsClusterWideEventsToOneNode(t *testing.T) {
	header := &agentv1.StreamHeader{ProtocolVersion: ProtocolVersion,
		Kind:      agentv1.StreamKind_STREAM_KIND_RESOURCE_WATCH,
		RequestId: "00000000-0000-4000-8000-000000000032", TimeoutMillis: 1000}
	valid := validResourceWatchRequest()
	valid.Namespace = ""
	valid.FieldSelector = "involvedObject.kind=Node,involvedObject.uid=node-uid"
	if err := validateResourceWatchRequest(header, valid); err != nil {
		t.Fatalf("exact Node snapshot rejected: %v", err)
	}
	for _, selector := range []string{
		"involvedObject.kind=Node",
		"involvedObject.uid=node-uid",
		"involvedObject.kind=Pod,involvedObject.uid=pod-uid",
		"",
	} {
		candidate := validResourceWatchRequest()
		candidate.Namespace = ""
		candidate.FieldSelector = selector
		if err := validateResourceWatchRequest(header, candidate); err == nil {
			t.Fatalf("unsafe cluster-wide selector accepted: %q", selector)
		}
	}
}

func validResourceWatchRequest() *agentv1.ResourceWatchRequest {
	return &agentv1.ResourceWatchRequest{
		Resource: &agentv1.GroupVersionResource{Version: "v1", Resource: "events"}, Namespace: "default",
		IncludeInitialEvents: true, InitialEventLimit: 100,
		MaxEventBytes: DefaultMaxResourceWatchEventBytes, MaxTotalBytes: DefaultMaxResourceWatchTotalBytes,
		MaxEvents: DefaultMaxResourceWatchEvents,
	}
}
