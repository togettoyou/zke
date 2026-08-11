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

// A Role granting Secret access answers to what the caller holds.
//
// Without this, `cluster.rbac.manage` would be a way around every Secret
// permission in the platform: write a Role granting `get` on `secrets`, bind it
// to a ServiceAccount you control, read the credential out of the workload. The
// separation between reading configuration and reading credentials would survive
// only until somebody wrote a Role.
func TestSecretRulesRequireTheCallersOwnSecretPermission(t *testing.T) {
	t.Parallel()

	role := func(grant SecretRuleGrant, verbs ...string) error {
		_, err := createAuthorizationResourceObject(CreateAuthorizationResourceInput{
			Resource: AuthorizationRoles, Namespace: "default", Name: "app",
			Rules: []AuthorizationPolicyRule{{
				Verbs: verbs, APIGroups: []string{""}, Resources: []string{"secrets"},
			}},
			SecretGrant: grant,
		})
		return err
	}

	if err := role(SecretRuleGrant{}, "get"); !errors.Is(err, ErrSecretRuleForbidden) {
		t.Fatalf("no grant: error = %v, want ErrSecretRuleForbidden", err)
	}
	if err := role(SecretRuleGrant{Read: true}, "get", "list", "watch"); err != nil {
		t.Fatalf("read grant refused a read-only rule: %v", err)
	}
	if err := role(SecretRuleGrant{Read: true}, "create"); !errors.Is(err, ErrSecretRuleForbidden) {
		t.Fatalf("read grant accepted a write rule: %v", err)
	}
	if err := role(SecretRuleGrant{Read: true, Manage: true}, "create", "delete"); err != nil {
		t.Fatalf("manage grant refused a write rule: %v", err)
	}
	// The three unconditional refusals are unaffected by any grant.
	if err := role(SecretRuleGrant{Read: true, Manage: true}, "escalate"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("escalate accepted with a grant: %v", err)
	}
}

// Adding a subject to a binding that already points at the Agent's ClusterRole.
//
// Creation refuses that RoleRef; updating used not to look at it, and the two
// are the same handover. A binding created outside ZKE carries none of ZKE's
// labels, so nothing else was in the way.
func TestUpdatingABindingChecksTheRoleRefItKeeps(t *testing.T) {
	t.Parallel()

	existing, err := runtime.DefaultUnstructuredConverter.ToUnstructured(
		&rbacv1.ClusterRoleBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: "borrowed"},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "zke-agent",
			},
			Subjects: []rbacv1.Subject{{
				APIGroup: "rbac.authorization.k8s.io", Kind: "Group", Name: "developers",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = updateAuthorizationResourceObject(existing, UpdateAuthorizationResourceInput{
		Resource: AuthorizationClusterRoleBindings, Name: "borrowed",
		Subjects: []AuthorizationSubject{
			{APIGroup: "rbac.authorization.k8s.io", Kind: "User", Name: "attacker"},
		},
	})
	if !errors.Is(err, ErrRoleRefForbidden) {
		t.Fatalf("error = %v, want ErrRoleRefForbidden", err)
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

func TestDeleteAuthorizationResourceProtectsClusterScopedZKEManagedObjects(t *testing.T) {
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
			t.Fatal("managed cluster resource reached mutation transport")
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

// The guard that makes the YAML editor an editor rather than a way around the
// typed API.
//
// Every case below is a write the form refuses. A document can express all of
// them, so the same answer has to be given here — otherwise `cluster.rbac.manage`
// buys the ability to grant `escalate` to anyone, which is the permission that
// grants every other one.
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

	secretRule := func(verbs ...string) []rbacv1.PolicyRule {
		return []rbacv1.PolicyRule{
			{Verbs: verbs, APIGroups: []string{""}, Resources: []string{"secrets"}},
		}
	}

	testCases := []struct {
		name      string
		current   map[string]any
		submitted map[string]any
		grant     SecretRuleGrant
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
		// A rule about Secrets hands Secret access to whoever is bound to the
		// role, so it answers to what the caller holds. These six cases are the
		// whole rule: nothing without the permission, reading with the read
		// permission, writing only with manage, and a wildcard resource counted
		// as naming Secrets.
		{
			name:      "reading Secrets without holding the permission",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole(secretRule("get"), nil),
			want:      ErrSecretRuleForbidden,
		},
		{
			name:      "reading Secrets while holding cluster.secret.read",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole(secretRule("get", "list", "watch"), nil),
			grant:     SecretRuleGrant{Read: true},
		},
		{
			name:      "writing Secrets while holding only cluster.secret.read",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole(secretRule("get", "update"), nil),
			grant:     SecretRuleGrant{Read: true},
			want:      ErrSecretRuleForbidden,
		},
		{
			name:      "writing Secrets while holding cluster.secret.manage",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole(secretRule("get", "update", "delete"), nil),
			grant:     SecretRuleGrant{Read: true, Manage: true},
		},
		{
			name:      "a wildcard verb is not read-only",
			current:   clusterRole(readPods, nil),
			submitted: clusterRole(secretRule("*"), nil),
			grant:     SecretRuleGrant{Read: true},
			want:      ErrSecretRuleForbidden,
		},
		{
			name:    "a wildcard resource covers Secrets",
			current: clusterRole(readPods, nil),
			submitted: clusterRole([]rbacv1.PolicyRule{
				{Verbs: []string{"get"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
			}, nil),
			want: ErrSecretRuleForbidden,
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
		{
			// Kubernetes would allow this one: the Agent holds every permission
			// in its own ClusterRole, so escalation prevention is satisfied.
			// This check is the only thing in the way.
			name:      "a binding pointing at the Agent's own ClusterRole",
			current:   binding("zke-agent"),
			submitted: binding("zke-agent"),
			want:      ErrInvalidInput,
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := AuthorizationManifestGuard(
				testCase.current, testCase.submitted, testCase.grant,
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

// The names a cluster actually holds.
//
// Kubernetes validates RBAC names as path segments, so the whole `system:`
// family and anything kubeadm installs carries a colon. Validating them as DNS
// subdomains refused every one of them: the list rendered, and opening any row
// answered `400 invalid_request` — the objects an operator is most likely to
// open, and the ones nobody can rename.
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
