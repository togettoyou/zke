package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ErrManifestForbidden is a document the caller's permissions do not cover.
//
// Separate from every transport and Kubernetes failure because it is neither: the
// request was understood, the document is well formed, and the Cluster was never
// asked. Only the grant decides it, and only granting the missing permission
// changes the answer.
var ErrManifestForbidden = errors.New("manifest document is not covered by the caller's permissions")

// ErrManifestResourceRefused is a document naming a resource ZKE does not write
// through any API, whatever the caller holds. Events are the whole set: they have
// a dedicated read-only surface and their own permission, and a manifest that
// wrote them would be writing a stream nothing reads back.
var ErrManifestResourceRefused = errors.New("Kubernetes resource cannot be written from a manifest")

// ManifestGrant is what the caller may do on one Cluster, resolved once per
// request before any document is looked at.
//
// A manifest carries objects of every family at once, and the families do not
// answer to the same permission: this is the whole reason a manifest endpoint
// cannot be guarded by a single route-level check the way every other write in
// the platform is. The grant is the resolved answer to every question the
// documents might raise, so the decision below is a field lookup rather than a
// round trip per document.
//
// Booleans rather than the permission names themselves, for the same reason
// middleware.SecretGrant uses them: this package must not depend on
// `pkg/server/rbac`, and the names belong to the layer that reads them.
type ManifestGrant struct {
	AgentNamespace        string
	ResourceCreate        bool
	ResourceUpdate        bool
	ResourceDelete        bool
	NamespaceManage       bool
	NodeManage            bool
	SecretRead            bool
	SecretManage          bool
	RBACManage            bool
	SystemNamespaceManage bool
	AgentNamespaceManage  bool
}

// ManifestFamily names which set of rules a document answers to. It mirrors the
// split the typed APIs already make — a Secret is not a ConfigMap with different
// contents, and a RoleBinding is not a Deployment — so that a manifest cannot
// reach through the generic path what the typed paths keep behind their own
// permissions.
type ManifestFamily string

const (
	ManifestFamilyGeneric       ManifestFamily = "generic"
	ManifestFamilyNamespace     ManifestFamily = "namespace"
	ManifestFamilyNode          ManifestFamily = "node"
	ManifestFamilySecret        ManifestFamily = "secret"
	ManifestFamilyAuthorization ManifestFamily = "authorization"
	// Refused outright, regardless of the grant.
	ManifestFamilyRefused ManifestFamily = "refused"
)

// ManifestRequirement names the permission a document answers to, without
// naming it in this package's vocabulary. The HTTP layer maps it to the
// `cluster.*` permission an operator sees; keeping the mapping there means the
// permission names live in exactly one place.
type ManifestRequirement string

const (
	ManifestRequirementResourceCreate        ManifestRequirement = "resource_create"
	ManifestRequirementResourceUpdate        ManifestRequirement = "resource_update"
	ManifestRequirementResourceDelete        ManifestRequirement = "resource_delete"
	ManifestRequirementNamespaceManage       ManifestRequirement = "namespace_manage"
	ManifestRequirementNodeManage            ManifestRequirement = "node_manage"
	ManifestRequirementSecretManage          ManifestRequirement = "secret_manage"
	ManifestRequirementRBACManage            ManifestRequirement = "rbac_manage"
	ManifestRequirementSystemNamespaceManage ManifestRequirement = "system_namespace_manage"
	ManifestRequirementAgentNamespaceManage  ManifestRequirement = "agent_namespace_manage"
)

type ManifestTarget struct {
	Namespace string
	Name      string
}

// ManifestFamilyFor answers which family a resource belongs to.
//
// The order matters: Secrets and the authorization resources are checked before
// anything else, because they are the two families the generic path refuses and
// the two whose permissions a manifest must not be able to skip.
func ManifestFamilyFor(resource ResourceIdentity) ManifestFamily {
	switch {
	case resource.Group == "" && resource.Resource == "secrets":
		return ManifestFamilySecret
	case IsAuthorizationResourceIdentity(resource):
		return ManifestFamilyAuthorization
	case resource.Resource == "events" &&
		(resource.Group == "" || resource.Group == "events.k8s.io"):
		return ManifestFamilyRefused
	case resource.Group == "" && resource.Resource == "namespaces":
		return ManifestFamilyNamespace
	case resource.Group == "" && resource.Resource == "nodes":
		return ManifestFamilyNode
	default:
		return ManifestFamilyGeneric
	}
}

// ManifestAccess applies and deletes manifest documents under one resolved
// grant.
//
// It exists because `secretAccess` is unexported: a manifest service living
// outside this package cannot set it. This type opens that flag only after the
// matching Secret grant is checked. Namespace permissions are resolved from
// the manifest target before a mutation reaches the generic resource service.
type ManifestAccess struct {
	service *Service
	grant   ManifestGrant
}

func NewManifestAccess(service *Service, grant ManifestGrant) *ManifestAccess {
	return &ManifestAccess{service: service, grant: grant}
}

// RequirementForApply reports which permission applying this resource answers to
// and whether the grant covers it.
//
// `creating` is the difference between `cluster.resource.create` and
// `cluster.resource.update` for the generic family, and it comes from a read the
// caller already performed: server-side Apply itself does not say which of the
// two it will do, so the answer has to be established before the write rather
// than inferred from it. Where the two collapse into one permission — Secrets,
// authorization, Namespaces — the flag is not consulted, because a permission
// that covers creating an object also covers changing it.
func (access *ManifestAccess) RequirementForApply(
	resource ResourceIdentity,
	creating bool,
	targets ...ManifestTarget,
) (ManifestRequirement, bool, error) {
	family := ManifestFamilyFor(resource)
	if requirement, allowed, protected := access.protectedNamespaceRequirement(resource, targets); protected {
		if family == ManifestFamilyRefused {
			return "", false, ErrManifestResourceRefused
		}
		if !allowed {
			return requirement, false, nil
		}
		// The protected Namespace permission replaces ordinary resource and
		// Namespace lifecycle permissions. Sensitive resource families retain
		// their own permission as an additional boundary.
		if family != ManifestFamilySecret && family != ManifestFamilyAuthorization {
			return requirement, true, nil
		}
	}
	switch family {
	case ManifestFamilyRefused:
		return "", false, ErrManifestResourceRefused
	case ManifestFamilySecret:
		return ManifestRequirementSecretManage, access.grant.SecretManage, nil
	case ManifestFamilyAuthorization:
		return ManifestRequirementRBACManage, access.grant.RBACManage, nil
	case ManifestFamilyNamespace:
		// Applying an ordinary Namespace is `cluster.namespace.manage` whether or
		// not it exists. Protected targets returned above use their own permission.
		return ManifestRequirementNamespaceManage, access.grant.NamespaceManage, nil
	case ManifestFamilyNode:
		// A Node is Cluster-scoped, so the protected-Namespace branch above never
		// matches one. Creating and changing a Node answer to the same permission:
		// a manifest that registers a Node and one that relabels it both decide
		// where the Cluster's workloads may run.
		return ManifestRequirementNodeManage, access.grant.NodeManage, nil
	default:
		if creating {
			return ManifestRequirementResourceCreate, access.grant.ResourceCreate, nil
		}
		return ManifestRequirementResourceUpdate, access.grant.ResourceUpdate, nil
	}
}

// RequirementForDelete reports which permission deleting this resource answers
// to and whether the grant covers it.
func (access *ManifestAccess) RequirementForDelete(
	resource ResourceIdentity,
	targets ...ManifestTarget,
) (ManifestRequirement, bool, error) {
	family := ManifestFamilyFor(resource)
	if requirement, allowed, protected := access.protectedNamespaceRequirement(resource, targets); protected {
		if family == ManifestFamilyRefused {
			return "", false, ErrManifestResourceRefused
		}
		if !allowed {
			return requirement, false, nil
		}
		if family != ManifestFamilySecret && family != ManifestFamilyAuthorization {
			return requirement, true, nil
		}
	}
	switch family {
	case ManifestFamilyRefused:
		return "", false, ErrManifestResourceRefused
	case ManifestFamilySecret:
		return ManifestRequirementSecretManage, access.grant.SecretManage, nil
	case ManifestFamilyAuthorization:
		return ManifestRequirementRBACManage, access.grant.RBACManage, nil
	case ManifestFamilyNamespace:
		return ManifestRequirementNamespaceManage, access.grant.NamespaceManage, nil
	case ManifestFamilyNode:
		return ManifestRequirementNodeManage, access.grant.NodeManage, nil
	default:
		return ManifestRequirementResourceDelete, access.grant.ResourceDelete, nil
	}
}

func (access *ManifestAccess) protectedNamespaceRequirement(
	resource ResourceIdentity,
	targets []ManifestTarget,
) (ManifestRequirement, bool, bool) {
	if len(targets) == 0 {
		return "", false, false
	}
	target := targets[0]
	namespace := target.Namespace
	if resource.Group == "" && resource.Version == "v1" && resource.Resource == "namespaces" {
		namespace = target.Name
	}
	if access.grant.AgentNamespace != "" && namespace == access.grant.AgentNamespace {
		return ManifestRequirementAgentNamespaceManage, access.grant.AgentNamespaceManage, true
	}
	if strings.HasPrefix(namespace, "kube-") ||
		(resource.Group == "" && resource.Resource == "namespaces" && namespace == "default") {
		return ManifestRequirementSystemNamespaceManage, access.grant.SystemNamespaceManage, true
	}
	return "", false, false
}

// DiscoverResources reports the Cluster's API catalog for manifest resolution.
//
// Unlike Service.DiscoverResources this keeps the resources that method drops,
// because a manifest may legitimately name them and the answer to "which GVR is
// this Kind" must not depend on which permission the caller holds. Whether the
// document may be written is decided afterwards, by the grant.
func (access *ManifestAccess) DiscoverResources(
	ctx context.Context,
	clusterID string,
) (ManifestCatalog, error) {
	if access.service == nil {
		return ManifestCatalog{}, ErrAgentUnsupported
	}
	return access.service.discoverManifestCatalog(ctx, clusterID)
}

// ManifestKind is how a document names its type: the group and version from
// `apiVersion`, plus `kind`. It is the full key on purpose — `Ingress` and
// `Event` each exist in more than one group, so a Kind alone identifies nothing,
// while a document always carries the `apiVersion` that pins it.
type ManifestKind struct {
	Group   string
	Version string
	Kind    string
}

// ManifestCatalogEntry is what resolution needs about one resource: where to
// send the request, whether a Namespace belongs in it, and which verbs the
// Cluster serves — so that "this type cannot be applied here" is reported
// against the document rather than discovered as a rejection from the Agent.
type ManifestCatalogEntry struct {
	Resource   ResourceIdentity
	Namespaced bool
	Verbs      []string
}

// ManifestCatalog resolves the Kinds a manifest names to the resources the
// Cluster serves.
type ManifestCatalog struct {
	Entries map[ManifestKind]ManifestCatalogEntry
	// Partial reports that one or more API groups could not be discovered, so a
	// Kind missing from this catalog may exist in the Cluster after all. It
	// changes what an unresolved Kind means, which is why the caller is told.
	Partial bool
}

// Lookup reports the resource serving a Kind. A zero catalog resolves nothing,
// which refuses every document rather than sending one somewhere unintended.
func (catalog ManifestCatalog) Lookup(kind ManifestKind) (ManifestCatalogEntry, bool) {
	entry, exists := catalog.Entries[kind]
	return entry, exists
}

func (service *Service) discoverManifestCatalog(
	ctx context.Context,
	clusterID string,
) (ManifestCatalog, error) {
	catalog, err := service.requestDiscovery(ctx, clusterID)
	if err != nil {
		return ManifestCatalog{}, err
	}
	entries := make(map[ManifestKind]ManifestCatalogEntry, len(catalog.Resources))
	for _, resource := range catalog.Resources {
		identity := ResourceIdentity{
			Group:    resource.Group,
			Version:  resource.Version,
			Resource: resource.Resource,
		}
		if !validResourceIdentity(identity) ||
			resource.Kind == "" ||
			len(resource.Kind) > 253 {
			continue
		}
		key := ManifestKind{
			Group:   resource.Group,
			Version: resource.Version,
			Kind:    resource.Kind,
		}
		// Discovery can report the same Kind twice within a group and version
		// when a resource and its subresource share it. The first primary
		// resource wins and later ones are ignored rather than overwriting it,
		// so resolution does not depend on map ordering.
		if _, exists := entries[key]; exists {
			continue
		}
		entries[key] = ManifestCatalogEntry{
			Resource:   identity,
			Namespaced: resource.Namespaced,
			Verbs:      supportedVerbs(resource.Verbs),
		}
	}
	// Secrets are declared rather than discovered, because the Agent removes them
	// from the catalog it reports: that catalog feeds the resource browser, which
	// must not offer a type it will refuse to open, and hiding them there is what
	// keeps Secrets reachable only through the API that requires
	// `cluster.secret.manage`.
	//
	// Resolution is a different question from access. "Which resource serves Kind
	// `Secret`" has one fixed answer that no Cluster varies, and leaving it to
	// discovery meant every Secret in a manifest came back as a Kind the Cluster
	// does not serve — a resolution failure worded as if the type did not exist,
	// for the one family whose whole point is that it does exist and is guarded.
	// Whether the document may be written is still decided afterwards, by the
	// grant, exactly as it is for every other entry here.
	entries[ManifestKind{Version: "v1", Kind: "Secret"}] = ManifestCatalogEntry{
		Resource:   secretIdentity,
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	}
	return ManifestCatalog{Entries: entries, Partial: catalog.Partial}, nil
}

type ManifestGetInput struct {
	ClusterID string
	Resource  ResourceIdentity
	Namespace string
	Name      string
}

// GetResource reads the object a document names, or reports ErrResourceNotFound
// when it does not exist.
//
// The read is what tells a create from an update, supplies the UID a delete is
// held to, and gives the guards the live object they compare against. It uses
// the same family decision as the write, so reading a Secret here requires the
// Secret permission just as writing one does.
func (access *ManifestAccess) GetResource(
	ctx context.Context,
	input ManifestGetInput,
) (map[string]any, error) {
	if access.service == nil {
		return nil, ErrAgentUnsupported
	}
	family := ManifestFamilyFor(input.Resource)
	if family == ManifestFamilyRefused {
		return nil, ErrManifestResourceRefused
	}
	if _, allowed, protected := access.protectedNamespaceRequirement(input.Resource, []ManifestTarget{{Namespace: input.Namespace, Name: input.Name}}); protected && !allowed {
		return nil, ErrManifestForbidden
	}
	// Reading a Secret's contents is `cluster.secret.read`, and it is a separate
	// question from writing one: a caller holding only `cluster.secret.manage`
	// still needs the live object to be compared against, so either permission
	// admits the read that the guards below depend on.
	if family == ManifestFamilySecret &&
		!access.grant.SecretRead && !access.grant.SecretManage {
		return nil, ErrManifestForbidden
	}
	return access.service.GetResource(ctx, GetResourceInput{
		ClusterID:    input.ClusterID,
		Resource:     input.Resource,
		Namespace:    input.Namespace,
		Name:         input.Name,
		secretAccess: family == ManifestFamilySecret,
	})
}

type ManifestApplyInput struct {
	ClusterID string
	Resource  ResourceIdentity
	Namespace string
	Name      string
	Object    map[string]any
	// The object as it exists in the Cluster, or nil when it does not. It is the
	// caller's own read rather than one taken here, so the create/update decision
	// the permission check already made and the one the guards make are the same
	// decision.
	Current        map[string]any
	DryRun         bool
	Force          bool
	Confirm        bool
	FieldManager   string
	IdempotencyKey string
}

// ApplyResource writes one document with server-side Apply.
//
// Apply rather than Create-or-Update because that is what `kubectl apply -f`
// means: the object is created when absent and merged when present, in one
// request, with no read-modify-write window in between for the object to change
// in. The permission check and the guards run first, so a refusal costs no write.
func (access *ManifestAccess) ApplyResource(
	ctx context.Context,
	input ManifestApplyInput,
) (map[string]any, error) {
	if access.service == nil {
		return nil, ErrAgentUnsupported
	}
	family := ManifestFamilyFor(input.Resource)
	_, allowed, err := access.RequirementForApply(
		input.Resource,
		input.Current == nil,
		ManifestTarget{Namespace: input.Namespace, Name: input.Name},
	)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrManifestForbidden
	}
	if err := access.guardApply(family, input.Current, input.Object); err != nil {
		return nil, err
	}
	patch, err := json.Marshal(input.Object)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return access.service.PatchResource(ctx, PatchResourceInput{
		ClusterID: input.ClusterID,
		Resource:  input.Resource,
		Namespace: input.Namespace,
		Name:      input.Name,
		PatchType: agentv1.PatchType_PATCH_TYPE_APPLY,
		Patch:     patch,
		Options: MutationOptions{
			DryRun:       input.DryRun,
			FieldManager: input.FieldManager,
			Force:        input.Force,
		},
		Confirm:        input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
		secretAccess:   family == ManifestFamilySecret,
	})
}

type ManifestDeleteInput struct {
	ClusterID string
	Resource  ResourceIdentity
	Namespace string
	Name      string
	// The object as it exists in the Cluster. Required: a delete with no live
	// object to check against is a delete that may land on a same-named object
	// created since the manifest was read.
	Current        map[string]any
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

// DeleteResource removes one object a document names, holding the delete to the
// UID and resourceVersion of the object the caller was shown.
func (access *ManifestAccess) DeleteResource(
	ctx context.Context,
	input ManifestDeleteInput,
) error {
	if access.service == nil {
		return ErrAgentUnsupported
	}
	family := ManifestFamilyFor(input.Resource)
	_, allowed, err := access.RequirementForDelete(input.Resource, ManifestTarget{Namespace: input.Namespace, Name: input.Name})
	if err != nil {
		return err
	}
	if !allowed {
		return ErrManifestForbidden
	}
	if err := access.guardDelete(family, input.Current); err != nil {
		return err
	}
	live := &unstructured.Unstructured{Object: input.Current}
	uid := string(live.GetUID())
	resourceVersion := live.GetResourceVersion()
	if uid == "" || resourceVersion == "" {
		return ErrInvalidInput
	}
	return access.service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID: input.ClusterID,
		Resource:  input.Resource,
		Namespace: input.Namespace,
		Name:      input.Name,
		DryRun:    input.DryRun,
		Confirm:   input.Confirm,
		// Background, as every other delete in the platform: foreground would
		// hold the request open for the dependents of every document in turn.
		Propagation: agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
		Preconditions: DeletePreconditions{
			UID:             uid,
			ResourceVersion: resourceVersion,
		},
		IdempotencyKey: input.IdempotencyKey,
		secretAccess:   family == ManifestFamilySecret,
	})
}

// The rules each family keeps, applied to an apply.
//
// These are the same guards the typed and YAML APIs run, reached from the one
// path that can write every family: without them a manifest would be the way
// around the checks those APIs exist to make, for anyone holding the family's
// permission. `current` is nil for a creation, and the rules that are about a
// change rather than about the submitted object are skipped then.
func (access *ManifestAccess) guardApply(
	family ManifestFamily,
	current map[string]any,
	submitted map[string]any,
) error {
	switch family {
	case ManifestFamilySecret:
		wanted, err := secretFromObject(submitted)
		if err != nil {
			return ErrInvalidInput
		}
		if current == nil {
			if platformManagedLabels(wanted.Labels) {
				return ErrPlatformLabelClaimed
			}
			return nil
		}
		live, err := secretFromObject(current)
		if err != nil {
			return ErrInvalidResponse
		}
		if platformManagedLabels(wanted.Labels) && !platformManagedLabels(live.Labels) {
			return ErrPlatformLabelClaimed
		}
		// The grant is not passed on: this guard is about a Secret object, not
		// about a PolicyRule handing Secret access to somebody else, and the
		// document already answered to `cluster.secret.manage`.
		return SecretManifestGuard(current, submitted, SecretRuleGrant{})
	case ManifestFamilyAuthorization:
		// The grant is passed on here, and it has to be: a PolicyRule is a way
		// to hand Secret access to every subject bound to it, so writing one
		// requires holding the access it grants. Without this, a manifest would
		// turn `cluster.rbac.manage` into every Secret permission in the
		// platform.
		return AuthorizationManifestGuard(current, submitted, SecretRuleGrant{
			Read:   access.grant.SecretRead,
			Manage: access.grant.SecretManage,
		})
	default:
		return nil
	}
}

func (access *ManifestAccess) guardDelete(
	family ManifestFamily,
	current map[string]any,
) error {
	if family == ManifestFamilyAuthorization {
		live := &unstructured.Unstructured{Object: current}
		if live.GetNamespace() == "" && managedAuthorizationLabels(live.GetLabels()) {
			return ErrManagedResource
		}
	}
	return nil
}
