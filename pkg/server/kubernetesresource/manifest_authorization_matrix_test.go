package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

// errManifestProbeAgent marks a request that got past every ZKE check and went
// looking for an Agent. These tests are about the checks, not about the Cluster,
// so reaching this is the positive result.
var errManifestProbeAgent = errors.New("test Agent is not connected")

// A Service whose requester always refuses, so a permitted read fails at the
// Agent rather than panicking on a nil requester. A refused read never gets this
// far, which is the difference the tests below are looking for.
func manifestProbeService() *Service {
	return NewService(&fakeResourceRequester{handle: func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Writer,
	) (*agentv1.ResourceResponse, error) {
		return nil, errManifestProbeAgent
	}})
}

// The manifest endpoint's authorization, checked as a matrix rather than as
// examples.
//
// A manifest is the one write in the platform whose required permission is not
// fixed by its route, so the mapping from resource to permission is the whole of
// its authorization. Examples can only show that the cases somebody thought of
// work; what has to be true is stronger — for every resource family and every
// operation, exactly one permission opens it and none of the others do. That is
// a statement about the entire product of families and permissions, so it is
// tested as one.
//
// The test is written to fail in both directions. A permission that stops
// covering what it should breaks it, and so does a permission that starts
// covering something it should not — which is the failure that matters, because
// it is the one that turns `cluster.resource.create` into a way to write Secrets.

// manifestAuthorizationCase is one resource and the permission each operation on
// it must answer to. An empty requirement means no permission opens it at all.
type manifestAuthorizationCase struct {
	name     string
	resource ResourceIdentity
	create   ManifestRequirement
	update   ManifestRequirement
	remove   ManifestRequirement
}

func (family manifestAuthorizationCase) required(operation string) ManifestRequirement {
	switch operation {
	case "apply/create":
		return family.create
	case "apply/update":
		return family.update
	default:
		return family.remove
	}
}

// Which permission each family answers to. Adding a family without adding it
// here leaves it untested; adding it here without implementing it fails.
var manifestAuthorizationMatrix = []manifestAuthorizationCase{
	{
		name:     "Deployment",
		resource: ResourceIdentity{Group: "apps", Version: "v1", Resource: "deployments"},
		create:   ManifestRequirementResourceCreate,
		update:   ManifestRequirementResourceUpdate,
		remove:   ManifestRequirementResourceDelete,
	},
	{
		name:     "ConfigMap",
		resource: ResourceIdentity{Version: "v1", Resource: "configmaps"},
		create:   ManifestRequirementResourceCreate,
		update:   ManifestRequirementResourceUpdate,
		remove:   ManifestRequirementResourceDelete,
	},
	{
		name:     "Service",
		resource: ResourceIdentity{Version: "v1", Resource: "services"},
		create:   ManifestRequirementResourceCreate,
		update:   ManifestRequirementResourceUpdate,
		remove:   ManifestRequirementResourceDelete,
	},
	{
		name: "Ingress",
		resource: ResourceIdentity{
			Group: "networking.k8s.io", Version: "v1", Resource: "ingresses",
		},
		create: ManifestRequirementResourceCreate,
		update: ManifestRequirementResourceUpdate,
		remove: ManifestRequirementResourceDelete,
	},
	{
		name:     "PersistentVolumeClaim",
		resource: ResourceIdentity{Version: "v1", Resource: "persistentvolumeclaims"},
		create:   ManifestRequirementResourceCreate,
		update:   ManifestRequirementResourceUpdate,
		remove:   ManifestRequirementResourceDelete,
	},
	{
		// A CustomResource is generic: ZKE does not know its semantics, and no
		// permission of its own guards it.
		name: "custom resource",
		resource: ResourceIdentity{
			Group: "example.com", Version: "v1", Resource: "widgets",
		},
		create: ManifestRequirementResourceCreate,
		update: ManifestRequirementResourceUpdate,
		remove: ManifestRequirementResourceDelete,
	},
	{
		// Creating and changing collapse into one permission: a permission that
		// covers bringing a Secret into existence covers changing it too.
		name:     "Secret",
		resource: ResourceIdentity{Version: "v1", Resource: "secrets"},
		create:   ManifestRequirementSecretManage,
		update:   ManifestRequirementSecretManage,
		remove:   ManifestRequirementSecretManage,
	},
	{
		name:     "ServiceAccount",
		resource: ResourceIdentity{Version: "v1", Resource: "serviceaccounts"},
		create:   ManifestRequirementRBACManage,
		update:   ManifestRequirementRBACManage,
		remove:   ManifestRequirementRBACManage,
	},
	{
		name: "Role",
		resource: ResourceIdentity{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles",
		},
		create: ManifestRequirementRBACManage,
		update: ManifestRequirementRBACManage,
		remove: ManifestRequirementRBACManage,
	},
	{
		name: "ClusterRole",
		resource: ResourceIdentity{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles",
		},
		create: ManifestRequirementRBACManage,
		update: ManifestRequirementRBACManage,
		remove: ManifestRequirementRBACManage,
	},
	{
		name: "RoleBinding",
		resource: ResourceIdentity{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings",
		},
		create: ManifestRequirementRBACManage,
		update: ManifestRequirementRBACManage,
		remove: ManifestRequirementRBACManage,
	},
	{
		name: "ClusterRoleBinding",
		resource: ResourceIdentity{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings",
		},
		create: ManifestRequirementRBACManage,
		update: ManifestRequirementRBACManage,
		remove: ManifestRequirementRBACManage,
	},
	{
		// Apply creates the object when it is absent, so an existing Namespace
		// does not lower what applying one costs — otherwise applying twice would
		// be a way to satisfy the weaker permission.
		name:     "Namespace",
		resource: ResourceIdentity{Version: "v1", Resource: "namespaces"},
		create:   ManifestRequirementNamespaceManage,
		update:   ManifestRequirementNamespaceManage,
		remove:   ManifestRequirementNamespaceManage,
	},
	{
		name:     "core Event",
		resource: ResourceIdentity{Version: "v1", Resource: "events"},
	},
	{
		name: "events.k8s.io Event",
		resource: ResourceIdentity{
			Group: "events.k8s.io", Version: "v1", Resource: "events",
		},
	},
}

// Every permission the manifest path knows about, each on its own. Testing them
// singly is what makes the negative half meaningful: a grant holding two
// permissions cannot tell which one opened the door.
var manifestGrantsByRequirement = map[ManifestRequirement]ManifestGrant{
	ManifestRequirementResourceCreate:  {ResourceCreate: true},
	ManifestRequirementResourceUpdate:  {ResourceUpdate: true},
	ManifestRequirementResourceDelete:  {ResourceDelete: true},
	ManifestRequirementNamespaceManage: {NamespaceManage: true},
	ManifestRequirementSecretManage:    {SecretManage: true},
	ManifestRequirementRBACManage:      {RBACManage: true},
}

func TestManifestAuthorizationMatrix(t *testing.T) {
	t.Parallel()

	// `cluster.secret.read` is in the grant but opens no write. It is there so a
	// PolicyRule handing out Secret reads can be checked against what the caller
	// holds — never so that holding it writes anything.
	grants := map[string]ManifestGrant{"secret_read_only": {SecretRead: true}}
	for requirement, grant := range manifestGrantsByRequirement {
		grants[string(requirement)] = grant
	}

	for _, family := range manifestAuthorizationMatrix {
		t.Run(family.name, func(t *testing.T) {
			t.Parallel()
			operations := []struct {
				name     string
				required ManifestRequirement
				ask      func(*ManifestAccess) (ManifestRequirement, bool, error)
			}{
				{
					name: "apply/create", required: family.create,
					ask: func(access *ManifestAccess) (ManifestRequirement, bool, error) {
						return access.RequirementForApply(family.resource, true)
					},
				},
				{
					name: "apply/update", required: family.update,
					ask: func(access *ManifestAccess) (ManifestRequirement, bool, error) {
						return access.RequirementForApply(family.resource, false)
					},
				},
				{
					name: "delete", required: family.remove,
					ask: func(access *ManifestAccess) (ManifestRequirement, bool, error) {
						return access.RequirementForDelete(family.resource)
					},
				},
			}
			for _, operation := range operations {
				for grantName, grant := range grants {
					access := NewManifestAccess(nil, grant)
					requirement, allowed, err := operation.ask(access)

					// A family no permission opens is refused before any grant is
					// consulted, and refused identically for every grant.
					if family.required(operation.name) == "" {
						if !errors.Is(err, ErrManifestResourceRefused) {
							t.Errorf(
								"%s %s with %s: err = %v, want ErrManifestResourceRefused",
								family.name, operation.name, grantName, err,
							)
						}
						continue
					}
					if err != nil {
						t.Errorf(
							"%s %s with %s: err = %v",
							family.name, operation.name, grantName, err,
						)
						continue
					}
					// The reported requirement never depends on the grant: it is a
					// property of the resource, and it is what the operator is told
					// to go and ask for.
					if requirement != operation.required {
						t.Errorf(
							"%s %s with %s: requirement = %q, want %q",
							family.name, operation.name, grantName,
							requirement, operation.required,
						)
					}
					wantAllowed := grantName == string(operation.required)
					if allowed != wantAllowed {
						verb := "did not open"
						if allowed {
							verb = "opened"
						}
						t.Errorf(
							"%s %s: %s %s it; only %s should",
							family.name, operation.name, grantName, verb,
							operation.required,
						)
					}
				}
			}
		})
	}
}

// A grant holding everything must still not reach the families that answer to no
// permission — the matrix above checks single permissions, and this closes the
// one case it cannot express.
func TestManifestAuthorizationRefusesEventsUnderAFullGrant(t *testing.T) {
	t.Parallel()

	full := ManifestGrant{
		ResourceCreate: true, ResourceUpdate: true, ResourceDelete: true,
		NamespaceManage: true, SecretRead: true, SecretManage: true, RBACManage: true,
	}
	access := NewManifestAccess(manifestProbeService(), full)
	for _, family := range manifestAuthorizationMatrix {
		if family.create != "" {
			continue
		}
		if _, _, err := access.RequirementForApply(family.resource, true); !errors.Is(
			err, ErrManifestResourceRefused,
		) {
			t.Errorf("apply %s under a full grant: err = %v", family.name, err)
		}
		if _, _, err := access.RequirementForDelete(family.resource); !errors.Is(
			err, ErrManifestResourceRefused,
		) {
			t.Errorf("delete %s under a full grant: err = %v", family.name, err)
		}
		// The read a plan performs is refused too, so a refused family cannot even
		// be probed for existence through this path.
		if _, err := access.GetResource(t.Context(), ManifestGetInput{
			ClusterID: testClusterID,
			Resource:  family.resource,
			Namespace: "team-a",
			Name:      "anything",
		}); !errors.Is(err, ErrManifestResourceRefused) {
			t.Errorf("read %s under a full grant: err = %v", family.name, err)
		}
	}
}

// Reading a Secret during planning is its own question from writing one: the
// plan needs the live object to compare against, and either Secret permission
// admits that read while neither being held refuses it.
func TestManifestSecretReadDuringPlanningRequiresASecretPermission(t *testing.T) {
	t.Parallel()

	secret := ResourceIdentity{Version: "v1", Resource: "secrets"}
	testCases := []struct {
		name    string
		grant   ManifestGrant
		refused bool
	}{
		{name: "no Secret permission", grant: ManifestGrant{
			ResourceCreate: true, ResourceUpdate: true, ResourceDelete: true,
			NamespaceManage: true, RBACManage: true,
		}, refused: true},
		{name: "read only", grant: ManifestGrant{SecretRead: true}},
		{name: "manage only", grant: ManifestGrant{SecretManage: true}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewManifestAccess(manifestProbeService(), testCase.grant).GetResource(
				t.Context(),
				ManifestGetInput{
					ClusterID: testClusterID,
					Resource:  secret,
					Namespace: "team-a",
					Name:      "credentials",
				},
			)
			if testCase.refused {
				if !errors.Is(err, ErrManifestForbidden) {
					t.Fatalf("err = %v, want ErrManifestForbidden", err)
				}
				return
			}
			// Permitted, so it gets as far as needing an Agent — which is exactly
			// as far as this test can follow it.
			if errors.Is(err, ErrManifestForbidden) {
				t.Fatal("a held Secret permission was refused the planning read")
			}
		})
	}
}
