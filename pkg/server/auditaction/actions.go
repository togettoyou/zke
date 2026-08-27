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

import (
	"slices"

	"github.com/togettoyou/zke/pkg/shared/permissionname"
)

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

	TenantCreate  = permissionname.TenantCreate
	TenantUpdate  = "tenant.update"
	TenantSuspend = "tenant.suspend"
	TenantResume  = "tenant.resume"
	TenantDelete  = "tenant.delete"

	ProjectCreate  = permissionname.ProjectCreate
	ProjectUpdate  = "project.update"
	ProjectSuspend = "project.suspend"
	ProjectResume  = "project.resume"
	ProjectDelete  = "project.delete"

	PlatformSettingsUpdate = "platform_settings.update"
	// The AI model endpoint is a section of platform settings with its own
	// revision, so its save is its own action: an operator reviewing who
	// changed where cluster content gets sent should not have to open every
	// platform_settings.update to find out.
	AIModelSettingsUpdate      = "ai_model_settings.update"
	AIModelEnabledUpdate       = "ai_model.enabled.update"
	AIModelSettingsTest        = "ai_model_settings.test"
	AgentEndpointProfileCreate = "agent_endpoint_profile.create"
	AgentEndpointProfileUpdate = "agent_endpoint_profile.update"
	AgentEndpointProfileDelete = "agent_endpoint_profile.delete"
	PlatformSettingsManage     = "platform.settings.manage"
	// The chart catalogue is platform configuration: it decides what every
	// operator on this Server may install anywhere. Adding a repository is
	// therefore recorded with the other platform changes rather than with the
	// Cluster ones, and the record names the repository, never its credential.
	HelmRepositoryCreate = "helm_repository.create"
	HelmRepositoryUpdate = "helm_repository.update"
	HelmRepositoryDelete = "helm_repository.delete"

	// AIOps reads a Cluster on an operator behalf, through a model that chose
	// what to read. Those reads are audited exactly like the ones an operator
	// makes by hand: otherwise AIOps would be a way to read Pod logs without
	// leaving the record that reading Pod logs leaves.
	//
	// One action for every tool rather than reusing each capability own action,
	// because the question an auditor asks here is different — not "who read
	// this Pod log" but "what did the agent do on this Cluster, and was it
	// allowed to". The tool name is in the detail.
	AIToolInvoke     = "ai_tool.invoke"
	AIApprovalDecide = "ai_approval.decide"

	ClusterEnroll                        = "cluster.enroll"
	ClusterUpdate                        = "cluster.update"
	ClusterSuspend                       = "cluster.suspend"
	ClusterResume                        = "cluster.resume"
	ClusterDelete                        = "cluster.delete"
	ClusterEnrollmentCreate              = permissionname.ClusterEnrollmentCreate
	ClusterEnrollmentRevoke              = permissionname.ClusterEnrollmentRevoke
	ClusterConnectionRevoke              = permissionname.ClusterConnectionRevoke
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
	// Evicting one Pod and deleting it answer to the same permission and end
	// the same way, so they would be indistinguishable in the trail if they
	// shared an action. They are worth telling apart: an eviction is the
	// disruption-budget-checked path, and "was this removal checked against the
	// budget" is exactly the question asked afterwards about an outage.
	KubernetesPodEvict       = "kubernetes_pod.evict"
	KubernetesPodEvictDryRun = "kubernetes_pod.evict.dry_run"
	// Running a CronJob now creates a Job that the schedule did not ask for.
	// The Job it creates is an ordinary object, but the decision to run it off
	// schedule is not, and reading it back out of a generic create record would
	// mean guessing from the object's name.
	KubernetesCronJobTrigger       = "kubernetes_cron_job.trigger"
	KubernetesCronJobTriggerDryRun = "kubernetes_cron_job.trigger.dry_run"
	KubernetesNodeDrain            = "kubernetes_node.drain"
	KubernetesNodeDrainDryRun      = "kubernetes_node.drain.dry_run"
	KubernetesEventRead            = "kubernetes_event.read"
	// A Helm release is stored in a Secret, and reading one hands back the
	// values the chart was installed with. That is the same exposure a Secret
	// read is, and it is recorded the same way — separately for a listing,
	// which returns no values at all, and for reading one release.
	KubernetesHelmReleaseList = "kubernetes_helm_release.list"
	KubernetesHelmReleaseRead = "kubernetes_helm_release.read"
	// A release write is not a resource write and is not recorded as one. One
	// install creates objects across a whole application, rewrites Helm's
	// history and can replace what a previous revision owned, so reading it out
	// of a stream of generic create records would mean reassembling the
	// operation from its parts. The four actions stay apart because the
	// question asked afterwards is which one happened: an upgrade and a
	// rollback both produce a new revision and mean opposite things.
	//
	// The dry-run twins are recorded too. A preview renders the chart against
	// the Cluster and returns the manifest it would apply, which is a read of
	// what the Cluster would accept; it writes nothing, and telling the two
	// apart in the trail is the whole point of recording them separately.
	KubernetesHelmReleaseInstall         = "kubernetes_helm_release.install"
	KubernetesHelmReleaseInstallDryRun   = "kubernetes_helm_release.install.dry_run"
	KubernetesHelmReleaseUpgrade         = "kubernetes_helm_release.upgrade"
	KubernetesHelmReleaseUpgradeDryRun   = "kubernetes_helm_release.upgrade.dry_run"
	KubernetesHelmReleaseRollback        = "kubernetes_helm_release.rollback"
	KubernetesHelmReleaseRollbackDryRun  = "kubernetes_helm_release.rollback.dry_run"
	KubernetesHelmReleaseUninstall       = "kubernetes_helm_release.uninstall"
	KubernetesHelmReleaseUninstallDryRun = "kubernetes_helm_release.uninstall.dry_run"

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
	DeniedTenantRead                        = permissionname.TenantRead
	DeniedTenantManage                      = permissionname.TenantManage
	DeniedProjectRead                       = permissionname.ProjectRead
	DeniedProjectManage                     = permissionname.ProjectManage
	DeniedClusterEnrollmentRead             = permissionname.ClusterEnrollmentRead
	DeniedClusterRead                       = permissionname.ClusterRead
	DeniedClusterPodLogsRead                = permissionname.ClusterPodLogsRead
	DeniedClusterPodExec                    = permissionname.ClusterPodExec
	DeniedClusterTerminalExec               = permissionname.ClusterTerminalExec
	DeniedClusterPodTerminalRecordingCreate = permissionname.ClusterPodTerminalRecordingCreate
	DeniedClusterPodTerminalRecordingRead   = permissionname.ClusterPodTerminalRecordingRead
	DeniedClusterPodPortForward             = permissionname.ClusterPodPortForward
	DeniedClusterNodeManage                 = permissionname.ClusterNodeManage
	DeniedClusterNodeDrain                  = permissionname.ClusterNodeDrain
	DeniedClusterEventRead                  = permissionname.ClusterEventRead
	DeniedClusterMetricsRead                = permissionname.ClusterMetricsRead
	DeniedClusterMetricsManage              = permissionname.ClusterMetricsManage
	DeniedClusterManage                     = permissionname.ClusterManage
	DeniedClusterNamespaceManage            = permissionname.ClusterNamespaceManage
	DeniedClusterSystemNamespaceManage      = permissionname.ClusterSystemNamespaceManage
	DeniedClusterAgentNamespaceManage       = permissionname.ClusterAgentNamespaceManage
	DeniedClusterResourceCreate             = permissionname.ClusterResourceCreate
	DeniedClusterResourceUpdate             = permissionname.ClusterResourceUpdate
	DeniedClusterResourceDelete             = permissionname.ClusterResourceDelete
	DeniedClusterRBACRead                   = permissionname.ClusterRBACRead
	DeniedClusterRBACManage                 = permissionname.ClusterRBACManage
	DeniedClusterSecretRead                 = permissionname.ClusterSecretRead
	DeniedClusterSecretManage               = permissionname.ClusterSecretManage
	DeniedClusterHelmManage                 = permissionname.ClusterHelmManage
	DeniedHelmRepositoryRead                = permissionname.HelmRepositoryRead
	DeniedHelmRepositoryManage              = permissionname.HelmRepositoryManage
	DeniedUserRead                          = permissionname.UserRead
	DeniedUserManage                        = permissionname.UserManage
	DeniedUserPasswordChange                = permissionname.UserPasswordChange
	DeniedRBACRead                          = permissionname.RBACRead
	DeniedRBACManage                        = permissionname.RBACManage
	DeniedAuditRead                         = permissionname.AuditRead
	DeniedAIRun                             = permissionname.AIRun
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
	GroupAI          = "aiops"
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
	TargetHelmRepository       = "helm_repository"
	TargetAISession            = "ai_session"
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
	TargetHelmRepository,
	TargetAISession,
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
	{AIModelSettingsUpdate, GroupPlatform},
	{AIModelEnabledUpdate, GroupPlatform},
	{AIModelSettingsTest, GroupPlatform},
	{AgentEndpointProfileCreate, GroupPlatform},
	{AgentEndpointProfileUpdate, GroupPlatform},
	{AgentEndpointProfileDelete, GroupPlatform},
	{HelmRepositoryCreate, GroupPlatform},
	{HelmRepositoryUpdate, GroupPlatform},
	{HelmRepositoryDelete, GroupPlatform},
	{PlatformSettingsManage, GroupPlatform},

	{AIToolInvoke, GroupAI},
	{AIApprovalDecide, GroupAI},

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
	{KubernetesPodEvict, GroupKubernetes},
	{KubernetesPodEvictDryRun, GroupKubernetes},
	{KubernetesCronJobTrigger, GroupKubernetes},
	{KubernetesCronJobTriggerDryRun, GroupKubernetes},
	{KubernetesNodeDrain, GroupKubernetes},
	{KubernetesNodeDrainDryRun, GroupKubernetes},
	{KubernetesEventRead, GroupKubernetes},
	{KubernetesSecretList, GroupKubernetes},
	{KubernetesSecretRead, GroupKubernetes},
	{KubernetesHelmReleaseList, GroupKubernetes},
	{KubernetesHelmReleaseRead, GroupKubernetes},
	{KubernetesHelmReleaseInstall, GroupKubernetes},
	{KubernetesHelmReleaseInstallDryRun, GroupKubernetes},
	{KubernetesHelmReleaseUpgrade, GroupKubernetes},
	{KubernetesHelmReleaseUpgradeDryRun, GroupKubernetes},
	{KubernetesHelmReleaseRollback, GroupKubernetes},
	{KubernetesHelmReleaseRollbackDryRun, GroupKubernetes},
	{KubernetesHelmReleaseUninstall, GroupKubernetes},
	{KubernetesHelmReleaseUninstallDryRun, GroupKubernetes},

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
	{DeniedClusterNodeManage, GroupDenied},
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
	{DeniedClusterHelmManage, GroupDenied},
	{DeniedHelmRepositoryRead, GroupDenied},
	{DeniedHelmRepositoryManage, GroupDenied},
	{DeniedClusterEnrollmentRead, GroupDenied},
	{DeniedUserRead, GroupDenied},
	{DeniedUserManage, GroupDenied},
	{DeniedUserPasswordChange, GroupDenied},
	{DeniedRBACRead, GroupDenied},
	{DeniedRBACManage, GroupDenied},
	{DeniedAuditRead, GroupDenied},
	{DeniedAIRun, GroupDenied},
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
