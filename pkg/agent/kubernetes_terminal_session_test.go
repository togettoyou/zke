package agent

import (
	"context"
	"slices"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestTerminalPolicyKeepsSecretAndRBACPermissionsIndependent(t *testing.T) {
	tests := []struct {
		name        string
		permissions []string
		secretVerbs []string
		rbacVerbs   []string
	}{
		{name: "generic resource permissions do not reach protected families",
			permissions: []string{"cluster.read", "cluster.resource.create", "cluster.resource.update", "cluster.resource.delete"}},
		{name: "Secret read is read only", permissions: []string{"cluster.secret.read"}, secretVerbs: []string{"get", "list", "watch"}},
		{name: "Secret manage does not imply read", permissions: []string{"cluster.secret.manage"}, secretVerbs: []string{"create", "update", "patch", "delete"}},
		{name: "RBAC read is read only", permissions: []string{"cluster.rbac.read"}, rbacVerbs: []string{"get", "list", "watch"}},
		{name: "RBAC manage does not add bind or escalate", permissions: []string{"cluster.rbac.manage"}, rbacVerbs: []string{"create", "update", "delete"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rules := terminalPolicyRules(test.permissions)
			assertTerminalVerbs(t, rules, "", "secrets", test.secretVerbs)
			assertTerminalVerbs(t, rules, "rbac.authorization.k8s.io", "roles", test.rbacVerbs)
			for _, rule := range rules {
				if slices.Contains(rule.Verbs, "bind") || slices.Contains(rule.Verbs, "escalate") || slices.Contains(rule.Verbs, "impersonate") {
					t.Fatalf("terminal policy contains privilege escalation verb: %+v", rule)
				}
			}
		})
	}
}

func TestTerminalClusterPolicyKeepsRBACIndependent(t *testing.T) {
	tests := []struct {
		name        string
		permissions []string
		want        []string
	}{
		{name: "generic resource permissions do not reach cluster RBAC",
			permissions: []string{"cluster.read", "cluster.resource.create", "cluster.resource.update", "cluster.resource.delete"}},
		{name: "cluster RBAC read is read only", permissions: []string{"cluster.rbac.read"}, want: []string{"get", "list", "watch"}},
		{name: "cluster RBAC manage cannot bind or escalate", permissions: []string{"cluster.rbac.manage"}, want: []string{"create"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rules := terminalClusterPolicyRules(test.permissions)
			assertTerminalVerbs(t, rules, "rbac.authorization.k8s.io", "clusterroles", test.want)
			for _, rule := range rules {
				if slices.Contains(rule.Verbs, "bind") || slices.Contains(rule.Verbs, "escalate") || slices.Contains(rule.Verbs, "impersonate") {
					t.Fatalf("terminal cluster policy contains privilege escalation verb: %+v", rule)
				}
			}
		})
	}
}

func TestTerminalClusterRBACMutationRulesExcludeZKEManagedObjects(t *testing.T) {
	rules := terminalClusterRBACMutationPolicyRules(
		[]rbacv1.ClusterRole{
			{ObjectMeta: metav1.ObjectMeta{Name: "app-operator"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "zke-agent", Labels: map[string]string{"app.kubernetes.io/managed-by": "zke-server"}}},
		},
		[]rbacv1.ClusterRoleBinding{
			{ObjectMeta: metav1.ObjectMeta{Name: "app-operators"}, RoleRef: rbacv1.RoleRef{Name: "app-operator"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "agent-delegation"}, RoleRef: rbacv1.RoleRef{Name: "zke-agent"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "zke-agent", Labels: map[string]string{"app.kubernetes.io/managed-by": "zke-server"}}, RoleRef: rbacv1.RoleRef{Name: "zke-agent"}},
		},
	)
	if len(rules) != 2 {
		t.Fatalf("RBAC mutation rules = %+v, want two restricted rules", rules)
	}
	if !slices.Equal(rules[0].ResourceNames, []string{"app-operator"}) ||
		!slices.Equal(rules[1].ResourceNames, []string{"app-operators"}) {
		t.Fatalf("RBAC mutation ResourceNames = %+v", rules)
	}
	for _, rule := range rules {
		if !slices.Equal(rule.Verbs, []string{"update", "delete"}) {
			t.Fatalf("RBAC mutation verbs = %v", rule.Verbs)
		}
	}
}

func TestDeleteTerminalSessionCleansLegacyNamespaceResources(t *testing.T) {
	sessionID := "00000000-0000-4000-8000-000000000001"
	name := terminalResourceName(sessionID)
	namespace := "team-a"
	client := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name + "-cluster"}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name + "-cluster"}},
	)
	if err := deleteKubernetesTerminalSession(context.Background(), client, namespace, name, sessionID); err != nil {
		t.Fatalf("deleteKubernetesTerminalSession() error = %v", err)
	}
	if pods, err := client.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{}); err != nil || len(pods.Items) != 0 {
		t.Fatalf("remaining Pods = %+v, error = %v", pods, err)
	}
	if roles, err := client.RbacV1().Roles(namespace).List(context.Background(), metav1.ListOptions{}); err != nil || len(roles.Items) != 0 {
		t.Fatalf("remaining Roles = %+v, error = %v", roles, err)
	}
	if bindings, err := client.RbacV1().RoleBindings(namespace).List(context.Background(), metav1.ListOptions{}); err != nil || len(bindings.Items) != 0 {
		t.Fatalf("remaining RoleBindings = %+v, error = %v", bindings, err)
	}
	if roles, err := client.RbacV1().ClusterRoles().List(context.Background(), metav1.ListOptions{}); err != nil || len(roles.Items) != 0 {
		t.Fatalf("remaining ClusterRoles = %+v, error = %v", roles, err)
	}
	if bindings, err := client.RbacV1().ClusterRoleBindings().List(context.Background(), metav1.ListOptions{}); err != nil || len(bindings.Items) != 0 {
		t.Fatalf("remaining ClusterRoleBindings = %+v, error = %v", bindings, err)
	}
}

func TestTerminalReadIncludesWatchForKubectlWaits(t *testing.T) {
	namespaced := terminalPolicyRules([]string{"cluster.read"})
	assertTerminalVerbs(t, namespaced, "", "pods", []string{"get", "list", "watch"})

	clusterScoped := terminalClusterPolicyRules([]string{"cluster.read"})
	assertTerminalVerbs(t, clusterScoped, "", "namespaces", []string{"get", "list", "watch"})
}

func TestTerminalDeadlineIsReportedAsTimeout(t *testing.T) {
	response := kubernetesTerminalSessionFailure(&agentv1.TerminalSessionResponse{}, context.DeadlineExceeded)
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_TIMEOUT || response.GetReason() != "Timeout" {
		t.Fatalf("deadline response = %+v, want timeout", response)
	}
}

func TestTerminalNamespaceRoleBindingsSelectProtectedRoles(t *testing.T) {
	namespaces := []corev1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "default"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		{ObjectMeta: metav1.ObjectMeta{Name: "zke-system"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		{ObjectMeta: metav1.ObjectMeta{Name: "deleting"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating}},
	}
	bindings := terminalNamespaceRoleBindings(namespaces, "zke-system", "terminal-sa", "terminal-role", nil, nil)
	if len(bindings) != 3 {
		t.Fatalf("role bindings = %+v, want three active Namespaces", bindings)
	}
	wantRoles := map[string]string{
		"default":     "terminal-role",
		"kube-system": "terminal-role-system",
		"zke-system":  "terminal-role-agent",
	}
	for _, binding := range bindings {
		if len(binding.Subjects) != 1 || binding.Subjects[0].Namespace != "zke-system" ||
			binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != wantRoles[binding.Namespace] {
			t.Fatalf("unexpected RoleBinding = %+v", binding)
		}
	}
}

func TestTerminalNamespaceLifecycleRulesUseIndependentPermissions(t *testing.T) {
	namespaces := []corev1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "zke-system"}},
	}
	rules := terminalNamespaceLifecyclePolicyRules(
		[]string{"cluster.namespace.manage", "cluster.system_namespace.manage", "cluster.agent_namespace.manage"},
		namespaces,
		"zke-system",
	)
	if len(rules) != 4 || !slices.Equal(rules[0].ResourceNames, []string{"team-a"}) ||
		!slices.Equal(rules[1].ResourceNames, []string{"default", "kube-system"}) ||
		!slices.Equal(rules[2].ResourceNames, []string{"zke-system"}) ||
		!slices.Equal(rules[3].Verbs, []string{"create"}) {
		t.Fatalf("lifecycle rules = %+v", rules)
	}

	ordinaryOnly := terminalNamespaceLifecyclePolicyRules(
		[]string{"cluster.namespace.manage"}, namespaces, "zke-system",
	)
	if len(ordinaryOnly) != 1 || !slices.Equal(ordinaryOnly[0].ResourceNames, []string{"team-a"}) || slices.Contains(ordinaryOnly[0].Verbs, "create") {
		t.Fatalf("ordinary lifecycle rules = %+v", ordinaryOnly)
	}
}

func TestTerminalProtectedPolicyReplacesGenericMutationAndStacksSensitivePermissions(t *testing.T) {
	ordinary := terminalProtectedPolicyRules(
		[]string{"cluster.read", "cluster.resource.create", "cluster.resource.update", "cluster.resource.delete"},
		"cluster.system_namespace.manage",
	)
	assertTerminalVerbs(t, ordinary, "", "pods", []string{"get", "list", "watch"})

	protected := terminalProtectedPolicyRules(
		[]string{"cluster.system_namespace.manage"},
		"cluster.system_namespace.manage",
	)
	assertTerminalVerbs(t, protected, "", "pods", []string{"create", "update", "patch", "delete"})
	assertTerminalVerbs(t, protected, "", "secrets", nil)

	sensitive := terminalProtectedPolicyRules(
		[]string{"cluster.system_namespace.manage", "cluster.secret.read", "cluster.secret.manage"},
		"cluster.system_namespace.manage",
	)
	assertTerminalVerbs(t, sensitive, "", "secrets", []string{"get", "list", "watch", "create", "update", "patch", "delete"})
}

func assertTerminalVerbs(t *testing.T, rules []rbacv1.PolicyRule, group, resource string, want []string) {
	t.Helper()
	for _, rule := range rules {
		if slices.Contains(rule.APIGroups, group) && slices.Contains(rule.Resources, resource) {
			if !slices.Equal(rule.Verbs, want) {
				t.Fatalf("verbs for %s/%s = %v, want %v", group, resource, rule.Verbs, want)
			}
			return
		}
	}
	if len(want) != 0 {
		t.Fatalf("no terminal policy rule for %s/%s", group, resource)
	}
}
