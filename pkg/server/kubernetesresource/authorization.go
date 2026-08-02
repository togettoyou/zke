package kubernetesresource

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
	DryRun                       bool
	Confirm                      bool
	IdempotencyKey               string
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
	DryRun                       bool
	Confirm                      bool
	IdempotencyKey               string
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
	if !exists || len(k8svalidation.IsDNS1123Subdomain(name)) != 0 {
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
	if !exists || !validAuthorizationMutationIdentity(input.Name, input.UID, input.ResourceVersion) {
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
	if !exists || !validAuthorizationMutationIdentity(input.Name, input.UID, input.ResourceVersion) {
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
	if len(k8svalidation.IsDNS1123Subdomain(input.Name)) != 0 || !validNamespaceLabels(input.Labels) || !validAuthorizationAnnotations(input.Annotations) {
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
		if input.AutomountServiceAccountToken != nil || len(input.Subjects) != 0 || input.RoleRef != nil || !validAuthorizationRules(input.Rules, input.Resource == AuthorizationClusterRoles) {
			return nil, ErrInvalidInput
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
		if input.AutomountServiceAccountToken != nil || len(input.Subjects) != 0 || !validAuthorizationRules(input.Rules, false) {
			return nil, ErrInvalidInput
		}
		var value rbacv1.Role
		if json.Unmarshal(body, &value) != nil {
			return nil, ErrInvalidResponse
		}
		value.Rules = kubernetesPolicyRules(input.Rules)
		return typedAuthorizationObject(&value, ErrInvalidResponse)
	case AuthorizationClusterRoles:
		if input.AutomountServiceAccountToken != nil || len(input.Subjects) != 0 || !validAuthorizationRules(input.Rules, true) {
			return nil, ErrInvalidInput
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
	case AuthorizationRoleBindings:
		if input.AutomountServiceAccountToken != nil || len(input.Rules) != 0 || !validAuthorizationSubjects(input.Subjects) {
			return nil, ErrInvalidInput
		}
		var value rbacv1.RoleBinding
		if json.Unmarshal(body, &value) != nil {
			return nil, ErrInvalidResponse
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

func validAuthorizationMutationIdentity(name, uid, resourceVersion string) bool {
	return len(k8svalidation.IsDNS1123Subdomain(name)) == 0 && strings.TrimSpace(uid) != "" && len(uid) <= 128 && strings.TrimSpace(resourceVersion) != "" && len(resourceVersion) <= 256
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

func validAuthorizationRules(rules []AuthorizationPolicyRule, allowNonResource bool) bool {
	if len(rules) > maxAuthorizationRules {
		return false
	}
	for _, rule := range rules {
		if len(rule.Verbs) == 0 || !validRuleValues(rule.Verbs, false) || !validRuleValues(rule.APIGroups, true) || !validRuleValues(rule.Resources, false) || !validRuleValues(rule.ResourceNames, false) || !validRuleValues(rule.NonResourceURLs, false) {
			return false
		}
		if forbiddenAuthorizationRule(rule) {
			return false
		}
		resourceRule := len(rule.Resources) > 0
		nonResourceRule := len(rule.NonResourceURLs) > 0
		if resourceRule == nonResourceRule || nonResourceRule && (!allowNonResource || len(rule.APIGroups) != 0 || len(rule.ResourceNames) != 0) {
			return false
		}
	}
	return true
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

func validAuthorizationRoleRef(resource AuthorizationResource, roleRef *AuthorizationRoleRef) bool {
	if roleRef == nil || roleRef.APIGroup != "rbac.authorization.k8s.io" || roleRef.Name == "zke-agent" || len(k8svalidation.IsDNS1123Subdomain(roleRef.Name)) != 0 {
		return false
	}
	if resource == AuthorizationRoleBindings {
		return roleRef.Kind == "Role" || roleRef.Kind == "ClusterRole"
	}
	return resource == AuthorizationClusterRoleBindings && roleRef.Kind == "ClusterRole"
}

func forbiddenAuthorizationRule(rule AuthorizationPolicyRule) bool {
	for _, verb := range rule.Verbs {
		if verb == "escalate" || verb == "bind" || verb == "impersonate" {
			return true
		}
	}
	for _, resource := range rule.Resources {
		if resource == "secrets" || resource == "serviceaccounts/token" {
			return true
		}
	}
	return false
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
