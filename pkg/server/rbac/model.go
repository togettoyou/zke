package rbac

type Permission string

const (
	PermissionTenantCreate            Permission = "tenant.create"
	PermissionTenantRead              Permission = "tenant.read"
	PermissionTenantManage            Permission = "tenant.manage"
	PermissionProjectCreate           Permission = "project.create"
	PermissionProjectRead             Permission = "project.read"
	PermissionProjectManage           Permission = "project.manage"
	PermissionClusterEnrollmentCreate Permission = "cluster.enrollment.create"
	PermissionClusterEnrollmentRead   Permission = "cluster.enrollment.read"
	PermissionClusterEnrollmentRevoke Permission = "cluster.enrollment.revoke"
	PermissionClusterRead             Permission = "cluster.read"
	PermissionClusterPodLogsRead      Permission = "cluster.pod.logs.read"
	PermissionClusterPodExec          Permission = "cluster.pod.exec"
	PermissionClusterEventRead        Permission = "cluster.event.read"
	PermissionClusterManage           Permission = "cluster.manage"
	PermissionClusterNamespaceManage  Permission = "cluster.namespace.manage"
	PermissionClusterResourceCreate   Permission = "cluster.resource.create"
	PermissionClusterResourceUpdate   Permission = "cluster.resource.update"
	PermissionClusterResourceDelete   Permission = "cluster.resource.delete"
	PermissionClusterRBACRead         Permission = "cluster.rbac.read"
	PermissionClusterRBACManage       Permission = "cluster.rbac.manage"
	PermissionClusterSecretRead       Permission = "cluster.secret.read"
	PermissionClusterSecretManage     Permission = "cluster.secret.manage"
	PermissionClusterConnectionRevoke Permission = "cluster.connection.revoke"
	PermissionUserRead                Permission = "user.read"
	PermissionUserManage              Permission = "user.manage"
	PermissionRBACRead                Permission = "rbac.read"
	PermissionRBACManage              Permission = "rbac.manage"
	PermissionAuditRead               Permission = "audit.read"
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
	TenantID  string
	ProjectID string
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
