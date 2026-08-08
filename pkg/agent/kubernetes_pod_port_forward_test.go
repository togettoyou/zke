package agent

import (
	"context"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestKubernetesPodPortForwardPinsIdentityAndPort(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset(testPodPortForwardPod())
	connection := &fakePortForwardConnection{}
	handler := newKubernetesPodPortForwardHandlerWithFactory(client, &rest.Config{}, func(
		_ context.Context, _ *rest.Config, _ kubernetes.Interface, namespace, podName string, port uint16,
	) (agentprotocol.PodPortForwardConnection, error) {
		if namespace != "workloads" || podName != "api-0" || port != 8080 {
			t.Fatalf("unexpected target %s/%s:%d", namespace, podName, port)
		}
		return connection, nil
	})
	response, gotConnection, err := handler(context.Background(), testPodPortForwardRequest())
	if err != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		response.GetPodUid() != "pod-uid" || response.GetPort() != 8080 || gotConnection != connection {
		t.Fatalf("response=%+v connection=%v err=%v", response, gotConnection, err)
	}
}

func TestKubernetesPodPortForwardRejectsChangedIdentityBeforeTransport(t *testing.T) {
	t.Parallel()
	called := false
	handler := newKubernetesPodPortForwardHandlerWithFactory(fake.NewClientset(testPodPortForwardPod()), &rest.Config{}, func(
		_ context.Context, _ *rest.Config, _ kubernetes.Interface, _, _ string, _ uint16,
	) (agentprotocol.PodPortForwardConnection, error) {
		called = true
		return nil, errors.New("must not be called")
	})
	request := testPodPortForwardRequest()
	request.PodUid = "stale-uid"
	response, connection, err := handler(context.Background(), request)
	if err != nil || connection != nil || called || response.GetReason() != "PodUIDMismatch" ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_CONFLICT {
		t.Fatalf("response=%+v connection=%v called=%t err=%v", response, connection, called, err)
	}
}

func testPodPortForwardPod() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-0", Namespace: "workloads", UID: types.UID("pod-uid")}}
}

func testPodPortForwardRequest() *agentv1.PodPortForwardRequest {
	return &agentv1.PodPortForwardRequest{Namespace: "workloads", PodName: "api-0", PodUid: "pod-uid",
		Port: 8080, MaxClientBytes: 1024, MaxPodBytes: 1024}
}

type fakePortForwardConnection struct{}

func (*fakePortForwardConnection) Read([]byte) (int, error)       { return 0, io.EOF }
func (*fakePortForwardConnection) Write(data []byte) (int, error) { return len(data), nil }
func (*fakePortForwardConnection) Close() error                   { return nil }
