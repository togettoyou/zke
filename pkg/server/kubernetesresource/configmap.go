package kubernetesresource

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"sort"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	maxConfigMapSize        = corev1.MaxSecretSize
	maxConfigMapAnnotations = 256 * 1024
)

var configMapIdentity = ResourceIdentity{
	Version:  "v1",
	Resource: "configmaps",
}

type ListConfigMapsInput struct {
	ClusterID     string
	Namespace     string
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type ConfigMapPage struct {
	ConfigMaps         []ConfigMapSummary `json:"config_maps"`
	ContinueToken      string             `json:"continue_token"`
	ResourceVersion    string             `json:"resource_version"`
	RemainingItemCount *int64             `json:"remaining_item_count"`
}

type ConfigMapSummary struct {
	Namespace         string            `json:"namespace"`
	Name              string            `json:"name"`
	UID               string            `json:"uid"`
	ResourceVersion   string            `json:"resource_version"`
	CreationTimestamp time.Time         `json:"creation_timestamp"`
	Labels            map[string]string `json:"labels"`
	DataKeys          []string          `json:"data_keys"`
	BinaryDataKeys    []string          `json:"binary_data_keys"`
	DataBytes         int64             `json:"data_bytes"`
	BinaryDataBytes   int64             `json:"binary_data_bytes"`
	Immutable         bool              `json:"immutable"`
}

type ConfigMapDetail struct {
	ConfigMapSummary
	Annotations map[string]string `json:"annotations"`
	Data        map[string]string `json:"data"`
	// BinaryData values use standard padded Base64. Keeping the wire model as
	// strings avoids exposing JSON's implementation-specific []byte handling.
	BinaryData map[string]string `json:"binary_data"`
}

type CreateConfigMapInput struct {
	ClusterID      string
	Namespace      string
	Name           string
	Labels         map[string]string
	Annotations    map[string]string
	Data           map[string]string
	BinaryData     map[string]string
	Immutable      bool
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type UpdateConfigMapInput struct {
	ClusterID       string
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	// Data and BinaryData are complete replacements and must both be present.
	Data           map[string]string
	BinaryData     map[string]string
	Immutable      *bool
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type DeleteConfigMapInput struct {
	ClusterID       string
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

func ConfigMapResourceIdentity() ResourceIdentity {
	return configMapIdentity
}

func (service *Service) ListConfigMaps(ctx context.Context, input ListConfigMapsInput) (ConfigMapPage, error) {
	if len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 {
		return ConfigMapPage{}, ErrInvalidInput
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID: input.ClusterID, Resource: configMapIdentity, Namespace: input.Namespace,
		Limit: input.Limit, ContinueToken: input.ContinueToken,
		LabelSelector: input.LabelSelector, FieldSelector: input.FieldSelector,
	})
	if err != nil {
		return ConfigMapPage{}, err
	}
	result := ConfigMapPage{
		ConfigMaps:    make([]ConfigMapSummary, 0, len(page.Items)),
		ContinueToken: page.ContinueToken, ResourceVersion: page.ResourceVersion,
		RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, err := configMapDetail(item, input.Namespace, "")
		if err != nil {
			return ConfigMapPage{}, err
		}
		result.ConfigMaps = append(result.ConfigMaps, detail.ConfigMapSummary)
	}
	return result, nil
}

func (service *Service) GetConfigMap(
	ctx context.Context,
	clusterID string,
	namespace string,
	name string,
) (ConfigMapDetail, error) {
	if !validConfigMapName(namespace, name) {
		return ConfigMapDetail{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID, Resource: configMapIdentity, Namespace: namespace, Name: name,
	})
	if err != nil {
		return ConfigMapDetail{}, err
	}
	return configMapDetail(object, namespace, name)
}

func (service *Service) CreateConfigMap(
	ctx context.Context,
	input CreateConfigMapInput,
) (ConfigMapDetail, error) {
	object, err := createConfigMapObject(input)
	if err != nil {
		return ConfigMapDetail{}, err
	}
	result, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID, Resource: configMapIdentity, Namespace: input.Namespace,
		Object: object, Options: MutationOptions{DryRun: input.DryRun},
		Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return ConfigMapDetail{}, err
	}
	return configMapDetail(result, input.Namespace, input.Name)
}

func (service *Service) UpdateConfigMap(
	ctx context.Context,
	input UpdateConfigMapInput,
) (ConfigMapDetail, error) {
	if !validConfigMapMutationIdentity(input.Namespace, input.Name, input.UID, input.ResourceVersion) ||
		input.Data == nil || input.BinaryData == nil {
		return ConfigMapDetail{}, ErrInvalidInput
	}
	binaryData, err := decodeConfigMapBinaryData(input.Data, input.BinaryData)
	if err != nil {
		return ConfigMapDetail{}, err
	}
	existing, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: input.ClusterID, Resource: configMapIdentity, Namespace: input.Namespace, Name: input.Name,
	})
	if err != nil {
		return ConfigMapDetail{}, err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.UID || current.GetResourceVersion() != input.ResourceVersion {
		return ConfigMapDetail{}, ErrUpstreamConflict
	}
	updated, err := updateConfigMapObject(existing, input, binaryData)
	if err != nil {
		return ConfigMapDetail{}, err
	}
	result, err := service.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: input.ClusterID, Resource: configMapIdentity, Namespace: input.Namespace, Name: input.Name,
		Object: updated, Options: MutationOptions{DryRun: input.DryRun},
		Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return ConfigMapDetail{}, err
	}
	return configMapDetail(result, input.Namespace, input.Name)
}

func (service *Service) DeleteConfigMap(ctx context.Context, input DeleteConfigMapInput) error {
	if !validConfigMapMutationIdentity(input.Namespace, input.Name, input.UID, input.ResourceVersion) {
		return ErrInvalidInput
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID: input.ClusterID, Resource: configMapIdentity, Namespace: input.Namespace, Name: input.Name,
		DryRun: input.DryRun, Confirm: input.Confirm,
		Preconditions:  DeletePreconditions{UID: input.UID, ResourceVersion: input.ResourceVersion},
		Propagation:    agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
		IdempotencyKey: input.IdempotencyKey,
	})
}

func createConfigMapObject(input CreateConfigMapInput) (map[string]any, error) {
	if !validConfigMapMetadata(input.Namespace, input.Name, input.Labels, input.Annotations) {
		return nil, ErrInvalidInput
	}
	binaryData, err := decodeConfigMapBinaryData(input.Data, input.BinaryData)
	if err != nil {
		return nil, err
	}
	immutable := input.Immutable
	object := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name: input.Name, Namespace: input.Namespace,
			Labels: maps.Clone(input.Labels), Annotations: maps.Clone(input.Annotations),
		},
		Data: maps.Clone(input.Data), BinaryData: binaryData,
	}
	if immutable {
		object.Immutable = &immutable
	}
	return configMapObject(object)
}

func updateConfigMapObject(
	existing map[string]any,
	input UpdateConfigMapInput,
	binaryData map[string][]byte,
) (map[string]any, error) {
	var object corev1.ConfigMap
	body, err := json.Marshal(existing)
	if err != nil || json.Unmarshal(body, &object) != nil ||
		object.APIVersion != "v1" || object.Kind != "ConfigMap" ||
		object.Namespace != input.Namespace || object.Name != input.Name {
		return nil, ErrInvalidResponse
	}
	requestedImmutable := object.Immutable
	if input.Immutable != nil {
		value := *input.Immutable
		requestedImmutable = &value
	}
	if object.Immutable != nil && *object.Immutable &&
		(!maps.Equal(object.Data, input.Data) || !equalBinaryData(object.BinaryData, binaryData) ||
			requestedImmutable == nil || !*requestedImmutable) {
		return nil, ErrConfigMapImmutable
	}
	if object.Immutable != nil && *object.Immutable {
		// Preserve nil-versus-empty map identity as well as values. Kubernetes
		// compares immutable ConfigMap data with reflect.DeepEqual, so replacing
		// an absent empty map with an explicit empty map would otherwise turn an
		// unchanged request into a rejected content mutation.
		return configMapObject(&object)
	}
	object.Data = maps.Clone(input.Data)
	object.BinaryData = cloneBinaryData(binaryData)
	object.Immutable = requestedImmutable
	return configMapObject(&object)
}

func configMapObject(object *corev1.ConfigMap) (map[string]any, error) {
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

func configMapDetail(object map[string]any, namespace, name string) (ConfigMapDetail, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return ConfigMapDetail{}, ErrInvalidResponse
	}
	var configMap corev1.ConfigMap
	if json.Unmarshal(body, &configMap) != nil || configMap.APIVersion != "v1" || configMap.Kind != "ConfigMap" ||
		configMap.Name == "" || configMap.Namespace != namespace || name != "" && configMap.Name != name {
		return ConfigMapDetail{}, ErrInvalidResponse
	}
	labels := maps.Clone(configMap.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := maps.Clone(configMap.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	data := maps.Clone(configMap.Data)
	if data == nil {
		data = map[string]string{}
	}
	binaryData := make(map[string]string, len(configMap.BinaryData))
	dataKeys := make([]string, 0, len(data))
	binaryDataKeys := make([]string, 0, len(configMap.BinaryData))
	var dataBytes, binaryDataBytes int64
	for key, value := range data {
		dataKeys = append(dataKeys, key)
		dataBytes += int64(len(value))
	}
	for key, value := range configMap.BinaryData {
		binaryDataKeys = append(binaryDataKeys, key)
		binaryDataBytes += int64(len(value))
		binaryData[key] = base64.StdEncoding.EncodeToString(value)
	}
	sort.Strings(dataKeys)
	sort.Strings(binaryDataKeys)
	return ConfigMapDetail{
		ConfigMapSummary: ConfigMapSummary{
			Namespace: configMap.Namespace, Name: configMap.Name, UID: string(configMap.UID),
			ResourceVersion: configMap.ResourceVersion, CreationTimestamp: configMap.CreationTimestamp.Time,
			Labels: labels, DataKeys: dataKeys, BinaryDataKeys: binaryDataKeys,
			DataBytes: dataBytes, BinaryDataBytes: binaryDataBytes,
			Immutable: configMap.Immutable != nil && *configMap.Immutable,
		},
		Annotations: annotations, Data: data, BinaryData: binaryData,
	}, nil
}

func validConfigMapMetadata(namespace, name string, labels, annotations map[string]string) bool {
	if !validConfigMapName(namespace, name) || !validNamespaceLabels(labels) {
		return false
	}
	total := 0
	for key, value := range annotations {
		if len(k8svalidation.IsQualifiedName(key)) != 0 {
			return false
		}
		total += len(key) + len(value)
		if total > maxConfigMapAnnotations {
			return false
		}
	}
	return true
}

func validConfigMapName(namespace, name string) bool {
	return len(k8svalidation.IsDNS1123Label(namespace)) == 0 &&
		len(k8svalidation.IsDNS1123Subdomain(name)) == 0
}

func validConfigMapMutationIdentity(namespace, name, uid, resourceVersion string) bool {
	return validConfigMapName(namespace, name) && strings.TrimSpace(uid) != "" && len(uid) <= 128 &&
		strings.TrimSpace(resourceVersion) != "" && len(resourceVersion) <= 256
}

func decodeConfigMapBinaryData(data, encoded map[string]string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(encoded))
	total := 0
	for key, value := range data {
		if len(k8svalidation.IsConfigMapKey(key)) != 0 {
			return nil, ErrInvalidInput
		}
		total += len(value)
		if total > maxConfigMapSize {
			return nil, ErrInvalidInput
		}
	}
	for key, value := range encoded {
		if len(k8svalidation.IsConfigMapKey(key)) != 0 {
			return nil, ErrInvalidInput
		}
		if _, exists := data[key]; exists {
			return nil, ErrInvalidInput
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil {
			return nil, ErrInvalidInput
		}
		total += len(decoded)
		if total > maxConfigMapSize {
			return nil, ErrInvalidInput
		}
		result[key] = decoded
	}
	return result, nil
}

func cloneBinaryData(source map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(source))
	for key, value := range source {
		result[key] = bytes.Clone(value)
	}
	return result
}

func equalBinaryData(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, exists := right[key]
		if !exists || !bytes.Equal(value, other) {
			return false
		}
	}
	return true
}
