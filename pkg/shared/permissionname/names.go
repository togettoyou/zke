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

	UserRead           = "user.read"
	UserManage         = "user.manage"
	UserPasswordChange = "user.password.change"

	RBACRead   = "rbac.read"
	RBACManage = "rbac.manage"
	AuditRead  = "audit.read"
	AIRun      = "ai.run"
)
