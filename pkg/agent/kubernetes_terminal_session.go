package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	terminalContainerName    = "terminal"
	terminalManagedLabel     = "zke.io/terminal-session"
	terminalKeepaliveCommand = `set -eu
mkdir -p /workspace/.kube
token="$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)"
kubectl config set-cluster in-cluster --server=https://kubernetes.default.svc --certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt --kubeconfig=/workspace/.kube/config >/dev/null
kubectl config set-credentials terminal --token="${token}" --kubeconfig=/workspace/.kube/config >/dev/null
kubectl config set-context terminal --cluster=in-cluster --user=terminal --namespace=default --kubeconfig=/workspace/.kube/config >/dev/null
kubectl config use-context terminal --kubeconfig=/workspace/.kube/config >/dev/null
unset token
trap : TERM INT
while :; do sleep 3600 & wait $!; done`
)

func newKubernetesTerminalSessionHandler(
	client kubernetes.Interface,
	identityNamespace string,
) agentprotocol.TerminalSessionHandler {
	return func(ctx context.Context, request *agentv1.TerminalSessionRequest) (*agentv1.TerminalSessionResponse, error) {
		response := &agentv1.TerminalSessionResponse{
			SessionId: request.GetSessionId(), Namespace: request.GetNamespace(),
		}
		if client == nil {
			return terminalSessionFailure(response, agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
				http.StatusServiceUnavailable, "KubernetesClientUnavailable", "Kubernetes client is unavailable"), nil
		}
		switch request.GetAction() {
		case agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE:
			if request.GetNamespace() != identityNamespace {
				return terminalSessionFailure(response, agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
					http.StatusForbidden, "TerminalNamespaceMismatch", "Terminal sessions must run in the Agent Namespace"), nil
			}
			return createKubernetesTerminalSession(ctx, client, request, response)
		case agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE:
			err := deleteKubernetesTerminalSession(ctx, client, request.GetNamespace(), terminalResourceName(request.GetSessionId()), request.GetSessionId())
			if err != nil {
				return kubernetesTerminalSessionFailure(response, err), nil
			}
			response.Result = agentv1.ResultCode_RESULT_CODE_OK
			response.KubernetesStatusCode = http.StatusOK
			return response, nil
		default:
			return terminalSessionFailure(response, agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				http.StatusBadRequest, "InvalidAction", "Terminal session action is invalid"), nil
		}
	}
}

func createKubernetesTerminalSession(
	ctx context.Context,
	client kubernetes.Interface,
	request *agentv1.TerminalSessionRequest,
	response *agentv1.TerminalSessionResponse,
) (*agentv1.TerminalSessionResponse, error) {
	name := terminalResourceName(request.GetSessionId())
	namespacedRoleName := name + "-namespaced"
	systemRoleName := namespacedRoleName + "-system"
	agentRoleName := namespacedRoleName + "-agent"
	expiresAt := time.Now().UTC().Add(time.Duration(request.GetTtlSeconds()) * time.Second)
	labels := map[string]string{
		"app.kubernetes.io/name":       "zke-terminal",
		"app.kubernetes.io/managed-by": "zke-agent",
		terminalManagedLabel:           request.GetSessionId(),
	}
	annotations := map[string]string{
		"zke.io/user-id":    request.GetUserId(),
		"zke.io/expires-at": expiresAt.Format(time.RFC3339),
	}
	namespaces, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return kubernetesTerminalSessionFailure(response, err), nil
	}
	clusterPolicyRules := terminalClusterPolicyRules(request.GetPermissions())
	if terminalHasPermission(request.GetPermissions(), "cluster.rbac.manage") {
		clusterRoles, listErr := client.RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return kubernetesTerminalSessionFailure(response, listErr), nil
		}
		clusterRoleBindings, listErr := client.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return kubernetesTerminalSessionFailure(response, listErr), nil
		}
		clusterPolicyRules = append(clusterPolicyRules,
			terminalClusterRBACMutationPolicyRules(clusterRoles.Items, clusterRoleBindings.Items)...)
	}

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta:                   metav1.ObjectMeta{Name: name, Namespace: request.GetNamespace(), Labels: labels, Annotations: annotations},
		AutomountServiceAccountToken: terminalPointer(true),
	}
	namespacedRoles := []*rbacv1.ClusterRole{{
		ObjectMeta: metav1.ObjectMeta{Name: namespacedRoleName, Labels: labels, Annotations: annotations},
		Rules:      terminalPolicyRules(request.GetPermissions()),
	}, {
		ObjectMeta: metav1.ObjectMeta{Name: systemRoleName, Labels: labels, Annotations: annotations},
		Rules:      terminalProtectedPolicyRules(request.GetPermissions(), "cluster.system_namespace.manage"),
	}, {
		ObjectMeta: metav1.ObjectMeta{Name: agentRoleName, Labels: labels, Annotations: annotations},
		Rules:      terminalProtectedPolicyRules(request.GetPermissions(), "cluster.agent_namespace.manage"),
	}}
	clusterName := name + "-cluster"
	clusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: clusterName, Labels: labels, Annotations: annotations},
		Rules: append(clusterPolicyRules,
			terminalNamespaceLifecyclePolicyRules(request.GetPermissions(), namespaces.Items, request.GetNamespace())...)}
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Labels: labels, Annotations: annotations},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: request.GetNamespace()}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterName},
	}
	nonRoot, userID, groupID, grace, deadline := true, int64(1000), int64(1000), int64(5), int64(request.GetTtlSeconds())
	fsGroupPolicy := corev1.FSGroupChangeOnRootMismatch
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: request.GetNamespace(), Labels: labels, Annotations: annotations},
		Spec: corev1.PodSpec{
			ServiceAccountName: name, AutomountServiceAccountToken: terminalPointer(true),
			RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline,
			TerminationGracePeriodSeconds: &grace,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot, RunAsUser: &userID, RunAsGroup: &groupID,
				FSGroup: &groupID, FSGroupChangePolicy: &fsGroupPolicy,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name: terminalContainerName, Image: request.GetImage(), ImagePullPolicy: corev1.PullPolicy(request.GetImagePullPolicy()),
				Command:    []string{"/bin/sh", "-c", terminalKeepaliveCommand},
				WorkingDir: "/workspace", Env: []corev1.EnvVar{{Name: "HOME", Value: "/workspace"},
					{Name: "KUBECONFIG", Value: "/workspace/.kube/config"}},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: terminalPointer(false),
					ReadOnlyRootFilesystem: terminalPointer(true),
					Capabilities:           &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("25m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "tmp", MountPath: "/tmp"}},
			}},
			Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		},
	}
	roleBindings := terminalNamespaceRoleBindings(namespaces.Items, request.GetNamespace(), name, namespacedRoleName, labels, annotations)

	created := false
	defer func() {
		if !created {
			cleanupContext, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancelCleanup()
			_ = deleteKubernetesTerminalSession(cleanupContext, client, request.GetNamespace(), name, request.GetSessionId())
		}
	}()
	if _, err := client.CoreV1().ServiceAccounts(request.GetNamespace()).Create(ctx, serviceAccount, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return kubernetesTerminalSessionFailure(response, err), nil
	}
	for _, namespacedRole := range namespacedRoles {
		if _, err := client.RbacV1().ClusterRoles().Create(ctx, namespacedRole, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return kubernetesTerminalSessionFailure(response, err), nil
		}
	}
	for _, roleBinding := range roleBindings {
		if _, err := client.RbacV1().RoleBindings(roleBinding.Namespace).Create(ctx, roleBinding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return kubernetesTerminalSessionFailure(response, err), nil
		}
	}
	if _, err := client.RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return kubernetesTerminalSessionFailure(response, err), nil
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return kubernetesTerminalSessionFailure(response, err), nil
	}
	createdPod, err := client.CoreV1().Pods(request.GetNamespace()).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return kubernetesTerminalSessionFailure(response, err), nil
	}
	if apierrors.IsAlreadyExists(err) {
		createdPod, err = client.CoreV1().Pods(request.GetNamespace()).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return kubernetesTerminalSessionFailure(response, err), nil
		}
	}
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		current, getErr := client.CoreV1().Pods(request.GetNamespace()).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return false, getErr
		}
		createdPod = current
		if current.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("terminal Pod failed: %s", current.Status.Message)
		}
		return current.Status.Phase == corev1.PodRunning, nil
	})
	if err != nil {
		return kubernetesTerminalSessionFailure(response, err), nil
	}
	created = true
	response.Result = agentv1.ResultCode_RESULT_CODE_OK
	response.KubernetesStatusCode = http.StatusCreated
	response.PodName = createdPod.Name
	response.PodUid = string(createdPod.UID)
	response.Container = terminalContainerName
	response.ExpiresAtUnix = expiresAt.Unix()
	return response, nil
}

func terminalNamespaceRoleBindings(
	namespaces []corev1.Namespace,
	terminalNamespace, serviceAccountName, roleName string,
	labels, annotations map[string]string,
) []*rbacv1.RoleBinding {
	bindings := make([]*rbacv1.RoleBinding, 0, len(namespaces))
	for _, namespace := range namespaces {
		if namespace.Status.Phase == corev1.NamespaceTerminating {
			continue
		}
		targetRoleName := roleName
		if namespace.Name == terminalNamespace {
			targetRoleName += "-agent"
		} else if strings.HasPrefix(namespace.Name, "kube-") {
			targetRoleName += "-system"
		}
		bindings = append(bindings, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: namespace.Name, Labels: labels, Annotations: annotations},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: serviceAccountName, Namespace: terminalNamespace}},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: targetRoleName},
		})
	}
	return bindings
}

func terminalNamespaceLifecyclePolicyRules(
	permissions []string,
	namespaces []corev1.Namespace,
	agentNamespace string,
) []rbacv1.PolicyRule {
	held := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		held[permission] = true
	}
	ordinary, system, agent := make([]string, 0, len(namespaces)), make([]string, 0, 5), make([]string, 0, 1)
	for _, namespace := range namespaces {
		switch {
		case namespace.Name == agentNamespace:
			agent = append(agent, namespace.Name)
		case namespace.Name == "default" || strings.HasPrefix(namespace.Name, "kube-"):
			system = append(system, namespace.Name)
		default:
			ordinary = append(ordinary, namespace.Name)
		}
	}
	rules := make([]rbacv1.PolicyRule, 0, 4)
	for _, item := range []struct {
		permission string
		names      []string
	}{
		{"cluster.namespace.manage", ordinary},
		{"cluster.system_namespace.manage", system},
		{"cluster.agent_namespace.manage", agent},
	} {
		if held[item.permission] && len(item.names) > 0 {
			rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"namespaces"},
				ResourceNames: item.names, Verbs: []string{"update", "patch", "delete"}})
		}
	}
	// Kubernetes RBAC cannot restrict Namespace create by object name. Grant it
	// only when the session holds all three Namespace classes, so no name can
	// cross a permission boundary after the request reaches the API Server.
	if held["cluster.namespace.manage"] && held["cluster.system_namespace.manage"] && held["cluster.agent_namespace.manage"] {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"create"}})
	}
	return rules
}

func terminalClusterRBACMutationPolicyRules(
	clusterRoles []rbacv1.ClusterRole,
	clusterRoleBindings []rbacv1.ClusterRoleBinding,
) []rbacv1.PolicyRule {
	roleNames := make([]string, 0, len(clusterRoles))
	for _, role := range clusterRoles {
		if !terminalZKEManagedObject(role.Name, role.Labels) {
			roleNames = append(roleNames, role.Name)
		}
	}
	bindingNames := make([]string, 0, len(clusterRoleBindings))
	for _, binding := range clusterRoleBindings {
		if !terminalZKEManagedObject(binding.Name, binding.Labels) && binding.RoleRef.Name != "zke-agent" {
			bindingNames = append(bindingNames, binding.Name)
		}
	}
	rules := make([]rbacv1.PolicyRule, 0, 2)
	if len(roleNames) > 0 {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"clusterroles"}, ResourceNames: roleNames, Verbs: []string{"update", "delete"}})
	}
	if len(bindingNames) > 0 {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"clusterrolebindings"}, ResourceNames: bindingNames, Verbs: []string{"update", "delete"}})
	}
	return rules
}

func terminalZKEManagedObject(name string, labels map[string]string) bool {
	return name == "zke-agent" || labels["app.kubernetes.io/managed-by"] == "zke-server"
}

func terminalHasPermission(permissions []string, want string) bool {
	for _, permission := range permissions {
		if permission == want {
			return true
		}
	}
	return false
}

func terminalPolicyRules(permissions []string) []rbacv1.PolicyRule {
	return terminalNamespacePolicyRules(permissions, "")
}

func terminalProtectedPolicyRules(permissions []string, protectedPermission string) []rbacv1.PolicyRule {
	return terminalNamespacePolicyRules(permissions, protectedPermission)
}

func terminalNamespacePolicyRules(permissions []string, protectedPermission string) []rbacv1.PolicyRule {
	held := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		held[permission] = true
	}
	protected := protectedPermission != ""
	protectedManage := !protected || held[protectedPermission]
	verbs := make([]string, 0, 7)
	if held["cluster.read"] {
		verbs = append(verbs, "get", "list", "watch")
	}
	if protectedManage && (protected || held["cluster.resource.create"]) {
		verbs = append(verbs, "create")
	}
	if protectedManage && (protected || held["cluster.resource.update"]) {
		verbs = append(verbs, "update", "patch")
	}
	if protectedManage && (protected || held["cluster.resource.delete"]) {
		verbs = append(verbs, "delete")
	}
	rules := make([]rbacv1.PolicyRule, 0, 12)
	if len(verbs) > 0 {
		rules = append(rules,
			rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods", "services", "configmaps", "persistentvolumeclaims", "resourcequotas", "limitranges"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"apps"}, Resources: []string{"deployments", "statefulsets", "daemonsets"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"batch"}, Resources: []string{"jobs", "cronjobs"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses", "networkpolicies"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"gateway.networking.k8s.io"}, Resources: []string{"gateways", "httproutes", "grpcroutes", "tlsroutes", "tcproutes", "udproutes"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"autoscaling"}, Resources: []string{"horizontalpodautoscalers"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"autoscaling.k8s.io"}, Resources: []string{"verticalpodautoscalers"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"keda.sh"}, Resources: []string{"scaledobjects"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"policy"}, Resources: []string{"poddisruptionbudgets"}, Verbs: verbs},
		)
		if held["cluster.read"] {
			rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{"apps"}, Resources: []string{"replicasets", "controllerrevisions"}, Verbs: []string{"get", "list", "watch"}})
		}
	}
	if held["cluster.event.read"] {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"get", "list", "watch"}})
	}
	if held["cluster.pod.logs.read"] {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods/log"}, Verbs: []string{"get"}})
	}
	if protectedManage && held["cluster.pod.exec"] {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"get", "create"}})
	}
	if protectedManage && held["cluster.pod.port_forward"] {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods/portforward"}, Verbs: []string{"get", "create"}})
	}
	secretVerbs := make([]string, 0, 7)
	if protectedManage && held["cluster.secret.read"] {
		secretVerbs = append(secretVerbs, "get", "list", "watch")
	}
	if protectedManage && held["cluster.secret.manage"] {
		secretVerbs = append(secretVerbs, "create", "update", "patch", "delete")
	}
	if len(secretVerbs) > 0 {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: secretVerbs})
	}
	rbacVerbs := make([]string, 0, 6)
	if held["cluster.rbac.read"] {
		rbacVerbs = append(rbacVerbs, "get", "list", "watch")
	}
	if protectedManage && held["cluster.rbac.manage"] {
		rbacVerbs = append(rbacVerbs, "create", "update", "delete")
	}
	if len(rbacVerbs) > 0 {
		rules = append(rules,
			rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"serviceaccounts"}, Verbs: rbacVerbs},
			rbacv1.PolicyRule{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"roles", "rolebindings"}, Verbs: rbacVerbs})
	}
	return rules
}

func terminalClusterPolicyRules(permissions []string) []rbacv1.PolicyRule {
	held := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		held[permission] = true
	}
	rules := make([]rbacv1.PolicyRule, 0, 8)
	verbs := make([]string, 0, 6)
	if held["cluster.read"] {
		verbs = append(verbs, "get", "list", "watch")
	}
	if held["cluster.resource.create"] {
		verbs = append(verbs, "create")
	}
	if held["cluster.resource.update"] {
		verbs = append(verbs, "update", "patch")
	}
	if held["cluster.resource.delete"] {
		verbs = append(verbs, "delete")
	}
	if len(verbs) > 0 {
		rules = append(rules,
			rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"persistentvolumes"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses"}, Verbs: verbs},
			rbacv1.PolicyRule{APIGroups: []string{"scheduling.k8s.io"}, Resources: []string{"priorityclasses"}, Verbs: verbs})
	}
	if held["cluster.read"] {
		rules = append(rules,
			rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"nodes", "namespaces"}, Verbs: []string{"get", "list", "watch"}},
			rbacv1.PolicyRule{APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}, Verbs: []string{"get", "list", "watch"}})
	}
	if held["cluster.resource.update"] {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"update", "patch"}})
	}
	rbacVerbs := make([]string, 0, 5)
	if held["cluster.rbac.read"] {
		rbacVerbs = append(rbacVerbs, "get", "list", "watch")
	}
	if held["cluster.rbac.manage"] {
		rbacVerbs = append(rbacVerbs, "create")
	}
	if len(rbacVerbs) > 0 {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"clusterroles", "clusterrolebindings"}, Verbs: rbacVerbs})
	}
	return rules
}

func deleteKubernetesTerminalSession(ctx context.Context, client kubernetes.Interface, namespace, name, sessionID string) error {
	grace := int64(0)
	policy := metav1.DeletePropagationBackground
	options := metav1.DeleteOptions{GracePeriodSeconds: &grace, PropagationPolicy: &policy}
	var cleanupErrors []error
	for _, remove := range []func() error{
		func() error { return client.CoreV1().Pods(namespace).Delete(ctx, name, options) },
		func() error { return client.RbacV1().RoleBindings(namespace).Delete(ctx, name, metav1.DeleteOptions{}) },
		func() error { return client.RbacV1().Roles(namespace).Delete(ctx, name, metav1.DeleteOptions{}) },
		func() error {
			return client.RbacV1().ClusterRoleBindings().Delete(ctx, name+"-cluster", metav1.DeleteOptions{})
		},
	} {
		if err := remove(); err != nil && !apierrors.IsNotFound(err) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	bindings, err := client.RbacV1().RoleBindings("").List(ctx, metav1.ListOptions{LabelSelector: terminalManagedLabel + "=" + sessionID})
	if err != nil {
		cleanupErrors = append(cleanupErrors, err)
	} else {
		for _, binding := range bindings.Items {
			if binding.Name != name || binding.RoleRef.Kind != "ClusterRole" ||
				!slices.Contains([]string{name + "-namespaced", name + "-namespaced-system", name + "-namespaced-agent"}, binding.RoleRef.Name) {
				continue
			}
			if err := client.RbacV1().RoleBindings(binding.Namespace).Delete(ctx, binding.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	for _, clusterRoleName := range []string{name + "-cluster", name + "-namespaced", name + "-namespaced-system", name + "-namespaced-agent"} {
		if err := client.RbacV1().ClusterRoles().Delete(ctx, clusterRoleName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if err := client.CoreV1().ServiceAccounts(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		cleanupErrors = append(cleanupErrors, err)
	}
	return errors.Join(cleanupErrors...)
}

func terminalResourceName(sessionID string) string {
	return "zke-terminal-" + strings.ReplaceAll(sessionID, "-", "")[:20]
}

func terminalSessionFailure(response *agentv1.TerminalSessionResponse, result agentv1.ResultCode, status int, reason, message string) *agentv1.TerminalSessionResponse {
	response.Result, response.KubernetesStatusCode, response.Reason, response.Message = result, uint32(status), reason, message
	return response
}

func kubernetesTerminalSessionFailure(response *agentv1.TerminalSessionResponse, err error) *agentv1.TerminalSessionResponse {
	result, status, reason := agentv1.ResultCode_RESULT_CODE_INTERNAL, http.StatusInternalServerError, "TerminalSessionFailed"
	if errors.Is(err, context.DeadlineExceeded) {
		result, status, reason = agentv1.ResultCode_RESULT_CODE_TIMEOUT, http.StatusGatewayTimeout, "Timeout"
	}
	if apierrors.IsForbidden(err) {
		result, status, reason = agentv1.ResultCode_RESULT_CODE_FORBIDDEN, http.StatusForbidden, "Forbidden"
	}
	if apierrors.IsNotFound(err) {
		result, status, reason = agentv1.ResultCode_RESULT_CODE_NOT_FOUND, http.StatusNotFound, "NotFound"
	}
	if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
		result, status, reason = agentv1.ResultCode_RESULT_CODE_CONFLICT, http.StatusConflict, "Conflict"
	}
	return terminalSessionFailure(response, result, status, reason, "Kubernetes terminal session operation failed")
}

func terminalPointer[T any](value T) *T { return &value }

func runTerminalSessionJanitor(ctx context.Context, client kubernetes.Interface, identityNamespace string, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := cleanExpiredTerminalSessions(ctx, client, identityNamespace, time.Now().UTC()); err != nil && ctx.Err() == nil {
			logger.Warn("clean expired Cluster terminal sessions", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func cleanExpiredTerminalSessions(ctx context.Context, client kubernetes.Interface, identityNamespace string, now time.Time) error {
	accounts, err := client.CoreV1().ServiceAccounts("").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=zke-terminal"})
	if err != nil {
		return err
	}
	var cleanupErrors []error
	for _, account := range accounts.Items {
		if !strings.HasPrefix(account.Name, "zke-terminal-") || account.Labels[terminalManagedLabel] == "" {
			continue
		}
		expiresAt, parseErr := time.Parse(time.RFC3339, account.Annotations["zke.io/expires-at"])
		if parseErr != nil || now.Before(expiresAt) {
			continue
		}
		if err := deleteKubernetesTerminalSession(ctx, client, account.Namespace, account.Name, account.Labels[terminalManagedLabel]); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}
