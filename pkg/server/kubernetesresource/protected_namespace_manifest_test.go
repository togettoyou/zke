package kubernetesresource

import "testing"

func TestManifestProtectedNamespacesRequireIndependentGrants(t *testing.T) {
	t.Parallel()
	configMap := ResourceIdentity{Version: "v1", Resource: "configmaps"}
	target := ManifestTarget{Namespace: "zke-system", Name: "agent-config"}

	ordinary := NewManifestAccess(NewService(nil), ManifestGrant{ResourceUpdate: true})
	requirement, allowed, err := ordinary.RequirementForApply(configMap, false, target)
	if err != nil || allowed || requirement != ManifestRequirementAgentNamespaceManage {
		t.Fatalf("ordinary grant requirement=%q allowed=%v err=%v", requirement, allowed, err)
	}

	protected := NewManifestAccess(NewService(nil), ManifestGrant{AgentNamespaceManage: true})
	requirement, allowed, err = protected.RequirementForApply(configMap, false, target)
	if err != nil || !allowed || requirement != ManifestRequirementAgentNamespaceManage {
		t.Fatalf("protected grant requirement=%q allowed=%v err=%v", requirement, allowed, err)
	}
}

func TestManifestProtectedNamespaceDoesNotReplaceSensitiveFamilyPermission(t *testing.T) {
	t.Parallel()
	secret := ResourceIdentity{Version: "v1", Resource: "secrets"}
	access := NewManifestAccess(NewService(nil), ManifestGrant{SystemNamespaceManage: true})
	requirement, allowed, err := access.RequirementForApply(
		secret,
		false,
		ManifestTarget{Namespace: "kube-system", Name: "credential"},
	)
	if err != nil || allowed || requirement != ManifestRequirementSecretManage {
		t.Fatalf("requirement=%q allowed=%v err=%v", requirement, allowed, err)
	}
}

func TestManifestDefaultNamespaceLifecycleUsesSystemGrant(t *testing.T) {
	t.Parallel()
	namespace := ResourceIdentity{Version: "v1", Resource: "namespaces"}
	access := NewManifestAccess(NewService(nil), ManifestGrant{NamespaceManage: true})
	requirement, allowed, err := access.RequirementForDelete(
		namespace,
		ManifestTarget{Name: "default"},
	)
	if err != nil || allowed || requirement != ManifestRequirementSystemNamespaceManage {
		t.Fatalf("requirement=%q allowed=%v err=%v", requirement, allowed, err)
	}
}
