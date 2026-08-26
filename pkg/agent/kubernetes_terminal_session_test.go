package agent

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/permissionname"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestTerminalPodUsesRequestedImagePullPolicy(t *testing.T) {
	namespace := "zke-system"
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	client.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: action.(k8stesting.GetAction).GetName(), Namespace: namespace, UID: "pod-uid"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}, nil
	})
	request := &agentv1.TerminalSessionRequest{
		Action:    agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE,
		SessionId: "11111111-1111-4111-8111-111111111111",
		UserId:    "22222222-2222-4222-8222-222222222222", Namespace: namespace,
		Permissions: []string{permissionname.ClusterTerminalExec}, TtlSeconds: 60,
		Image: "registry.example.com/zke-terminal:v1", ImagePullPolicy: "Never",
	}
	response, err := createKubernetesTerminalSession(context.Background(), client, request, &agentv1.TerminalSessionResponse{})
	if err != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("create terminal session response = %+v, error = %v", response, err)
	}
	pods, err := client.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("terminal Pods = %+v, error = %v", pods, err)
	}
	if got := pods.Items[0].Spec.Containers[0].ImagePullPolicy; got != corev1.PullNever {
		t.Fatalf("image pull policy = %q, want %q", got, corev1.PullNever)
	}
}

func TestAIOpsTerminalPodKeepsCredentialOutOfCommandContainer(t *testing.T) {
	namespace := "zke-system"
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	client.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: action.(k8stesting.GetAction).GetName(), Namespace: namespace, UID: "pod-uid"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue,
			}}},
		}, nil
	})
	request := &agentv1.TerminalSessionRequest{
		Action:    agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE,
		SessionId: "11111111-1111-4111-8111-111111111111",
		UserId:    "22222222-2222-4222-8222-222222222222", Namespace: namespace,
		Permissions: []string{permissionname.ClusterTerminalExec, permissionname.ClusterPodExec}, TtlSeconds: 60,
		Image: "registry.example.com/zke-terminal:v1", ImagePullPolicy: "Never",
		CredentialProxy: true,
	}
	response, err := createKubernetesTerminalSession(context.Background(), client, request, &agentv1.TerminalSessionResponse{})
	if err != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("create AIOps terminal response = %+v, error = %v", response, err)
	}
	pods, err := client.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("terminal Pods = %+v, error = %v", pods, err)
	}
	pod := pods.Items[0]
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken ||
		pod.Labels[terminalCredentialProxyLabel] != "true" ||
		len(pod.Spec.Containers) != 2 || pod.Spec.Containers[0].Name != terminalContainerName ||
		pod.Spec.Containers[1].Name != terminalProxyContainer {
		t.Fatalf("AIOps terminal Pod = %+v", pod.Spec)
	}
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		if mount.Name == "credential" {
			t.Fatalf("command container received credential mount: %+v", mount)
		}
	}
	proxyHasCredential := false
	for _, mount := range pod.Spec.Containers[1].VolumeMounts {
		proxyHasCredential = proxyHasCredential || mount.Name == "credential"
	}
	if !proxyHasCredential {
		t.Fatalf("credential proxy has no projected token mount: %+v", pod.Spec.Containers[1].VolumeMounts)
	}
	proxyCommand := strings.Join(pod.Spec.Containers[1].Command, " ")
	if !strings.Contains(proxyCommand, terminalCredentialProxyRejectPaths) {
		t.Fatalf("credential proxy does not reject access to Terminal Pods: %q", proxyCommand)
	}
	rejectPaths := regexp.MustCompile(terminalCredentialProxyRejectPaths)
	for _, path := range []string{
		"/api/v1/namespaces/zke-system/pods/zke-terminal-abcd/exec",
		"/api/v1/namespaces/zke-system/pods/zke-terminal-abcd/attach",
		"/api/v1/namespaces/zke-system/pods/zke-terminal-abcd/portforward",
	} {
		if !rejectPaths.MatchString(path) {
			t.Fatalf("credential proxy accepted Terminal Pod path %q", path)
		}
	}
	if rejectPaths.MatchString("/api/v1/namespaces/zke-system/pods/zke-metrics-collector/exec") {
		t.Fatal("credential proxy rejected a non-Terminal Pod exec path")
	}
	accounts, err := client.CoreV1().ServiceAccounts(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(accounts.Items) != 1 || accounts.Items[0].AutomountServiceAccountToken == nil ||
		*accounts.Items[0].AutomountServiceAccountToken {
		t.Fatalf("AIOps terminal ServiceAccount = %+v, error = %v", accounts, err)
	}
}

func TestTerminalPolicyKeepsSecretAndRBACPermissionsIndependent(t *testing.T) {
	tests := []struct {
		name        string
		permissions []string
		secretVerbs []string
		rbacVerbs   []string
	}{
		{name: "generic resource permissions do not reach protected families",
			permissions: []string{permissionname.ClusterRead, permissionname.ClusterResourceCreate, permissionname.ClusterResourceUpdate, permissionname.ClusterResourceDelete}},
		{name: "Secret read is read only", permissions: []string{permissionname.ClusterSecretRead}, secretVerbs: []string{"get", "list", "watch"}},
		{name: "Secret manage does not imply read", permissions: []string{permissionname.ClusterSecretManage}, secretVerbs: []string{"create", "update", "patch", "delete"}},
		{name: "RBAC read is read only", permissions: []string{permissionname.ClusterRBACRead}, rbacVerbs: []string{"get", "list", "watch"}},
		{name: "RBAC manage does not add bind or escalate", permissions: []string{permissionname.ClusterRBACManage}, rbacVerbs: []string{"create", "update", "delete"}},
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
			permissions: []string{permissionname.ClusterRead, permissionname.ClusterResourceCreate, permissionname.ClusterResourceUpdate, permissionname.ClusterResourceDelete}},
		{name: "cluster RBAC read is read only", permissions: []string{permissionname.ClusterRBACRead}, want: []string{"get", "list", "watch"}},
		{name: "cluster RBAC manage cannot bind or escalate", permissions: []string{permissionname.ClusterRBACManage}, want: []string{"create"}},
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
	namespaced := terminalPolicyRules([]string{permissionname.ClusterRead})
	assertTerminalVerbs(t, namespaced, "", "pods", []string{"get", "list", "watch"})

	clusterScoped := terminalClusterPolicyRules([]string{permissionname.ClusterRead})
	assertTerminalVerbs(t, clusterScoped, "", "namespaces", []string{"get", "list", "watch"})

	allNamespaces := terminalNamespacedReadPolicyRules([]string{
		permissionname.ClusterRead, permissionname.ClusterEventRead, permissionname.ClusterPodLogsRead,
	})
	assertTerminalVerbs(t, allNamespaces, "", "pods", []string{"get", "list", "watch"})
	assertTerminalVerbs(t, allNamespaces, "", "events", []string{"get", "list", "watch"})
	assertTerminalVerbs(t, allNamespaces, "", "pods/log", []string{"get"})
	assertTerminalVerbs(t, allNamespaces, "", "pods/exec", nil)
}

func TestTerminalNamespacedClusterReadsNeverCarryWritesOrStreamingAccess(t *testing.T) {
	rules := terminalNamespacedReadPolicyRules([]string{
		permissionname.ClusterRead,
		permissionname.ClusterResourceCreate,
		permissionname.ClusterResourceUpdate,
		permissionname.ClusterResourceDelete,
		permissionname.ClusterPodExec,
		permissionname.ClusterPodPortForward,
		permissionname.ClusterSecretRead,
		permissionname.ClusterSecretManage,
		permissionname.ClusterSystemNamespaceManage,
		permissionname.ClusterAgentNamespaceManage,
		permissionname.ClusterRBACRead,
		permissionname.ClusterRBACManage,
	})
	assertTerminalVerbs(t, rules, "", "secrets", []string{"get", "list", "watch"})
	assertTerminalVerbs(t, rules, "rbac.authorization.k8s.io", "roles", []string{"get", "list", "watch"})
	assertTerminalVerbs(t, rules, "", "pods/exec", nil)
	assertTerminalVerbs(t, rules, "", "pods/portforward", nil)
	for _, rule := range rules {
		for _, verb := range rule.Verbs {
			if slices.Contains([]string{"create", "update", "patch", "delete"}, verb) {
				t.Fatalf("Cluster-wide namespaced read rule contains %q: %+v", verb, rule)
			}
		}
	}
	withoutProtectedSecretGrants := terminalNamespacedReadPolicyRules([]string{permissionname.ClusterSecretRead})
	assertTerminalVerbs(t, withoutProtectedSecretGrants, "", "secrets", nil)
}

func TestTerminalStreamingSubresourcesSupportWebSocketAndSPDY(t *testing.T) {
	rules := terminalPolicyRules([]string{permissionname.ClusterPodExec, permissionname.ClusterPodPortForward})
	assertTerminalVerbs(t, rules, "", "pods/exec", []string{"get", "create"})
	assertTerminalVerbs(t, rules, "", "pods/portforward", []string{"get", "create"})
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
		[]string{permissionname.ClusterNamespaceManage, permissionname.ClusterSystemNamespaceManage, permissionname.ClusterAgentNamespaceManage},
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
		[]string{permissionname.ClusterNamespaceManage}, namespaces, "zke-system",
	)
	if len(ordinaryOnly) != 1 || !slices.Equal(ordinaryOnly[0].ResourceNames, []string{"team-a"}) || slices.Contains(ordinaryOnly[0].Verbs, "create") {
		t.Fatalf("ordinary lifecycle rules = %+v", ordinaryOnly)
	}
}

func TestTerminalProtectedPolicyReplacesGenericMutationAndStacksSensitivePermissions(t *testing.T) {
	ordinary := terminalProtectedPolicyRules(
		[]string{permissionname.ClusterRead, permissionname.ClusterResourceCreate, permissionname.ClusterResourceUpdate, permissionname.ClusterResourceDelete},
		permissionname.ClusterSystemNamespaceManage,
	)
	assertTerminalVerbs(t, ordinary, "", "pods", []string{"get", "list", "watch"})

	protected := terminalProtectedPolicyRules(
		[]string{permissionname.ClusterSystemNamespaceManage},
		permissionname.ClusterSystemNamespaceManage,
	)
	assertTerminalVerbs(t, protected, "", "pods", []string{"create", "update", "patch", "delete"})
	assertTerminalVerbs(t, protected, "", "secrets", nil)

	sensitive := terminalProtectedPolicyRules(
		[]string{permissionname.ClusterSystemNamespaceManage, permissionname.ClusterSecretRead, permissionname.ClusterSecretManage},
		permissionname.ClusterSystemNamespaceManage,
	)
	assertTerminalVerbs(t, sensitive, "", "secrets", []string{"get", "list", "watch", "create", "update", "patch", "delete"})
}

// kubectl inside the Cluster terminal writes a Node only for a session whose
// operator holds `cluster.node.manage`. The generic resource permissions cover
// the other Cluster-scoped objects the terminal projects — PersistentVolumes,
// StorageClasses, PriorityClasses — and must not reach a Node through
// `kubectl label node` or `kubectl edit node`, which is exactly what the HTTP
// routes refuse.
func TestTerminalClusterPolicyKeepsNodeWritesBehindTheNodePermission(t *testing.T) {
	nodeVerbs := func(permissions []string) []string {
		verbs := []string{}
		for _, rule := range terminalClusterPolicyRules(permissions) {
			if slices.Contains(rule.APIGroups, "") && slices.Contains(rule.Resources, "nodes") {
				verbs = append(verbs, rule.Verbs...)
			}
		}
		return verbs
	}

	generic := nodeVerbs([]string{
		permissionname.ClusterRead,
		permissionname.ClusterResourceCreate,
		permissionname.ClusterResourceUpdate,
		permissionname.ClusterResourceDelete,
	})
	if !slices.Equal(generic, []string{"get", "list", "watch"}) {
		t.Fatalf("Node verbs without cluster.node.manage = %v, want reads only", generic)
	}

	withNodeManage := nodeVerbs([]string{permissionname.ClusterRead, permissionname.ClusterNodeManage})
	if !slices.Contains(withNodeManage, "update") || !slices.Contains(withNodeManage, "patch") {
		t.Fatalf("Node verbs with cluster.node.manage = %v, want update and patch", withNodeManage)
	}
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
