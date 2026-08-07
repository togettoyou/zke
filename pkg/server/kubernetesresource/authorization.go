package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentinstall"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8scontent "k8s.io/apimachinery/pkg/api/validate/content"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

type AuthorizationResource string

const (
	AuthorizationServiceAccounts     AuthorizationResource = "serviceaccounts"
	AuthorizationRoles               AuthorizationResource = "roles"
	AuthorizationClusterRoles        AuthorizationResource = "clusterroles"
	AuthorizationRoleBindings        AuthorizationResource = "rolebindings"
	AuthorizationClusterRoleBindings AuthorizationResource = "clusterrolebindings"
	maxAuthorizationRules                                  = 128
	maxAuthorizationSubjects                               = 256
	maxAuthorizationRuleValues                             = 64
	maxAuthorizationAnnotations                            = 256 * 1024
)

// ErrPlatformLabelClaimed reports a manifest awarding itself the label ZKE puts
// on its own objects. It is refused rather than accepted-and-ignored: the label
// is what the platform reads to decide an object is not the operator's, and a
// write that sets it is a write that removes the object from this API.
var ErrPlatformLabelClaimed = errors.New("Kubernetes object claims the ZKE managed-by label")

// ErrRoleRefImmutable reports an edit repointing a binding at another role.
// Kubernetes refuses it too; this is the same refusal, before the write.
var ErrRoleRefImmutable = errors.New("Kubernetes RoleRef cannot be changed")

// ErrRoleRefForbidden reports a binding pointing at a role this API will not
// hand out — today, the Agent's own ClusterRole. Reported separately from a
// malformed request because the object may be perfectly valid Kubernetes and
// still not something ZKE will add a subject to.
var ErrRoleRefForbidden = errors.New("Kubernetes RoleRef is not allowed")

// ErrSecretRuleForbidden reports a PolicyRule handing out access to Secrets that
// the caller does not hold themselves. It is distinct from a malformed rule: the
// manifest is well formed, and the same rule submitted by someone holding
// `cluster.secret.manage` would be written.
var ErrSecretRuleForbidden = errors.New(
	"Kubernetes PolicyRule grants Secret access the caller does not hold",
)

// What the caller may write into a PolicyRule about Secrets.
//
// Kubernetes RBAC is a way to hand out access, so a rule mentioning `secrets` is
// a way to hand out Secret access — and `cluster.rbac.manage` on its own must not
// be one. Without this, the separation between reading workload configuration and
// reading credentials would survive only until somebody wrote a Role.
//
// It is the same rule the platform applies to its own roles: you cannot grant
// what you do not hold. Resolved per request from the caller's permissions on the
// target Cluster, and zero-valued by default so a path that forgets to set it
// refuses rather than allows.
type SecretRuleGrant struct {
	// The caller holds `cluster.secret.read` on this Cluster.
	Read bool
	// The caller holds `cluster.secret.manage` on this Cluster.
	Manage bool
}

var authorizationResourceIdentities = map[AuthorizationResource]ResourceIdentity{
	AuthorizationServiceAccounts:     {Version: "v1", Resource: "serviceaccounts"},
	AuthorizationRoles:               {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
	AuthorizationClusterRoles:        {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
	AuthorizationRoleBindings:        {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
	AuthorizationClusterRoleBindings: {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
}

type ListAuthorizationResourcesInput struct {
	ClusterID     string
	Namespace     string
	Resource      AuthorizationResource
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type AuthorizationResourcePage struct {
	Resources          []AuthorizationResourceSummary `json:"resources"`
	ContinueToken      string                         `json:"continue_token"`
	ResourceVersion    string                         `json:"resource_version"`
	RemainingItemCount *int64                         `json:"remaining_item_count"`
}

type AuthorizationRoleRef struct {
	APIGroup string `json:"api_group"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

type AuthorizationSubject struct {
	APIGroup  string `json:"api_group"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type AuthorizationPolicyRule struct {
	Verbs           []string `json:"verbs"`
	APIGroups       []string `json:"api_groups"`
	Resources       []string `json:"resources"`
	ResourceNames   []string `json:"resource_names"`
	NonResourceURLs []string `json:"non_resource_urls"`
}

type AuthorizationResourceSummary struct {
	Resource                     AuthorizationResource `json:"resource"`
	APIVersion                   string                `json:"api_version"`
	Kind                         string                `json:"kind"`
	Namespace                    string                `json:"namespace"`
	Name                         string                `json:"name"`
	UID                          string                `json:"uid"`
	ResourceVersion              string                `json:"resource_version"`
	CreationTimestamp            time.Time             `json:"creation_timestamp"`
	Labels                       map[string]string     `json:"labels"`
	RuleCount                    int                   `json:"rule_count"`
	SubjectCount                 int                   `json:"subject_count"`
	SecretReferenceCount         int                   `json:"secret_reference_count"`
	RoleRef                      *AuthorizationRoleRef `json:"role_ref,omitempty"`
	AutomountServiceAccountToken *bool                 `json:"automount_service_account_token,omitempty"`
	Aggregated                   bool                  `json:"aggregated"`
	ManagedByZKE                 bool                  `json:"managed_by_zke"`
}

type AuthorizationResourceDetail struct {
	AuthorizationResourceSummary
	Annotations map[string]string         `json:"annotations"`
	Rules       []AuthorizationPolicyRule `json:"rules"`
	Subjects    []AuthorizationSubject    `json:"subjects"`
}

type CreateAuthorizationResourceInput struct {
	ClusterID                    string
	Namespace                    string
	Resource                     AuthorizationResource
	Name                         string
	Labels                       map[string]string
	Annotations                  map[string]string
	AutomountServiceAccountToken *bool
	Rules                        []AuthorizationPolicyRule
	Subjects                     []AuthorizationSubject
	RoleRef                      *AuthorizationRoleRef
	// What the caller may hand out about Secrets. Zero-valued means "nothing",
	// so a call site that forgets it refuses rather than allows.
	SecretGrant    SecretRuleGrant
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type UpdateAuthorizationResourceInput struct {
	ClusterID                    string
	Namespace                    string
	Resource                     AuthorizationResource
	Name                         string
	UID                          string
	ResourceVersion              string
	AutomountServiceAccountToken *bool
	Rules                        []AuthorizationPolicyRule
	Subjects                     []AuthorizationSubject
	// See CreateAuthorizationResourceInput.SecretGrant.
	SecretGrant    SecretRuleGrant
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type DeleteAuthorizationResourceInput struct {
	ClusterID       string
	Namespace       string
	Resource        AuthorizationResource
	Name            string
	UID             string
	ResourceVersion string
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

func AuthorizationResourceIdentity(resource AuthorizationResource) (ResourceIdentity, bool) {
	identity, exists := authorizationResourceIdentities[resource]
	return identity, exists
}

// ScopedAuthorizationResourceIdentity answers with the identity only when the
// Namespace matches the family's scope: a ClusterRole named inside a Namespace,
// or a Role named without one, is a request about an object that does not
// exist, and resolving it either way would invent a target.
func ScopedAuthorizationResourceIdentity(
	resource AuthorizationResource,
	namespace string,
) (ResourceIdentity, bool) {
	return authorizationResourceIdentity(resource, namespace)
}

func IsAuthorizationResourceIdentity(identity ResourceIdentity) bool {
	for _, candidate := range authorizationResourceIdentities {
		if candidate == identity {
			return true
		}
	}
	return false
}

func (service *Service) ListAuthorizationResources(ctx context.Context, input ListAuthorizationResourcesInput) (AuthorizationResourcePage, error) {
	identity, exists := authorizationResourceIdentity(input.Resource, input.Namespace)
	if !exists {
		return AuthorizationResourcePage{}, ErrInvalidInput
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace,
		Limit: input.Limit, ContinueToken: input.ContinueToken,
		LabelSelector: input.LabelSelector, FieldSelector: input.FieldSelector,
	})
	if err != nil {
		return AuthorizationResourcePage{}, err
	}
	result := AuthorizationResourcePage{
		Resources:     make([]AuthorizationResourceSummary, 0, len(page.Items)),
		ContinueToken: page.ContinueToken, ResourceVersion: page.ResourceVersion,
		RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, detailErr := authorizationResourceDetail(item, input.Resource, input.Namespace, "")
		if detailErr != nil {
			return AuthorizationResourcePage{}, detailErr
		}
		result.Resources = append(result.Resources, detail.AuthorizationResourceSummary)
	}
	return result, nil
}

func (service *Service) GetAuthorizationResource(ctx context.Context, clusterID, namespace string, resource AuthorizationResource, name string) (AuthorizationResourceDetail, error) {
	identity, exists := authorizationResourceIdentity(resource, namespace)
	if !exists || !validAuthorizationName(resource, name) {
		return AuthorizationResourceDetail{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{ClusterID: clusterID, Resource: identity, Namespace: namespace, Name: name})
	if err != nil {
		return AuthorizationResourceDetail{}, err
	}
	return authorizationResourceDetail(object, resource, namespace, name)
}

func (service *Service) CreateAuthorizationResource(ctx context.Context, input CreateAuthorizationResourceInput) (AuthorizationResourceDetail, error) {
	identity, exists := authorizationResourceIdentity(input.Resource, input.Namespace)
	if !exists {
		return AuthorizationResourceDetail{}, ErrInvalidInput
	}
	object, err := createAuthorizationResourceObject(input)
	if err != nil {
		return AuthorizationResourceDetail{}, err
	}
	result, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Object: object,
		Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return AuthorizationResourceDetail{}, err
	}
	return authorizationResourceDetail(result, input.Resource, input.Namespace, input.Name)
}

func (service *Service) UpdateAuthorizationResource(ctx context.Context, input UpdateAuthorizationResourceInput) (AuthorizationResourceDetail, error) {
	identity, exists := authorizationResourceIdentity(input.Resource, input.Namespace)
	if !exists || !validAuthorizationMutationIdentity(input.Resource, input.Name, input.UID, input.ResourceVersion) {
		return AuthorizationResourceDetail{}, ErrInvalidInput
	}
	existing, err := service.GetResource(ctx, GetResourceInput{ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name})
	if err != nil {
		return AuthorizationResourceDetail{}, err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.UID || current.GetResourceVersion() != input.ResourceVersion {
		return AuthorizationResourceDetail{}, ErrUpstreamConflict
	}
	if managedAuthorizationLabels(current.GetLabels()) {
		return AuthorizationResourceDetail{}, ErrManagedResource
	}
	updated, err := updateAuthorizationResourceObject(existing, input)
	if err != nil {
		return AuthorizationResourceDetail{}, err
	}
	result, err := service.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name, Object: updated,
		Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return AuthorizationResourceDetail{}, err
	}
	return authorizationResourceDetail(result, input.Resource, input.Namespace, input.Name)
}

func (service *Service) DeleteAuthorizationResource(ctx context.Context, input DeleteAuthorizationResourceInput) error {
	identity, exists := authorizationResourceIdentity(input.Resource, input.Namespace)
	if !exists || !validAuthorizationMutationIdentity(input.Resource, input.Name, input.UID, input.ResourceVersion) {
		return ErrInvalidInput
	}
	existing, err := service.GetResource(ctx, GetResourceInput{ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name})
	if err != nil {
		return err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.UID || current.GetResourceVersion() != input.ResourceVersion {
		return ErrUpstreamConflict
	}
	if managedAuthorizationLabels(current.GetLabels()) {
		return ErrManagedResource
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
		DryRun: input.DryRun, Confirm: input.Confirm, Propagation: agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
		Preconditions: DeletePreconditions{UID: input.UID, ResourceVersion: input.ResourceVersion}, IdempotencyKey: input.IdempotencyKey,
	})
}

// The typed API's rules, applied to a submitted manifest.
//
// A YAML document can say everything a form can and more, so the editor has to
// refuse what the form refuses. Without this it would not be an editor for the
// fields the form does not model — it would be the way around the checks that
// decide what every other permission in the Cluster is worth, reachable by
// anyone holding `cluster.rbac.manage`.
//
// What it does not repeat: the identity, scope and version checks the YAML
// service already made, and Kubernetes' own validation, which stays the final
// word on whether the object is well-formed.
//
// A nil `current` means the object does not exist yet, which is what a manifest
// apply that creates one submits. Every rule below still applies except the one
// that is about a change rather than about the submitted object: a RoleRef
// cannot have moved when there was nothing to move it from.
func AuthorizationManifestGuard(
	current map[string]any,
	submitted map[string]any,
	grant SecretRuleGrant,
) error {
	live := &unstructured.Unstructured{Object: current}
	if managedAuthorizationLabels(live.GetLabels()) {
		return ErrManagedResource
	}
	wanted := &unstructured.Unstructured{Object: submitted}
	// Awarding an object ZKE's own label would make the typed API treat it as
	// the platform's and refuse to touch it again — a lock anyone could fit to
	// someone else's Role, and one no ZKE page offers a way to remove.
	if managedAuthorizationLabels(wanted.GetLabels()) {
		return ErrPlatformLabelClaimed
	}
	resource, exists := authorizationResourceForKind(wanted.GetKind())
	if !exists {
		return ErrInvalidInput
	}
	body, err := json.Marshal(submitted)
	if err != nil {
		return ErrInvalidInput
	}
	switch resource {
	case AuthorizationServiceAccounts:
		return nil
	case AuthorizationRoles, AuthorizationClusterRoles:
		// ClusterRole decodes a Role too: the rules are the same field, and only
		// the scope differs — which is what the second argument below carries.
		var value rbacv1.ClusterRole
		if json.Unmarshal(body, &value) != nil {
			return ErrInvalidInput
		}
		// An aggregated ClusterRole is editable here although the typed form
		// refuses it: YAML is the only honest view of an `aggregationRule`, and
		// the rules a controller synthesises are the controller's to overwrite.
		return validateAuthorizationRules(
			policyRuleViews(value.Rules),
			resource == AuthorizationClusterRoles,
			grant,
		)
	case AuthorizationRoleBindings, AuthorizationClusterRoleBindings:
		var value rbacv1.ClusterRoleBinding
		if json.Unmarshal(body, &value) != nil {
			return ErrInvalidInput
		}
		roleRef := roleRefView(value.RoleRef)
		if !validAuthorizationSubjects(subjectViews(value.Subjects)) ||
			!validAuthorizationRoleRef(resource, &roleRef) {
			return ErrInvalidInput
		}
		if current == nil {
			return nil
		}
		// Kubernetes refuses a changed RoleRef as well, with a message about a
		// field the operator can see. Refusing it here says the same thing
		// before a write is attempted, and says it about the one edit that
		// silently repoints an existing grant at another role.
		var existing rbacv1.ClusterRoleBinding
		currentBody, err := json.Marshal(current)
		if err != nil || json.Unmarshal(currentBody, &existing) != nil {
			return ErrInvalidResponse
		}
		if existing.RoleRef != value.RoleRef {
			return ErrRoleRefImmutable
		}
		return nil
	default:
		return ErrInvalidInput
	}
}

// authorizationResourceForKind answers which family a manifest belongs to. The
// Kind is authoritative because the YAML service has already checked it against
// both the object being replaced and the resource named in the request.
func authorizationResourceForKind(kind string) (AuthorizationResource, bool) {
	for _, resource := range []AuthorizationResource{
		AuthorizationServiceAccounts,
		AuthorizationRoles,
		AuthorizationClusterRoles,
		AuthorizationRoleBindings,
		AuthorizationClusterRoleBindings,
	} {
		if _, expected := authorizationTypeIdentity(resource); expected == kind {
			return resource, true
		}
	}
	return "", false
}

func authorizationResourceIdentity(resource AuthorizationResource, namespace string) (ResourceIdentity, bool) {
	identity, exists := AuthorizationResourceIdentity(resource)
	if !exists {
		return ResourceIdentity{}, false
	}
	namespaced := resource == AuthorizationServiceAccounts || resource == AuthorizationRoles || resource == AuthorizationRoleBindings
	if namespaced != (len(k8svalidation.IsDNS1123Label(namespace)) == 0) {
		return ResourceIdentity{}, false
	}
	return identity, true
}

func createAuthorizationResourceObject(input CreateAuthorizationResourceInput) (map[string]any, error) {
	if !validAuthorizationName(input.Resource, input.Name) || !validNamespaceLabels(input.Labels) || !validAuthorizationAnnotations(input.Annotations) {
		return nil, ErrInvalidInput
	}
	metadata := metav1.ObjectMeta{Name: input.Name, Namespace: input.Namespace, Labels: maps.Clone(input.Labels), Annotations: maps.Clone(input.Annotations)}
	var object any
	switch input.Resource {
	case AuthorizationServiceAccounts:
		if len(input.Rules) != 0 || len(input.Subjects) != 0 || input.RoleRef != nil {
			return nil, ErrInvalidInput
		}
		object = &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: metadata, AutomountServiceAccountToken: cloneBoolPointer(input.AutomountServiceAccountToken)}
	case AuthorizationRoles, AuthorizationClusterRoles:
		if input.AutomountServiceAccountToken != nil || len(input.Subjects) != 0 || input.RoleRef != nil {
			return nil, ErrInvalidInput
		}
		if err := validateAuthorizationRules(
			input.Rules,
			input.Resource == AuthorizationClusterRoles,
			input.SecretGrant,
		); err != nil {
			return nil, err
		}
		rules := kubernetesPolicyRules(input.Rules)
		if input.Resource == AuthorizationRoles {
			object = &rbacv1.Role{TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"}, ObjectMeta: metadata, Rules: rules}
		} else {
			object = &rbacv1.ClusterRole{TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"}, ObjectMeta: metadata, Rules: rules}
		}
	case AuthorizationRoleBindings, AuthorizationClusterRoleBindings:
		if input.AutomountServiceAccountToken != nil || len(input.Rules) != 0 || !validAuthorizationSubjects(input.Subjects) || !validAuthorizationRoleRef(input.Resource, input.RoleRef) {
			return nil, ErrInvalidInput
		}
		subjects := kubernetesSubjects(input.Subjects)
		roleRef := kubernetesRoleRef(*input.RoleRef)
		if input.Resource == AuthorizationRoleBindings {
			object = &rbacv1.RoleBinding{TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"}, ObjectMeta: metadata, Subjects: subjects, RoleRef: roleRef}
		} else {
			object = &rbacv1.ClusterRoleBinding{TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"}, ObjectMeta: metadata, Subjects: subjects, RoleRef: roleRef}
		}
	default:
		return nil, ErrInvalidInput
	}
	return typedAuthorizationObject(object, ErrInvalidInput)
}

func updateAuthorizationResourceObject(existing map[string]any, input UpdateAuthorizationResourceInput) (map[string]any, error) {
	body, err := json.Marshal(existing)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	switch input.Resource {
	case AuthorizationServiceAccounts:
		if len(input.Rules) != 0 || len(input.Subjects) != 0 {
			return nil, ErrInvalidInput
		}
		var value corev1.ServiceAccount
		if json.Unmarshal(body, &value) != nil {
			return nil, ErrInvalidResponse
		}
		value.AutomountServiceAccountToken = cloneBoolPointer(input.AutomountServiceAccountToken)
		return typedAuthorizationObject(&value, ErrInvalidResponse)
	case AuthorizationRoles:
		if input.AutomountServiceAccountToken != nil || len(input.Subjects) != 0 {
			return nil, ErrInvalidInput
		}
		if err := validateAuthorizationRules(
			input.Rules, false, input.SecretGrant,
		); err != nil {
			return nil, err
		}
		var value rbacv1.Role
		if json.Unmarshal(body, &value) != nil {
			return nil, ErrInvalidResponse
		}
		value.Rules = kubernetesPolicyRules(input.Rules)
		return typedAuthorizationObject(&value, ErrInvalidResponse)
	case AuthorizationClusterRoles:
		if input.AutomountServiceAccountToken != nil || len(input.Subjects) != 0 {
			return nil, ErrInvalidInput
		}
		if err := validateAuthorizationRules(
			input.Rules, true, input.SecretGrant,
		); err != nil {
			return nil, err
		}
		var value rbacv1.ClusterRole
		if json.Unmarshal(body, &value) != nil {
			return nil, ErrInvalidResponse
		}
		if value.AggregationRule != nil {
			return nil, ErrInvalidInput
		}
		value.Rules = kubernetesPolicyRules(input.Rules)
		return typedAuthorizationObject(&value, ErrInvalidResponse)
	// The two binding families keep the RoleRef they have — it is not taken from
	// the request — but the one they have still has to be a RoleRef this API
	// would point at.
	//
	// Creation checks that; an update used not to, and the difference was
	// reachable: a ClusterRoleBinding created outside ZKE pointing at the Agent's
	// own ClusterRole carries none of ZKE's labels, so nothing stopped an
	// operator from adding subjects to it here. Adding a subject to that binding
	// hands over exactly what refusing to create one prevents.
	case AuthorizationRoleBindings:
		if input.AutomountServiceAccountToken != nil || len(input.Rules) != 0 || !validAuthorizationSubjects(input.Subjects) {
			return nil, ErrInvalidInput
		}
		var value rbacv1.RoleBinding
		if json.Unmarshal(body, &value) != nil {
			return nil, ErrInvalidResponse
		}
		existingRoleRef := roleRefView(value.RoleRef)
		if !validAuthorizationRoleRef(input.Resource, &existingRoleRef) {
			return nil, ErrRoleRefForbidden
		}
		value.Subjects = kubernetesSubjects(input.Subjects)
		return typedAuthorizationObject(&value, ErrInvalidResponse)
	case AuthorizationClusterRoleBindings:
		if input.AutomountServiceAccountToken != nil || len(input.Rules) != 0 || !validAuthorizationSubjects(input.Subjects) {
			return nil, ErrInvalidInput
		}
		var value rbacv1.ClusterRoleBinding
		if json.Unmarshal(body, &value) != nil {
			return nil, ErrInvalidResponse
		}
		existingRoleRef := roleRefView(value.RoleRef)
		if !validAuthorizationRoleRef(input.Resource, &existingRoleRef) {
			return nil, ErrRoleRefForbidden
		}
		value.Subjects = kubernetesSubjects(input.Subjects)
		return typedAuthorizationObject(&value, ErrInvalidResponse)
	default:
		return nil, ErrInvalidInput
	}
}

func authorizationResourceDetail(object map[string]any, resource AuthorizationResource, namespace, name string) (AuthorizationResourceDetail, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return AuthorizationResourceDetail{}, ErrInvalidResponse
	}
	var metadata metav1.ObjectMeta
	var typeMeta metav1.TypeMeta
	detail := AuthorizationResourceDetail{Rules: []AuthorizationPolicyRule{}, Subjects: []AuthorizationSubject{}}
	switch resource {
	case AuthorizationServiceAccounts:
		var value corev1.ServiceAccount
		if json.Unmarshal(body, &value) != nil {
			return AuthorizationResourceDetail{}, ErrInvalidResponse
		}
		metadata, typeMeta = value.ObjectMeta, value.TypeMeta
		detail.AutomountServiceAccountToken = cloneBoolPointer(value.AutomountServiceAccountToken)
		detail.SecretReferenceCount = len(value.Secrets) + len(value.ImagePullSecrets)
	case AuthorizationRoles:
		var value rbacv1.Role
		if json.Unmarshal(body, &value) != nil {
			return AuthorizationResourceDetail{}, ErrInvalidResponse
		}
		metadata, typeMeta, detail.Rules = value.ObjectMeta, value.TypeMeta, policyRuleViews(value.Rules)
	case AuthorizationClusterRoles:
		var value rbacv1.ClusterRole
		if json.Unmarshal(body, &value) != nil {
			return AuthorizationResourceDetail{}, ErrInvalidResponse
		}
		metadata, typeMeta, detail.Rules = value.ObjectMeta, value.TypeMeta, policyRuleViews(value.Rules)
		detail.Aggregated = value.AggregationRule != nil
	case AuthorizationRoleBindings:
		var value rbacv1.RoleBinding
		if json.Unmarshal(body, &value) != nil {
			return AuthorizationResourceDetail{}, ErrInvalidResponse
		}
		metadata, typeMeta, detail.Subjects = value.ObjectMeta, value.TypeMeta, subjectViews(value.Subjects)
		roleRef := roleRefView(value.RoleRef)
		detail.RoleRef = &roleRef
	case AuthorizationClusterRoleBindings:
		var value rbacv1.ClusterRoleBinding
		if json.Unmarshal(body, &value) != nil {
			return AuthorizationResourceDetail{}, ErrInvalidResponse
		}
		metadata, typeMeta, detail.Subjects = value.ObjectMeta, value.TypeMeta, subjectViews(value.Subjects)
		roleRef := roleRefView(value.RoleRef)
		detail.RoleRef = &roleRef
	default:
		return AuthorizationResourceDetail{}, ErrInvalidResponse
	}
	expectedAPIVersion, expectedKind := authorizationTypeIdentity(resource)
	if typeMeta.APIVersion != expectedAPIVersion || typeMeta.Kind != expectedKind ||
		metadata.Namespace != namespace || name != "" && metadata.Name != name || metadata.Name == "" {
		return AuthorizationResourceDetail{}, ErrInvalidResponse
	}
	detail.AuthorizationResourceSummary = AuthorizationResourceSummary{
		Resource: resource, APIVersion: typeMeta.APIVersion, Kind: typeMeta.Kind, Namespace: metadata.Namespace,
		Name: metadata.Name, UID: string(metadata.UID), ResourceVersion: metadata.ResourceVersion,
		CreationTimestamp: metadata.CreationTimestamp.Time, Labels: normalizedStringMap(metadata.Labels),
		RuleCount: len(detail.Rules), SubjectCount: len(detail.Subjects), SecretReferenceCount: detail.SecretReferenceCount,
		RoleRef: detail.RoleRef, AutomountServiceAccountToken: detail.AutomountServiceAccountToken,
		Aggregated: detail.Aggregated, ManagedByZKE: managedAuthorizationLabels(metadata.Labels),
	}
	detail.Annotations = normalizedStringMap(metadata.Annotations)
	return detail, nil
}

func authorizationTypeIdentity(resource AuthorizationResource) (string, string) {
	switch resource {
	case AuthorizationServiceAccounts:
		return "v1", "ServiceAccount"
	case AuthorizationRoles:
		return "rbac.authorization.k8s.io/v1", "Role"
	case AuthorizationClusterRoles:
		return "rbac.authorization.k8s.io/v1", "ClusterRole"
	case AuthorizationRoleBindings:
		return "rbac.authorization.k8s.io/v1", "RoleBinding"
	case AuthorizationClusterRoleBindings:
		return "rbac.authorization.k8s.io/v1", "ClusterRoleBinding"
	default:
		return "", ""
	}
}

// The name rules each family actually has.
//
// Kubernetes validates RBAC names as path segments rather than as DNS
// subdomains, and that is not a technicality: `system:basic-user`,
// `system:aggregate-to-edit` and `kubeadm:cluster-admins` all carry a colon,
// which no DNS name may hold. Asking for a DNS subdomain here refused every
// built-in role and binding in the cluster — the objects an operator opens most
// often, and the ones they are least able to rename.
//
// ServiceAccounts are the exception and really are DNS subdomains: they are
// core/v1 objects, and Kubernetes validates them as such.
func validAuthorizationName(resource AuthorizationResource, name string) bool {
	if resource == AuthorizationServiceAccounts {
		return len(k8svalidation.IsDNS1123Subdomain(name)) == 0
	}
	return validRBACName(name)
}

// validRBACName mirrors Kubernetes' own rule for these objects, plus a length
// bound this API adds: the upstream validator deliberately has none, and a name
// with no ceiling ends up in a URL, a log line and an audit record.
func validRBACName(name string) bool {
	return name != "" &&
		len(name) <= 253 &&
		strings.TrimSpace(name) == name &&
		len(k8scontent.IsPathSegmentName(name)) == 0
}

func validAuthorizationMutationIdentity(resource AuthorizationResource, name, uid, resourceVersion string) bool {
	return validAuthorizationName(resource, name) && strings.TrimSpace(uid) != "" && len(uid) <= 128 && strings.TrimSpace(resourceVersion) != "" && len(resourceVersion) <= 256
}

func validAuthorizationAnnotations(annotations map[string]string) bool {
	total := 0
	for key, value := range annotations {
		if len(k8svalidation.IsQualifiedName(key)) != 0 {
			return false
		}
		total += len(key) + len(value)
		if total > maxAuthorizationAnnotations {
			return false
		}
	}
	return true
}

// validateAuthorizationRules reports why a rule set may not be written, or nil.
// It answers with an error rather than a bool because two of the reasons are
// different answers: a malformed rule is a bad request, while a rule handing out
// Secret access the caller does not hold is a refusal that naming the missing
// permission makes actionable.
func validateAuthorizationRules(
	rules []AuthorizationPolicyRule,
	allowNonResource bool,
	grant SecretRuleGrant,
) error {
	if len(rules) > maxAuthorizationRules {
		return ErrInvalidInput
	}
	for _, rule := range rules {
		if len(rule.Verbs) == 0 || !validRuleValues(rule.Verbs, false) || !validRuleValues(rule.APIGroups, true) || !validRuleValues(rule.Resources, false) || !validRuleValues(rule.ResourceNames, false) || !validRuleValues(rule.NonResourceURLs, false) {
			return ErrInvalidInput
		}
		resourceRule := len(rule.Resources) > 0
		nonResourceRule := len(rule.NonResourceURLs) > 0
		if resourceRule == nonResourceRule || nonResourceRule && (!allowNonResource || len(rule.APIGroups) != 0 || len(rule.ResourceNames) != 0) {
			return ErrInvalidInput
		}
		if forbiddenAuthorizationRule(rule, grant) {
			// Told apart so the caller learns which of the two it was. A rule
			// refused only for its Secret access is one the operator can fix by
			// asking for the permission; the rest are rules to rewrite.
			if mentionsSecrets(rule) && !secretRuleWithinGrant(rule.Verbs, grant) {
				return ErrSecretRuleForbidden
			}
			return ErrInvalidInput
		}
	}
	return nil
}

func mentionsSecrets(rule AuthorizationPolicyRule) bool {
	for _, resource := range rule.Resources {
		if resource == "secrets" || resource == "*" {
			return true
		}
	}
	return false
}

func validRuleValues(values []string, allowEmpty bool) bool {
	if len(values) > maxAuthorizationRuleValues {
		return false
	}
	for _, value := range values {
		if len(value) > 1024 || strings.TrimSpace(value) != value || !allowEmpty && value == "" {
			return false
		}
	}
	return true
}

func validAuthorizationSubjects(subjects []AuthorizationSubject) bool {
	if len(subjects) == 0 || len(subjects) > maxAuthorizationSubjects {
		return false
	}
	for _, subject := range subjects {
		switch subject.Kind {
		case "ServiceAccount":
			if subject.APIGroup != "" || len(k8svalidation.IsDNS1123Subdomain(subject.Name)) != 0 || len(k8svalidation.IsDNS1123Label(subject.Namespace)) != 0 {
				return false
			}
		case "User", "Group":
			if subject.APIGroup != "rbac.authorization.k8s.io" || subject.Name == "" || len(subject.Name) > 253 || subject.Namespace != "" || strings.TrimSpace(subject.Name) != subject.Name {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Whether a binding may point where it points.
//
// The Agent's own ClusterRole is refused, and this is the only thing refusing
// it. Kubernetes will not help: creating a binding requires the actor to hold
// every permission in the referenced role, the actor is the Agent's
// ServiceAccount, and that ClusterRole is precisely the Agent's own permission
// set — so escalation prevention says yes. The `managed-by` label does not help
// either; it protects that ClusterRole from being edited or deleted, while a
// binding to it is a new object carrying no such label.
//
// What it would hand over is the Agent's cluster-wide reach: Secrets and RBAC
// CRUD in every Namespace. The name comes from the installer rather than a
// literal, so renaming the ServiceAccount cannot silently switch this check off.
func validAuthorizationRoleRef(resource AuthorizationResource, roleRef *AuthorizationRoleRef) bool {
	if roleRef == nil || roleRef.APIGroup != "rbac.authorization.k8s.io" ||
		roleRef.Name == agentinstall.ServiceAccountName ||
		!validRBACName(roleRef.Name) {
		return false
	}
	if resource == AuthorizationRoleBindings {
		return roleRef.Kind == "Role" || roleRef.Kind == "ClusterRole"
	}
	return resource == AuthorizationClusterRoleBindings && roleRef.Kind == "ClusterRole"
}

// The rules no manifest may carry, and the one that depends on who is asking.
//
// `escalate`, `bind` and `impersonate` are refused outright, as is
// `serviceaccounts/token`. Not really because of ZKE: the Agent's ClusterRole
// holds none of them, so Kubernetes' own escalation prevention refuses the write
// anyway. Refusing here turns a 502 about the Agent's Kubernetes permissions —
// which reads as "grant the Agent more and it will work", and it will not — into
// a 400 about the rule that was submitted.
//
// Secrets are the conditional one. A rule mentioning them is refused unless the
// caller holds the matching Secret permission on this Cluster, because the rule
// hands that access to somebody else. Read-only verbs need `cluster.secret.read`;
// anything else needs `cluster.secret.manage`.
func forbiddenAuthorizationRule(rule AuthorizationPolicyRule, grant SecretRuleGrant) bool {
	for _, verb := range rule.Verbs {
		if verb == "escalate" || verb == "bind" || verb == "impersonate" {
			return true
		}
	}
	for _, resource := range rule.Resources {
		switch resource {
		case "serviceaccounts/token":
			return true
		// A wildcard covers Secrets as surely as naming them does. Kubernetes
		// refuses a wildcard rule anyway — the Agent holds no wildcard — but
		// leaving it to that backstop makes this check depend on a property of
		// the Agent's ClusterRole that nothing here can see.
		case "secrets", "*":
			if !secretRuleWithinGrant(rule.Verbs, grant) {
				return true
			}
		}
	}
	return false
}

func secretRuleWithinGrant(verbs []string, grant SecretRuleGrant) bool {
	if grant.Manage {
		return true
	}
	if !grant.Read {
		return false
	}
	// Someone who can read Secrets but not change them may hand out reading, and
	// nothing else. A wildcard verb is not read-only.
	for _, verb := range verbs {
		switch verb {
		case "get", "list", "watch":
		default:
			return false
		}
	}
	return true
}

func managedAuthorizationLabels(labels map[string]string) bool {
	return labels["app.kubernetes.io/managed-by"] == "zke-server"
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func kubernetesPolicyRules(values []AuthorizationPolicyRule) []rbacv1.PolicyRule {
	result := make([]rbacv1.PolicyRule, 0, len(values))
	for _, value := range values {
		result = append(result, rbacv1.PolicyRule{Verbs: append([]string{}, value.Verbs...), APIGroups: append([]string{}, value.APIGroups...), Resources: append([]string{}, value.Resources...), ResourceNames: append([]string{}, value.ResourceNames...), NonResourceURLs: append([]string{}, value.NonResourceURLs...)})
	}
	return result
}

func policyRuleViews(values []rbacv1.PolicyRule) []AuthorizationPolicyRule {
	result := make([]AuthorizationPolicyRule, 0, len(values))
	for _, value := range values {
		result = append(result, AuthorizationPolicyRule{Verbs: append([]string{}, value.Verbs...), APIGroups: append([]string{}, value.APIGroups...), Resources: append([]string{}, value.Resources...), ResourceNames: append([]string{}, value.ResourceNames...), NonResourceURLs: append([]string{}, value.NonResourceURLs...)})
	}
	return result
}

func kubernetesSubjects(values []AuthorizationSubject) []rbacv1.Subject {
	result := make([]rbacv1.Subject, 0, len(values))
	for _, value := range values {
		result = append(result, rbacv1.Subject{APIGroup: value.APIGroup, Kind: value.Kind, Name: value.Name, Namespace: value.Namespace})
	}
	return result
}
func subjectViews(values []rbacv1.Subject) []AuthorizationSubject {
	result := make([]AuthorizationSubject, 0, len(values))
	for _, value := range values {
		result = append(result, AuthorizationSubject{APIGroup: value.APIGroup, Kind: value.Kind, Name: value.Name, Namespace: value.Namespace})
	}
	return result
}
func kubernetesRoleRef(value AuthorizationRoleRef) rbacv1.RoleRef {
	return rbacv1.RoleRef{APIGroup: value.APIGroup, Kind: value.Kind, Name: value.Name}
}
func roleRefView(value rbacv1.RoleRef) AuthorizationRoleRef {
	return AuthorizationRoleRef{APIGroup: value.APIGroup, Kind: value.Kind, Name: value.Name}
}

func typedAuthorizationObject(object any, wrapped error) (map[string]any, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return nil, wrapped
	}
	var result map[string]any
	if json.Unmarshal(body, &result) != nil {
		return nil, wrapped
	}
	return result, nil
}
