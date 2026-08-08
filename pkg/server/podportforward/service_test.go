package podportforward

import (
	"context"
	"errors"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

const (
	testUserID    = "00000000-0000-4000-8000-000000000001"
	testSessionID = "00000000-0000-4000-8000-000000000002"
	testClusterID = "00000000-0000-4000-8000-000000000003"
)

func TestSessionIsBoundIdempotentAndOneTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, Config{MaxPending: 2})
	input := createInput(now)
	created, err := service.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := service.Create(input)
	if err != nil || retried.ID != created.ID {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	changed := input
	changed.Port = 9090
	if _, err := service.Create(changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict=%v", err)
	}
	consume := ConsumeInput{ID: created.ID, UserID: testUserID, AuthSessionID: testSessionID,
		ClusterID: testClusterID, Namespace: "workloads", PodName: "api-0", Now: now}
	if _, err := service.Consume(consume); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Consume(consume); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second consume=%v", err)
	}
}

func TestBindingMismatchConsumesTicket(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, Config{})
	created, err := service.Create(createInput(now))
	if err != nil {
		t.Fatal(err)
	}
	consume := ConsumeInput{ID: created.ID, UserID: "00000000-0000-4000-8000-000000000099",
		AuthSessionID: testSessionID, ClusterID: testClusterID, Namespace: "workloads", PodName: "api-0", Now: now}
	if _, err := service.Consume(consume); !errors.Is(err, ErrSessionBindingMismatch) {
		t.Fatalf("mismatch=%v", err)
	}
	consume.UserID = testUserID
	if _, err := service.Consume(consume); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("ticket survived=%v", err)
	}
}

func TestRunBuildsUIDAndPortBoundRequestWithLimits(t *testing.T) {
	t.Parallel()
	requester := &fakeRequester{}
	service := NewService(requester, Config{MaxClientBytes: 123, MaxPodBytes: 456})
	result, err := service.Run(context.Background(), Session{ID: "00000000-0000-4000-8000-000000000010",
		ClusterID: testClusterID, Namespace: "workloads", PodName: "api-0", PodUID: "pod-uid", Port: 8080}, &fakePeer{})
	if err != nil {
		t.Fatal(err)
	}
	request := requester.request
	if requester.clusterID != testClusterID || request.GetPodUid() != "pod-uid" || request.GetPort() != 8080 ||
		request.GetMaxClientBytes() != 123 || request.GetMaxPodBytes() != 456 || result.ClientBytes != 12 || result.PodBytes != 34 {
		t.Fatalf("request=%+v result=%+v", request, result)
	}
}

func createInput(now time.Time) CreateInput {
	return CreateInput{UserID: testUserID, AuthSessionID: testSessionID, IdempotencyKey: "port-forward-session-1",
		ClusterID: testClusterID, Namespace: "workloads", PodName: "api-0", PodUID: "pod-uid", Port: 8080,
		Confirm: true, Now: now}
}

type fakeRequester struct {
	clusterID string
	request   *agentv1.PodPortForwardRequest
}

func (requester *fakeRequester) RequestPodPortForward(_ context.Context, clusterID string,
	request *agentv1.PodPortForwardRequest, _ agentprotocol.PodPortForwardPeer,
) (*agentv1.PodPortForwardResponse, *agentv1.PodPortForwardExit, error) {
	requester.clusterID, requester.request = clusterID, request
	return &agentv1.PodPortForwardResponse{Result: agentv1.ResultCode_RESULT_CODE_OK, PodUid: request.GetPodUid(), Port: request.GetPort()},
		&agentv1.PodPortForwardExit{Result: agentv1.ResultCode_RESULT_CODE_OK, ClientBytes: 12, PodBytes: 34}, nil
}

type fakePeer struct{}

func (*fakePeer) Read(ctx context.Context) ([]byte, error) { return nil, ctx.Err() }
func (*fakePeer) Write(context.Context, []byte) error      { return nil }
