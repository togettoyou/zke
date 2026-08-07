package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func writeSecretList(
	t *testing.T,
	writer io.Writer,
	secrets ...*corev1.Secret,
) *agentv1.ResourceResponse {
	t.Helper()
	list := corev1.SecretList{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"},
		ListMeta: metav1.ListMeta{ResourceVersion: "9"},
	}
	for _, secret := range secrets {
		list.Items = append(list.Items, *secret)
	}
	return writeKubernetesObject(t, writer, &list)
}

func platformSecret() *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "zke-agent-enrollment", Namespace: "zke-system",
			UID: types.UID("platform-uid"), ResourceVersion: "3",
			Labels: map[string]string{"app.kubernetes.io/managed-by": "zke-server"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"token": []byte("enrollment-token")},
	}
}

func workloadSecret() *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "registry", Namespace: "default",
			UID: types.UID("secret-uid"), ResourceVersion: "7",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"password": []byte("hunter2")},
	}
}

// Every Secret request has to carry the flag, because the Agent refuses one
// that does not. A path that forgot it would fail in the cluster rather than
// here, and only for Agents new enough to be looking.
func TestSecretRequestsAskForSecretAccess(t *testing.T) {
	t.Parallel()

	var verbs []agentv1.ResourceVerb
	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if !request.GetSecretAccess() {
				t.Fatalf("request without secret access: %+v", request)
			}
			verbs = append(verbs, request.GetVerb())
			if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_LIST {
				return writeSecretList(t, responseBody, workloadSecret()), nil
			}
			return writeKubernetesObject(t, responseBody, workloadSecret()), nil
		},
		mutate: func(_ context.Context, _ string, request *agentv1.ResourceRequest, _ io.Reader, responseBody io.Writer, _ string) (*agentv1.ResourceResponse, error) {
			if !request.GetSecretAccess() {
				t.Fatalf("mutation without secret access: %+v", request)
			}
			verbs = append(verbs, request.GetVerb())
			if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DELETE {
				return &agentv1.ResourceResponse{
					Result:               agentv1.ResultCode_RESULT_CODE_OK,
					KubernetesStatusCode: http.StatusOK,
				}, nil
			}
			return writeKubernetesObject(t, responseBody, workloadSecret()), nil
		},
	}
	service := NewService(requester)
	ctx := context.Background()
	if _, err := service.ListSecrets(ctx, ListSecretsInput{
		ClusterID: testClusterID, Namespace: "default", Limit: 25,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetSecret(ctx, testClusterID, "default", "registry"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSecret(ctx, CreateSecretInput{
		ClusterID: testClusterID, Namespace: "default", Name: "registry",
		Data:    map[string]string{"password": "aHVudGVyMg=="},
		Confirm: true, IdempotencyKey: "secret-create-0001",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSecret(ctx, UpdateSecretInput{
		ClusterID: testClusterID, Namespace: "default", Name: "registry",
		UID: "secret-uid", ResourceVersion: "7",
		Data:    map[string]string{"password": "aHVudGVyMw=="},
		Confirm: true, IdempotencyKey: "secret-update-0001",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteSecret(ctx, DeleteSecretInput{
		ClusterID: testClusterID, Namespace: "default", Name: "registry",
		UID: "secret-uid", ResourceVersion: "7",
		Confirm: true, IdempotencyKey: "secret-delete-0001",
	}); err != nil {
		t.Fatal(err)
	}
	if len(verbs) < 5 {
		t.Fatalf("expected every verb to reach the transport, got %v", verbs)
	}
}

// ZKE's own Secrets hold the Agent's enrollment token and the certificates it
// trusts the Server by. They are not workload configuration and this API does
// not hand them out.
func TestPlatformSecretsAreNeitherListedNorReadable(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_LIST {
				neighbour := workloadSecret()
				neighbour.Namespace = "zke-system"
				return writeSecretList(t, responseBody, platformSecret(), neighbour), nil
			}
			return writeKubernetesObject(t, responseBody, platformSecret()), nil
		},
		mutate: func(context.Context, string, *agentv1.ResourceRequest, io.Reader, io.Writer, string) (*agentv1.ResourceResponse, error) {
			t.Fatal("a platform Secret reached the mutation transport")
			return nil, nil
		},
	}
	service := NewService(requester)
	ctx := context.Background()

	page, err := service.ListSecrets(ctx, ListSecretsInput{
		ClusterID: testClusterID, Namespace: "zke-system", Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range page.Secrets {
		if secret.Name == "zke-agent-enrollment" {
			t.Fatalf("platform Secret appeared in the page: %+v", page.Secrets)
		}
	}

	if _, err := service.GetSecret(
		ctx, testClusterID, "zke-system", "zke-agent-enrollment",
	); !errors.Is(err, ErrSecretManagedByPlatform) {
		t.Fatalf("get error = %v, want managed by platform", err)
	}
	if _, err := service.UpdateSecret(ctx, UpdateSecretInput{
		ClusterID: testClusterID, Namespace: "zke-system", Name: "zke-agent-enrollment",
		UID: "platform-uid", ResourceVersion: "3", Data: map[string]string{},
		Confirm: true, IdempotencyKey: "secret-update-0002",
	}); !errors.Is(err, ErrSecretManagedByPlatform) {
		t.Fatalf("update error = %v, want managed by platform", err)
	}
	if err := service.DeleteSecret(ctx, DeleteSecretInput{
		ClusterID: testClusterID, Namespace: "zke-system", Name: "zke-agent-enrollment",
		UID: "platform-uid", ResourceVersion: "3",
		Confirm: true, IdempotencyKey: "secret-delete-0002",
	}); !errors.Is(err, ErrSecretManagedByPlatform) {
		t.Fatalf("delete error = %v, want managed by platform", err)
	}
}

// A list is a page of many objects, and no page of them needs every credential
// in a namespace.
func TestSecretListReportsKeysWithoutValues(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, _ *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			return writeSecretList(t, responseBody, workloadSecret()), nil
		},
	}
	page, err := NewService(requester).ListSecrets(context.Background(), ListSecretsInput{
		ClusterID: testClusterID, Namespace: "default", Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Secrets) != 1 {
		t.Fatalf("unexpected page: %+v", page.Secrets)
	}
	summary := page.Secrets[0]
	if len(summary.DataKeys) != 1 || summary.DataKeys[0] != "password" ||
		summary.DataBytes != int64(len("hunter2")) || summary.Type != "Opaque" {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	// Neither the value nor its encoding may appear anywhere in a list response.
	for _, forbidden := range []string{"hunter2", "aHVudGVyMg=="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Secret value leaked into the list response: %s", body)
		}
	}
}

func TestCreateSecretRejectsPlatformLabelAndTokenType(t *testing.T) {
	t.Parallel()

	base := CreateSecretInput{
		ClusterID: testClusterID, Namespace: "default", Name: "registry",
		Data: map[string]string{"password": "aHVudGVyMg=="}, Confirm: true,
	}
	labelled := base
	labelled.Labels = map[string]string{"app.kubernetes.io/managed-by": "zke-server"}
	if _, err := createSecretObject(labelled); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want invalid input for a platform label", err)
	}
	tokenTyped := base
	tokenTyped.Type = "kubernetes.io/service-account-token"
	if _, err := createSecretObject(tokenTyped); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want invalid input for a ServiceAccount token type", err)
	}
	notBase64 := base
	notBase64.Data = map[string]string{"password": "not base64"}
	if _, err := createSecretObject(notBase64); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want invalid input for a value that is not Base64", err)
	}
}

// The YAML access is the Secret service's, not a general one pointed at
// Secrets.
//
// It reaches a Secret because it goes through the same read the typed API uses,
// which is also what keeps ZKE's own Secrets out of it. Anything that is not a
// Secret is refused outright: this access exists to serve one endpoint, and an
// accessor that would fetch a Deployment if asked is one that has to be trusted
// rather than one that cannot be misused.
func TestSecretYAMLAccessStaysOnSecretsAndRefusesTheseOfZKE(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if !request.GetSecretAccess() {
				t.Fatalf("YAML read did not ask for Secret access: %+v", request)
			}
			if request.GetName() == "zke-agent-enrollment" {
				return writeKubernetesObject(t, responseBody, platformSecret()), nil
			}
			return writeKubernetesObject(t, responseBody, workloadSecret()), nil
		},
	}
	access := NewSecretYAMLAccess(NewService(requester))
	ctx := context.Background()

	object, err := access.GetResource(ctx, GetResourceInput{
		ClusterID: testClusterID, Resource: SecretResourceIdentity(),
		Namespace: "default", Name: "registry",
	})
	if err != nil || object["kind"] != "Secret" {
		t.Fatalf("object = %v, err = %v", object, err)
	}

	if _, err := access.GetResource(ctx, GetResourceInput{
		ClusterID: testClusterID, Resource: SecretResourceIdentity(),
		Namespace: "zke-system", Name: "zke-agent-enrollment",
	}); !errors.Is(err, ErrSecretManagedByPlatform) {
		t.Fatalf("platform Secret error = %v, want managed by platform", err)
	}

	if _, err := access.GetResource(ctx, GetResourceInput{
		ClusterID: testClusterID,
		Resource:  ResourceIdentity{Group: "apps", Version: "v1", Resource: "deployments"},
		Namespace: "default", Name: "api",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-Secret error = %v, want invalid input", err)
	}
	if _, err := access.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: testClusterID,
		Resource:  ResourceIdentity{Group: "apps", Version: "v1", Resource: "deployments"},
		Namespace: "default", Name: "api", Object: map[string]any{},
		Confirm: true, IdempotencyKey: "secret-yaml-0001",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-Secret update error = %v, want invalid input", err)
	}
}

// The two rules a Secret manifest has to keep that Kubernetes has no opinion
// about, plus the immutable answer the form already gives.
func TestSecretManifestGuard(t *testing.T) {
	t.Parallel()

	object := func(secret *corev1.Secret) map[string]any {
		t.Helper()
		result, err := secretObject(secret)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	immutable := workloadSecret()
	value := true
	immutable.Immutable = &value
	retyped := workloadSecret()
	retyped.Type = corev1.SecretTypeDockerConfigJson
	claimed := workloadSecret()
	claimed.Labels = map[string]string{"app.kubernetes.io/managed-by": "zke-server"}
	edited := workloadSecret()
	edited.Data = map[string][]byte{"password": []byte("hunter3")}

	testCases := []struct {
		name      string
		current   *corev1.Secret
		submitted *corev1.Secret
		want      error
	}{
		{name: "a changed value", current: workloadSecret(), submitted: edited},
		{name: "an immutable Secret", current: immutable, submitted: edited, want: ErrSecretImmutable},
		{name: "a changed type", current: workloadSecret(), submitted: retyped, want: ErrSecretTypeImmutable},
		{name: "claiming ZKE's label", current: workloadSecret(), submitted: claimed, want: ErrPlatformLabelClaimed},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// The grant is about PolicyRules handing Secret access to others,
			// which a Secret manifest cannot do; this guard ignores it.
			err := SecretManifestGuard(
				object(testCase.current), object(testCase.submitted), SecretRuleGrant{},
			)
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("guard refused a permitted change: %v", err)
				}
				return
			}
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}
