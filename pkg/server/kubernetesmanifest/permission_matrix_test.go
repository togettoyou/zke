package kubernetesmanifest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

// One document of each family, in a manifest that reads like the ones operators
// actually paste: a Namespace, the configuration in it, the identity it runs as,
// the grants that identity holds, and the workload.
//
// The families are the point. Four different permissions are needed to apply
// this file, and the endpoint has to require all four — a manifest is the only
// place in the platform where one request touches families that the typed APIs
// deliberately keep behind separate permissions.
func manifestOfEveryFamily() string {
	return strings.Join([]string{
		document("v1", "Namespace", "", "test-env"),
		document("v1", "ConfigMap", "test-env", "app-config"),
		document("v1", "Secret", "test-env", "app-secret"),
		document("v1", "ServiceAccount", "test-env", "app-sa"),
		document("rbac.authorization.k8s.io/v1", "Role", "test-env", "app-role"),
		document("apps/v1", "Deployment", "test-env", "test-app"),
	}, "---\n")
}

// Which permission each document of the manifest above answers to, in order.
var everyFamilyRequirements = []kubernetesresource.ManifestRequirement{
	kubernetesresource.ManifestRequirementNamespaceManage,
	kubernetesresource.ManifestRequirementResourceCreate,
	kubernetesresource.ManifestRequirementSecretManage,
	kubernetesresource.ManifestRequirementRBACManage,
	kubernetesresource.ManifestRequirementRBACManage,
	kubernetesresource.ManifestRequirementResourceCreate,
}

func fullManifestGrant() kubernetesresource.ManifestGrant {
	return kubernetesresource.ManifestGrant{
		ResourceCreate: true, ResourceUpdate: true, ResourceDelete: true,
		NamespaceManage: true, NodeManage: true, SecretRead: true, SecretManage: true,
		RBACManage: true,
	}
}

// A Node reached through a manifest answers to `cluster.node.manage`, the same
// permission the typed and generic routes require for one. Without this the
// manifest endpoint would be the way around it: relabelling every Node in the
// Cluster would need nothing but `cluster.resource.update`, which is also what
// changing one ConfigMap needs.
func TestManifestNodeDocumentAnswersToTheNodePermission(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 20, FieldManager: "zke-manifest"})
	manifest := document("v1", "Node", "", "worker-1")

	for _, operation := range []Operation{OperationApply, OperationDelete} {
		t.Run(string(operation), func(t *testing.T) {
			t.Parallel()

			// Every resource permission there is, and no Node permission.
			access := newFakeAccess()
			access.grant = fullManifestGrant()
			access.grant.NodeManage = false
			access.existing["v1/nodes//worker-1"] = liveObject("uid-node", "1")

			result, err := service.Execute(context.Background(), access, Input{
				ClusterID: testClusterID,
				Manifest:  []byte(manifest),
				Operation: operation,
				Confirm:   true,
			})
			if err != nil {
				t.Fatalf("Execute() = %v", err)
			}
			if result.Allowed {
				t.Fatal("a Node document was allowed without cluster.node.manage")
			}
			if len(access.applied) != 0 || len(access.deleted) != 0 {
				t.Fatal("a refused Node document reached the Cluster")
			}
			if got := result.Documents[0].Requirement; got != kubernetesresource.ManifestRequirementNodeManage {
				t.Fatalf("requirement = %q, want %q", got, kubernetesresource.ManifestRequirementNodeManage)
			}

			// The same document with the Node permission goes through, so the
			// refusal above is about that permission and not about the document.
			access = newFakeAccess()
			access.grant = fullManifestGrant()
			access.existing["v1/nodes//worker-1"] = liveObject("uid-node", "1")
			result, err = service.Execute(context.Background(), access, Input{
				ClusterID: testClusterID,
				Manifest:  []byte(manifest),
				Operation: operation,
				Confirm:   true,
			})
			if err != nil {
				t.Fatalf("Execute() = %v", err)
			}
			if !result.Allowed || result.Failed {
				t.Fatalf("allowed = %v failed = %v", result.Allowed, result.Failed)
			}
		})
	}
}

// Dropping any one of the four permissions this manifest needs must refuse the
// whole manifest — and must refuse exactly the documents that answer to the
// dropped permission, so the operator is told which permission to go and ask for
// rather than that "something" was denied.
func TestManifestRefusesWhenAnySingleRequiredPermissionIsMissing(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 20, FieldManager: "zke-manifest"})
	drops := []struct {
		name        string
		drop        func(*kubernetesresource.ManifestGrant)
		requirement kubernetesresource.ManifestRequirement
	}{
		{
			name:        "without cluster.namespace.manage",
			drop:        func(g *kubernetesresource.ManifestGrant) { g.NamespaceManage = false },
			requirement: kubernetesresource.ManifestRequirementNamespaceManage,
		},
		{
			name: "without cluster.resource.create",
			drop: func(g *kubernetesresource.ManifestGrant) {
				g.ResourceCreate = false
			},
			requirement: kubernetesresource.ManifestRequirementResourceCreate,
		},
		{
			name:        "without cluster.secret.manage",
			drop:        func(g *kubernetesresource.ManifestGrant) { g.SecretManage = false },
			requirement: kubernetesresource.ManifestRequirementSecretManage,
		},
		{
			name:        "without cluster.rbac.manage",
			drop:        func(g *kubernetesresource.ManifestGrant) { g.RBACManage = false },
			requirement: kubernetesresource.ManifestRequirementRBACManage,
		},
	}
	for _, testCase := range drops {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			access := newFakeAccess()
			access.grant = fullManifestGrant()
			testCase.drop(&access.grant)

			result, err := service.Execute(context.Background(), access, Input{
				ClusterID: testClusterID,
				Manifest:  []byte(manifestOfEveryFamily()),
				Operation: OperationApply,
				Confirm:   true,
			})
			if err != nil {
				t.Fatalf("Execute() = %v", err)
			}
			if result.Allowed {
				t.Fatal("a manifest holding an uncovered document was reported as allowed")
			}
			// Nothing at all reached the Cluster. Applying the documents the caller
			// *was* allowed to write would be a partial apply chosen by permissions
			// rather than by the operator.
			if len(access.applied) != 0 {
				t.Fatalf("a refused manifest wrote %d documents", len(access.applied))
			}
			for index, document := range result.Documents {
				wantRefused := everyFamilyRequirements[index] == testCase.requirement
				refused := document.Status == StatusRefused
				if refused != wantRefused {
					t.Errorf(
						"document %d (%s) refused = %v, want %v",
						index, document.Kind, refused, wantRefused,
					)
				}
				if refused && document.Requirement != testCase.requirement {
					t.Errorf(
						"document %d (%s) names requirement %q, want %q",
						index, document.Kind, document.Requirement, testCase.requirement,
					)
				}
			}
		})
	}
}

// The same manifest under a grant holding all four applies completely. Without
// this the test above would still pass if the endpoint refused everything.
func TestManifestAppliesEveryFamilyUnderAFullGrant(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	access.grant = fullManifestGrant()
	service := NewService(Config{MaxDocuments: 20, FieldManager: "zke-manifest"})

	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifestOfEveryFamily()),
		Operation: OperationApply,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if !result.Allowed || result.Failed {
		t.Fatalf("allowed = %v failed = %v", result.Allowed, result.Failed)
	}
	if len(access.applied) != len(everyFamilyRequirements) {
		t.Fatalf("applied %d documents, want %d", len(access.applied), len(everyFamilyRequirements))
	}
	for index, document := range result.Documents {
		if document.Status != StatusSucceeded {
			t.Errorf("document %d (%s) status = %q", index, document.Kind, document.Status)
		}
		if document.Requirement != everyFamilyRequirements[index] {
			t.Errorf(
				"document %d (%s) requirement = %q, want %q",
				index, document.Kind, document.Requirement, everyFamilyRequirements[index],
			)
		}
	}
}

// Deleting the same manifest answers to the delete permissions, which are not
// the apply ones for the generic family and are the same ones for the families
// that have a single permission.
func TestManifestDeleteRequiresTheDeletePermissions(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 20, FieldManager: "zke-manifest"})
	existing := func() *fakeAccess {
		access := newFakeAccess()
		access.grant = fullManifestGrant()
		for _, key := range []string{
			"v1/namespaces//test-env",
			"v1/configmaps/test-env/app-config",
			"v1/secrets/test-env/app-secret",
			"v1/serviceaccounts/test-env/app-sa",
			"v1/roles/test-env/app-role",
			"v1/deployments/test-env/test-app",
		} {
			access.existing[key] = liveObject("uid-"+key, "1")
		}
		return access
	}

	// A caller who may create everything but delete nothing gets the whole
	// manifest refused: create and delete are different permissions, and a delete
	// that accepted the create permission would be the platform's clearest
	// boundary quietly removed.
	access := existing()
	access.grant.ResourceDelete = false
	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifestOfEveryFamily()),
		Operation: OperationDelete,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if result.Allowed {
		t.Fatal("cluster.resource.create was enough to delete generic resources")
	}
	if len(access.deleted) != 0 {
		t.Fatalf("a refused delete removed %d objects", len(access.deleted))
	}

	// With the delete permission the whole manifest goes, in reverse order.
	access = existing()
	result, err = service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifestOfEveryFamily()),
		Operation: OperationDelete,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if !result.Allowed || result.Failed {
		t.Fatalf("allowed = %v failed = %v", result.Allowed, result.Failed)
	}
	if len(access.deleted) != len(everyFamilyRequirements) {
		t.Fatalf("deleted %d objects, want %d", len(access.deleted), len(everyFamilyRequirements))
	}
	// The Namespace is written first in the file and must be removed last, or it
	// takes everything the rest of the file names with it.
	if access.deleted[0].Name != "test-app" ||
		access.deleted[len(access.deleted)-1].Name != "test-env" {
		t.Fatalf(
			"deleted %q first and %q last; want the workload first and the Namespace last",
			access.deleted[0].Name, access.deleted[len(access.deleted)-1].Name,
		)
	}
}

// Events resolve to a real resource and are still refused, whatever the caller
// holds. They have a dedicated read-only surface and a permission of their own,
// and a manifest that wrote them would be writing a stream nothing reads back.
func TestManifestRefusesEventsUnderEveryGrant(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	access.grant = fullManifestGrant()
	service := NewService(Config{MaxDocuments: 20, FieldManager: "zke-manifest"})

	manifest := document("v1", "ConfigMap", "test-env", "app-config") +
		"---\n" + document("v1", "Event", "test-env", "something-happened")
	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifest),
		Operation: OperationApply,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if result.Documents[1].Status != StatusInvalid {
		t.Fatalf("Event status = %q, want invalid", result.Documents[1].Status)
	}
	if !errors.Is(result.Documents[1].Err, kubernetesresource.ErrManifestResourceRefused) {
		t.Fatalf("Event error = %v", result.Documents[1].Err)
	}
	// An unwritable document is a broken manifest, so nothing in it is written —
	// the same whole-request rule a refusal follows.
	if len(access.applied) != 0 {
		t.Fatalf("a manifest holding an Event wrote %d documents", len(access.applied))
	}
}

// A grant covering every generic write must not reach the three families that
// have permissions of their own — the case the whole per-document design exists
// for, stated against a realistic manifest rather than one resource at a time.
func TestGenericPermissionsReachNoGuardedFamily(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	access.grant = kubernetesresource.ManifestGrant{
		ResourceCreate: true, ResourceUpdate: true, ResourceDelete: true,
	}
	service := NewService(Config{MaxDocuments: 20, FieldManager: "zke-manifest"})

	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifestOfEveryFamily()),
		Operation: OperationApply,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if result.Allowed {
		t.Fatal("the generic permissions alone were enough for the whole manifest")
	}
	guarded := map[string]struct{}{
		"Namespace": {}, "Secret": {}, "ServiceAccount": {}, "Role": {},
	}
	for _, document := range result.Documents {
		_, isGuarded := guarded[document.Kind]
		refused := document.Status == StatusRefused
		if refused != isGuarded {
			t.Errorf(
				"%s refused = %v, want %v",
				document.Kind, refused, isGuarded,
			)
		}
	}
}
