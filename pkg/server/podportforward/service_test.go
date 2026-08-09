package podportforward

import (
	"context"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

const (
	testClusterID = "00000000-0000-4000-8000-000000000003"
)

func TestRunBuildsUIDAndPortBoundRequestWithLimits(t *testing.T) {
	t.Parallel()
	requester := &fakeRequester{}
	service := NewService(requester)
	result, err := service.Run(context.Background(), Session{ID: "00000000-0000-4000-8000-000000000010",
		ClusterID: testClusterID, Namespace: "workloads", PodName: "api-0", PodUID: "pod-uid", Port: 8080},
		&fakePeer{}, 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	request := requester.request
	if requester.clusterID != testClusterID || request.GetPodUid() != "pod-uid" || request.GetPort() != 8080 ||
		request.GetMaxClientBytes() != 123 || request.GetMaxPodBytes() != 456 || result.ClientBytes != 12 || result.PodBytes != 34 {
		t.Fatalf("request=%+v result=%+v", request, result)
	}
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
