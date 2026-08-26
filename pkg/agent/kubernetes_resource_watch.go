package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

func newKubernetesResourceWatchHandler(client kubernetes.Interface) agentprotocol.ResourceWatchHandler {
	return func(ctx context.Context, request *agentv1.ResourceWatchRequest) (
		*agentv1.ResourceWatchResponse, agentprotocol.ResourceWatchSource, error,
	) {
		resource := request.GetResource()
		if client == nil || resource.GetGroup() != "" || resource.GetVersion() != "v1" || resource.GetResource() != "events" {
			return resourceWatchResponseError(
				agentv1.ResultCode_RESULT_CODE_FORBIDDEN, http.StatusForbidden,
				"ResourceWatchForbidden", "only core/v1 Kubernetes Events may be watched",
			), nil, nil
		}
		// An empty Namespace means every Namespace in the Cluster, which two
		// callers are allowed to ask for and nobody else is. The cluster-wide
		// Event route says so with `cluster_event_access`; a describe gets the
		// narrower exception — a bounded snapshot pinned to one exact Node UID —
		// and may not follow.
		if request.GetNamespace() == "" && !request.GetClusterEventAccess() &&
			(!request.GetIncludeInitialEvents() || request.GetFollow() ||
				!agentprotocol.IsNodeEventFieldSelector(request.GetFieldSelector())) {
			return resourceWatchResponseError(
				agentv1.ResultCode_RESULT_CODE_FORBIDDEN, http.StatusForbidden,
				"ClusterEventWatchForbidden",
				"cluster-wide Events require an exact Node UID snapshot",
			), nil, nil
		}
		options := metav1.ListOptions{
			FieldSelector:       request.GetFieldSelector(),
			ResourceVersion:     request.GetResourceVersion(),
			AllowWatchBookmarks: request.GetAllowWatchBookmarks(),
		}
		source := &kubernetesEventSource{}
		resourceVersion := request.GetResourceVersion()
		truncated := false
		if request.GetIncludeInitialEvents() {
			options.Limit = int64(request.GetInitialEventLimit())
			list, err := client.CoreV1().Events(request.GetNamespace()).List(ctx, options)
			if err != nil {
				return kubernetesResourceWatchResponseError(err), nil, nil
			}
			resourceVersion = list.GetResourceVersion()
			truncated = list.GetContinue() != ""
			source.initial = make([]runtime.Object, 0, len(list.Items))
			source.initialResourceVersion = resourceVersion
			for index := range list.Items {
				source.initial = append(source.initial, &list.Items[index])
			}
		}
		if request.GetFollow() {
			options.Limit = 0
			options.ResourceVersion = resourceVersion
			watcher, err := client.CoreV1().Events(request.GetNamespace()).Watch(ctx, options)
			if err != nil {
				return kubernetesResourceWatchResponseError(err), nil, nil
			}
			source.watcher = watcher
		}
		return &agentv1.ResourceWatchResponse{
			Result:                 agentv1.ResultCode_RESULT_CODE_OK,
			KubernetesStatusCode:   http.StatusOK,
			ContentType:            "application/json",
			ResourceVersion:        resourceVersion,
			InitialEventsTruncated: truncated,
		}, source, nil
	}
}

type kubernetesEventSource struct {
	initial                []runtime.Object
	initialResourceVersion string
	watcher                watch.Interface
}

func (source *kubernetesEventSource) Next(ctx context.Context) (*agentv1.ResourceWatchEvent, error) {
	if len(source.initial) > 0 {
		object := source.initial[0]
		source.initial = source.initial[1:]
		event, err := serializeResourceWatchEvent(agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED, object)
		if err == nil && source.initialResourceVersion != "" {
			event.ResourceVersion = source.initialResourceVersion
		}
		return event, err
	}
	if source.watcher == nil {
		return nil, io.EOF
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case event, open := <-source.watcher.ResultChan():
		if !open {
			return nil, &agentprotocol.ResourceWatchSourceError{
				Result:  agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
				Reason:  "WatchClosed",
				Message: "Kubernetes Event watch closed; reconnect from the last resource version",
			}
		}
		if event.Type == watch.Error {
			return nil, kubernetesResourceWatchSourceError(apierrors.FromObject(event.Object))
		}
		var eventType agentv1.ResourceWatchEventType
		switch event.Type {
		case watch.Added:
			eventType = agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED
		case watch.Modified:
			eventType = agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_MODIFIED
		case watch.Deleted:
			eventType = agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_DELETED
		case watch.Bookmark:
			eventType = agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_BOOKMARK
		default:
			return nil, errors.New("Kubernetes returned an unsupported watch event type")
		}
		return serializeResourceWatchEvent(eventType, event.Object)
	}
}

func (source *kubernetesEventSource) Close() error {
	if source != nil && source.watcher != nil {
		source.watcher.Stop()
	}
	return nil
}

func serializeResourceWatchEvent(
	eventType agentv1.ResourceWatchEventType,
	object runtime.Object,
) (*agentv1.ResourceWatchEvent, error) {
	data, err := json.Marshal(object)
	if err != nil {
		return nil, errors.New("serialize Kubernetes watch event")
	}
	resourceVersion := ""
	if accessor, err := meta.Accessor(object); err == nil {
		resourceVersion = accessor.GetResourceVersion()
	}
	return &agentv1.ResourceWatchEvent{Type: eventType, Object: data, ResourceVersion: resourceVersion}, nil
}

func kubernetesResourceWatchResponseError(err error) *agentv1.ResourceWatchResponse {
	status := apierrors.APIStatus(nil)
	if errors.As(err, &status) {
		code := status.Status().Code
		reason := string(status.Status().Reason)
		if reason == "" {
			reason = "KubernetesAPIError"
		}
		return resourceWatchResponseError(
			resourceWatchResult(code), code, reason, "Kubernetes Event request failed",
		)
	}
	return resourceWatchResponseError(
		agentv1.ResultCode_RESULT_CODE_UNAVAILABLE, http.StatusServiceUnavailable,
		"KubernetesAPIUnavailable", "Kubernetes Event request failed",
	)
}

func kubernetesResourceWatchSourceError(err error) error {
	response := kubernetesResourceWatchResponseError(err)
	return &agentprotocol.ResourceWatchSourceError{
		Result: response.GetResult(), KubernetesStatusCode: response.GetKubernetesStatusCode(),
		Reason: response.GetReason(), Message: response.GetMessage(),
	}
}

func resourceWatchResult(statusCode int32) agentv1.ResultCode {
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT
	case http.StatusUnauthorized:
		return agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED
	case http.StatusForbidden:
		return agentv1.ResultCode_RESULT_CODE_FORBIDDEN
	case http.StatusNotFound:
		return agentv1.ResultCode_RESULT_CODE_NOT_FOUND
	case http.StatusConflict, http.StatusGone:
		return agentv1.ResultCode_RESULT_CODE_CONFLICT
	case http.StatusTooManyRequests:
		return agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED
	case http.StatusGatewayTimeout:
		return agentv1.ResultCode_RESULT_CODE_TIMEOUT
	default:
		return agentv1.ResultCode_RESULT_CODE_UNAVAILABLE
	}
}

func resourceWatchResponseError(
	result agentv1.ResultCode, statusCode int32, reason string, message string,
) *agentv1.ResourceWatchResponse {
	return &agentv1.ResourceWatchResponse{
		Result: result, KubernetesStatusCode: statusCode, Reason: reason, Message: message,
	}
}
