package agentprotocol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func TestPodExecFrameWriterStopsExactlyAtOutputLimit(t *testing.T) {
	t.Parallel()

	var encoded bytes.Buffer
	writer := &podExecFrameWriter{
		stream:  &encoded,
		maximum: 5,
		ready:   make(chan struct{}),
	}
	writer.release()
	written, err := writer.output(
		agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDOUT,
	).Write([]byte("12345678"))
	if written != 5 || !errors.Is(err, ErrPodExecOutputLimit) ||
		writer.bytes.Load() != 5 || !writer.limit.Load() {
		t.Fatalf("written=%d bytes=%d limited=%t err=%v", written, writer.bytes.Load(), writer.limit.Load(), err)
	}
	frame := &agentv1.PodExecFrame{}
	if err := ReadMessage(&encoded, frame); err != nil {
		t.Fatal(err)
	}
	if got := string(frame.GetOutput().GetData()); got != "12345" {
		t.Fatalf("output = %q", got)
	}
}

func TestPodExecRequestSeparatesInteractiveShellFromBoundedCommand(t *testing.T) {
	header := &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_POD_EXEC,
		RequestId:       "00000000-0000-4000-8000-000000000031",
		TimeoutMillis:   1000,
	}
	request := &agentv1.PodExecRequest{
		Namespace: "zke-system", PodName: "zke-terminal-test", PodUid: "pod-uid", Container: "terminal",
		Tty: false, Columns: 80, Rows: 24, MaxInputBytes: 1, MaxOutputBytes: 1024,
		Command: []string{"/bin/sh", "-c", "kubectl get pods"},
	}
	if err := validatePodExecRequest(header, request, 1024, 1024); err != nil {
		t.Fatalf("command request rejected: %v", err)
	}
	request.Command = nil
	if err := validatePodExecRequest(header, request, 1024, 1024); !errors.Is(err, ErrStreamProtocol) {
		t.Fatalf("non-TTY request without command error = %v, want protocol error", err)
	}
	request.Tty = true
	request.Command = []string{"/bin/sh", "-c", "id"}
	if err := validatePodExecRequest(header, request, 1024, 1024); !errors.Is(err, ErrStreamProtocol) {
		t.Fatalf("TTY command request error = %v, want protocol error", err)
	}
}

func TestPodExecFrameWriterSharesOutputLimitAcrossConcurrentStreams(t *testing.T) {
	t.Parallel()

	const maximum = 64 * 1024
	var encoded bytes.Buffer
	writer := &podExecFrameWriter{
		stream:  &encoded,
		maximum: maximum,
		ready:   make(chan struct{}),
	}
	writer.release()

	type result struct {
		written int
		err     error
	}
	results := make(chan result, 2)
	for _, stream := range []agentv1.PodExecOutputStream{
		agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDOUT,
		agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDERR,
	} {
		go func(stream agentv1.PodExecOutputStream) {
			written, err := writer.output(stream).Write(bytes.Repeat([]byte{'x'}, maximum))
			results <- result{written: written, err: err}
		}(stream)
	}

	totalWritten := 0
	limited := 0
	for range 2 {
		result := <-results
		totalWritten += result.written
		if errors.Is(result.err, ErrPodExecOutputLimit) {
			limited++
		} else if result.err != nil {
			t.Fatalf("write output: %v", result.err)
		}
	}
	if totalWritten != maximum || writer.bytes.Load() != maximum || limited == 0 || !writer.limit.Load() {
		t.Fatalf(
			"written=%d bytes=%d limited=%d limit_flag=%t",
			totalWritten,
			writer.bytes.Load(),
			limited,
			writer.limit.Load(),
		)
	}

	decodedBytes := 0
	for encoded.Len() > 0 {
		frame := &agentv1.PodExecFrame{}
		if err := ReadMessage(&encoded, frame); err != nil {
			t.Fatal(err)
		}
		decodedBytes += len(frame.GetOutput().GetData())
	}
	if decodedBytes != maximum {
		t.Fatalf("decoded output bytes = %d, want %d", decodedBytes, maximum)
	}
}

func TestRealQUICPodExecRelaysInputResizeOutputAndExit(t *testing.T) {
	client, server, stop := openStreamTestConnection(t)
	defer stop()

	resizeObserved := make(chan PodExecSize, 1)
	streamServer, err := NewStreamServer(StreamServerConfig{
		HeaderTimeout: 200 * time.Millisecond,
		MaxTimeout:    2 * time.Second,
		Handlers: map[agentv1.StreamKind]StreamHandlerConfig{
			agentv1.StreamKind_STREAM_KIND_POD_EXEC: {
				MaxConcurrent: 2,
				Handle: PodExecStreamHandler(1024, 1024, func(
					_ context.Context,
					request *agentv1.PodExecRequest,
					stdin io.Reader,
					stdout io.Writer,
					_ io.Writer,
					sizes PodExecSizeQueue,
				) (*agentv1.PodExecResponse, <-chan *agentv1.PodExecExit, error) {
					initial := sizes.Next()
					if initial == nil || initial.Columns != 120 || initial.Rows != 40 {
						t.Fatalf("initial terminal size = %+v", initial)
					}
					exits := make(chan *agentv1.PodExecExit, 1)
					go func() {
						resized := sizes.Next()
						if resized != nil {
							resizeObserved <- *resized
						}
						data, readErr := io.ReadAll(stdin)
						if readErr != nil {
							exits <- &agentv1.PodExecExit{
								Result: agentv1.ResultCode_RESULT_CODE_INTERNAL,
								Reason: "ReadFailed", Message: "read failed",
							}
							return
						}
						output := append([]byte("echo:"), data...)
						written, writeErr := stdout.Write(output)
						if writeErr != nil {
							return
						}
						exits <- &agentv1.PodExecExit{
							Result:      agentv1.ResultCode_RESULT_CODE_OK,
							ExitCode:    3,
							OutputBytes: uint64(written),
						}
					}()
					return &agentv1.PodExecResponse{
						Result:               agentv1.ResultCode_RESULT_CODE_OK,
						KubernetesStatusCode: 200,
						PodUid:               request.GetPodUid(),
						Container:            request.GetContainer(),
					}, exits, nil
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
				t.Errorf("stop Pod Exec Stream Server: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Pod Exec Stream Server did not stop")
		}
	}()

	peer := newTestPodExecPeer(
		&agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_Resize{
			Resize: &agentv1.PodExecResize{Columns: 160, Rows: 50},
		}},
		&agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_Input{
			Input: &agentv1.PodExecInput{Data: []byte("id\n")},
		}},
		&agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_CloseInput{
			CloseInput: &agentv1.PodExecCloseInput{},
		}},
	)
	response, exit, err := DoPodExec(
		context.Background(),
		client,
		&agentv1.StreamHeader{
			ProtocolVersion: ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_POD_EXEC,
			RequestId:       "00000000-0000-4000-8000-000000000031",
			TimeoutMillis:   1000,
		},
		&agentv1.PodExecRequest{
			Namespace: "workloads", PodName: "api-0", PodUid: "pod-uid", Container: "main",
			Tty: true, Columns: 120, Rows: 40, MaxInputBytes: 1024, MaxOutputBytes: 1024,
		},
		peer,
		1024,
		1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		exit.GetResult() != agentv1.ResultCode_RESULT_CODE_OK || exit.GetExitCode() != 3 {
		t.Fatalf("response=%+v exit=%+v", response, exit)
	}
	if resize := <-resizeObserved; resize.Columns != 160 || resize.Rows != 50 {
		t.Fatalf("resize = %+v", resize)
	}
	frames := peer.sentFrames()
	if len(frames) != 2 || string(frames[0].GetOutput().GetData()) != "echo:id\n" ||
		frames[1].GetExit().GetOutputBytes() != uint64(len("echo:id\n")) {
		t.Fatalf("unexpected Server peer frames: %+v", frames)
	}
}

type testPodExecPeer struct {
	inputs chan *agentv1.PodExecFrame
	mutex  sync.Mutex
	sent   []*agentv1.PodExecFrame
}

func newTestPodExecPeer(frames ...*agentv1.PodExecFrame) *testPodExecPeer {
	inputs := make(chan *agentv1.PodExecFrame, len(frames))
	for _, frame := range frames {
		inputs <- frame
	}
	return &testPodExecPeer{inputs: inputs}
}

func (peer *testPodExecPeer) Receive(ctx context.Context) (*agentv1.PodExecFrame, error) {
	select {
	case frame := <-peer.inputs:
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (peer *testPodExecPeer) Send(_ context.Context, frame *agentv1.PodExecFrame) error {
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	peer.sent = append(peer.sent, frame)
	return nil
}

func (peer *testPodExecPeer) sentFrames() []*agentv1.PodExecFrame {
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	return append([]*agentv1.PodExecFrame(nil), peer.sent...)
}
