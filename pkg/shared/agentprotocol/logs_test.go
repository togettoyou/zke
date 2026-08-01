package agentprotocol

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"google.golang.org/protobuf/proto"
)

func TestRelayPodLogsFramesChunksAndLimitTrailer(t *testing.T) {
	t.Parallel()

	var encoded bytes.Buffer
	if err := relayPodLogs(
		context.Background(),
		&encoded,
		strings.NewReader("first\nsecond\n"),
		8,
	); err != nil {
		t.Fatal(err)
	}
	frame := &agentv1.PodLogsFrame{}
	if err := ReadMessage(&encoded, frame); err != nil {
		t.Fatal(err)
	}
	if got := string(frame.GetChunk().GetData()); got != "first\nse" {
		t.Fatalf("log chunk = %q", got)
	}
	frame.Reset()
	if err := ReadMessage(&encoded, frame); err != nil {
		t.Fatal(err)
	}
	trailer := frame.GetTrailer()
	if trailer.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		trailer.GetBytesSent() != 8 || !trailer.GetLimitReached() {
		t.Fatalf("unexpected log trailer: %+v", trailer)
	}
	if encoded.Len() != 0 {
		t.Fatal("relay emitted data after its trailer")
	}
}

func TestPodLogsValidationRequiresExplicitBoundedIdentity(t *testing.T) {
	t.Parallel()

	header := &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_POD_LOGS,
		RequestId:       "00000000-0000-4000-8000-000000000020",
		TimeoutMillis:   1000,
	}
	tail := int64(200)
	since := int64(60)
	valid := &agentv1.PodLogsRequest{
		Namespace:    "model-serving",
		PodName:      "inference-abcde",
		PodUid:       "pod-uid",
		Container:    "main",
		Follow:       true,
		TailLines:    &tail,
		SinceSeconds: &since,
		MaxBytes:     1024,
	}
	if err := validatePodLogsRequest(header, valid, 1024); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for _, mutate := range []func(*agentv1.PodLogsRequest){
		func(request *agentv1.PodLogsRequest) { request.PodUid = "" },
		func(request *agentv1.PodLogsRequest) { request.Container = "" },
		func(request *agentv1.PodLogsRequest) { request.MaxBytes = 1025 },
		func(request *agentv1.PodLogsRequest) { value := int64(5001); request.TailLines = &value },
		func(request *agentv1.PodLogsRequest) { value := int64(0); request.SinceSeconds = &value },
	} {
		request := proto.Clone(valid).(*agentv1.PodLogsRequest)
		mutate(request)
		if err := validatePodLogsRequest(header, request, 1024); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
}

func TestRealQUICPodLogsStreamPreservesDataAndTrailer(t *testing.T) {
	client, server, stop := openStreamTestConnection(t)
	defer stop()
	streamServer, err := NewStreamServer(StreamServerConfig{
		HeaderTimeout: 200 * time.Millisecond,
		MaxTimeout:    2 * time.Second,
		Handlers: map[agentv1.StreamKind]StreamHandlerConfig{
			agentv1.StreamKind_STREAM_KIND_POD_LOGS: {
				MaxConcurrent: 2,
				Handle: PodLogsStreamHandler(
					64,
					func(
						_ context.Context,
						request *agentv1.PodLogsRequest,
					) (*agentv1.PodLogsResponse, io.ReadCloser, error) {
						return &agentv1.PodLogsResponse{
							Result:               agentv1.ResultCode_RESULT_CODE_OK,
							KubernetesStatusCode: 200,
							PodUid:               request.GetPodUid(),
							Container:            request.GetContainer(),
							Follow:               request.GetFollow(),
							ContentType:          "text/plain",
						}, io.NopCloser(strings.NewReader("line-1\nline-2\n")), nil
					},
				),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveContext, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- streamServer.Serve(serveContext, server) }()
	defer func() {
		cancelServe()
		_ = client.CloseWithError(0, "test complete")
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("stop Pod Logs Stream Server: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Pod Logs Stream Server did not stop")
		}
	}()

	request := &agentv1.PodLogsRequest{
		Namespace: "model-serving",
		PodName:   "inference-abcde",
		PodUid:    "pod-uid",
		Container: "main",
		Follow:    true,
		MaxBytes:  64,
	}
	var output bytes.Buffer
	response, trailer, err := DoPodLogs(
		context.Background(),
		client,
		&agentv1.StreamHeader{
			ProtocolVersion: ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_POD_LOGS,
			RequestId:       "00000000-0000-4000-8000-000000000021",
			TimeoutMillis:   1000,
		},
		request,
		&output,
		64,
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetPodUid() != "pod-uid" ||
		trailer.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		trailer.GetLimitReached() ||
		trailer.GetBytesSent() != uint64(output.Len()) ||
		output.String() != "line-1\nline-2\n" {
		t.Fatalf("response=%+v trailer=%+v output=%q", response, trailer, output.String())
	}
}
