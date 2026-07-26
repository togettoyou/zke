package rbac

type Permission string

const (
	PermissionTenantCreate          Permission = "tenant.create"
	PermissionProjectCreate         Permission = "project.create"
	PermissionAgentEnrollmentCreate Permission = "agent.enrollment.create"
	PermissionClusterRead           Permission = "cluster.read"
	PermissionAgentRead             Permission = "agent.read"
	PermissionAgentRevoke           Permission = "agent.revoke"
	PermissionUserRead              Permission = "user.read"
	PermissionUserManage            Permission = "user.manage"
	PermissionRBACRead              Permission = "rbac.read"
	PermissionRBACManage            Permission = "rbac.manage"
	PermissionAuditRead             Permission = "audit.read"
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

type Visibility struct {
	global      bool
	tenantWide  map[string]struct{}
	projectOnly map[string]string
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
