package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/podlogs"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	testClusterID = "336045d5-d509-4ea4-9aa0-ffc606c57ad6"
	testUserID    = "8f14e45f-ceea-467a-9f0b-1c0ea1f7c2d1"
)

func TestDecodeRefusesFieldsTheSchemaDoesNotDeclare(t *testing.T) {
	t.Parallel()
	var arguments listResourcesArguments
	err := decode(json.RawMessage(`{"api_version":"v1","kind":"Pod","all_namespaces":true}`), &arguments)
	if err == nil {
		t.Fatalf("decode() accepted an undeclared field: %+v", arguments)
	}
	// The point of refusing rather than ignoring: a dropped scope field turns a
	// narrow read into a Cluster-wide one and the model never learns it asked
	// the wrong question.
	if !strings.Contains(err.Error(), "Schema") {
		t.Fatalf("decode() error = %v", err)
	}
}

func TestDecodeAcceptsTheDeclaredShape(t *testing.T) {
	t.Parallel()
	var arguments listResourcesArguments
	if err := decode(
		json.RawMessage(`{"api_version":"apps/v1","kind":"Deployment","namespace":"web","limit":10}`),
		&arguments,
	); err != nil {
		t.Fatalf("decode() error = %v", err)
	}
	if arguments.Kind != "Deployment" || arguments.Namespace != "web" || arguments.Limit != 10 {
		t.Fatalf("decode() = %+v", arguments)
	}
}

// A listing that repeated every healthy condition of every object would spend
// the context window on the word "True".
func TestSummarizeKeepsIdentityAndProblemsAndDropsHealthyConditions(t *testing.T) {
	t.Parallel()
	object := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name": "web-0", "namespace": "default",
			"labels": map[string]any{"app": "web"},
		},
		"spec": map[string]any{"nodeName": "node-1"},
		"status": map[string]any{
			"phase": "Running",
			"conditions": []any{
				map[string]any{"type": "Initialized", "status": "True"},
				map[string]any{"type": "Ready", "status": "False", "reason": "ContainersNotReady"},
				map[string]any{"type": "MemoryPressure", "status": "False"},
			},
			"containerStatuses": []any{
				map[string]any{
					"name": "web", "ready": false, "restartCount": int64(7),
					"state": map[string]any{
						"waiting": map[string]any{"reason": "CrashLoopBackOff"},
					},
				},
			},
		},
	}}

	summary := summarize(object)

	if summary["name"] != "web-0" || summary["namespace"] != "default" ||
		summary["phase"] != "Running" || summary["nodeName"] != "node-1" {
		t.Fatalf("summarize() = %+v", summary)
	}
	conditions, _ := summary["conditions"].([]map[string]any)
	if len(conditions) != 1 || conditions[0]["type"] != "Ready" {
		t.Fatalf("summarize() conditions = %+v", summary["conditions"])
	}
	containers, _ := summary["containers"].([]map[string]any)
	if len(containers) != 1 || containers[0]["reason"] != "CrashLoopBackOff" ||
		containers[0]["restarts"] != int64(7) {
		t.Fatalf("summarize() containers = %+v", summary["containers"])
	}
}

// An oversized answer keeps both ends. Which half matters depends on the tool —
// the head of a listing says what shape the answer has, and a container that
// crashed says why at the end of its log — so cutting only the front is how a
// model ends up reasoning about a startup banner.
func TestEncodeKeepsBothEndsAndSaysWhatItRemoved(t *testing.T) {
	t.Parallel()
	catalogue := New(Dependencies{}, Config{})
	items := make([]string, 4_000)
	for index := range items {
		items[index] = "a-fairly-long-object-name-that-repeats"
	}
	items[0] = "first-object-in-the-answer"
	items[len(items)-1] = "last-object-in-the-answer"

	text := catalogue.encode(items)

	if length := len([]rune(text)); length > DefaultResultThresholdRunes {
		t.Fatalf("encode() length = %d, want it inside the bound", length)
	}
	if !strings.Contains(text, "first-object-in-the-answer") {
		t.Fatal("encode() dropped the head of the answer")
	}
	if !strings.Contains(text, "last-object-in-the-answer") {
		t.Fatal("encode() dropped the tail of the answer")
	}
	// A model told the list was cut asks a narrower question; one that is not
	// concludes from a partial answer it believes is whole.
	if !strings.Contains(text, "已省略") {
		t.Fatalf("encode() did not announce what it removed: %q", text)
	}
}

// A result already inside the budget is returned exactly as it was rendered.
func TestEncodeLeavesAnAnswerInsideTheBoundAlone(t *testing.T) {
	t.Parallel()
	catalogue := New(Dependencies{}, Config{})

	text := catalogue.encode([]string{"one", "two"})

	if strings.Contains(text, "已省略") {
		t.Fatalf("encode() pruned a short answer: %q", text)
	}
}

// The end of a log is where a crash explains itself; a head-bounded reader
// would return the startup banner every time.
func TestTailBufferKeepsTheEndOnALineBoundary(t *testing.T) {
	t.Parallel()
	sink := &tailBuffer{limit: 40}
	for index := 0; index < 200; index++ {
		if _, err := sink.Write([]byte("line-of-container-output\n")); err != nil {
			t.Fatal(err)
		}
	}

	text := sink.String()

	if len(text) > 40 {
		t.Fatalf("tail length = %d, want at most the limit", len(text))
	}
	if !strings.HasPrefix(text, "line-of-container-output") {
		t.Fatalf("tail did not start at a line boundary: %q", text)
	}
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("tail = %q", text)
	}
}

func TestTailBufferReportsAnEmptyLogPlainly(t *testing.T) {
	t.Parallel()
	sink := &tailBuffer{limit: 40}
	if sink.String() != "(容器没有输出日志)" {
		t.Fatalf("empty tail = %q", sink.String())
	}
}

func TestStripNoiseRemovesTheLastAppliedAnnotation(t *testing.T) {
	t.Parallel()
	object := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": "settings",
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": "{...}",
				"owner": "platform",
			},
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
		},
	}}

	stripNoise(&object)

	annotations := object.GetAnnotations()
	if _, present := annotations["kubectl.kubernetes.io/last-applied-configuration"]; present {
		t.Fatalf("annotations = %+v", annotations)
	}
	if annotations["owner"] != "platform" {
		t.Fatalf("stripNoise() removed an annotation somebody wrote: %+v", annotations)
	}
	if _, _, err := unstructured.NestedSlice(object.Object, "metadata", "managedFields"); err == nil {
		if fields, present, _ := unstructured.NestedSlice(object.Object, "metadata", "managedFields"); present {
			t.Fatalf("managedFields survived: %+v", fields)
		}
	}
}

func TestSpecsDropToolsWhoseDependencyIsAbsent(t *testing.T) {
	t.Parallel()
	// A deployment without multi-Cluster metrics should not be told about
	// metric tools: a model plans against the list it is given, and an
	// advertised tool that always fails wastes a step every time.
	catalogue := New(Dependencies{}, Config{})

	if len(catalogue.Specs()) != 0 {
		t.Fatalf("Specs() = %+v, want an empty catalogue", catalogue.Specs())
	}
}

// The log stream refuses a read that does not identify the Pod instance, and
// the model has no UID to give it. Resolving the Pod first is what makes the
// tool work at all; asserting it here is what keeps it working.
type stubResources struct {
	pod map[string]any
	got kubernetesresource.GetResourceInput
}

func (stub *stubResources) DiscoverResources(
	context.Context, string,
) (kubernetescatalog.Catalog, error) {
	return kubernetescatalog.Catalog{Resources: []kubernetescatalog.Resource{
		{Group: "", Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true},
	}}, nil
}

func (stub *stubResources) GetResource(
	_ context.Context, input kubernetesresource.GetResourceInput,
) (map[string]any, error) {
	stub.got = input
	return stub.pod, nil
}

func (stub *stubResources) ListResources(
	context.Context, kubernetesresource.ListResourcesInput,
) (kubernetesresource.ResourcePage, error) {
	return kubernetesresource.ResourcePage{}, nil
}

func (stub *stubResources) ListNodes(
	context.Context, kubernetesresource.ListNodesInput,
) (kubernetesresource.NodePage, error) {
	return kubernetesresource.NodePage{}, nil
}

type stubLogs struct{ got podlogs.Input }

func (stub *stubLogs) Stream(
	_ context.Context, input podlogs.Input, destination io.Writer,
) (podlogs.Result, error) {
	stub.got = input
	_, _ = destination.Write([]byte("2026-08-21T08:00:00Z 启动完成\n"))
	return podlogs.Result{}, nil
}

func podObject(containers ...string) map[string]any {
	specContainers := make([]any, 0, len(containers))
	for _, name := range containers {
		specContainers = append(specContainers, map[string]any{"name": name})
	}
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": "web-0", "namespace": "default",
			"uid": "29fdc7f8-08ad-4e00-af97-2d46bcb73cf3",
		},
		"spec": map[string]any{"containers": specContainers},
	}
}

func TestPodLogsResolvesThePodIdentityAndItsOnlyContainer(t *testing.T) {
	t.Parallel()
	resources := &stubResources{pod: podObject("app")}
	logs := &stubLogs{}
	catalogue := New(Dependencies{Resources: resources, Logs: logs}, Config{})

	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPodLogs, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"namespace":"default","pod":"web-0"}`),
	})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if logs.got.PodUID != "29fdc7f8-08ad-4e00-af97-2d46bcb73cf3" || logs.got.Container != "app" {
		t.Fatalf("log stream input = %+v", logs.got)
	}
	if !strings.Contains(result.Text, "启动完成") {
		t.Fatalf("result = %q", result.Text)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Container != "app" {
		t.Fatalf("evidence = %+v", result.Evidence)
	}
}

// Picking one of several containers would answer a question about one workload
// with the log of another, so the model is told to name it instead.
func TestPodLogsAsksWhichContainerWhenThereAreSeveral(t *testing.T) {
	t.Parallel()
	logs := &stubLogs{}
	catalogue := New(Dependencies{
		Resources: &stubResources{pod: podObject("app", "sidecar")}, Logs: logs,
	}, Config{})

	_, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPodLogs, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"namespace":"default","pod":"web-0"}`),
	})
	if !errors.Is(err, airuntime.ErrInvalidInput) || !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("Invoke() error = %v, want the container names", err)
	}
	if logs.got.PodName != "" {
		t.Fatalf("an ambiguous read reached the cluster: %+v", logs.got)
	}
}
