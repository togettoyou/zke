package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type podPortForwardConnectionFactory func(
	context.Context,
	*rest.Config,
	kubernetes.Interface,
	string,
	string,
	uint16,
) (agentprotocol.PodPortForwardConnection, error)

func newKubernetesPodPortForwardHandler(
	client kubernetes.Interface,
	config *rest.Config,
) agentprotocol.PodPortForwardHandler {
	if config != nil {
		config = rest.CopyConfig(config)
		config.Timeout = 0
	}
	return newKubernetesPodPortForwardHandlerWithFactory(
		client,
		config,
		newKubernetesPodPortForwardConnection,
	)
}

func newKubernetesPodPortForwardHandlerWithFactory(
	client kubernetes.Interface,
	config *rest.Config,
	factory podPortForwardConnectionFactory,
) agentprotocol.PodPortForwardHandler {
	return func(
		ctx context.Context,
		request *agentv1.PodPortForwardRequest,
	) (*agentv1.PodPortForwardResponse, agentprotocol.PodPortForwardConnection, error) {
		if client == nil || config == nil || factory == nil {
			return podPortForwardErrorResponse(
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
			return kubernetesPodPortForwardError(err), nil, nil
		}
		if string(pod.UID) != request.GetPodUid() {
			return podPortForwardErrorResponse(
				agentv1.ResultCode_RESULT_CODE_CONFLICT,
				http.StatusConflict,
				"PodUIDMismatch",
				"Kubernetes Pod identity changed before port forwarding was opened",
			), nil, nil
		}
		connection, err := factory(
			ctx,
			config,
			client,
			request.GetNamespace(),
			request.GetPodName(),
			uint16(request.GetPort()),
		)
		if err != nil {
			return podPortForwardErrorResponse(
				agentv1.ResultCode_RESULT_CODE_INTERNAL,
				http.StatusBadGateway,
				"PortForwardTransportUnavailable",
				"Kubernetes port-forward transport could not be established",
			), nil, nil
		}
		return &agentv1.PodPortForwardResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_OK,
			KubernetesStatusCode: http.StatusOK,
			PodUid:               request.GetPodUid(),
			Port:                 request.GetPort(),
		}, connection, nil
	}
}

func newKubernetesPodPortForwardConnection(
	ctx context.Context,
	config *rest.Config,
	client kubernetes.Interface,
	namespace string,
	podName string,
	remotePort uint16,
) (agentprotocol.PodPortForwardConnection, error) {
	requestURL := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("portforward").
		URL()
	dialer, err := newKubernetesPodPortForwardDialer(config, requestURL)
	if err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	ready := make(chan struct{})
	forwardErrors := make(chan error, 1)
	errorOutput := &bytes.Buffer{}
	forwarder, err := portforward.NewOnAddresses(
		dialer,
		[]string{"127.0.0.1"},
		[]string{fmt.Sprintf("0:%d", remotePort)},
		stop,
		ready,
		io.Discard,
		errorOutput,
	)
	if err != nil {
		close(stop)
		return nil, err
	}
	go func() { forwardErrors <- forwarder.ForwardPorts() }()
	select {
	case <-ctx.Done():
		close(stop)
		return nil, ctx.Err()
	case err := <-forwardErrors:
		close(stop)
		if err == nil {
			err = errors.New("Kubernetes port-forward stopped before it became ready")
		}
		return nil, err
	case <-ready:
	}
	ports, err := forwarder.GetPorts()
	if err != nil || len(ports) != 1 || ports[0].Remote != remotePort || ports[0].Local == 0 {
		close(stop)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("Kubernetes port-forward returned an invalid local port")
	}
	tcpConnection, err := (&net.Dialer{}).DialContext(
		ctx,
		"tcp",
		fmt.Sprintf("127.0.0.1:%d", ports[0].Local),
	)
	if err != nil {
		close(stop)
		return nil, err
	}
	return &podPortForwardConnection{
		Conn: tcpConnection,
		stop: stop,
	}, nil
}

func newKubernetesPodPortForwardDialer(
	config *rest.Config,
	requestURL *url.URL,
) (httpstream.Dialer, error) {
	roundTripper, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return nil, err
	}
	legacy := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, requestURL)
	webSocket, err := portforward.NewSPDYOverWebsocketDialer(requestURL, config)
	if err != nil {
		return nil, err
	}
	return portforward.NewFallbackDialer(webSocket, legacy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	}), nil
}

type podPortForwardConnection struct {
	net.Conn
	stop chan struct{}
	once sync.Once
}

func (connection *podPortForwardConnection) Close() error {
	connection.once.Do(func() { close(connection.stop) })
	return connection.Conn.Close()
}

func kubernetesPodPortForwardError(err error) *agentv1.PodPortForwardResponse {
	response := kubernetesResourceError(err)
	return podPortForwardErrorResponse(
		response.GetResult(),
		response.GetKubernetesStatusCode(),
		response.GetReason(),
		response.GetMessage(),
	)
}

func podPortForwardErrorResponse(
	result agentv1.ResultCode,
	statusCode int32,
	reason string,
	message string,
) *agentv1.PodPortForwardResponse {
	return &agentv1.PodPortForwardResponse{
		Result:               result,
		KubernetesStatusCode: statusCode,
		Reason:               reason,
		Message:              message,
	}
}
