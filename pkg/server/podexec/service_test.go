package podexec

import (
	"context"
	"errors"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

const (
	testUserID        = "00000000-0000-4000-8000-000000000001"
	testAuthSessionID = "00000000-0000-4000-8000-000000000002"
	testClusterID     = "00000000-0000-4000-8000-000000000003"
)

func TestPodExecSessionIsIdempotentBoundAndOneTime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, nil, Config{SessionTTL: 30 * time.Second, MaxPending: 2})
	input := testCreateInput(now)
	created, err := service.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := service.Create(input)
	if err != nil || retried.ID != created.ID {
		t.Fatalf("idempotent retry=%+v err=%v", retried, err)
	}
	conflict := input
	conflict.Container = "sidecar"
	if _, err := service.Create(conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting retry error = %v", err)
	}
	if _, err := service.Consume(ConsumeInput{
		ID: created.ID, UserID: testUserID, AuthSessionID: testAuthSessionID,
		ClusterID: testClusterID, Namespace: "workloads", PodName: "api-0", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Consume(ConsumeInput{
		ID: created.ID, UserID: testUserID, AuthSessionID: testAuthSessionID,
		ClusterID: testClusterID, Namespace: "workloads", PodName: "api-0", Now: now,
	}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second consume error = %v", err)
	}
}

func TestPodExecSessionBindingMismatchConsumesTicket(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, nil, Config{})
	created, err := service.Create(testCreateInput(now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Consume(ConsumeInput{
		ID: created.ID, UserID: "00000000-0000-4000-8000-000000000099",
		AuthSessionID: testAuthSessionID, ClusterID: testClusterID,
		Namespace: "workloads", PodName: "api-0", Now: now,
	}); !errors.Is(err, ErrSessionBindingMismatch) {
		t.Fatalf("binding mismatch error = %v", err)
	}
	if _, err := service.Consume(ConsumeInput{
		ID: created.ID, UserID: testUserID, AuthSessionID: testAuthSessionID,
		ClusterID: testClusterID, Namespace: "workloads", PodName: "api-0", Now: now,
	}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("ticket survived binding mismatch: %v", err)
	}
}

func TestPodExecRunBuildsBoundedTTYRequest(t *testing.T) {
	t.Parallel()

	requester := &fakePodExecRequester{}
	service := NewService(requester, nil, Config{MaxInputBytes: 123, MaxOutputBytes: 456})
	session := Session{
		ID: "00000000-0000-4000-8000-000000000010", ClusterID: testClusterID,
		Namespace: "workloads", PodName: "api-0", PodUID: "pod-uid", Container: "main",
		Columns: 120, Rows: 40,
	}
	result, err := service.Run(context.Background(), session, &fakePodExecPeer{})
	if err != nil {
		t.Fatal(err)
	}
	request := requester.request
	if requester.clusterID != testClusterID || request == nil || !request.GetTty() ||
		request.GetMaxInputBytes() != 123 || request.GetMaxOutputBytes() != 456 ||
		request.GetPodUid() != "pod-uid" || request.GetContainer() != "main" ||
		result.ExitCode != 9 || result.OutputBytes != 42 {
		t.Fatalf("request=%+v result=%+v", request, result)
	}
}

func TestPodExecRecordingCapturesOnlyBoundedOutput(t *testing.T) {
	t.Parallel()
	store := &fakeRecordingStore{}
	service := NewService(
		&recordingPodExecRequester{},
		store,
		Config{MaxOutputBytes: 64, MaxRecordingBytes: 5, RecordingRetention: time.Hour},
	)
	session := Session{
		ID: "00000000-0000-4000-8000-000000000010", UserID: testUserID,
		ClusterID: testClusterID, Namespace: "workloads", PodName: "api-0",
		PodUID: "pod-uid", Container: "main", Columns: 120, Rows: 40,
		RecordingID: "00000000-0000-4000-8000-000000000011",
	}
	result, err := service.Run(context.Background(), session, &fakePodExecPeer{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RecordingSaved || result.RecordingID != session.RecordingID {
		t.Fatalf("result=%+v", result)
	}
	recording := store.saved
	if recording.UserID != testUserID || recording.ClusterID != testClusterID ||
		recording.RecordingBytes != 5 || !recording.Truncated || len(recording.Frames) != 1 ||
		string(recording.Frames[0].Data) != "secre" {
		t.Fatalf("recording=%+v", recording)
	}
}

func testCreateInput(now time.Time) CreateInput {
	return CreateInput{
		UserID: testUserID, AuthSessionID: testAuthSessionID,
		IdempotencyKey: "pod-terminal-session-0001", ClusterID: testClusterID,
		Namespace: "workloads", PodName: "api-0", PodUID: "pod-uid", Container: "main",
		Columns: 120, Rows: 40, Confirm: true, Now: now,
	}
}

type fakePodExecRequester struct {
	clusterID string
	request   *agentv1.PodExecRequest
}

func (requester *fakePodExecRequester) RequestPodExec(
	_ context.Context,
	clusterID string,
	request *agentv1.PodExecRequest,
	_ agentprotocol.PodExecPeer,
) (*agentv1.PodExecResponse, *agentv1.PodExecExit, error) {
	requester.clusterID = clusterID
	requester.request = request
	return &agentv1.PodExecResponse{
			Result: agentv1.ResultCode_RESULT_CODE_OK,
		}, &agentv1.PodExecExit{
			Result: agentv1.ResultCode_RESULT_CODE_OK, ExitCode: 9, OutputBytes: 42,
		}, nil
}

type fakePodExecPeer struct{}

func (*fakePodExecPeer) Receive(context.Context) (*agentv1.PodExecFrame, error) {
	return nil, context.Canceled
}

func (*fakePodExecPeer) Send(context.Context, *agentv1.PodExecFrame) error {
	return nil
}

type fakeRecordingStore struct {
	saved Recording
}

func (store *fakeRecordingStore) SaveRecording(_ context.Context, recording Recording) error {
	store.saved = recording
	return nil
}

func (*fakeRecordingStore) ListRecordings(context.Context, RecordingScope, int) ([]Recording, error) {
	return nil, nil
}

func (*fakeRecordingStore) GetRecording(context.Context, RecordingScope, string) (Recording, error) {
	return Recording{}, ErrRecordingNotFound
}

type recordingPodExecRequester struct{}

func (*recordingPodExecRequester) RequestPodExec(
	ctx context.Context,
	_ string,
	_ *agentv1.PodExecRequest,
	peer agentprotocol.PodExecPeer,
) (*agentv1.PodExecResponse, *agentv1.PodExecExit, error) {
	if err := peer.Send(ctx, &agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_Output{
		Output: &agentv1.PodExecOutput{
			Stream: agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDOUT,
			Data:   []byte("secret-output"),
		},
	}}); err != nil {
		return nil, nil, err
	}
	return &agentv1.PodExecResponse{Result: agentv1.ResultCode_RESULT_CODE_OK},
		&agentv1.PodExecExit{Result: agentv1.ResultCode_RESULT_CODE_OK, OutputBytes: 13}, nil
}
