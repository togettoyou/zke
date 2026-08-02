package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestCreateAuthorizationObjects(t *testing.T) {
	t.Parallel()

	roleObject, err := createAuthorizationResourceObject(CreateAuthorizationResourceInput{
		Resource: AuthorizationRoles, Namespace: "default", Name: "pod-reader",
		Rules: []AuthorizationPolicyRule{{Verbs: []string{"get", "list"}, APIGroups: []string{""}, Resources: []string{"pods"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.Role
	if runtime.DefaultUnstructuredConverter.FromUnstructured(roleObject, &role) != nil || len(role.Rules) != 1 || role.Rules[0].Resources[0] != "pods" {
		t.Fatalf("unexpected Role: %+v", role)
	}

	bindingObject, err := createAuthorizationResourceObject(CreateAuthorizationResourceInput{
		Resource: AuthorizationClusterRoleBindings, Name: "pod-readers",
		RoleRef:  &AuthorizationRoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "pod-reader"},
		Subjects: []AuthorizationSubject{{APIGroup: "rbac.authorization.k8s.io", Kind: "Group", Name: "developers"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var binding rbacv1.ClusterRoleBinding
	if runtime.DefaultUnstructuredConverter.FromUnstructured(bindingObject, &binding) != nil || binding.RoleRef.Name != "pod-reader" || len(binding.Subjects) != 1 {
		t.Fatalf("unexpected ClusterRoleBinding: %+v", binding)
	}
}

func TestAuthorizationValidationRejectsPrivilegeShapeErrors(t *testing.T) {
	t.Parallel()

	tests := []CreateAuthorizationResourceInput{
		{Resource: AuthorizationRoles, Namespace: "default", Name: "bad", Rules: []AuthorizationPolicyRule{{Verbs: []string{"get"}, NonResourceURLs: []string{"/healthz"}}}},
		{Resource: AuthorizationRoles, Namespace: "default", Name: "bad", Rules: []AuthorizationPolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"secrets"}}}},
		{Resource: AuthorizationClusterRoles, Name: "bad", Rules: []AuthorizationPolicyRule{{Verbs: []string{"impersonate"}, APIGroups: []string{""}, Resources: []string{"users"}}}},
		{Resource: AuthorizationClusterRoleBindings, Name: "bad", RoleRef: &AuthorizationRoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader"}, Subjects: []AuthorizationSubject{{Kind: "ServiceAccount", Name: "app", Namespace: "default"}}},
		{Resource: AuthorizationClusterRoleBindings, Name: "bad", RoleRef: &AuthorizationRoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "zke-agent"}, Subjects: []AuthorizationSubject{{APIGroup: "rbac.authorization.k8s.io", Kind: "Group", Name: "developers"}}},
		{Resource: AuthorizationRoleBindings, Namespace: "default", Name: "bad", RoleRef: &AuthorizationRoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader"}, Subjects: []AuthorizationSubject{{APIGroup: "rbac.authorization.k8s.io", Kind: "ServiceAccount", Name: "app", Namespace: "default"}}},
	}
	for index, input := range tests {
		if _, err := createAuthorizationResourceObject(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
}

func TestAuthorizationDetailDoesNotExposeServiceAccountSecretNames(t *testing.T) {
	t.Parallel()

	serviceAccount := &corev1.ServiceAccount{
		TypeMeta:         metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta:       metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Secrets:          []corev1.ObjectReference{{Name: "legacy-token"}},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-credential"}},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(serviceAccount)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := authorizationResourceDetail(object, AuthorizationServiceAccounts, "default", "app")
	if err != nil {
		t.Fatal(err)
	}
	if detail.SecretReferenceCount != 2 || len(detail.Rules) != 0 || len(detail.Subjects) != 0 {
		t.Fatalf("unexpected detail: %+v", detail)
	}
}

func TestDeleteAuthorizationResourceProtectsZKEManagedObjects(t *testing.T) {
	t.Parallel()

	role := &rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
		ObjectMeta: metav1.ObjectMeta{Name: "zke-agent", UID: types.UID("role-uid"), ResourceVersion: "8", Labels: map[string]string{"app.kubernetes.io/managed-by": "zke-server"}},
	}
	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET {
				t.Fatalf("unexpected request: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, role), nil
		},
		mutate: func(context.Context, string, *agentv1.ResourceRequest, io.Reader, io.Writer, string) (*agentv1.ResourceResponse, error) {
			t.Fatal("managed resource reached mutation transport")
			return nil, nil
		},
	}
	err := NewService(requester).DeleteAuthorizationResource(context.Background(), DeleteAuthorizationResourceInput{
		ClusterID: testClusterID, Resource: AuthorizationClusterRoles, Name: "zke-agent", UID: "role-uid", ResourceVersion: "8", Confirm: true, IdempotencyKey: "authorization-delete-0001",
	})
	if !errors.Is(err, ErrManagedResource) {
		t.Fatalf("error = %v, want ErrManagedResource", err)
	}
}
