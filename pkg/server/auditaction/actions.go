// Package auditaction is the single definition of the audit action vocabulary.
//
// It is a leaf package on purpose. The names are written by the store, quoted by
// the HTTP handlers and offered to the Console as a filter; anything that has to
// agree on them can import this without pulling in the rest of the server, and
// without the import cycle that would follow from putting them in the audit
// service, which itself depends on the store.
//
// Two vocabularies are easy to confuse and must not be merged. A *permission*
// (`pkg/server/rbac`) says what an operator may do; an *action* says what
// happened. They overlap in five names and diverge everywhere else — one
// `user.manage` permission produces six distinct user actions, and `auth.login`
// corresponds to no permission at all.
//
// They do meet in one place: a refused request is recorded under the permission
// it refused, so the permission names are published here too, in their own
// group. That is a deliberate, bounded overlap, not a merge — see the denied
// constants below.
package auditaction

import "slices"

// Actions written by the Server. Every writer names one of these constants:
// where the action used to be a literal inside the store's SQL it is now a bound
// parameter, so a rename is a compile error rather than a silently unmatched
// filter. `TestPhase1BackendEndToEnd` still asserts that every action an
// exercised flow actually writes is listed here, which is what catches an action
// that is written but never declared.
const (
	AuthLogin               = "auth.login"
	AuthLogout              = "auth.logout"
	AuthPasswordChange      = "auth.password.change"
	AuthAccountLock         = "auth.account.lock"
	AuthAccountLockWithheld = "auth.account.lock_withheld"
	AuthAccountAutoUnlock   = "auth.account.auto_unlock"
	AuthAdministratorSetup  = "auth.administrator.setup"

	UserCreate        = "user.create"
	UserUpdate        = "user.update"
	UserDelete        = "user.delete"
	UserStatusUpdate  = "user.status.update"
	UserUnlock        = "user.unlock"
	UserPasswordReset = "user.password.reset"

	RoleBindingCreate = "role_binding.create"
	RoleBindingDelete = "role_binding.delete"

	// Changing a role changes what everyone already bound to it can do, without
	// touching a single binding. That makes these three the widest-reaching
	// actions in the access vocabulary, and the reason the update record is
	// worth keeping even when the permission set is the only thing that moved.
	RoleCreate = "role.create"
	RoleUpdate = "role.update"
	RoleDelete = "role.delete"

	TenantCreate  = "tenant.create"
	TenantUpdate  = "tenant.update"
	TenantSuspend = "tenant.suspend"
	TenantResume  = "tenant.resume"
	TenantDelete  = "tenant.delete"

	ProjectCreate  = "project.create"
	ProjectUpdate  = "project.update"
	ProjectSuspend = "project.suspend"
	ProjectResume  = "project.resume"
	ProjectDelete  = "project.delete"

	PlatformSettingsUpdate     = "platform_settings.update"
	AgentEndpointProfileCreate = "agent_endpoint_profile.create"
	AgentEndpointProfileUpdate = "agent_endpoint_profile.update"
	AgentEndpointProfileDelete = "agent_endpoint_profile.delete"
	PlatformSettingsManage     = "platform.settings.manage"

	ClusterEnroll                        = "cluster.enroll"
	ClusterUpdate                        = "cluster.update"
	ClusterSuspend                       = "cluster.suspend"
	ClusterResume                        = "cluster.resume"
	ClusterDelete                        = "cluster.delete"
	ClusterEnrollmentCreate              = "cluster.enrollment.create"
	ClusterEnrollmentRevoke              = "cluster.enrollment.revoke"
	ClusterConnectionRevoke              = "cluster.connection.revoke"
	ClusterConnectionReenroll            = "cluster.connection.reenroll"
	ClusterMetricsCollectorInstall       = "cluster.metrics.collector.install"
	ClusterMetricsCollectorUninstall     = "cluster.metrics.collector.uninstall"
	KubernetesResourceCreate             = "kubernetes_resource.create"
	KubernetesResourceUpdate             = "kubernetes_resource.update"
	KubernetesResourcePatch              = "kubernetes_resource.patch"
	KubernetesResourceDelete             = "kubernetes_resource.delete"
	KubernetesResourceCreateDryRun       = "kubernetes_resource.create.dry_run"
	KubernetesResourceUpdateDryRun       = "kubernetes_resource.update.dry_run"
	KubernetesResourcePatchDryRun        = "kubernetes_resource.patch.dry_run"
	KubernetesResourceDeleteDryRun       = "kubernetes_resource.delete.dry_run"
	KubernetesPodLogsRead                = "kubernetes_pod.logs.read"
	KubernetesPodExecSessionCreate       = "kubernetes_pod.exec_session.create"
	KubernetesPodExec                    = "kubernetes_pod.exec"
	KubernetesTerminalSessionCreate      = "kubernetes_terminal.session.create"
	KubernetesPodTerminalRecordingCreate = "kubernetes_pod.terminal_recording.create"
	KubernetesPodTerminalRecordingList   = "kubernetes_pod.terminal_recording.list"
	KubernetesPodTerminalRecordingRead   = "kubernetes_pod.terminal_recording.read"
	KubernetesPodAccessSessionCreate     = "kubernetes_pod.access_session.create"
	KubernetesPodAccess                  = "kubernetes_pod.access"
	KubernetesNodeDrain                  = "kubernetes_node.drain"
	KubernetesNodeDrainDryRun            = "kubernetes_node.drain.dry_run"
	KubernetesEventRead                  = "kubernetes_event.read"

	// Reading a Secret is recorded because reading it is the whole exposure:
	// unlike a ConfigMap or a Deployment, one successful GET hands the caller a
	// credential, and nothing afterwards can un-hand it. The record names the
	// Cluster, the Namespace and the object; it never carries a key name or any
	// part of a value. Listing is recorded separately from reading one object
	// because the list returns no value at all — knowing which of the two
	// happened is the difference between browsing and taking.
	KubernetesSecretList = "kubernetes_secret.list"
	KubernetesSecretRead = "kubernetes_secret.read"

	// Written by the Agent connection, not by an operator: the Agent asks for a
	// new client certificate over the control stream and the Server signs it.
	// It is grouped with the Cluster actions rather than under an Agent group of
	// its own because the Agent is not a resource an operator manages — a
	// Cluster is, and this is that Cluster's connection credential being
	// rolled. The name keeps its `agent.` prefix because that is what is already
	// in the column; the group is declared, not read off the name.
	AgentCertificateRenew = "agent.certificate.renew"
)

// Permission names that reach the action column.
//
// An authorization denial has no action of its own to record: nothing happened,
// which is the point. The middleware records it under the permission it refused,
// so these names appear in the audit trail as surely as the ones above and have
// to be published with them — the Console's filter is an exact match over this
// vocabulary, and a name missing from it is a denial nobody can look up.
//
// Only the permissions whose name is not already an action are listed. Five are:
// `tenant.create`, `project.create`, `cluster.enrollment.create`,
// `cluster.enrollment.revoke` and `cluster.connection.revoke` are both a thing
// an operator may do and a thing that happened. A name can only carry one group
// in an exact-match picker, so those stay declared once, in the family they act
// on, and a filter on them returns the successful operation and the refused
// attempt together — told apart by `result`, which is what `result` is for.
const (
	DeniedTenantRead                        = "tenant.read"
	DeniedTenantManage                      = "tenant.manage"
	DeniedProjectRead                       = "project.read"
	DeniedProjectManage                     = "project.manage"
	DeniedClusterEnrollmentRead             = "cluster.enrollment.read"
	DeniedClusterRead                       = "cluster.read"
	DeniedClusterPodLogsRead                = "cluster.pod.logs.read"
	DeniedClusterPodExec                    = "cluster.pod.exec"
	DeniedClusterTerminalExec               = "cluster.terminal.exec"
	DeniedClusterPodTerminalRecordingCreate = "cluster.pod.terminal_recording.create"
	DeniedClusterPodTerminalRecordingRead   = "cluster.pod.terminal_recording.read"
	DeniedClusterPodPortForward             = "cluster.pod.port_forward"
	DeniedClusterNodeDrain                  = "cluster.node.drain"
	DeniedClusterEventRead                  = "cluster.event.read"
	DeniedClusterMetricsRead                = "cluster.metrics.read"
	DeniedClusterMetricsManage              = "cluster.metrics.manage"
	DeniedClusterManage                     = "cluster.manage"
	DeniedClusterNamespaceManage            = "cluster.namespace.manage"
	DeniedClusterSystemNamespaceManage      = "cluster.system_namespace.manage"
	DeniedClusterAgentNamespaceManage       = "cluster.agent_namespace.manage"
	DeniedClusterResourceCreate             = "cluster.resource.create"
	DeniedClusterResourceUpdate             = "cluster.resource.update"
	DeniedClusterResourceDelete             = "cluster.resource.delete"
	DeniedClusterRBACRead                   = "cluster.rbac.read"
	DeniedClusterRBACManage                 = "cluster.rbac.manage"
	DeniedClusterSecretRead                 = "cluster.secret.read"
	DeniedClusterSecretManage               = "cluster.secret.manage"
	DeniedUserRead                          = "user.read"
	DeniedUserManage                        = "user.manage"
	DeniedUserPasswordChange                = "user.password.change"
	DeniedRBACRead                          = "rbac.read"
	DeniedRBACManage                        = "rbac.manage"
	DeniedAuditRead                         = "audit.read"
)

// Group names the family an action belongs to. It is declared here rather than
// derived from the name's first segment because the segments are not a reliable
// tree: `cluster.enrollment.create` and `cluster.delete` belong to the same
// family at different depths, and a client splitting on dots would have to guess
// which prefix is the group.
const (
	GroupAuth        = "auth"
	GroupUser        = "user"
	GroupRole        = "role"
	GroupRoleBinding = "role_binding"
	GroupTenant      = "tenant"
	GroupProject     = "project"
	GroupPlatform    = "platform"
	GroupCluster     = "cluster"
	GroupKubernetes  = "kubernetes_resource"
	// GroupDenied holds the permission names above. They are grouped apart
	// rather than filed under the resource they name because they answer a
	// different question: not "what happened to this tenant" but "who was turned
	// away". Events in this group only ever carry result `denied`.
	GroupDenied = "denied"
)

// Target types written to the audit trail. Like the actions, this is a closed
// vocabulary offered to the Console as an exact-match filter, so it is declared
// rather than derived: a target type nobody publishes is a filter nobody can
// use.
//
// These name the kind of object an event is about, which is not the same as the
// scope it belongs to. A `cluster.enroll` event is scoped to a Cluster and
// targets a Cluster; `cluster.enrollment.create` is scoped to a Project and
// targets an Enrollment.
const (
	TargetUser                 = "user"
	TargetSession              = "session"
	TargetRole                 = "role"
	TargetRoleBinding          = "role_binding"
	TargetTenant               = "tenant"
	TargetProject              = "project"
	TargetCluster              = "cluster"
	TargetAgent                = "agent"
	TargetAgentCredential      = "agent_credential"
	TargetEnrollment           = "enrollment"
	TargetAuditEvent           = "audit_event"
	TargetKubernetesResource   = "kubernetes_resource"
	TargetPlatformSettings     = "platform_settings"
	TargetAgentEndpointProfile = "agent_endpoint_profile"
)

var targetTypes = []string{
	TargetUser,
	TargetSession,
	TargetRole,
	TargetRoleBinding,
	TargetTenant,
	TargetProject,
	TargetCluster,
	TargetAgent,
	TargetAgentCredential,
	TargetEnrollment,
	TargetAuditEvent,
	TargetKubernetesResource,
	TargetPlatformSettings,
	TargetAgentEndpointProfile,
}

// TargetTypes reports the target type vocabulary in presentation order.
func TargetTypes() []string {
	return slices.Clone(targetTypes)
}

// KnownTargetType reports whether name is part of the target type vocabulary.
func KnownTargetType(name string) bool {
	return slices.Contains(targetTypes, name)
}

// Action is one entry of the vocabulary.
type Action struct {
	Name  string
	Group string
}

// Ordered by group, then by the order an operator meets them: create before
// update before delete, so the list reads as a lifecycle rather than as an
// alphabetisation of it.
var actions = []Action{
	{AuthLogin, GroupAuth},
	{AuthLogout, GroupAuth},
	{AuthPasswordChange, GroupAuth},
	{AuthAccountLock, GroupAuth},
	{AuthAccountLockWithheld, GroupAuth},
	{AuthAccountAutoUnlock, GroupAuth},
	{AuthAdministratorSetup, GroupAuth},

	{UserCreate, GroupUser},
	{UserUpdate, GroupUser},
	{UserStatusUpdate, GroupUser},
	{UserUnlock, GroupUser},
	{UserPasswordReset, GroupUser},
	{UserDelete, GroupUser},

	{RoleBindingCreate, GroupRoleBinding},
	{RoleBindingDelete, GroupRoleBinding},

	{RoleCreate, GroupRole},
	{RoleUpdate, GroupRole},
	{RoleDelete, GroupRole},

	{TenantCreate, GroupTenant},
	{TenantUpdate, GroupTenant},
	{TenantSuspend, GroupTenant},
	{TenantResume, GroupTenant},
	{TenantDelete, GroupTenant},

	{ProjectCreate, GroupProject},
	{ProjectUpdate, GroupProject},
	{ProjectSuspend, GroupProject},
	{ProjectResume, GroupProject},
	{ProjectDelete, GroupProject},

	{PlatformSettingsUpdate, GroupPlatform},
	{AgentEndpointProfileCreate, GroupPlatform},
	{AgentEndpointProfileUpdate, GroupPlatform},
	{AgentEndpointProfileDelete, GroupPlatform},
	{PlatformSettingsManage, GroupPlatform},

	{ClusterEnroll, GroupCluster},
	{ClusterUpdate, GroupCluster},
	{ClusterSuspend, GroupCluster},
	{ClusterResume, GroupCluster},
	{ClusterDelete, GroupCluster},
	{ClusterEnrollmentCreate, GroupCluster},
	{ClusterEnrollmentRevoke, GroupCluster},
	{ClusterConnectionRevoke, GroupCluster},
	{ClusterConnectionReenroll, GroupCluster},
	{ClusterMetricsCollectorInstall, GroupCluster},
	{ClusterMetricsCollectorUninstall, GroupCluster},
	{AgentCertificateRenew, GroupCluster},

	{KubernetesResourceCreate, GroupKubernetes},
	{KubernetesResourceUpdate, GroupKubernetes},
	{KubernetesResourcePatch, GroupKubernetes},
	{KubernetesResourceDelete, GroupKubernetes},
	{KubernetesResourceCreateDryRun, GroupKubernetes},
	{KubernetesResourceUpdateDryRun, GroupKubernetes},
	{KubernetesResourcePatchDryRun, GroupKubernetes},
	{KubernetesResourceDeleteDryRun, GroupKubernetes},
	{KubernetesPodLogsRead, GroupKubernetes},
	{KubernetesPodExecSessionCreate, GroupKubernetes},
	{KubernetesPodExec, GroupKubernetes},
	{KubernetesTerminalSessionCreate, GroupKubernetes},
	{KubernetesPodTerminalRecordingCreate, GroupKubernetes},
	{KubernetesPodTerminalRecordingList, GroupKubernetes},
	{KubernetesPodTerminalRecordingRead, GroupKubernetes},
	{KubernetesPodAccessSessionCreate, GroupKubernetes},
	{KubernetesPodAccess, GroupKubernetes},
	{KubernetesNodeDrain, GroupKubernetes},
	{KubernetesNodeDrainDryRun, GroupKubernetes},
	{KubernetesEventRead, GroupKubernetes},
	{KubernetesSecretList, GroupKubernetes},
	{KubernetesSecretRead, GroupKubernetes},

	{DeniedTenantRead, GroupDenied},
	{DeniedTenantManage, GroupDenied},
	{DeniedProjectRead, GroupDenied},
	{DeniedProjectManage, GroupDenied},
	{DeniedClusterRead, GroupDenied},
	{DeniedClusterPodLogsRead, GroupDenied},
	{DeniedClusterPodExec, GroupDenied},
	{DeniedClusterTerminalExec, GroupDenied},
	{DeniedClusterPodTerminalRecordingCreate, GroupDenied},
	{DeniedClusterPodTerminalRecordingRead, GroupDenied},
	{DeniedClusterPodPortForward, GroupDenied},
	{DeniedClusterNodeDrain, GroupDenied},
	{DeniedClusterEventRead, GroupDenied},
	{DeniedClusterMetricsRead, GroupDenied},
	{DeniedClusterMetricsManage, GroupDenied},
	{DeniedClusterManage, GroupDenied},
	{DeniedClusterNamespaceManage, GroupDenied},
	{DeniedClusterSystemNamespaceManage, GroupDenied},
	{DeniedClusterAgentNamespaceManage, GroupDenied},
	{DeniedClusterResourceCreate, GroupDenied},
	{DeniedClusterResourceUpdate, GroupDenied},
	{DeniedClusterResourceDelete, GroupDenied},
	{DeniedClusterRBACRead, GroupDenied},
	{DeniedClusterRBACManage, GroupDenied},
	{DeniedClusterSecretRead, GroupDenied},
	{DeniedClusterSecretManage, GroupDenied},
	{DeniedClusterEnrollmentRead, GroupDenied},
	{DeniedUserRead, GroupDenied},
	{DeniedUserManage, GroupDenied},
	{DeniedUserPasswordChange, GroupDenied},
	{DeniedRBACRead, GroupDenied},
	{DeniedRBACManage, GroupDenied},
	{DeniedAuditRead, GroupDenied},
}

// All reports the vocabulary in presentation order. The slice is copied so that
// a caller cannot reorder the definition for everyone else.
func All() []Action {
	return slices.Clone(actions)
}

// Known reports whether name is part of the vocabulary.
func Known(name string) bool {
	return slices.ContainsFunc(actions, func(action Action) bool {
		return action.Name == name
	})
}
