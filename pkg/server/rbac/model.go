package rbac

type Permission string

const (
	PermissionTenantCreate          Permission = "tenant.create"
	PermissionProjectCreate         Permission = "project.create"
	PermissionAgentEnrollmentCreate Permission = "agent.enrollment.create"
	PermissionClusterRead           Permission = "cluster.read"
	PermissionAgentRead             Permission = "agent.read"
	PermissionAgentRevoke           Permission = "agent.revoke"
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
