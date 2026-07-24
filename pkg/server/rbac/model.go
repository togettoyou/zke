package rbac

type Permission string

const (
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
