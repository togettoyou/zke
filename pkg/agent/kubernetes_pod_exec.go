package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	clientexec "k8s.io/client-go/util/exec"
)

const podExecShellSelector = "if command -v bash >/dev/null 2>&1; then exec bash; else exec /bin/sh; fi"

type podExecExecutorFactory func(
	*rest.Config,
	kubernetes.Interface,
	string,
	string,
	corev1.PodExecOptions,
) (remotecommand.Executor, error)

func newKubernetesPodExecHandler(
	client kubernetes.Interface,
	config *rest.Config,
) agentprotocol.PodExecHandler {
	if config != nil {
		config = rest.CopyConfig(config)
		// The Pod Exec Stream Context owns the total session deadline. Keeping
		// the REST client timeout used by short resource calls would terminate
		// an otherwise healthy interactive session early.
		config.Timeout = 0
	}
	return newKubernetesPodExecHandlerWithFactory(
		client,
		config,
		newKubernetesPodExecExecutor,
	)
}

func newKubernetesPodExecHandlerWithFactory(
	client kubernetes.Interface,
	config *rest.Config,
	factory podExecExecutorFactory,
) agentprotocol.PodExecHandler {
	return func(
		ctx context.Context,
		request *agentv1.PodExecRequest,
		stdin io.Reader,
		stdout io.Writer,
		stderr io.Writer,
		sizes agentprotocol.PodExecSizeQueue,
	) (*agentv1.PodExecResponse, <-chan *agentv1.PodExecExit, error) {
		if client == nil || config == nil || factory == nil {
			return podExecErrorResponse(
				agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
				http.StatusServiceUnavailable,
				"KubernetesClientUnavailable",
				"Kubernetes client is unavailable",
			), nil, nil
		}
		pod, err := client.CoreV1().Pods(request.GetNamespace()).Get(
			ctx,
			request.GetPodName(),
			metav1.GetOptions{},
		)
		if err != nil {
			return kubernetesPodExecError(err), nil, nil
		}
		if string(pod.UID) != request.GetPodUid() {
			return podExecErrorResponse(
				agentv1.ResultCode_RESULT_CODE_CONFLICT,
				http.StatusConflict,
				"PodUIDMismatch",
				"Kubernetes Pod identity changed before the terminal was opened",
			), nil, nil
		}
		if !podHasContainer(pod, request.GetContainer()) {
			return podExecErrorResponse(
				agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				http.StatusBadRequest,
				"ContainerNotFound",
				"requested container does not exist in the Kubernetes Pod",
			), nil, nil
		}

		executor, err := factory(config, client, request.GetNamespace(), request.GetPodName(), corev1.PodExecOptions{
			Container: request.GetContainer(),
			Command:   []string{"/bin/sh", "-c", podExecShellSelector},
			Stdin:     true,
			Stdout:    true,
			Stderr:    !request.GetTty(),
			TTY:       request.GetTty(),
		})
		if err != nil {
			return podExecErrorResponse(
				agentv1.ResultCode_RESULT_CODE_INTERNAL,
				http.StatusInternalServerError,
				"ExecTransportUnavailable",
				"Kubernetes Exec transport could not be created",
			), nil, nil
		}

		var outputBytes atomic.Uint64
		countedStdout := &podExecCountingWriter{writer: stdout, bytes: &outputBytes}
		exits := make(chan *agentv1.PodExecExit, 1)
		go func() {
			defer close(exits)
			err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
				Stdin:             stdin,
				Stdout:            countedStdout,
				Stderr:            nil,
				Tty:               true,
				TerminalSizeQueue: podExecRemoteSizeQueue{source: sizes},
			})
			exits <- podExecExit(err, outputBytes.Load())
		}()
		return &agentv1.PodExecResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_OK,
			KubernetesStatusCode: http.StatusOK,
			PodUid:               request.GetPodUid(),
			Container:            request.GetContainer(),
		}, exits, nil
	}
}

func newKubernetesPodExecExecutor(
	config *rest.Config,
	client kubernetes.Interface,
	namespace string,
	podName string,
	options corev1.PodExecOptions,
) (remotecommand.Executor, error) {
	requestURL := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec").
		VersionedParams(&options, scheme.ParameterCodec).
		URL()
	return newKubernetesPodExecTransport(config, requestURL)
}

func newKubernetesPodExecTransport(
	config *rest.Config,
	requestURL *url.URL,
) (remotecommand.Executor, error) {
	legacyExecutor, err := remotecommand.NewSPDYExecutor(
		config,
		http.MethodPost,
		requestURL,
	)
	if err != nil {
		return nil, err
	}
	webSocketExecutor, err := remotecommand.NewWebSocketExecutor(
		config,
		http.MethodGet,
		requestURL.String(),
	)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewFallbackExecutor(
		webSocketExecutor,
		legacyExecutor,
		func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		},
	)
}

type podExecRemoteSizeQueue struct {
	source agentprotocol.PodExecSizeQueue
}

func (queue podExecRemoteSizeQueue) Next() *remotecommand.TerminalSize {
	if queue.source == nil {
		return nil
	}
	size := queue.source.Next()
	if size == nil {
		return nil
	}
	return &remotecommand.TerminalSize{
		Width:  uint16(size.Columns),
		Height: uint16(size.Rows),
	}
}

type podExecCountingWriter struct {
	writer io.Writer
	bytes  *atomic.Uint64
}

func (writer *podExecCountingWriter) Write(data []byte) (int, error) {
	written, err := writer.writer.Write(data)
	writer.bytes.Add(uint64(written))
	return written, err
}

func podExecExit(err error, outputBytes uint64) *agentv1.PodExecExit {
	exit := &agentv1.PodExecExit{
		Result:      agentv1.ResultCode_RESULT_CODE_OK,
		OutputBytes: outputBytes,
	}
	if err == nil {
		return exit
	}
	var processExit clientexec.ExitError
	if errors.As(err, &processExit) && processExit.Exited() && processExit.ExitStatus() >= 0 {
		exit.ExitCode = int32(processExit.ExitStatus())
		return exit
	}
	if errors.Is(err, agentprotocol.ErrPodExecOutputLimit) {
		exit.Result = agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED
		exit.Reason = "OutputLimitReached"
		exit.Message = "terminal output exceeded the configured limit"
		exit.OutputLimitReached = true
		return exit
	}
	response := kubernetesResourceError(err)
	exit.Result = response.GetResult()
	exit.Reason = response.GetReason()
	exit.Message = response.GetMessage()
	return exit
}

func kubernetesPodExecError(err error) *agentv1.PodExecResponse {
	response := kubernetesResourceError(err)
	return podExecErrorResponse(
		response.GetResult(),
		response.GetKubernetesStatusCode(),
		response.GetReason(),
		response.GetMessage(),
	)
}

func podExecErrorResponse(
	result agentv1.ResultCode,
	statusCode int32,
	reason string,
	message string,
) *agentv1.PodExecResponse {
	return &agentv1.PodExecResponse{
		Result:               result,
		KubernetesStatusCode: statusCode,
		Reason:               reason,
		Message:              message,
	}
}
