package kubernetesresource

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestServiceListsAndGetsNamespaces(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, time.July, 30, 8, 0, 0, 0, time.UTC)
	namespace := corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              "model-serving",
			UID:               "namespace-uid",
			ResourceVersion:   "42",
			CreationTimestamp: metav1.NewTime(created),
			Labels:            map[string]string{"team": "inference"},
			Annotations:       map[string]string{"owner": "zke"},
			Finalizers:        []string{"kubernetes"},
		},
		Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}
	remaining := int64(1)
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		clusterID string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if clusterID != testClusterID ||
			request.GetResource().GetResource() != "namespaces" ||
			request.GetResource().GetVersion() != "v1" ||
			request.GetNamespace() != "" {
			t.Fatalf("unexpected Namespace request: cluster=%q request=%+v", clusterID, request)
		}
		if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_GET {
			if request.GetName() != namespace.Name {
				t.Fatalf("get name = %q", request.GetName())
			}
			return writeKubernetesObject(t, responseBody, &namespace), nil
		}
		list := corev1.NamespaceList{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NamespaceList"},
			ListMeta: metav1.ListMeta{
				ResourceVersion:    "43",
				Continue:           "next",
				RemainingItemCount: &remaining,
			},
			Items: []corev1.Namespace{namespace},
		}
		return writeKubernetesObject(t, responseBody, &list), nil
	}}
	service := NewService(requester)

	page, err := service.ListNamespaces(context.Background(), ListNamespacesInput{
		ClusterID:     testClusterID,
		Limit:         25,
		ContinueToken: "current",
		LabelSelector: "team=inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Namespaces) != 1 ||
		page.Namespaces[0].Name != namespace.Name ||
		page.Namespaces[0].Phase != string(corev1.NamespaceActive) ||
		page.ContinueToken != "next" ||
		page.RemainingItemCount == nil ||
		*page.RemainingItemCount != 1 {
		t.Fatalf("unexpected Namespace page: %+v", page)
	}

	detail, err := service.GetNamespace(
		context.Background(),
		testClusterID,
		namespace.Name,
	)
	if err != nil {
		t.Fatal(err)
	}
	if detail.UID != "namespace-uid" ||
		detail.Labels["team"] != "inference" ||
		detail.Annotations["owner"] != "zke" ||
		len(detail.Finalizers) != 1 {
		t.Fatalf("unexpected Namespace detail: %+v", detail)
	}
}

func TestServiceCreatesAndDeletesNamespaceWithSafetyOptions(t *testing.T) {
	t.Parallel()

	const key = "0123456789abcdef"
	calls := 0
	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("Namespace mutation used read-only transport")
			return nil, nil
		},
		mutate: func(
			_ context.Context,
			clusterID string,
			request *agentv1.ResourceRequest,
			requestBody io.Reader,
			responseBody io.Writer,
			idempotencyKey string,
		) (*agentv1.ResourceResponse, error) {
			calls++
			if clusterID != testClusterID ||
				idempotencyKey != key ||
				request.GetResource().GetResource() != "namespaces" ||
				request.GetNamespace() != "" {
				t.Fatalf("unexpected Namespace mutation: cluster=%q request=%+v", clusterID, request)
			}
			if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DELETE {
				preconditions := request.GetDeleteOptions().GetPreconditions()
				if !request.GetDeleteOptions().GetDryRun() ||
					request.GetName() != "model-serving" ||
					preconditions.GetUid() != "namespace-uid" ||
					preconditions.GetResourceVersion() != "42" {
					t.Fatalf("unexpected Namespace delete: %+v", request)
				}
				return &agentv1.ResourceResponse{
					Result:               agentv1.ResultCode_RESULT_CODE_OK,
					KubernetesStatusCode: 200,
				}, nil
			}
			body, err := io.ReadAll(requestBody)
			if err != nil {
				t.Fatal(err)
			}
			var namespace corev1.Namespace
			if err := json.Unmarshal(body, &namespace); err != nil {
				t.Fatal(err)
			}
			if !request.GetMutationOptions().GetDryRun() ||
				namespace.Name != "model-serving" ||
				namespace.Labels["team"] != "inference" {
				t.Fatalf("unexpected Namespace create: request=%+v object=%+v", request, namespace)
			}
			namespace.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}
			namespace.UID = "namespace-uid"
			namespace.ResourceVersion = "42"
			namespace.Status.Phase = corev1.NamespaceActive
			return writeKubernetesObject(t, responseBody, &namespace), nil
		},
	}
	service := NewService(requester)

	created, err := service.CreateNamespace(context.Background(), CreateNamespaceInput{
		ClusterID:      testClusterID,
		Name:           "model-serving",
		Labels:         map[string]string{"team": "inference"},
		DryRun:         true,
		IdempotencyKey: key,
	})
	if err != nil || created.Name != "model-serving" {
		t.Fatalf("CreateNamespace() detail=%+v err=%v", created, err)
	}
	err = service.DeleteNamespace(context.Background(), DeleteNamespaceInput{
		ClusterID:       testClusterID,
		Name:            "model-serving",
		UID:             "namespace-uid",
		ResourceVersion: "42",
		DryRun:          true,
		IdempotencyKey:  key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("mutation calls = %d, want 2", calls)
	}
}

func TestServiceRejectsInvalidNamespaceNameBeforeTransport(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("invalid Namespace reached transport")
			return nil, nil
		},
		mutate: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Reader,
			io.Writer,
			string,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("invalid Namespace reached transport")
			return nil, nil
		},
	}
	_, err := NewService(requester).CreateNamespace(
		context.Background(),
		CreateNamespaceInput{ClusterID: testClusterID, Name: "Invalid_Name"},
	)
	if err != ErrInvalidInput {
		t.Fatalf("CreateNamespace() error = %v, want %v", err, ErrInvalidInput)
	}
	_, err = NewService(requester).CreateNamespace(
		context.Background(),
		CreateNamespaceInput{
			ClusterID: testClusterID,
			Name:      "valid-name",
			Labels:    map[string]string{"invalid key": "value"},
		},
	)
	if err != ErrInvalidInput {
		t.Fatalf("CreateNamespace() label error = %v, want %v", err, ErrInvalidInput)
	}
}
