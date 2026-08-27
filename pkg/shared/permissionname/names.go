// Package permissionname defines the permission names exchanged between ZKE
// components. It deliberately contains only string constants so both Server
// authorization and Agent-side permission projection can depend on one small,
// cycle-free vocabulary.
package permissionname

const (
	TenantCreate = "tenant.create"
	TenantRead   = "tenant.read"
	TenantManage = "tenant.manage"

	ProjectCreate = "project.create"
	ProjectRead   = "project.read"
	ProjectManage = "project.manage"

	ClusterEnrollmentCreate = "cluster.enrollment.create"
	ClusterEnrollmentRead   = "cluster.enrollment.read"
	ClusterEnrollmentRevoke = "cluster.enrollment.revoke"

	ClusterRead                       = "cluster.read"
	ClusterPodLogsRead                = "cluster.pod.logs.read"
	ClusterPodExec                    = "cluster.pod.exec"
	ClusterTerminalExec               = "cluster.terminal.exec"
	ClusterPodTerminalRecordingCreate = "cluster.pod.terminal_recording.create"
	ClusterPodTerminalRecordingRead   = "cluster.pod.terminal_recording.read"
	ClusterPodPortForward             = "cluster.pod.port_forward"
	ClusterNodeManage                 = "cluster.node.manage"
	ClusterNodeDrain                  = "cluster.node.drain"
	ClusterEventRead                  = "cluster.event.read"
	ClusterMetricsRead                = "cluster.metrics.read"
	ClusterMetricsManage              = "cluster.metrics.manage"
	ClusterManage                     = "cluster.manage"
	ClusterNamespaceManage            = "cluster.namespace.manage"
	ClusterSystemNamespaceManage      = "cluster.system_namespace.manage"
	ClusterAgentNamespaceManage       = "cluster.agent_namespace.manage"
	ClusterResourceCreate             = "cluster.resource.create"
	ClusterResourceUpdate             = "cluster.resource.update"
	ClusterResourceDelete             = "cluster.resource.delete"
	ClusterRBACRead                   = "cluster.rbac.read"
	ClusterRBACManage                 = "cluster.rbac.manage"
	ClusterSecretRead                 = "cluster.secret.read"
	ClusterSecretManage               = "cluster.secret.manage"
	ClusterConnectionRevoke           = "cluster.connection.revoke"
	// ClusterHelmManage is installing, upgrading, rolling back and
	// uninstalling a Helm release in a Cluster. It is deliberately one
	// permission rather than four: the four differ in which objects they write
	// and not in what they are able to write, and a role that may install a
	// chart may already replace anything the chart owns.
	//
	// It is not sufficient on its own. Every Helm write route also requires
	// the object permissions the operation actually spends — creating and
	// updating objects, deleting them on an uninstall — and the Secret
	// permission, because Helm's own release storage is a Secret. The
	// protected-Namespace rules apply as they do everywhere else.
	ClusterHelmManage = "cluster.helm.manage"

	UserRead           = "user.read"
	UserManage         = "user.manage"
	UserPasswordChange = "user.password.change"

	// The chart catalogue is platform-wide, so its permissions are global
	// rather than per Cluster: reading it says what may be installed anywhere,
	// and installing it anywhere still needs cluster.helm.manage there.
	HelmRepositoryRead   = "helm.repository.read"
	HelmRepositoryManage = "helm.repository.manage"

	RBACRead   = "rbac.read"
	RBACManage = "rbac.manage"
	AuditRead  = "audit.read"
	AIRun      = "ai.run"
)
