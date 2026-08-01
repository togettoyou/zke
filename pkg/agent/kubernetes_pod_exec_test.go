package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	clientexec "k8s.io/client-go/util/exec"
)

func TestKubernetesPodExecUsesFixedShellAndReturnsProcessExit(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset(testExecPod())
	executor := &fakePodExecExecutor{run: func(options remotecommand.StreamOptions) error {
		if !options.Tty || options.Stdin == nil || options.Stdout == nil ||
			options.Stderr != nil || options.TerminalSizeQueue == nil {
			t.Fatalf("unexpected Exec stream options: %+v", options)
		}
		if _, err := options.Stdout.Write([]byte("ready")); err != nil {
			return err
		}
		return clientexec.CodeExitError{Err: errors.New("exit 7"), Code: 7}
	}}
	factory := func(
		_ *rest.Config,
		_ kubernetes.Interface,
		namespace string,
		podName string,
		options corev1.PodExecOptions,
	) (remotecommand.Executor, error) {
		if namespace != "workloads" || podName != "api-0" ||
			options.Container != "main" || !options.Stdin || !options.Stdout ||
			!options.TTY || options.Stderr || len(options.Command) != 3 ||
			options.Command[0] != "/bin/sh" || options.Command[1] != "-c" ||
			options.Command[2] != podExecShellSelector {
			t.Fatalf("unexpected Pod Exec target/options: %s/%s %+v", namespace, podName, options)
		}
		return executor, nil
	}
	handler := newKubernetesPodExecHandlerWithFactory(client, &rest.Config{}, factory)
	var stdout bytes.Buffer
	response, exits, err := handler(
		context.Background(),
		testPodExecRequest(),
		bytes.NewBufferString("id\n"),
		&stdout,
		io.Discard,
		staticPodExecSizeQueue{},
	)
	if err != nil {
		t.Fatal(err)
	}
	exit := <-exits
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		response.GetPodUid() != "pod-uid" || response.GetContainer() != "main" ||
		exit.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		exit.GetExitCode() != 7 || exit.GetOutputBytes() != 5 || stdout.String() != "ready" {
		t.Fatalf("response=%+v exit=%+v stdout=%q", response, exit, stdout.String())
	}
}

func TestKubernetesPodExecRejectsChangedPodIdentityBeforeTransport(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset(testExecPod())
	called := false
	handler := newKubernetesPodExecHandlerWithFactory(
		client,
		&rest.Config{},
		func(
			_ *rest.Config,
			_ kubernetes.Interface,
			_ string,
			_ string,
			_ corev1.PodExecOptions,
		) (remotecommand.Executor, error) {
			called = true
			return nil, errors.New("must not be called")
		},
	)
	request := testPodExecRequest()
	request.PodUid = "stale-uid"
	response, exits, err := handler(
		context.Background(), request, bytes.NewReader(nil), io.Discard, io.Discard, nil,
	)
	if err != nil || exits != nil || called ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_CONFLICT ||
		response.GetReason() != "PodUIDMismatch" {
		t.Fatalf("response=%+v exits=%v called=%t err=%v", response, exits, called, err)
	}
}

func TestPodExecExitReportsOutputLimitWithoutContent(t *testing.T) {
	t.Parallel()

	exit := podExecExit(agentprotocol.ErrPodExecOutputLimit, 1024)
	if exit.GetResult() != agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED ||
		!exit.GetOutputLimitReached() || exit.GetOutputBytes() != 1024 ||
		exit.GetReason() != "OutputLimitReached" {
		t.Fatalf("unexpected output limit exit: %+v", exit)
	}
}

func TestKubernetesPodExecTransportPrefersWebSocketWithLegacyFallback(t *testing.T) {
	t.Parallel()

	requestURL, err := url.Parse("https://kubernetes.example.invalid/api/v1/namespaces/workloads/pods/api-0/exec")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := newKubernetesPodExecTransport(&rest.Config{}, requestURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := executor.(*remotecommand.FallbackExecutor); !ok {
		t.Fatalf("transport type = %T, want WebSocket-primary FallbackExecutor", executor)
	}
}

func testExecPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "workloads",
			Name:      "api-0",
			UID:       types.UID("pod-uid"),
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
	}
}

func testPodExecRequest() *agentv1.PodExecRequest {
	return &agentv1.PodExecRequest{
		Namespace:      "workloads",
		PodName:        "api-0",
		PodUid:         "pod-uid",
		Container:      "main",
		Tty:            true,
		Columns:        120,
		Rows:           40,
		MaxInputBytes:  1024,
		MaxOutputBytes: 1024,
	}
}

type fakePodExecExecutor struct {
	run func(remotecommand.StreamOptions) error
}

func (executor *fakePodExecExecutor) Stream(options remotecommand.StreamOptions) error {
	return executor.run(options)
}

func (executor *fakePodExecExecutor) StreamWithContext(
	_ context.Context,
	options remotecommand.StreamOptions,
) error {
	return executor.run(options)
}

type staticPodExecSizeQueue struct{}

func (staticPodExecSizeQueue) Next() *agentprotocol.PodExecSize {
	return nil
}
