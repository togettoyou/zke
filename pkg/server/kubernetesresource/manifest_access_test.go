package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
)

// The Agent removes Secrets from the catalog it reports, because that catalog
// feeds the resource browser and the browser must not offer a type it will
// refuse to open. Resolution is a different question from access: without a
// declared entry, every Secret in a manifest came back as "the Cluster does not
// serve this Kind" — a resolution failure worded as if the type did not exist,
// for the one family whose whole point is that it exists and is guarded.
func TestManifestCatalogResolvesSecretsTheAgentDoesNotReport(t *testing.T) {
	t.Parallel()

	catalog := kubernetescatalog.Catalog{
		Resources: []kubernetescatalog.Resource{{
			Group: "", Version: "v1", Resource: "configmaps", Kind: "ConfigMap",
			Namespaced: true, Verbs: []string{"get", "create", "patch"},
		}},
	}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		_ string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER {
			t.Fatalf("unexpected request: %+v", request)
		}
		return writeKubernetesObject(t, responseBody, catalog), nil
	}}

	resolved, err := NewManifestAccess(
		NewService(requester),
		ManifestGrant{SecretManage: true},
	).DiscoverResources(context.Background(), testClusterID)
	if err != nil {
		t.Fatal(err)
	}
	entry, exists := resolved.Lookup(ManifestKind{Version: "v1", Kind: "Secret"})
	if !exists {
		t.Fatal("Kind Secret did not resolve; a manifest holding one cannot be applied")
	}
	if entry.Resource != SecretResourceIdentity() || !entry.Namespaced {
		t.Fatalf("Secret resolved to %+v", entry)
	}
	// Resolving it is not permitting it: the grant still decides, exactly as it
	// does for every discovered entry.
	if _, allowed, _ := NewManifestAccess(nil, ManifestGrant{
		ResourceCreate: true, ResourceUpdate: true,
	}).RequirementForApply(entry.Resource, true); allowed {
		t.Fatal("declaring the Secret entry made it writable without cluster.secret.manage")
	}
	if _, exists := resolved.Lookup(ManifestKind{Version: "v1", Kind: "ConfigMap"}); !exists {
		t.Fatal("the declared entry displaced a discovered one")
	}
}

// The whole reason a manifest cannot be guarded by a single route-level
// permission: a file holding these five objects touches four permissions, and
// three of them are the ones the typed APIs exist to keep apart.
func TestManifestFamilyForSeparatesTheFamiliesWithTheirOwnPermissions(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		resource ResourceIdentity
		want     ManifestFamily
	}{
		{ResourceIdentity{Version: "v1", Resource: "secrets"}, ManifestFamilySecret},
		{ResourceIdentity{Version: "v1", Resource: "serviceaccounts"}, ManifestFamilyAuthorization},
		{
			ResourceIdentity{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
			ManifestFamilyAuthorization,
		},
		{
			ResourceIdentity{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
			ManifestFamilyAuthorization,
		},
		{ResourceIdentity{Version: "v1", Resource: "namespaces"}, ManifestFamilyNamespace},
		{ResourceIdentity{Version: "v1", Resource: "events"}, ManifestFamilyRefused},
		{
			ResourceIdentity{Group: "events.k8s.io", Version: "v1", Resource: "events"},
			ManifestFamilyRefused,
		},
		{ResourceIdentity{Version: "v1", Resource: "configmaps"}, ManifestFamilyGeneric},
		{
			ResourceIdentity{Group: "apps", Version: "v1", Resource: "deployments"},
			ManifestFamilyGeneric,
		},
		// A CRD in a group that happens to be called `secrets` or `namespaces` is
		// not the core resource and must not borrow its permission.
		{
			ResourceIdentity{Group: "example.com", Version: "v1", Resource: "secrets"},
			ManifestFamilyGeneric,
		},
		{
			ResourceIdentity{Group: "example.com", Version: "v1", Resource: "namespaces"},
			ManifestFamilyGeneric,
		},
	}
	for _, testCase := range testCases {
		if got := ManifestFamilyFor(testCase.resource); got != testCase.want {
			t.Errorf(
				"ManifestFamilyFor(%v) = %q, want %q",
				testCase.resource, got, testCase.want,
			)
		}
	}
}

// A grant covering everything the generic path can do must not reach a Secret, a
// RoleBinding or a Namespace. This is the check that keeps the manifest endpoint
// from being a way around three permissions.
func TestManifestRequirementsAreNotSatisfiedByTheResourcePermissions(t *testing.T) {
	t.Parallel()

	access := NewManifestAccess(nil, ManifestGrant{
		ResourceCreate: true,
		ResourceUpdate: true,
		ResourceDelete: true,
	})
	testCases := []struct {
		name        string
		resource    ResourceIdentity
		requirement ManifestRequirement
	}{
		{
			name:        "Secret",
			resource:    ResourceIdentity{Version: "v1", Resource: "secrets"},
			requirement: ManifestRequirementSecretManage,
		},
		{
			name: "RoleBinding",
			resource: ResourceIdentity{
				Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings",
			},
			requirement: ManifestRequirementRBACManage,
		},
		{
			name:        "Namespace",
			resource:    ResourceIdentity{Version: "v1", Resource: "namespaces"},
			requirement: ManifestRequirementNamespaceManage,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			for _, creating := range []bool{true, false} {
				requirement, allowed, err := access.RequirementForApply(
					testCase.resource, creating,
				)
				if err != nil {
					t.Fatalf("RequirementForApply() = %v", err)
				}
				if allowed {
					t.Fatalf(
						"the resource permissions were enough to apply a %s",
						testCase.name,
					)
				}
				if requirement != testCase.requirement {
					t.Fatalf("requirement = %q, want %q", requirement, testCase.requirement)
				}
			}
			requirement, allowed, err := access.RequirementForDelete(testCase.resource)
			if err != nil {
				t.Fatalf("RequirementForDelete() = %v", err)
			}
			if allowed {
				t.Fatalf("the resource permissions were enough to delete a %s", testCase.name)
			}
			if requirement != testCase.requirement {
				t.Fatalf("delete requirement = %q, want %q", requirement, testCase.requirement)
			}
		})
	}
}

// Creating and changing an object are different permissions for the generic
// family, and the manifest endpoint has to keep them apart even though
// server-side Apply performs both with one request.
func TestGenericApplySplitsCreateFromUpdate(t *testing.T) {
	t.Parallel()

	configMap := ResourceIdentity{Version: "v1", Resource: "configmaps"}
	creator := NewManifestAccess(nil, ManifestGrant{ResourceCreate: true})
	requirement, allowed, _ := creator.RequirementForApply(configMap, true)
	if !allowed || requirement != ManifestRequirementResourceCreate {
		t.Fatalf("creating: requirement=%q allowed=%v", requirement, allowed)
	}
	if _, allowed, _ := creator.RequirementForApply(configMap, false); allowed {
		t.Fatal("cluster.resource.create was enough to change an existing object")
	}

	updater := NewManifestAccess(nil, ManifestGrant{ResourceUpdate: true})
	requirement, allowed, _ = updater.RequirementForApply(configMap, false)
	if !allowed || requirement != ManifestRequirementResourceUpdate {
		t.Fatalf("updating: requirement=%q allowed=%v", requirement, allowed)
	}
	if _, allowed, _ := updater.RequirementForApply(configMap, true); allowed {
		t.Fatal("cluster.resource.update was enough to create an object")
	}
}

// Events have a dedicated read-only surface and a permission of their own; no
// grant makes them writable from a manifest.
func TestManifestRefusesEventsWhateverTheGrant(t *testing.T) {
	t.Parallel()

	access := NewManifestAccess(nil, ManifestGrant{
		ResourceCreate: true, ResourceUpdate: true, ResourceDelete: true,
		NamespaceManage: true, SecretRead: true, SecretManage: true, RBACManage: true,
	})
	events := ResourceIdentity{Version: "v1", Resource: "events"}
	if _, _, err := access.RequirementForApply(events, true); !errors.Is(err, ErrManifestResourceRefused) {
		t.Fatalf("RequirementForApply(events) = %v, want ErrManifestResourceRefused", err)
	}
	if _, _, err := access.RequirementForDelete(events); !errors.Is(err, ErrManifestResourceRefused) {
		t.Fatalf("RequirementForDelete(events) = %v, want ErrManifestResourceRefused", err)
	}
}

// A manifest reaching the write path for every family is exactly where the
// families' own rules have to be reapplied, or holding a family's permission
// would be a way around the checks its typed API makes.
func TestManifestApplyGuardsKeepTheFamilyRules(t *testing.T) {
	t.Parallel()

	access := NewManifestAccess(nil, ManifestGrant{
		SecretManage: true, RBACManage: true,
	})
	platformLabelled := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      "credentials",
			"namespace": "team-a",
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "zke-server",
			},
		},
		"type": "Opaque",
	}
	plainSecret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "credentials", "namespace": "team-a"},
		"type":       "Opaque",
	}

	// An object may not award itself ZKE's ownership label.
	if err := access.guardApply(ManifestFamilySecret, nil, platformLabelled); !errors.Is(err, ErrPlatformLabelClaimed) {
		t.Fatalf("claiming the platform label = %v, want ErrPlatformLabelClaimed", err)
	}
	// An existing ZKE-labelled Secret remains writable after authorization.
	if err := access.guardApply(ManifestFamilySecret, platformLabelled, plainSecret); err != nil {
		t.Fatalf("writing a ZKE Secret = %v", err)
	}
	// `type` is fixed at creation, and the manifest path says so rather than
	// letting the rejection arrive as a message about a field nobody touched.
	retyped := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "credentials", "namespace": "team-a"},
		"type":       "kubernetes.io/dockerconfigjson",
	}
	if err := access.guardApply(ManifestFamilySecret, plainSecret, retyped); !errors.Is(err, ErrSecretTypeImmutable) {
		t.Fatalf("changing a Secret type = %v, want ErrSecretTypeImmutable", err)
	}
	// A creation has no live object to compare against and must still pass.
	if err := access.guardApply(ManifestFamilySecret, nil, plainSecret); err != nil {
		t.Fatalf("creating a Secret = %v", err)
	}
}

// A PolicyRule is a way to hand access to somebody else, so writing one requires
// holding what it hands out. Without this, a manifest would turn
// `cluster.rbac.manage` into every Secret permission in the platform.
func TestManifestAuthorizationGuardRefusesGrantingSecretAccessTheCallerLacks(t *testing.T) {
	t.Parallel()

	role := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "Role",
		"metadata":   map[string]any{"name": "reader", "namespace": "team-a"},
		"rules": []any{
			map[string]any{
				"apiGroups": []any{""},
				"resources": []any{"secrets"},
				"verbs":     []any{"get"},
			},
		},
	}

	withoutSecrets := NewManifestAccess(nil, ManifestGrant{RBACManage: true})
	if err := withoutSecrets.guardApply(ManifestFamilyAuthorization, nil, role); !errors.Is(err, ErrSecretRuleForbidden) {
		t.Fatalf("granting Secret reads without holding them = %v, want ErrSecretRuleForbidden", err)
	}

	withSecrets := NewManifestAccess(nil, ManifestGrant{
		RBACManage: true, SecretRead: true,
	})
	if err := withSecrets.guardApply(ManifestFamilyAuthorization, nil, role); err != nil {
		t.Fatalf("granting Secret reads while holding them = %v", err)
	}
}

// The RoleRef check is about a change, so it must not fire on a creation — a
// RoleRef cannot have moved when there was nothing to move it from.
func TestManifestAuthorizationGuardAllowsCreatingABinding(t *testing.T) {
	t.Parallel()

	access := NewManifestAccess(nil, ManifestGrant{RBACManage: true})
	binding := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "RoleBinding",
		"metadata":   map[string]any{"name": "readers", "namespace": "team-a"},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "Role",
			"name":     "reader",
		},
		"subjects": []any{
			map[string]any{
				"kind":      "ServiceAccount",
				"name":      "runner",
				"namespace": "team-a",
			},
		},
	}
	if err := access.guardApply(ManifestFamilyAuthorization, nil, binding); err != nil {
		t.Fatalf("creating a RoleBinding = %v", err)
	}

	repointed := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "RoleBinding",
		"metadata":   map[string]any{"name": "readers", "namespace": "team-a"},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "Role",
			"name":     "writer",
		},
		"subjects": []any{
			map[string]any{
				"kind":      "ServiceAccount",
				"name":      "runner",
				"namespace": "team-a",
			},
		},
	}
	if err := access.guardApply(ManifestFamilyAuthorization, binding, repointed); !errors.Is(err, ErrRoleRefImmutable) {
		t.Fatalf("repointing an existing RoleBinding = %v, want ErrRoleRefImmutable", err)
	}
}

// Managed labels are informational after the independent permissions pass.
func TestManifestDeleteGuardKeepsClusterScopedManagedBoundary(t *testing.T) {
	t.Parallel()

	access := NewManifestAccess(nil, ManifestGrant{
		SecretManage: true, RBACManage: true,
	})
	managed := map[string]any{
		"metadata": map[string]any{
			"name": "zke-agent",
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "zke-server",
			},
		},
	}
	if err := access.guardDelete(ManifestFamilyAuthorization, managed); !errors.Is(err, ErrManagedResource) {
		t.Fatalf("deleting a ZKE ClusterRole = %v, want ErrManagedResource", err)
	}
	managedNamespaced := map[string]any{
		"metadata": map[string]any{
			"name":      "zke-agent",
			"namespace": "zke-system",
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "zke-server",
			},
		},
	}
	if err := access.guardDelete(ManifestFamilyAuthorization, managedNamespaced); err != nil {
		t.Fatalf("deleting a namespaced ZKE Role = %v", err)
	}
	if err := access.guardDelete(ManifestFamilySecret, managed); err != nil {
		t.Fatalf("deleting a ZKE Secret = %v", err)
	}
}

// A delete with no live object to check against is a delete that may land on a
// same-named object created since the manifest was read.
func TestManifestDeleteRequiresThePreconditionsItWasPlannedWith(t *testing.T) {
	t.Parallel()

	access := NewManifestAccess(&Service{}, ManifestGrant{ResourceDelete: true})
	err := access.DeleteResource(t.Context(), ManifestDeleteInput{
		ClusterID: "0f6f2c2a-6b1a-4a0c-9d3a-4a1f2b3c4d5e",
		Resource:  ResourceIdentity{Version: "v1", Resource: "configmaps"},
		Namespace: "team-a",
		Name:      "settings",
		Current:   map[string]any{"metadata": map[string]any{"name": "settings"}},
		Confirm:   true,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("DeleteResource() without a UID = %v, want ErrInvalidInput", err)
	}
}
