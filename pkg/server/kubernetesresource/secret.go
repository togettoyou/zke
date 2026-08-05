package kubernetesresource

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"sort"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

/*
 * Secrets, on a path of their own.
 *
 * Everything here exists because a Secret is not a ConfigMap with a different
 * name. It is reachable only through this service, which is the only thing in
 * the process that sets `secretAccess` on a request; the generic resource and
 * YAML APIs refuse Secrets outright, and the Agent refuses them again unless
 * that flag is set and the namespace is not its own.
 *
 * What travels: the list returns key names and sizes and never a value, so
 * browsing a namespace does not move its credentials into a browser. The detail
 * returns values as Kubernetes stores them — standard Base64 — for the one page
 * that asked for one object by name.
 *
 * What does not travel: ZKE's own Secrets. They are the platform's credentials
 * rather than the operator's workload configuration, and an API that returned
 * them would be an API for reading the Agent's enrollment token.
 */

const (
	maxSecretSize        = corev1.MaxSecretSize
	maxSecretAnnotations = 256 * 1024
	managedByLabel       = "app.kubernetes.io/managed-by"
	managedByServer      = "zke-server"
)

// ErrSecretManagedByPlatform reports a Secret this API will not act on. It is
// distinct from a permission error: the caller may hold every Secret permission
// there is, and this object is still not theirs to read.
var ErrSecretManagedByPlatform = errors.New("secret is managed by ZKE")

// ErrSecretImmutable reports a change to a Secret Kubernetes has been told to
// keep as it is.
var ErrSecretImmutable = errors.New("secret is immutable")

var secretIdentity = ResourceIdentity{
	Version:  "v1",
	Resource: "secrets",
}

type ListSecretsInput struct {
	ClusterID     string
	Namespace     string
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type SecretPage struct {
	Secrets            []SecretSummary `json:"secrets"`
	ContinueToken      string          `json:"continue_token"`
	ResourceVersion    string          `json:"resource_version"`
	RemainingItemCount *int64          `json:"remaining_item_count"`
}

type SecretSummary struct {
	Namespace         string            `json:"namespace"`
	Name              string            `json:"name"`
	UID               string            `json:"uid"`
	ResourceVersion   string            `json:"resource_version"`
	CreationTimestamp time.Time         `json:"creation_timestamp"`
	Labels            map[string]string `json:"labels"`
	Type              string            `json:"type"`
	// Key names and the size of what they hold. Not the values: a list is a
	// page of many objects, and no page needs every credential in a namespace.
	DataKeys  []string `json:"data_keys"`
	DataBytes int64    `json:"data_bytes"`
	Immutable bool     `json:"immutable"`
}

type SecretDetail struct {
	SecretSummary
	Annotations map[string]string `json:"annotations"`
	// Values in standard padded Base64, as Kubernetes stores them. Decoding is
	// left to the caller so this response carries no assumption that a Secret's
	// bytes are text.
	Data map[string]string `json:"data"`
}

type CreateSecretInput struct {
	ClusterID      string
	Namespace      string
	Name           string
	Type           string
	Labels         map[string]string
	Annotations    map[string]string
	Data           map[string]string
	Immutable      bool
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type UpdateSecretInput struct {
	ClusterID       string
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	// A complete replacement, and required: an update that omitted it would be
	// indistinguishable from one emptying the Secret.
	Data           map[string]string
	Immutable      *bool
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type DeleteSecretInput struct {
	ClusterID       string
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

func SecretResourceIdentity() ResourceIdentity {
	return secretIdentity
}

func (service *Service) ListSecrets(ctx context.Context, input ListSecretsInput) (SecretPage, error) {
	if len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 {
		return SecretPage{}, ErrInvalidInput
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID: input.ClusterID, Resource: secretIdentity, Namespace: input.Namespace,
		Limit: input.Limit, ContinueToken: input.ContinueToken,
		LabelSelector: input.LabelSelector, FieldSelector: input.FieldSelector,
		secretAccess: true,
	})
	if err != nil {
		return SecretPage{}, err
	}
	result := SecretPage{
		Secrets:       make([]SecretSummary, 0, len(page.Items)),
		ContinueToken: page.ContinueToken, ResourceVersion: page.ResourceVersion,
		RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, err := secretDetail(item, input.Namespace, "")
		if err != nil {
			return SecretPage{}, err
		}
		// Dropped from the page rather than refused: a namespace holding one of
		// ZKE's own Secrets is still a namespace whose other Secrets an operator
		// may list. The page can therefore be shorter than the requested limit,
		// which continuation already allows for.
		if platformManagedLabels(detail.Labels) {
			continue
		}
		result.Secrets = append(result.Secrets, detail.SecretSummary)
	}
	return result, nil
}

func (service *Service) GetSecret(
	ctx context.Context,
	clusterID string,
	namespace string,
	name string,
) (SecretDetail, error) {
	if !validSecretName(namespace, name) {
		return SecretDetail{}, ErrInvalidInput
	}
	object, err := service.getSecretObject(ctx, clusterID, namespace, name)
	if err != nil {
		return SecretDetail{}, err
	}
	return secretDetail(object, namespace, name)
}

func (service *Service) CreateSecret(
	ctx context.Context,
	input CreateSecretInput,
) (SecretDetail, error) {
	object, err := createSecretObject(input)
	if err != nil {
		return SecretDetail{}, err
	}
	result, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID, Resource: secretIdentity, Namespace: input.Namespace,
		Object: object, Options: MutationOptions{DryRun: input.DryRun},
		Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey,
		secretAccess: true,
	})
	if err != nil {
		return SecretDetail{}, err
	}
	return secretDetail(result, input.Namespace, input.Name)
}

func (service *Service) UpdateSecret(
	ctx context.Context,
	input UpdateSecretInput,
) (SecretDetail, error) {
	if !validSecretMutationIdentity(input.Namespace, input.Name, input.UID, input.ResourceVersion) ||
		input.Data == nil {
		return SecretDetail{}, ErrInvalidInput
	}
	data, err := decodeSecretData(input.Data)
	if err != nil {
		return SecretDetail{}, err
	}
	existing, err := service.getSecretObject(ctx, input.ClusterID, input.Namespace, input.Name)
	if err != nil {
		return SecretDetail{}, err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.UID ||
		current.GetResourceVersion() != input.ResourceVersion {
		return SecretDetail{}, ErrUpstreamConflict
	}
	updated, err := updateSecretObject(existing, input, data)
	if err != nil {
		return SecretDetail{}, err
	}
	result, err := service.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: input.ClusterID, Resource: secretIdentity,
		Namespace: input.Namespace, Name: input.Name,
		Object: updated, Options: MutationOptions{DryRun: input.DryRun},
		Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey,
		secretAccess: true,
	})
	if err != nil {
		return SecretDetail{}, err
	}
	return secretDetail(result, input.Namespace, input.Name)
}

func (service *Service) DeleteSecret(ctx context.Context, input DeleteSecretInput) error {
	if !validSecretMutationIdentity(input.Namespace, input.Name, input.UID, input.ResourceVersion) {
		return ErrInvalidInput
	}
	// Read before deleting, so one of ZKE's own Secrets is refused rather than
	// removed by a caller who knew its name.
	if _, err := service.getSecretObject(
		ctx, input.ClusterID, input.Namespace, input.Name,
	); err != nil {
		return err
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID: input.ClusterID, Resource: secretIdentity,
		Namespace: input.Namespace, Name: input.Name,
		DryRun: input.DryRun, Confirm: input.Confirm,
		Preconditions:  DeletePreconditions{UID: input.UID, ResourceVersion: input.ResourceVersion},
		Propagation:    agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
		IdempotencyKey: input.IdempotencyKey,
		secretAccess:   true,
	})
}

/*
 * Reads one Secret and refuses ZKE's own.
 *
 * Named access is refused rather than filtered: an operator who typed the name
 * of the Agent's enrollment Secret asked a question with only one honest
 * answer, and returning an empty object instead would suggest the name was
 * wrong.
 */
func (service *Service) getSecretObject(
	ctx context.Context,
	clusterID string,
	namespace string,
	name string,
) (map[string]any, error) {
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID, Resource: secretIdentity,
		Namespace: namespace, Name: name, secretAccess: true,
	})
	if err != nil {
		return nil, err
	}
	current := &unstructured.Unstructured{Object: object}
	if platformManagedLabels(current.GetLabels()) {
		return nil, ErrSecretManagedByPlatform
	}
	return object, nil
}

func platformManagedLabels(labels map[string]string) bool {
	return labels[managedByLabel] == managedByServer
}

func createSecretObject(input CreateSecretInput) (map[string]any, error) {
	if !validSecretMetadata(input.Namespace, input.Name, input.Labels, input.Annotations) ||
		!validSecretType(input.Type) ||
		platformManagedLabels(input.Labels) {
		return nil, ErrInvalidInput
	}
	data, err := decodeSecretData(input.Data)
	if err != nil {
		return nil, err
	}
	secretType := corev1.SecretType(input.Type)
	if secretType == "" {
		secretType = corev1.SecretTypeOpaque
	}
	object := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name: input.Name, Namespace: input.Namespace,
			Labels: maps.Clone(input.Labels), Annotations: maps.Clone(input.Annotations),
		},
		Type: secretType,
		Data: data,
	}
	if input.Immutable {
		immutable := true
		object.Immutable = &immutable
	}
	return secretObject(object)
}

func updateSecretObject(
	existing map[string]any,
	input UpdateSecretInput,
	data map[string][]byte,
) (map[string]any, error) {
	var object corev1.Secret
	body, err := json.Marshal(existing)
	if err != nil || json.Unmarshal(body, &object) != nil ||
		object.APIVersion != "v1" || object.Kind != "Secret" ||
		object.Namespace != input.Namespace || object.Name != input.Name {
		return nil, ErrInvalidResponse
	}
	requestedImmutable := object.Immutable
	if input.Immutable != nil {
		value := *input.Immutable
		requestedImmutable = &value
	}
	if object.Immutable != nil && *object.Immutable {
		// Kubernetes refuses both a content change and a return to mutable, so
		// the only update an immutable Secret accepts is one that changes
		// nothing — and that is not worth a write.
		return nil, ErrSecretImmutable
	}
	// `type` is fixed at creation. It is not taken from the request at all, so
	// there is no value a caller could send that would appear to change it.
	object.Data = cloneBinaryData(data)
	object.StringData = nil
	object.Immutable = requestedImmutable
	return secretObject(&object)
}

func secretObject(object *corev1.Secret) (map[string]any, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return nil, ErrInvalidInput
	}
	var result unstructured.Unstructured
	if result.UnmarshalJSON(body) != nil {
		return nil, ErrInvalidInput
	}
	return result.Object, nil
}

func secretDetail(object map[string]any, namespace string, name string) (SecretDetail, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return SecretDetail{}, ErrInvalidResponse
	}
	var secret corev1.Secret
	if json.Unmarshal(body, &secret) != nil ||
		secret.APIVersion != "v1" || secret.Kind != "Secret" ||
		len(k8svalidation.IsDNS1123Label(secret.Namespace)) != 0 ||
		(namespace != "" && secret.Namespace != namespace) ||
		secret.Name == "" || (name != "" && secret.Name != name) {
		return SecretDetail{}, ErrInvalidResponse
	}
	keys := make([]string, 0, len(secret.Data))
	values := make(map[string]string, len(secret.Data))
	var total int64
	for key, value := range secret.Data {
		keys = append(keys, key)
		values[key] = base64.StdEncoding.EncodeToString(value)
		total += int64(len(value))
	}
	sort.Strings(keys)
	return SecretDetail{
		SecretSummary: SecretSummary{
			Namespace:         secret.Namespace,
			Name:              secret.Name,
			UID:               string(secret.UID),
			ResourceVersion:   secret.ResourceVersion,
			CreationTimestamp: secret.CreationTimestamp.Time,
			Labels:            cloneMap(secret.Labels),
			Type:              string(secret.Type),
			DataKeys:          keys,
			DataBytes:         total,
			Immutable:         secret.Immutable != nil && *secret.Immutable,
		},
		Annotations: cloneMap(secret.Annotations),
		Data:        values,
	}, nil
}

/*
 * Decodes the submitted values and enforces the size Kubernetes enforces.
 *
 * Values arrive Base64-encoded because that is how they are returned, and a
 * form that re-submits what it was shown should not have to know which of the
 * two representations it is holding.
 */
func decodeSecretData(data map[string]string) (map[string][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	result := make(map[string][]byte, len(data))
	var total int
	for key, value := range data {
		if !validSecretKey(key) {
			return nil, ErrInvalidInput
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, ErrInvalidInput
		}
		total += len(decoded)
		if total > maxSecretSize {
			return nil, ErrInvalidInput
		}
		result[key] = decoded
	}
	return result, nil
}

/*
 * The types this API will create.
 *
 * `kubernetes.io/service-account-token` is refused: it is issued by Kubernetes
 * for a ServiceAccount, and creating one by hand is a way to mint a token for
 * an identity rather than a way to store configuration.
 */
func validSecretType(value string) bool {
	switch corev1.SecretType(value) {
	case "",
		corev1.SecretTypeOpaque,
		corev1.SecretTypeDockerConfigJson,
		corev1.SecretTypeBasicAuth,
		corev1.SecretTypeSSHAuth,
		corev1.SecretTypeTLS:
		return true
	default:
		return false
	}
}

func validSecretKey(key string) bool {
	return key != "" && key != "." && key != ".." &&
		len(key) <= 253 && len(k8svalidation.IsConfigMapKey(key)) == 0
}

func validSecretName(namespace string, name string) bool {
	return len(k8svalidation.IsDNS1123Label(namespace)) == 0 &&
		len(k8svalidation.IsDNS1123Subdomain(name)) == 0
}

func validSecretMutationIdentity(namespace, name, uid, resourceVersion string) bool {
	return validSecretName(namespace, name) && uid != "" && resourceVersion != ""
}

func validSecretMetadata(
	namespace string,
	name string,
	labels map[string]string,
	annotations map[string]string,
) bool {
	if !validSecretName(namespace, name) || !validNamespaceLabels(labels) {
		return false
	}
	total := 0
	for key, value := range annotations {
		if len(k8svalidation.IsQualifiedName(key)) != 0 {
			return false
		}
		total += len(key) + len(value)
	}
	return total <= maxSecretAnnotations
}
