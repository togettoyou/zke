package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestKubernetesPodLogsHandlerChecksIdentityAndStreamsOptions(t *testing.T) {
	t.Parallel()

	var logQuery string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/namespaces/model-serving/pods/inference-abcde":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(corev1.Pod{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
				ObjectMeta: metav1.ObjectMeta{
					Name: "inference-abcde", Namespace: "model-serving", UID: types.UID("pod-uid"),
				},
				Spec: corev1.PodSpec{
					Containers:     []corev1.Container{{Name: "main"}},
					InitContainers: []corev1.Container{{Name: "init"}},
				},
			})
		case "/api/v1/namespaces/model-serving/pods/inference-abcde/log":
			logQuery = request.URL.RawQuery
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(writer, "first\nsecond\n")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	tail := int64(20)
	since := int64(60)
	request := &agentv1.PodLogsRequest{
		Namespace:    "model-serving",
		PodName:      "inference-abcde",
		PodUid:       "pod-uid",
		Container:    "main",
		Follow:       true,
		Previous:     true,
		TailLines:    &tail,
		SinceSeconds: &since,
		Timestamps:   true,
		MaxBytes:     4096,
	}
	response, stream, err := newKubernetesPodLogsHandler(client)(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK || stream == nil {
		t.Fatalf("unexpected Pod logs response: %+v", response)
	}
	body, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "first\nsecond\n" {
		t.Fatalf("Pod logs = %q", body)
	}
	for _, expected := range []string{
		"container=main", "follow=true", "previous=true", "tailLines=20",
		"sinceSeconds=60", "timestamps=true", "limitBytes=4096",
	} {
		if !queryContains(logQuery, expected) {
			t.Errorf("Pod logs query %q is missing %q", logQuery, expected)
		}
	}
}

func TestKubernetesPodLogsHandlerRejectsReplacedPodAndUnknownContainer(t *testing.T) {
	t.Parallel()

	logRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/namespaces/default/pods/example/log" {
			logRequests++
			_, _ = io.WriteString(writer, "must not be read")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(corev1.Pod{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "example", Namespace: "default", UID: types.UID("current-uid"),
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
		})
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	handler := newKubernetesPodLogsHandler(client)

	response, stream, err := handler(context.Background(), &agentv1.PodLogsRequest{
		Namespace: "default", PodName: "example", PodUid: "stale-uid",
		Container: "main", MaxBytes: 1024,
	})
	if err != nil || stream != nil || response.GetReason() != "PodUIDMismatch" ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_CONFLICT {
		t.Fatalf("unexpected replaced Pod response=%+v stream=%v err=%v", response, stream, err)
	}
	response, stream, err = handler(context.Background(), &agentv1.PodLogsRequest{
		Namespace: "default", PodName: "example", PodUid: "current-uid",
		Container: "missing", MaxBytes: 1024,
	})
	if err != nil || stream != nil || response.GetReason() != "ContainerNotFound" ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT {
		t.Fatalf("unexpected container response=%+v stream=%v err=%v", response, stream, err)
	}
	if logRequests != 0 {
		t.Fatalf("unsafe Pod log requests = %d", logRequests)
	}
}

func TestKubernetesPodLogsHandlerRechecksUIDAfterOpeningStream(t *testing.T) {
	t.Parallel()

	getCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/log") {
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(writer, "new-pod-secret\n")
			return
		}
		getCount++
		uid := "pod-uid"
		if getCount > 1 {
			uid = "replacement-uid"
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(corev1.Pod{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "example", Namespace: "default", UID: types.UID(uid),
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
		})
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	response, stream, err := newKubernetesPodLogsHandler(client)(
		context.Background(),
		&agentv1.PodLogsRequest{
			Namespace: "default", PodName: "example", PodUid: "pod-uid",
			Container: "main", MaxBytes: 1024,
		},
	)
	if err != nil || stream != nil || response.GetReason() != "PodUIDMismatch" ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_CONFLICT {
		t.Fatalf("response=%+v stream=%v err=%v", response, stream, err)
	}
	if getCount != 2 {
		t.Fatalf("Pod identity GET count = %d, want 2", getCount)
	}
}

func queryContains(raw string, expected string) bool {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return false
	}
	parts := strings.SplitN(expected, "=", 2)
	return len(parts) == 2 && values.Get(parts[0]) == parts[1]
}
