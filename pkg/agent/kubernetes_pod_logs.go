package agent

import (
	"context"
	"io"
	"net/http"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func newKubernetesPodLogsHandler(client kubernetes.Interface) func(
	context.Context,
	*agentv1.PodLogsRequest,
) (*agentv1.PodLogsResponse, io.ReadCloser, error) {
	return func(
		ctx context.Context,
		request *agentv1.PodLogsRequest,
	) (*agentv1.PodLogsResponse, io.ReadCloser, error) {
		if client == nil {
			return podLogsErrorResponse(
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
			return kubernetesPodLogsError(err), nil, nil
		}
		if string(pod.UID) != request.GetPodUid() {
			return podLogsErrorResponse(
				agentv1.ResultCode_RESULT_CODE_CONFLICT,
				http.StatusConflict,
				"PodUIDMismatch",
				"Kubernetes Pod identity changed before logs were opened",
			), nil, nil
		}
		if !podHasContainer(pod, request.GetContainer()) {
			return podLogsErrorResponse(
				agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				http.StatusBadRequest,
				"ContainerNotFound",
				"requested container does not exist in the Kubernetes Pod",
			), nil, nil
		}
		// A container that has never restarted has no previous instance to read.
		// Kubernetes answers that with a 400, which would reach the operator as
		// "check your input" — but the request is well formed, and the honest
		// answer is that the thing asked for does not exist.
		if request.GetPrevious() && !podHasPreviousInstance(pod, request.GetContainer()) {
			return podLogsErrorResponse(
				agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
				http.StatusNotFound,
				"PreviousLogsNotFound",
				"container has no previous terminated instance",
			), nil, nil
		}

		options := &corev1.PodLogOptions{
			Container:  request.GetContainer(),
			Follow:     request.GetFollow(),
			Previous:   request.GetPrevious(),
			Timestamps: request.GetTimestamps(),
		}
		if request.TailLines != nil {
			value := request.GetTailLines()
			options.TailLines = &value
		}
		if request.SinceSeconds != nil {
			value := request.GetSinceSeconds()
			options.SinceSeconds = &value
		}
		limitBytes := int64(request.GetMaxBytes())
		options.LimitBytes = &limitBytes

		stream, err := client.CoreV1().Pods(request.GetNamespace()).GetLogs(
			request.GetPodName(),
			options,
		).Stream(ctx)
		if err != nil {
			return kubernetesPodLogsStreamError(err, request.GetPrevious()), nil, nil
		}
		current, err := client.CoreV1().Pods(request.GetNamespace()).Get(
			ctx,
			request.GetPodName(),
			metav1.GetOptions{},
		)
		if err != nil {
			_ = stream.Close()
			return kubernetesPodLogsError(err), nil, nil
		}
		if string(current.UID) != request.GetPodUid() {
			_ = stream.Close()
			return podLogsErrorResponse(
				agentv1.ResultCode_RESULT_CODE_CONFLICT,
				http.StatusConflict,
				"PodUIDMismatch",
				"Kubernetes Pod identity changed while logs were opened",
			), nil, nil
		}
		return &agentv1.PodLogsResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_OK,
			KubernetesStatusCode: http.StatusOK,
			PodUid:               request.GetPodUid(),
			Container:            request.GetContainer(),
			Follow:               request.GetFollow(),
			ContentType:          "text/plain",
		}, stream, nil
	}
}

func podHasContainer(pod *corev1.Pod, name string) bool {
	if pod == nil || name == "" {
		return false
	}
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return true
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if container.Name == name {
			return true
		}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		if container.Name == name {
			return true
		}
	}
	return false
}

// podHasPreviousInstance reports whether the named container has run before.
//
// Either signal is enough, and neither is claimed on a container Kubernetes has
// not restarted, so this only refuses a read that has nothing to return.
func podHasPreviousInstance(pod *corev1.Pod, name string) bool {
	if pod == nil {
		return false
	}
	statuses := make(
		[]corev1.ContainerStatus,
		0,
		len(pod.Status.ContainerStatuses)+
			len(pod.Status.InitContainerStatuses)+
			len(pod.Status.EphemeralContainerStatuses),
	)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	statuses = append(statuses, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.EphemeralContainerStatuses...)
	for _, status := range statuses {
		if status.Name != name {
			continue
		}
		return status.RestartCount > 0 || status.LastTerminationState.Terminated != nil
	}
	return false
}

// kubernetesPodLogsStreamError maps a failure to open the Kubernetes log stream.
//
// A rejected read of a previous instance is reported as not found rather than as
// invalid input: the Server has already bounded the numeric options and refused
// follow with previous, and this Agent has already checked that the container
// exists and has run before. What is left for Kubernetes to reject is the
// previous instance's log itself being gone — rotated off the node — which is a
// missing object, not a malformed request.
func kubernetesPodLogsStreamError(err error, previous bool) *agentv1.PodLogsResponse {
	response := kubernetesPodLogsError(err)
	if previous && response.GetResult() == agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT {
		return podLogsErrorResponse(
			agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
			http.StatusNotFound,
			"PreviousLogsNotFound",
			"previous container instance logs are no longer available",
		)
	}
	return response
}

func kubernetesPodLogsError(err error) *agentv1.PodLogsResponse {
	resourceResponse := kubernetesResourceError(err)
	return podLogsErrorResponse(
		resourceResponse.GetResult(),
		resourceResponse.GetKubernetesStatusCode(),
		resourceResponse.GetReason(),
		resourceResponse.GetMessage(),
	)
}

func podLogsErrorResponse(
	result agentv1.ResultCode,
	statusCode int32,
	reason string,
	message string,
) *agentv1.PodLogsResponse {
	return &agentv1.PodLogsResponse{
		Result:               result,
		KubernetesStatusCode: statusCode,
		Reason:               reason,
		Message:              message,
	}
}
