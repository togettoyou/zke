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

/*
 * The guard that makes the YAML editor an editor rather than a way around the
 * typed API.
 *
 * Every case below is a write the form refuses. A document can express all of
 * them, so the same answer has to be given here — otherwise `cluster.rbac.manage`
 * buys the ability to grant `escalate` to anyone, which is the permission that
 * grants every other one.
 */
func TestAuthorizationManifestGuardRefusesWhatTheTypedAPIRefuses(t *testing.T) {
	t.Parallel()

	object := func(value any) map[string]any {
		t.Helper()
		result, err := runtime.DefaultUnstructuredConverter.ToUnstructured(value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	clusterRole := func(rules []rbacv1.PolicyRule, labels map[string]string) map[string]any {
		return object(&rbacv1.ClusterRole{
			TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
			ObjectMeta: metav1.ObjectMeta{Name: "editor", Labels: labels},
			Rules:      rules,
		})
	}
	readPods := []rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"pods"}}}
	binding := func(roleName string) map[string]any {
		return object(&rbacv1.ClusterRoleBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: "editors"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
			Subjects:   []rbacv1.Subject{{APIGroup: "rbac.authorization.k8s.io", Kind: "Group", Name: "developers"}},
		})
	}

	testCases := []struct {
		name      string
		current   map[string]any
		submitted map[string]any
		want      error
	}{
		{
			name:      "rules the form would submit",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole([]rbacv1.PolicyRule{{Verbs: []string{"get", "list"}, APIGroups: []string{""}, Resources: []string{"pods", "configmaps"}}}, nil),
		},
		{
			name:      "escalate",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole([]rbacv1.PolicyRule{{Verbs: []string{"escalate"}, APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"clusterroles"}}}, nil),
			want:      ErrInvalidInput,
		},
		{
			name:      "reading Secrets through a role",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole([]rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"secrets"}}}, nil),
			want:      ErrInvalidInput,
		},
		{
			name:      "minting a ServiceAccount token",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole([]rbacv1.PolicyRule{{Verbs: []string{"create"}, APIGroups: []string{""}, Resources: []string{"serviceaccounts/token"}}}, nil),
			want:      ErrInvalidInput,
		},
		{
			name:      "one of ZKE's own objects",
			current:   clusterRole(readPods, map[string]string{"app.kubernetes.io/managed-by": "zke-server"}),
			submitted: clusterRole(readPods, map[string]string{"app.kubernetes.io/managed-by": "zke-server"}),
			want:      ErrManagedResource,
		},
		{
			name:      "claiming ZKE's label",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole(readPods, map[string]string{"app.kubernetes.io/managed-by": "zke-server"}),
			want:      ErrPlatformLabelClaimed,
		},
		{
			name:      "repointing a binding at another role",
			current:   binding("viewer"),
			submitted: binding("cluster-admin"),
			want:      ErrRoleRefImmutable,
		},
		{
			name:      "a binding that changes only its subjects",
			current:   binding("viewer"),
			submitted: binding("viewer"),
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := AuthorizationManifestGuard(testCase.current, testCase.submitted)
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

/*
 * The names a cluster actually holds.
 *
 * Kubernetes validates RBAC names as path segments, so the whole `system:`
 * family and anything kubeadm installs carries a colon. Validating them as DNS
 * subdomains refused every one of them: the list rendered, and opening any row
 * answered `400 invalid_request` — the objects an operator is most likely to
 * open, and the ones nobody can rename.
 */
func TestAuthorizationNamesFollowKubernetesRules(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"system:basic-user",
		"system:aggregate-to-edit",
		"kubeadm:cluster-admins",
		"system:controller:deployment-controller",
		"cluster-admin",
	} {
		if !validAuthorizationName(AuthorizationClusterRoles, name) {
			t.Fatalf("%q was refused as a ClusterRole name", name)
		}
		if !validAuthorizationName(AuthorizationClusterRoleBindings, name) {
			t.Fatalf("%q was refused as a ClusterRoleBinding name", name)
		}
		// A binding may point at any of them, so the RoleRef rule is the same
		// rule; requiring a DNS name there refused binding to a built-in role.
		if !validAuthorizationRoleRef(AuthorizationClusterRoleBindings, &AuthorizationRoleRef{
			APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: name,
		}) {
			t.Fatalf("%q was refused as a RoleRef", name)
		}
	}

	// ServiceAccounts are core/v1 objects and really are DNS subdomains, so the
	// looser rule must not spread to them.
	if validAuthorizationName(AuthorizationServiceAccounts, "system:basic-user") {
		t.Fatal("a colon was accepted in a ServiceAccount name")
	}
	for _, name := range []string{"", ".", "..", "a/b", "a%2Fb", " padded", "padded "} {
		if validAuthorizationName(AuthorizationClusterRoles, name) {
			t.Fatalf("%q was accepted as a ClusterRole name", name)
		}
	}
}
