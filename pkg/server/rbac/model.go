package rbac

import "github.com/togettoyou/zke/pkg/shared/permissionname"

type Permission string

const (
	PermissionTenantCreate                      Permission = permissionname.TenantCreate
	PermissionTenantRead                        Permission = permissionname.TenantRead
	PermissionTenantManage                      Permission = permissionname.TenantManage
	PermissionProjectCreate                     Permission = permissionname.ProjectCreate
	PermissionProjectRead                       Permission = permissionname.ProjectRead
	PermissionProjectManage                     Permission = permissionname.ProjectManage
	PermissionClusterEnrollmentCreate           Permission = permissionname.ClusterEnrollmentCreate
	PermissionClusterEnrollmentRead             Permission = permissionname.ClusterEnrollmentRead
	PermissionClusterEnrollmentRevoke           Permission = permissionname.ClusterEnrollmentRevoke
	PermissionClusterRead                       Permission = permissionname.ClusterRead
	PermissionClusterPodLogsRead                Permission = permissionname.ClusterPodLogsRead
	PermissionClusterPodExec                    Permission = permissionname.ClusterPodExec
	PermissionClusterTerminalExec               Permission = permissionname.ClusterTerminalExec
	PermissionClusterPodTerminalRecordingCreate Permission = permissionname.ClusterPodTerminalRecordingCreate
	PermissionClusterPodTerminalRecordingRead   Permission = permissionname.ClusterPodTerminalRecordingRead
	PermissionClusterPodPortForward             Permission = permissionname.ClusterPodPortForward
	// PermissionClusterNodeManage covers writes to the Node object itself —
	// YAML, labels, annotations, taints and `spec.unschedulable` — through
	// every path that reaches one: the typed Node views, the generic
	// Resource/YAML routes and a Node document in a manifest. A Node is not one
	// workload among many: its labels and taints decide where every workload in
	// the Cluster may run, so it is deliberately not implied by
	// `cluster.resource.create/update/delete`. Evicting the Pods already on it
	// remains the separate PermissionClusterNodeDrain.
	PermissionClusterNodeManage            Permission = permissionname.ClusterNodeManage
	PermissionClusterNodeDrain             Permission = permissionname.ClusterNodeDrain
	PermissionClusterEventRead             Permission = permissionname.ClusterEventRead
	PermissionClusterMetricsRead           Permission = permissionname.ClusterMetricsRead
	PermissionClusterMetricsManage         Permission = permissionname.ClusterMetricsManage
	PermissionClusterManage                Permission = permissionname.ClusterManage
	PermissionClusterNamespaceManage       Permission = permissionname.ClusterNamespaceManage
	PermissionClusterSystemNamespaceManage Permission = permissionname.ClusterSystemNamespaceManage
	PermissionClusterAgentNamespaceManage  Permission = permissionname.ClusterAgentNamespaceManage
	PermissionClusterResourceCreate        Permission = permissionname.ClusterResourceCreate
	PermissionClusterResourceUpdate        Permission = permissionname.ClusterResourceUpdate
	PermissionClusterResourceDelete        Permission = permissionname.ClusterResourceDelete
	PermissionClusterRBACRead              Permission = permissionname.ClusterRBACRead
	PermissionClusterRBACManage            Permission = permissionname.ClusterRBACManage
	PermissionClusterSecretRead            Permission = permissionname.ClusterSecretRead
	PermissionClusterSecretManage          Permission = permissionname.ClusterSecretManage
	PermissionClusterConnectionRevoke      Permission = permissionname.ClusterConnectionRevoke
	PermissionUserRead                     Permission = permissionname.UserRead
	PermissionUserManage                   Permission = permissionname.UserManage
	PermissionUserPasswordChange           Permission = permissionname.UserPasswordChange
	PermissionRBACRead                     Permission = permissionname.RBACRead
	PermissionRBACManage                   Permission = permissionname.RBACManage
	PermissionAuditRead                    Permission = permissionname.AuditRead
	// PermissionAIRun opens the AIOps runtime inside the scope where the
	// binding applies. It grants no Kubernetes read permission by itself: the
	// runtime rechecks the concrete cluster permission for every piece of
	// evidence it reads.
	PermissionAIRun Permission = permissionname.AIRun
)

type scopeType string

const (
	scopeGlobal  scopeType = "global"
	scopeTenant  scopeType = "tenant"
	scopeProject scopeType = "project"
)

type scope struct {
	Type      scopeType
	TenantID  string
	ProjectID string
}

// ResolvedScope reports the Tenant and Project a scoped authorization check
// resolved its target to. Returning it lets the caller reuse the lookup the
// check already paid for — for logging, auditing or a follow-up query —
// instead of resolving the same ownership a second time.
type ResolvedScope struct {
	TenantID       string
	ProjectID      string
	AgentNamespace string
}

type Visibility struct {
	global      bool
	tenantWide  map[string]struct{}
	projectOnly map[string]string
}

type Capability struct {
	Role        string
	ScopeType   string
	TenantID    string
	ProjectID   string
	Permissions []Permission
}

func (visibility Visibility) AllowsTenant(tenantID string) bool {
	if visibility.global {
		return true
	}
	if _, exists := visibility.tenantWide[tenantID]; exists {
		return true
	}
	for _, projectTenantID := range visibility.projectOnly {
		if projectTenantID == tenantID {
			return true
		}
	}
	return false
}

func (visibility Visibility) AllowsProject(
	tenantID string,
	projectID string,
) bool {
	if visibility.global {
		return true
	}
	if _, exists := visibility.tenantWide[tenantID]; exists {
		return true
	}
	return visibility.projectOnly[projectID] == tenantID
}

func (visibility Visibility) IsGlobal() bool {
	return visibility.global
}

func (visibility Visibility) HasAny() bool {
	return visibility.global ||
		len(visibility.tenantWide) > 0 ||
		len(visibility.projectOnly) > 0
}

func (visibility Visibility) TenantIDs() []string {
	result := make([]string, 0, len(visibility.tenantWide))
	for tenantID := range visibility.tenantWide {
		result = append(result, tenantID)
	}
	return result
}

func (visibility Visibility) ProjectIDs() []string {
	result := make([]string, 0, len(visibility.projectOnly))
	for projectID := range visibility.projectOnly {
		result = append(result, projectID)
	}
	return result
}

// ProjectTenantIDs reports the tenants that own the individually granted
// projects. A project-scoped user must still be able to see the tenant holding
// their project, otherwise they could never navigate to it.
func (visibility Visibility) ProjectTenantIDs() []string {
	seen := make(map[string]struct{}, len(visibility.projectOnly))
	result := make([]string, 0, len(visibility.projectOnly))
	for _, tenantID := range visibility.projectOnly {
		if _, exists := seen[tenantID]; exists {
			continue
		}
		seen[tenantID] = struct{}{}
		result = append(result, tenantID)
	}
	return result
}

func globalScope() scope {
	return scope{Type: scopeGlobal}
}

func tenantScope(tenantID string) scope {
	return scope{
		Type:     scopeTenant,
		TenantID: tenantID,
	}
}

func projectScope(tenantID string, projectID string) scope {
	return scope{
		Type:      scopeProject,
		TenantID:  tenantID,
		ProjectID: projectID,
	}
}
