package agentprotocol

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func TestRealQUICPodPortForwardRelaysBinaryTrafficAndExit(t *testing.T) {
	client, server, stop := openStreamTestConnection(t)
	defer stop()
	streamServer, err := NewStreamServer(StreamServerConfig{
		HeaderTimeout: 200 * time.Millisecond,
		MaxTimeout:    2 * time.Second,
		Handlers: map[agentv1.StreamKind]StreamHandlerConfig{
			agentv1.StreamKind_STREAM_KIND_POD_PORT_FORWARD: {
				MaxConcurrent: 1,
				Handle: PodPortForwardStreamHandler(1024, 1024, func(
					_ context.Context,
					request *agentv1.PodPortForwardRequest,
				) (*agentv1.PodPortForwardResponse, PodPortForwardConnection, error) {
					forward, backend := net.Pipe()
					go func() {
						defer backend.Close()
						buffer := make([]byte, 4)
						if _, readErr := backend.Read(buffer); readErr == nil && string(buffer) == "ping" {
							_, _ = backend.Write([]byte("pong"))
						}
					}()
					return &agentv1.PodPortForwardResponse{Result: agentv1.ResultCode_RESULT_CODE_OK,
						KubernetesStatusCode: 200, PodUid: request.GetPodUid(), Port: request.GetPort()}, forward, nil
				}),
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
				t.Errorf("stop Port Forward Stream Server: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Port Forward Stream Server did not stop")
		}
	}()
	peer := &testPodPortForwardPeer{}
	response, exit, err := DoPodPortForward(context.Background(), client,
		&agentv1.StreamHeader{ProtocolVersion: ProtocolVersion,
			Kind:      agentv1.StreamKind_STREAM_KIND_POD_PORT_FORWARD,
			RequestId: "00000000-0000-4000-8000-000000000081", TimeoutMillis: 1000},
		&agentv1.PodPortForwardRequest{Namespace: "workloads", PodName: "api-0", PodUid: "pod-uid",
			Port: 8080, MaxClientBytes: 1024, MaxPodBytes: 1024}, peer)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetPodUid() != "pod-uid" || response.GetPort() != 8080 ||
		exit.GetResult() != agentv1.ResultCode_RESULT_CODE_OK || exit.GetClientBytes() != 4 ||
		exit.GetPodBytes() != 4 || string(peer.output()) != "pong" {
		t.Fatalf("response=%+v exit=%+v output=%q", response, exit, peer.output())
	}
}

func TestPodPortForwardFramePumpsEnforceDirectionLimits(t *testing.T) {
	var clientCount atomic.Uint64
	err := receivePortForwardFrames(context.Background(), messageBuffer(t,
		&agentv1.PodPortForwardFrame{Message: &agentv1.PodPortForwardFrame_Data{
			Data: &agentv1.PodPortForwardData{Data: []byte("too-long")},
		}}), &discardWriter{}, 3, &clientCount)
	if !errors.Is(err, ErrPodPortForwardClientLimit) || clientCount.Load() != 0 {
		t.Fatalf("client limit err=%v bytes=%d", err, clientCount.Load())
	}
	var podCount atomic.Uint64
	err = sendPortForwardFrames(context.Background(), &discardWriter{}, &oneRead{data: []byte("too-long")}, 3, &podCount)
	if !errors.Is(err, ErrPodPortForwardPodLimit) || podCount.Load() != 0 {
		t.Fatalf("Pod limit err=%v bytes=%d", err, podCount.Load())
	}
}

type testPodPortForwardPeer struct {
	read    atomic.Bool
	mutex   sync.Mutex
	written []byte
}

func (peer *testPodPortForwardPeer) Read(ctx context.Context) ([]byte, error) {
	if peer.read.CompareAndSwap(false, true) {
		return []byte("ping"), nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (peer *testPodPortForwardPeer) Write(_ context.Context, data []byte) error {
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	peer.written = append(peer.written, data...)
	return nil
}

func (peer *testPodPortForwardPeer) output() []byte {
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	return append([]byte(nil), peer.written...)
}

type discardWriter struct{}

func (*discardWriter) Write(data []byte) (int, error) { return len(data), nil }

type oneRead struct{ data []byte }

func (reader *oneRead) Read(destination []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, context.Canceled
	}
	n := copy(destination, reader.data)
	reader.data = nil
	return n, nil
}

func messageBuffer(t *testing.T, message any) *syncBuffer {
	t.Helper()
	buffer := &syncBuffer{}
	protoMessage, ok := message.(*agentv1.PodPortForwardFrame)
	if !ok {
		t.Fatal("invalid test message")
	}
	if err := WriteMessage(buffer, protoMessage); err != nil {
		t.Fatal(err)
	}
	return buffer
}

type syncBuffer struct {
	mutex sync.Mutex
	data  []byte
}

func (buffer *syncBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	buffer.data = append(buffer.data, data...)
	return len(data), nil
}

func (buffer *syncBuffer) Read(destination []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	if len(buffer.data) == 0 {
		return 0, context.Canceled
	}
	n := copy(destination, buffer.data)
	buffer.data = buffer.data[n:]
	return n, nil
}
