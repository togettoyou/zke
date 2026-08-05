package podlogs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
)

const podLogsTestClusterID = "00000000-0000-4000-8000-000000000003"

type fakeRequester struct {
	handle func(context.Context, string, *agentv1.PodLogsRequest, io.Writer) (
		*agentv1.PodLogsResponse,
		*agentv1.PodLogsTrailer,
		error,
	)
}

func (fake *fakeRequester) RequestPodLogs(
	ctx context.Context,
	clusterID string,
	request *agentv1.PodLogsRequest,
	destination io.Writer,
) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
	return fake.handle(ctx, clusterID, request, destination)
}

func TestServiceStreamsExplicitBoundedPodLogs(t *testing.T) {
	t.Parallel()

	tail := int64(25)
	since := int64(60)
	requester := &fakeRequester{handle: func(
		_ context.Context,
		clusterID string,
		request *agentv1.PodLogsRequest,
		destination io.Writer,
	) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
		if clusterID != podLogsTestClusterID ||
			request.GetNamespace() != "model-serving" ||
			request.GetPodName() != "inference-abcde" ||
			request.GetPodUid() != "pod-uid" ||
			request.GetContainer() != "main" || !request.GetFollow() ||
			request.GetPrevious() || request.GetTailLines() != 25 ||
			request.GetSinceSeconds() != 60 || !request.GetTimestamps() ||
			request.GetMaxBytes() != 4096 {
			t.Fatalf("unexpected Pod logs request: cluster=%q request=%+v", clusterID, request)
		}
		written, err := io.WriteString(destination, "one\ntwo\n")
		if err != nil {
			return nil, nil, err
		}
		return &agentv1.PodLogsResponse{
				Result: agentv1.ResultCode_RESULT_CODE_OK,
			}, &agentv1.PodLogsTrailer{
				Result:    agentv1.ResultCode_RESULT_CODE_OK,
				BytesSent: uint64(written),
			}, nil
	}}
	service := NewService(requester, Config{MaxBytes: 4096})
	var output bytes.Buffer
	result, err := service.Stream(context.Background(), Input{
		ClusterID: podLogsTestClusterID, Namespace: "model-serving",
		PodName: "inference-abcde", PodUID: "pod-uid", Container: "main",
		Follow: true, TailLines: &tail, SinceSeconds: &since,
		Timestamps: true,
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "one\ntwo\n" || result.BytesSent != uint64(output.Len()) ||
		result.LimitReached {
		t.Fatalf("output=%q result=%+v", output.String(), result)
	}
}

func TestServiceRejectsInvalidScopeBeforeTransport(t *testing.T) {
	t.Parallel()

	requester := &fakeRequester{handle: func(
		context.Context,
		string,
		*agentv1.PodLogsRequest,
		io.Writer,
	) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
		t.Fatal("invalid Pod logs request reached transport")
		return nil, nil, nil
	}}
	service := NewService(requester, Config{})
	_, err := service.Stream(context.Background(), Input{
		ClusterID: podLogsTestClusterID,
		Namespace: "INVALID_NAMESPACE",
		PodName:   "example",
		PodUID:    "pod-uid",
		Container: "main",
	}, io.Discard)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid scope error = %v", err)
	}
}

func TestServiceMapsTransportAndPodReplacementErrors(t *testing.T) {
	t.Parallel()

	valid := Input{
		ClusterID: podLogsTestClusterID,
		Namespace: "default",
		PodName:   "example",
		PodUID:    "pod-uid",
		Container: "main",
	}
	service := NewService(&fakeRequester{handle: func(
		context.Context,
		string,
		*agentv1.PodLogsRequest,
		io.Writer,
	) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
		return nil, nil, agentconn.ErrPodLogsRequestExhausted
	}}, Config{})
	if _, err := service.Stream(context.Background(), valid, io.Discard); !errors.Is(err, ErrRequestCapacity) {
		t.Fatalf("capacity error = %v", err)
	}

	service = NewService(&fakeRequester{handle: func(
		context.Context,
		string,
		*agentv1.PodLogsRequest,
		io.Writer,
	) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
		return &agentv1.PodLogsResponse{
			Result: agentv1.ResultCode_RESULT_CODE_CONFLICT,
			Reason: "PodUIDMismatch",
		}, nil, nil
	}}, Config{})
	if _, err := service.Stream(context.Background(), valid, io.Discard); !errors.Is(err, ErrPodReplaced) {
		t.Fatalf("Pod replacement error = %v", err)
	}
}

func TestServiceReportsMissingPreviousInstanceApartFromInvalidInput(t *testing.T) {
	t.Parallel()

	previous := Input{
		ClusterID: podLogsTestClusterID,
		Namespace: "default",
		PodName:   "example",
		PodUID:    "pod-uid",
		Container: "main",
		Previous:  true,
	}
	service := NewService(&fakeRequester{handle: func(
		context.Context,
		string,
		*agentv1.PodLogsRequest,
		io.Writer,
	) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
		return &agentv1.PodLogsResponse{
			Result: agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
			Reason: "PreviousLogsNotFound",
		}, nil, nil
	}}, Config{})
	if _, err := service.Stream(context.Background(), previous, io.Discard); !errors.Is(
		err,
		ErrPreviousLogsNotFound,
	) {
		t.Fatalf("missing previous instance error = %v", err)
	}

	// A missing Pod is still a missing Pod.
	service = NewService(&fakeRequester{handle: func(
		context.Context,
		string,
		*agentv1.PodLogsRequest,
		io.Writer,
	) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
		return &agentv1.PodLogsResponse{
			Result: agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
			Reason: "NotFound",
		}, nil, nil
	}}, Config{})
	if _, err := service.Stream(context.Background(), previous, io.Discard); !errors.Is(
		err,
		ErrPodNotFound,
	) {
		t.Fatalf("missing Pod error = %v", err)
	}

	// Following a terminated instance never reaches the cluster.
	following := previous
	following.Follow = true
	service = NewService(&fakeRequester{handle: func(
		context.Context,
		string,
		*agentv1.PodLogsRequest,
		io.Writer,
	) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
		t.Fatal("follow with previous reached transport")
		return nil, nil, nil
	}}, Config{})
	if _, err := service.Stream(context.Background(), following, io.Discard); !errors.Is(
		err,
		ErrInvalidInput,
	) {
		t.Fatalf("follow with previous error = %v", err)
	}
}
