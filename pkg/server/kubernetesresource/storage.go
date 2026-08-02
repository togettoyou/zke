package kubernetesresource

import (
	"context"
	"encoding/json"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
)

type StorageResource string

const (
	StoragePersistentVolumes      StorageResource = "persistentvolumes"
	StoragePersistentVolumeClaims StorageResource = "persistentvolumeclaims"
	StorageClasses                StorageResource = "storageclasses"

	maxStorageAnnotations  = 256 * 1024
	maxStorageMapSize      = 256 * 1024
	maxStorageMapEntries   = 512
	maxStorageMountOptions = 128
)

var storageResourceIdentities = map[StorageResource]ResourceIdentity{
	StoragePersistentVolumes:      {Version: "v1", Resource: "persistentvolumes"},
	StoragePersistentVolumeClaims: {Version: "v1", Resource: "persistentvolumeclaims"},
	StorageClasses:                {Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"},
}

type ListStorageResourcesInput struct {
	ClusterID     string
	Namespace     string
	Resource      StorageResource
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type StorageResourcePage struct {
	Resources          []StorageResourceSummary `json:"resources"`
	ContinueToken      string                   `json:"continue_token"`
	ResourceVersion    string                   `json:"resource_version"`
	RemainingItemCount *int64                   `json:"remaining_item_count"`
}

type StorageResourceSummary struct {
	Resource              StorageResource               `json:"resource"`
	APIVersion            string                        `json:"api_version"`
	Kind                  string                        `json:"kind"`
	Namespace             string                        `json:"namespace"`
	Name                  string                        `json:"name"`
	UID                   string                        `json:"uid"`
	ResourceVersion       string                        `json:"resource_version"`
	CreationTimestamp     time.Time                     `json:"creation_timestamp"`
	Labels                map[string]string             `json:"labels"`
	PersistentVolume      *PersistentVolumeSummary      `json:"persistent_volume,omitempty"`
	PersistentVolumeClaim *PersistentVolumeClaimSummary `json:"persistent_volume_claim,omitempty"`
	StorageClass          *StorageClassSummary          `json:"storage_class,omitempty"`
}

type StorageResourceDetail struct {
	StorageResourceSummary
	Annotations                 map[string]string            `json:"annotations"`
	PersistentVolume            *PersistentVolumeDetail      `json:"persistent_volume_detail,omitempty"`
	PersistentVolumeClaimDetail *PersistentVolumeClaimDetail `json:"persistent_volume_claim_detail,omitempty"`
	StorageClassDetail          *StorageClassDetail          `json:"storage_class_detail,omitempty"`
}

type StorageObjectReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type PersistentVolumeSummary struct {
	Phase              string                  `json:"phase"`
	Capacity           string                  `json:"capacity"`
	AccessModes        []string                `json:"access_modes"`
	ReclaimPolicy      string                  `json:"reclaim_policy"`
	StorageClassName   string                  `json:"storage_class_name"`
	VolumeMode         string                  `json:"volume_mode"`
	SourceType         string                  `json:"source_type"`
	ClaimRef           *StorageObjectReference `json:"claim_ref,omitempty"`
	LastTransitionTime *time.Time              `json:"last_transition_time"`
}

type PersistentVolumeDetail struct {
	MountOptions []string                   `json:"mount_options"`
	NodeAffinity *StorageNodeSelector       `json:"node_affinity,omitempty"`
	Source       PersistentVolumeSourceView `json:"source"`
	Reason       string                     `json:"reason"`
	Message      string                     `json:"message"`
}

type PersistentVolumeClaimSummary struct {
	Phase             string   `json:"phase"`
	RequestedCapacity string   `json:"requested_capacity"`
	Capacity          string   `json:"capacity"`
	AccessModes       []string `json:"access_modes"`
	StorageClassName  *string  `json:"storage_class_name"`
	VolumeName        string   `json:"volume_name"`
	VolumeMode        string   `json:"volume_mode"`
}

type PersistentVolumeClaimCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastProbeTime      time.Time `json:"last_probe_time"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

type PersistentVolumeClaimDetail struct {
	Selector   *WorkloadSelector                `json:"selector,omitempty"`
	Conditions []PersistentVolumeClaimCondition `json:"conditions"`
}

type StorageClassSummary struct {
	Provisioner          string `json:"provisioner"`
	ReclaimPolicy        string `json:"reclaim_policy"`
	VolumeBindingMode    string `json:"volume_binding_mode"`
	AllowVolumeExpansion bool   `json:"allow_volume_expansion"`
	Default              bool   `json:"default"`
}

type StorageTopologyTerm struct {
	MatchLabelExpressions []StorageTopologyRequirement `json:"match_label_expressions"`
}

type StorageTopologyRequirement struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

type StorageClassDetail struct {
	Parameters        map[string]string     `json:"parameters"`
	MountOptions      []string              `json:"mount_options"`
	AllowedTopologies []StorageTopologyTerm `json:"allowed_topologies"`
}

type StorageNodeSelector struct {
	Terms []StorageNodeSelectorTerm `json:"terms"`
}

type StorageNodeSelectorTerm struct {
	MatchExpressions []StorageNodeSelectorRequirement `json:"match_expressions"`
	MatchFields      []StorageNodeSelectorRequirement `json:"match_fields"`
}

type StorageNodeSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

type StorageSecretReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type CSIPersistentVolumeSource struct {
	Driver                     string                  `json:"driver"`
	VolumeHandle               string                  `json:"volume_handle"`
	ReadOnly                   bool                    `json:"read_only"`
	FSType                     string                  `json:"fs_type"`
	VolumeAttributes           map[string]string       `json:"volume_attributes"`
	ControllerPublishSecretRef *StorageSecretReference `json:"controller_publish_secret_ref,omitempty"`
	NodeStageSecretRef         *StorageSecretReference `json:"node_stage_secret_ref,omitempty"`
	NodePublishSecretRef       *StorageSecretReference `json:"node_publish_secret_ref,omitempty"`
	ControllerExpandSecretRef  *StorageSecretReference `json:"controller_expand_secret_ref,omitempty"`
	NodeExpandSecretRef        *StorageSecretReference `json:"node_expand_secret_ref,omitempty"`
}

type NFSPersistentVolumeSource struct {
	Server   string `json:"server"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
}

type LocalPersistentVolumeSource struct {
	Path   string `json:"path"`
	FSType string `json:"fs_type"`
}

type PersistentVolumeSourceView struct {
	Type  string                       `json:"type"`
	CSI   *CSIPersistentVolumeSource   `json:"csi,omitempty"`
	NFS   *NFSPersistentVolumeSource   `json:"nfs,omitempty"`
	Local *LocalPersistentVolumeSource `json:"local,omitempty"`
}

type PersistentVolumeCreateSpec struct {
	Capacity         string                      `json:"capacity"`
	AccessModes      []string                    `json:"access_modes"`
	ReclaimPolicy    string                      `json:"reclaim_policy"`
	StorageClassName string                      `json:"storage_class_name"`
	VolumeMode       string                      `json:"volume_mode"`
	MountOptions     []string                    `json:"mount_options"`
	ClaimRef         *StorageObjectReference     `json:"claim_ref,omitempty"`
	NodeAffinity     *StorageNodeSelector        `json:"node_affinity,omitempty"`
	Source           *PersistentVolumeSourceView `json:"source"`
}

type PersistentVolumeClaimCreateSpec struct {
	RequestedCapacity string            `json:"requested_capacity"`
	AccessModes       []string          `json:"access_modes"`
	StorageClassName  *string           `json:"storage_class_name"`
	VolumeName        string            `json:"volume_name"`
	VolumeMode        string            `json:"volume_mode"`
	Selector          *WorkloadSelector `json:"selector,omitempty"`
}

type StorageClassCreateSpec struct {
	Provisioner          string                `json:"provisioner"`
	Parameters           map[string]string     `json:"parameters"`
	ReclaimPolicy        string                `json:"reclaim_policy"`
	VolumeBindingMode    string                `json:"volume_binding_mode"`
	AllowVolumeExpansion bool                  `json:"allow_volume_expansion"`
	MountOptions         []string              `json:"mount_options"`
	AllowedTopologies    []StorageTopologyTerm `json:"allowed_topologies"`
}

type CreateStorageResourceInput struct {
	ClusterID             string
	Namespace             string
	Resource              StorageResource
	Name                  string
	Labels                map[string]string
	Annotations           map[string]string
	PersistentVolume      *PersistentVolumeCreateSpec
	PersistentVolumeClaim *PersistentVolumeClaimCreateSpec
	StorageClass          *StorageClassCreateSpec
	DryRun                bool
	Confirm               bool
	IdempotencyKey        string
}

type PersistentVolumeUpdateSpec struct {
	ReclaimPolicy string `json:"reclaim_policy"`
}

type PersistentVolumeClaimUpdateSpec struct {
	RequestedCapacity string `json:"requested_capacity"`
}

type StorageClassUpdateSpec struct {
	AllowVolumeExpansion *bool `json:"allow_volume_expansion"`
}

type UpdateStorageResourceInput struct {
	ClusterID             string
	Namespace             string
	Resource              StorageResource
	Name                  string
	UID                   string
	ResourceVersion       string
	PersistentVolume      *PersistentVolumeUpdateSpec
	PersistentVolumeClaim *PersistentVolumeClaimUpdateSpec
	StorageClass          *StorageClassUpdateSpec
	DryRun                bool
	Confirm               bool
	IdempotencyKey        string
}

type DeleteStorageResourceInput struct {
	ClusterID       string
	Namespace       string
	Resource        StorageResource
	Name            string
	UID             string
	ResourceVersion string
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

func ParseStorageResource(value string) (StorageResource, bool) {
	resourceName := StorageResource(value)
	_, exists := storageResourceIdentities[resourceName]
	return resourceName, exists
}

func StorageResourceIdentity(resourceName StorageResource) (ResourceIdentity, bool) {
	identity, exists := storageResourceIdentities[resourceName]
	return identity, exists
}

func (service *Service) ListStorageResources(ctx context.Context, input ListStorageResourcesInput) (StorageResourcePage, error) {
	identity, ok := storageResourceIdentities[input.Resource]
	if !ok || !validStorageScope(input.Resource, input.Namespace) {
		return StorageResourcePage{}, ErrInvalidInput
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace,
		Limit: input.Limit, ContinueToken: input.ContinueToken,
		LabelSelector: input.LabelSelector, FieldSelector: input.FieldSelector,
	})
	if err != nil {
		return StorageResourcePage{}, err
	}
	result := StorageResourcePage{
		Resources: make([]StorageResourceSummary, 0, len(page.Items)), ContinueToken: page.ContinueToken,
		ResourceVersion: page.ResourceVersion, RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, err := storageResourceDetail(item, input.Resource, input.Namespace, "")
		if err != nil {
			return StorageResourcePage{}, err
		}
		result.Resources = append(result.Resources, detail.StorageResourceSummary)
	}
	return result, nil
}

func (service *Service) GetStorageResource(
	ctx context.Context,
	clusterID, namespace string,
	resourceName StorageResource,
	name string,
) (StorageResourceDetail, error) {
	identity, ok := storageResourceIdentities[resourceName]
	if !ok || !validStorageScope(resourceName, namespace) || len(k8svalidation.IsDNS1123Subdomain(name)) != 0 {
		return StorageResourceDetail{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID, Resource: identity, Namespace: namespace, Name: name,
	})
	if err != nil {
		return StorageResourceDetail{}, err
	}
	return storageResourceDetail(object, resourceName, namespace, name)
}

func (service *Service) CreateStorageResource(
	ctx context.Context,
	input CreateStorageResourceInput,
) (StorageResourceDetail, error) {
	identity, ok := storageResourceIdentities[input.Resource]
	if !ok {
		return StorageResourceDetail{}, ErrInvalidInput
	}
	object, err := createStorageObject(input)
	if err != nil {
		return StorageResourceDetail{}, err
	}
	result, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Object: object,
		Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return StorageResourceDetail{}, err
	}
	return storageResourceDetail(result, input.Resource, input.Namespace, input.Name)
}

func (service *Service) UpdateStorageResource(
	ctx context.Context,
	input UpdateStorageResourceInput,
) (StorageResourceDetail, error) {
	identity, ok := storageResourceIdentities[input.Resource]
	if !ok || !validStorageMutationIdentity(input.Resource, input.Namespace, input.Name, input.UID, input.ResourceVersion) ||
		!validStorageUpdateSpec(input) {
		return StorageResourceDetail{}, ErrInvalidInput
	}
	existing, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
	})
	if err != nil {
		return StorageResourceDetail{}, err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.UID || current.GetResourceVersion() != input.ResourceVersion {
		return StorageResourceDetail{}, ErrUpstreamConflict
	}
	updated, err := updateStorageObject(existing, input)
	if err != nil {
		return StorageResourceDetail{}, err
	}
	result, err := service.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
		Object: updated, Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return StorageResourceDetail{}, err
	}
	return storageResourceDetail(result, input.Resource, input.Namespace, input.Name)
}

func (service *Service) DeleteStorageResource(ctx context.Context, input DeleteStorageResourceInput) error {
	identity, ok := storageResourceIdentities[input.Resource]
	if !ok || !validStorageMutationIdentity(input.Resource, input.Namespace, input.Name, input.UID, input.ResourceVersion) {
		return ErrInvalidInput
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
		DryRun: input.DryRun, Confirm: input.Confirm,
		Preconditions:  DeletePreconditions{UID: input.UID, ResourceVersion: input.ResourceVersion},
		Propagation:    agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
		IdempotencyKey: input.IdempotencyKey,
	})
}

func validStorageScope(resourceName StorageResource, namespace string) bool {
	if resourceName == StoragePersistentVolumeClaims {
		return len(k8svalidation.IsDNS1123Label(namespace)) == 0
	}
	return namespace == ""
}

func validStorageMutationIdentity(resourceName StorageResource, namespace, name, uid, resourceVersion string) bool {
	return validStorageScope(resourceName, namespace) && len(k8svalidation.IsDNS1123Subdomain(name)) == 0 &&
		strings.TrimSpace(uid) != "" && len(uid) <= 128 && strings.TrimSpace(resourceVersion) != "" && len(resourceVersion) <= 256
}

func validStorageMetadata(resourceName StorageResource, namespace, name string, labels, annotations map[string]string) bool {
	if !validStorageScope(resourceName, namespace) || len(k8svalidation.IsDNS1123Subdomain(name)) != 0 || !validNamespaceLabels(labels) {
		return false
	}
	total := 0
	for key, value := range annotations {
		if len(k8svalidation.IsQualifiedName(key)) != 0 {
			return false
		}
		total += len(key) + len(value)
		if total > maxStorageAnnotations {
			return false
		}
	}
	return true
}

func validStorageUpdateSpec(input UpdateStorageResourceInput) bool {
	switch input.Resource {
	case StoragePersistentVolumes:
		return input.PersistentVolume != nil && input.PersistentVolumeClaim == nil && input.StorageClass == nil &&
			validReclaimPolicy(input.PersistentVolume.ReclaimPolicy, false)
	case StoragePersistentVolumeClaims:
		return input.PersistentVolume == nil && input.PersistentVolumeClaim != nil && input.StorageClass == nil &&
			validPositiveQuantity(input.PersistentVolumeClaim.RequestedCapacity)
	case StorageClasses:
		return input.PersistentVolume == nil && input.PersistentVolumeClaim == nil && input.StorageClass != nil &&
			input.StorageClass.AllowVolumeExpansion != nil
	default:
		return false
	}
}

func createStorageObject(input CreateStorageResourceInput) (map[string]any, error) {
	if !validStorageMetadata(input.Resource, input.Namespace, input.Name, input.Labels, input.Annotations) {
		return nil, ErrInvalidInput
	}
	metadata := metav1.ObjectMeta{
		Name: input.Name, Namespace: input.Namespace, Labels: maps.Clone(input.Labels), Annotations: maps.Clone(input.Annotations),
	}
	var object any
	switch input.Resource {
	case StoragePersistentVolumes:
		if input.PersistentVolume == nil || input.PersistentVolumeClaim != nil || input.StorageClass != nil {
			return nil, ErrInvalidInput
		}
		spec, err := persistentVolumeKubernetesSpec(*input.PersistentVolume)
		if err != nil {
			return nil, err
		}
		object = &corev1.PersistentVolume{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"}, ObjectMeta: metadata, Spec: spec,
		}
	case StoragePersistentVolumeClaims:
		if input.PersistentVolume != nil || input.PersistentVolumeClaim == nil || input.StorageClass != nil {
			return nil, ErrInvalidInput
		}
		spec, err := persistentVolumeClaimKubernetesSpec(*input.PersistentVolumeClaim)
		if err != nil {
			return nil, err
		}
		object = &corev1.PersistentVolumeClaim{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"}, ObjectMeta: metadata, Spec: spec,
		}
	case StorageClasses:
		if input.PersistentVolume != nil || input.PersistentVolumeClaim != nil || input.StorageClass == nil {
			return nil, ErrInvalidInput
		}
		class, err := storageClassKubernetesObject(metadata, *input.StorageClass)
		if err != nil {
			return nil, err
		}
		object = class
	default:
		return nil, ErrInvalidInput
	}
	return typedStorageObject(object, ErrInvalidInput)
}

func updateStorageObject(existing map[string]any, input UpdateStorageResourceInput) (map[string]any, error) {
	body, err := json.Marshal(existing)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	switch input.Resource {
	case StoragePersistentVolumes:
		var object corev1.PersistentVolume
		if json.Unmarshal(body, &object) != nil || !validTypedStorageIdentity(&object.ObjectMeta, object.APIVersion, object.Kind, input.Namespace, input.Name, "v1", "PersistentVolume") {
			return nil, ErrInvalidResponse
		}
		object.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimPolicy(input.PersistentVolume.ReclaimPolicy)
		return typedStorageObject(&object, ErrInvalidResponse)
	case StoragePersistentVolumeClaims:
		var object corev1.PersistentVolumeClaim
		if json.Unmarshal(body, &object) != nil || !validTypedStorageIdentity(&object.ObjectMeta, object.APIVersion, object.Kind, input.Namespace, input.Name, "v1", "PersistentVolumeClaim") {
			return nil, ErrInvalidResponse
		}
		requested, err := resource.ParseQuantity(input.PersistentVolumeClaim.RequestedCapacity)
		if err != nil || requested.Sign() <= 0 {
			return nil, ErrInvalidInput
		}
		current := object.Spec.Resources.Requests[corev1.ResourceStorage]
		if !current.IsZero() && requested.Cmp(current) < 0 {
			return nil, ErrInvalidInput
		}
		if object.Spec.Resources.Requests == nil {
			object.Spec.Resources.Requests = corev1.ResourceList{}
		}
		object.Spec.Resources.Requests[corev1.ResourceStorage] = requested
		return typedStorageObject(&object, ErrInvalidResponse)
	case StorageClasses:
		var object storagev1.StorageClass
		if json.Unmarshal(body, &object) != nil || !validTypedStorageIdentity(&object.ObjectMeta, object.APIVersion, object.Kind, input.Namespace, input.Name, "storage.k8s.io/v1", "StorageClass") {
			return nil, ErrInvalidResponse
		}
		value := *input.StorageClass.AllowVolumeExpansion
		object.AllowVolumeExpansion = &value
		return typedStorageObject(&object, ErrInvalidResponse)
	default:
		return nil, ErrInvalidInput
	}
}

func persistentVolumeKubernetesSpec(input PersistentVolumeCreateSpec) (corev1.PersistentVolumeSpec, error) {
	capacity, err := resource.ParseQuantity(input.Capacity)
	if err != nil || capacity.Sign() <= 0 || !validAccessModes(input.AccessModes) ||
		!validReclaimPolicy(input.ReclaimPolicy, true) || !validVolumeMode(input.VolumeMode) ||
		!validMountOptions(input.MountOptions) || input.Source == nil {
		return corev1.PersistentVolumeSpec{}, ErrInvalidInput
	}
	source, err := persistentVolumeKubernetesSource(*input.Source, input.VolumeMode)
	if err != nil {
		return corev1.PersistentVolumeSpec{}, err
	}
	nodeSelector, err := storageNodeSelector(input.NodeAffinity)
	if err != nil || input.Source.Local != nil && nodeSelector == nil {
		return corev1.PersistentVolumeSpec{}, ErrInvalidInput
	}
	mode := corev1.PersistentVolumeFilesystem
	if input.VolumeMode != "" {
		mode = corev1.PersistentVolumeMode(input.VolumeMode)
	}
	policy := corev1.PersistentVolumeReclaimRetain
	if input.ReclaimPolicy != "" {
		policy = corev1.PersistentVolumeReclaimPolicy(input.ReclaimPolicy)
	}
	spec := corev1.PersistentVolumeSpec{
		Capacity: corev1.ResourceList{corev1.ResourceStorage: capacity}, PersistentVolumeSource: source,
		AccessModes: storageAccessModes(input.AccessModes), PersistentVolumeReclaimPolicy: policy,
		StorageClassName: input.StorageClassName, MountOptions: slices.Clone(input.MountOptions), VolumeMode: &mode,
		NodeAffinity: nodeSelector,
	}
	if input.ClaimRef != nil {
		if len(k8svalidation.IsDNS1123Label(input.ClaimRef.Namespace)) != 0 || len(k8svalidation.IsDNS1123Subdomain(input.ClaimRef.Name)) != 0 || input.ClaimRef.UID != "" {
			return corev1.PersistentVolumeSpec{}, ErrInvalidInput
		}
		spec.ClaimRef = &corev1.ObjectReference{Kind: "PersistentVolumeClaim", APIVersion: "v1", Namespace: input.ClaimRef.Namespace, Name: input.ClaimRef.Name}
	}
	return spec, nil
}

func persistentVolumeClaimKubernetesSpec(input PersistentVolumeClaimCreateSpec) (corev1.PersistentVolumeClaimSpec, error) {
	requested, err := resource.ParseQuantity(input.RequestedCapacity)
	if err != nil || requested.Sign() <= 0 || !validAccessModes(input.AccessModes) || !validVolumeMode(input.VolumeMode) ||
		(input.VolumeName != "" && len(k8svalidation.IsDNS1123Subdomain(input.VolumeName)) != 0) {
		return corev1.PersistentVolumeClaimSpec{}, ErrInvalidInput
	}
	selector, err := storageLabelSelector(input.Selector)
	if err != nil {
		return corev1.PersistentVolumeClaimSpec{}, ErrInvalidInput
	}
	mode := corev1.PersistentVolumeFilesystem
	if input.VolumeMode != "" {
		mode = corev1.PersistentVolumeMode(input.VolumeMode)
	}
	return corev1.PersistentVolumeClaimSpec{
		AccessModes: storageAccessModes(input.AccessModes), Selector: selector,
		Resources:  corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: requested}},
		VolumeName: input.VolumeName, StorageClassName: cloneStringPointer(input.StorageClassName), VolumeMode: &mode,
	}, nil
}

func storageClassKubernetesObject(metadata metav1.ObjectMeta, input StorageClassCreateSpec) (*storagev1.StorageClass, error) {
	if len(k8svalidation.IsQualifiedName(strings.ToLower(input.Provisioner))) != 0 || input.Provisioner != strings.TrimSpace(input.Provisioner) ||
		!validStorageMap(input.Parameters, false) || !validReclaimPolicy(input.ReclaimPolicy, true) ||
		!validVolumeBindingMode(input.VolumeBindingMode) || !validMountOptions(input.MountOptions) ||
		!validStorageTopologies(input.AllowedTopologies) {
		return nil, ErrInvalidInput
	}
	policy := corev1.PersistentVolumeReclaimDelete
	if input.ReclaimPolicy != "" {
		policy = corev1.PersistentVolumeReclaimPolicy(input.ReclaimPolicy)
	}
	bindingMode := storagev1.VolumeBindingImmediate
	if input.VolumeBindingMode != "" {
		bindingMode = storagev1.VolumeBindingMode(input.VolumeBindingMode)
	}
	allowExpansion := input.AllowVolumeExpansion
	return &storagev1.StorageClass{
		TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"}, ObjectMeta: metadata,
		Provisioner: input.Provisioner, Parameters: maps.Clone(input.Parameters), ReclaimPolicy: &policy,
		MountOptions: slices.Clone(input.MountOptions), AllowVolumeExpansion: &allowExpansion,
		VolumeBindingMode: &bindingMode, AllowedTopologies: storageTopologyTerms(input.AllowedTopologies),
	}, nil
}

func persistentVolumeKubernetesSource(input PersistentVolumeSourceView, volumeMode string) (corev1.PersistentVolumeSource, error) {
	count := 0
	if input.CSI != nil {
		count++
	}
	if input.NFS != nil {
		count++
	}
	if input.Local != nil {
		count++
	}
	if count != 1 || input.Type != "" &&
		(input.CSI != nil && input.Type != "csi" || input.NFS != nil && input.Type != "nfs" || input.Local != nil && input.Type != "local") {
		return corev1.PersistentVolumeSource{}, ErrInvalidInput
	}
	if input.CSI != nil {
		value := input.CSI
		if len(k8svalidation.IsQualifiedName(strings.ToLower(value.Driver))) != 0 || strings.TrimSpace(value.VolumeHandle) == "" ||
			!validStorageMap(value.VolumeAttributes, false) || volumeMode == string(corev1.PersistentVolumeBlock) && value.FSType != "" ||
			!validStorageSecretReferences(value.ControllerPublishSecretRef, value.NodeStageSecretRef, value.NodePublishSecretRef, value.ControllerExpandSecretRef, value.NodeExpandSecretRef) {
			return corev1.PersistentVolumeSource{}, ErrInvalidInput
		}
		return corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
			Driver: value.Driver, VolumeHandle: value.VolumeHandle, ReadOnly: value.ReadOnly, FSType: value.FSType,
			VolumeAttributes:           maps.Clone(value.VolumeAttributes),
			ControllerPublishSecretRef: kubernetesSecretReference(value.ControllerPublishSecretRef),
			NodeStageSecretRef:         kubernetesSecretReference(value.NodeStageSecretRef),
			NodePublishSecretRef:       kubernetesSecretReference(value.NodePublishSecretRef),
			ControllerExpandSecretRef:  kubernetesSecretReference(value.ControllerExpandSecretRef),
			NodeExpandSecretRef:        kubernetesSecretReference(value.NodeExpandSecretRef),
		}}, nil
	}
	if input.NFS != nil {
		value := input.NFS
		if volumeMode == string(corev1.PersistentVolumeBlock) || strings.TrimSpace(value.Server) == "" || len(value.Server) > 253 ||
			strings.ContainsAny(value.Server, " \t\r\n") || !path.IsAbs(value.Path) {
			return corev1.PersistentVolumeSource{}, ErrInvalidInput
		}
		return corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: value.Server, Path: value.Path, ReadOnly: value.ReadOnly}}, nil
	}
	value := input.Local
	if !path.IsAbs(value.Path) || volumeMode == string(corev1.PersistentVolumeBlock) && value.FSType != "" {
		return corev1.PersistentVolumeSource{}, ErrInvalidInput
	}
	fsType := value.FSType
	return corev1.PersistentVolumeSource{Local: &corev1.LocalVolumeSource{Path: value.Path, FSType: optionalStringPointer(fsType)}}, nil
}

func storageResourceDetail(object map[string]any, resourceName StorageResource, namespace, name string) (StorageResourceDetail, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return StorageResourceDetail{}, ErrInvalidResponse
	}
	switch resourceName {
	case StoragePersistentVolumes:
		var value corev1.PersistentVolume
		if json.Unmarshal(body, &value) != nil || !validTypedStorageIdentity(&value.ObjectMeta, value.APIVersion, value.Kind, namespace, name, "v1", "PersistentVolume") {
			return StorageResourceDetail{}, ErrInvalidResponse
		}
		return persistentVolumeResourceDetail(&value), nil
	case StoragePersistentVolumeClaims:
		var value corev1.PersistentVolumeClaim
		if json.Unmarshal(body, &value) != nil || !validTypedStorageIdentity(&value.ObjectMeta, value.APIVersion, value.Kind, namespace, name, "v1", "PersistentVolumeClaim") {
			return StorageResourceDetail{}, ErrInvalidResponse
		}
		return persistentVolumeClaimResourceDetail(&value), nil
	case StorageClasses:
		var value storagev1.StorageClass
		if json.Unmarshal(body, &value) != nil || !validTypedStorageIdentity(&value.ObjectMeta, value.APIVersion, value.Kind, namespace, name, "storage.k8s.io/v1", "StorageClass") {
			return StorageResourceDetail{}, ErrInvalidResponse
		}
		return storageClassResourceDetail(&value), nil
	default:
		return StorageResourceDetail{}, ErrInvalidInput
	}
}

func persistentVolumeResourceDetail(value *corev1.PersistentVolume) StorageResourceDetail {
	claimRef := storageObjectReference(value.Spec.ClaimRef)
	lastTransition := optionalTimePointer(value.Status.LastPhaseTransitionTime)
	summary := PersistentVolumeSummary{
		Phase: string(value.Status.Phase), Capacity: storageResourceQuantity(value.Spec.Capacity),
		AccessModes: storageAccessModeStrings(value.Spec.AccessModes), ReclaimPolicy: string(value.Spec.PersistentVolumeReclaimPolicy),
		StorageClassName: value.Spec.StorageClassName, VolumeMode: persistentVolumeMode(value.Spec.VolumeMode),
		SourceType: persistentVolumeSourceType(value.Spec.PersistentVolumeSource), ClaimRef: claimRef, LastTransitionTime: lastTransition,
	}
	return StorageResourceDetail{
		StorageResourceSummary: storageSummary(StoragePersistentVolumes, value.TypeMeta, value.ObjectMeta, &summary, nil, nil),
		Annotations:            normalizedStringMap(value.Annotations),
		PersistentVolume: &PersistentVolumeDetail{
			MountOptions: normalizedStrings(value.Spec.MountOptions), NodeAffinity: storageNodeSelectorView(value.Spec.NodeAffinity),
			Source: persistentVolumeSourceView(value.Spec.PersistentVolumeSource), Reason: value.Status.Reason, Message: value.Status.Message,
		},
	}
}

func persistentVolumeClaimResourceDetail(value *corev1.PersistentVolumeClaim) StorageResourceDetail {
	summary := PersistentVolumeClaimSummary{
		Phase: string(value.Status.Phase), RequestedCapacity: storageResourceQuantity(value.Spec.Resources.Requests),
		Capacity: storageResourceQuantity(value.Status.Capacity), AccessModes: storageAccessModeStrings(value.Spec.AccessModes),
		StorageClassName: cloneStringPointer(value.Spec.StorageClassName), VolumeName: value.Spec.VolumeName,
		VolumeMode: persistentVolumeMode(value.Spec.VolumeMode),
	}
	conditions := make([]PersistentVolumeClaimCondition, 0, len(value.Status.Conditions))
	for _, condition := range value.Status.Conditions {
		conditions = append(conditions, PersistentVolumeClaimCondition{
			Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason, Message: condition.Message,
			LastProbeTime: condition.LastProbeTime.Time, LastTransitionTime: condition.LastTransitionTime.Time,
		})
	}
	return StorageResourceDetail{
		StorageResourceSummary:      storageSummary(StoragePersistentVolumeClaims, value.TypeMeta, value.ObjectMeta, nil, &summary, nil),
		Annotations:                 normalizedStringMap(value.Annotations),
		PersistentVolumeClaimDetail: &PersistentVolumeClaimDetail{Selector: storageSelectorView(value.Spec.Selector), Conditions: conditions},
	}
}

func storageClassResourceDetail(value *storagev1.StorageClass) StorageResourceDetail {
	summary := StorageClassSummary{
		Provisioner: value.Provisioner, ReclaimPolicy: storageClassReclaimPolicy(value.ReclaimPolicy),
		VolumeBindingMode:    storageClassBindingMode(value.VolumeBindingMode),
		AllowVolumeExpansion: value.AllowVolumeExpansion != nil && *value.AllowVolumeExpansion,
		Default:              storageClassDefault(value.Annotations),
	}
	return StorageResourceDetail{
		StorageResourceSummary: storageSummary(StorageClasses, value.TypeMeta, value.ObjectMeta, nil, nil, &summary),
		Annotations:            normalizedStringMap(value.Annotations),
		StorageClassDetail: &StorageClassDetail{
			Parameters: normalizedStringMap(value.Parameters), MountOptions: normalizedStrings(value.MountOptions),
			AllowedTopologies: storageTopologyViews(value.AllowedTopologies),
		},
	}
}

func storageSummary(resourceName StorageResource, typeMeta metav1.TypeMeta, metadata metav1.ObjectMeta, pv *PersistentVolumeSummary, pvc *PersistentVolumeClaimSummary, class *StorageClassSummary) StorageResourceSummary {
	return StorageResourceSummary{
		Resource: resourceName, APIVersion: typeMeta.APIVersion, Kind: typeMeta.Kind,
		Namespace: metadata.Namespace, Name: metadata.Name, UID: string(metadata.UID), ResourceVersion: metadata.ResourceVersion,
		CreationTimestamp: metadata.CreationTimestamp.Time, Labels: normalizedStringMap(metadata.Labels),
		PersistentVolume: pv, PersistentVolumeClaim: pvc, StorageClass: class,
	}
}

func typedStorageObject(object any, failure error) (map[string]any, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return nil, failure
	}
	var result unstructured.Unstructured
	if result.UnmarshalJSON(body) != nil {
		return nil, failure
	}
	return result.Object, nil
}

func validTypedStorageIdentity(metadata *metav1.ObjectMeta, apiVersion, kind, namespace, name, expectedVersion, expectedKind string) bool {
	return apiVersion == expectedVersion && kind == expectedKind && metadata.Name != "" && metadata.Namespace == namespace && (name == "" || metadata.Name == name)
}

func validAccessModes(values []string) bool {
	if len(values) == 0 || len(values) > 4 {
		return false
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		switch corev1.PersistentVolumeAccessMode(value) {
		case corev1.ReadWriteOnce, corev1.ReadOnlyMany, corev1.ReadWriteMany, corev1.ReadWriteOncePod:
		default:
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validReclaimPolicy(value string, allowDefault bool) bool {
	return allowDefault && value == "" || value == string(corev1.PersistentVolumeReclaimRetain) || value == string(corev1.PersistentVolumeReclaimDelete)
}

func validVolumeMode(value string) bool {
	return value == "" || value == string(corev1.PersistentVolumeFilesystem) || value == string(corev1.PersistentVolumeBlock)
}

func validVolumeBindingMode(value string) bool {
	return value == "" || value == string(storagev1.VolumeBindingImmediate) || value == string(storagev1.VolumeBindingWaitForFirstConsumer)
}

func validPositiveQuantity(value string) bool {
	quantity, err := resource.ParseQuantity(value)
	return err == nil && quantity.Sign() > 0
}

func validMountOptions(values []string) bool {
	if len(values) > maxStorageMountOptions {
		return false
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validStorageMap(values map[string]string, requireQualifiedKeys bool) bool {
	if len(values) > maxStorageMapEntries {
		return false
	}
	total := 0
	for key, value := range values {
		if key == "" || requireQualifiedKeys && len(k8svalidation.IsQualifiedName(key)) != 0 {
			return false
		}
		total += len(key) + len(value)
		if total > maxStorageMapSize {
			return false
		}
	}
	return true
}

func validStorageSecretReferences(values ...*StorageSecretReference) bool {
	for _, value := range values {
		if value != nil && (len(k8svalidation.IsDNS1123Label(value.Namespace)) != 0 || len(k8svalidation.IsDNS1123Subdomain(value.Name)) != 0) {
			return false
		}
	}
	return true
}

func validStorageTopologies(terms []StorageTopologyTerm) bool {
	if len(terms) > 64 {
		return false
	}
	for _, term := range terms {
		if len(term.MatchLabelExpressions) == 0 || len(term.MatchLabelExpressions) > 64 {
			return false
		}
		for _, requirement := range term.MatchLabelExpressions {
			if len(k8svalidation.IsQualifiedName(requirement.Key)) != 0 || len(requirement.Values) == 0 || len(requirement.Values) > 64 {
				return false
			}
			for _, value := range requirement.Values {
				if len(k8svalidation.IsValidLabelValue(value)) != 0 {
					return false
				}
			}
		}
	}
	return true
}

func storageLabelSelector(selector *WorkloadSelector) (*metav1.LabelSelector, error) {
	if selector == nil {
		return nil, nil
	}
	result := gatewayLabelSelector(selector)
	if _, err := metav1.LabelSelectorAsSelector(result); err != nil {
		return nil, err
	}
	return result, nil
}

func storageNodeSelector(input *StorageNodeSelector) (*corev1.VolumeNodeAffinity, error) {
	if input == nil {
		return nil, nil
	}
	if len(input.Terms) == 0 || len(input.Terms) > 64 {
		return nil, ErrInvalidInput
	}
	selector := &corev1.NodeSelector{NodeSelectorTerms: make([]corev1.NodeSelectorTerm, 0, len(input.Terms))}
	for _, term := range input.Terms {
		if len(term.MatchExpressions)+len(term.MatchFields) == 0 || len(term.MatchExpressions) > 64 || len(term.MatchFields) > 64 {
			return nil, ErrInvalidInput
		}
		nodeTerm := corev1.NodeSelectorTerm{
			MatchExpressions: storageNodeSelectorRequirements(term.MatchExpressions),
			MatchFields:      storageNodeSelectorRequirements(term.MatchFields),
		}
		selector.NodeSelectorTerms = append(selector.NodeSelectorTerms, nodeTerm)
	}
	if _, err := nodeaffinity.NewNodeSelector(selector); err != nil {
		return nil, err
	}
	return &corev1.VolumeNodeAffinity{Required: selector}, nil
}

func storageNodeSelectorRequirements(values []StorageNodeSelectorRequirement) []corev1.NodeSelectorRequirement {
	result := make([]corev1.NodeSelectorRequirement, 0, len(values))
	for _, requirement := range values {
		result = append(result, corev1.NodeSelectorRequirement{
			Key: requirement.Key, Operator: corev1.NodeSelectorOperator(requirement.Operator), Values: slices.Clone(requirement.Values),
		})
	}
	return result
}

func storageAccessModes(values []string) []corev1.PersistentVolumeAccessMode {
	result := make([]corev1.PersistentVolumeAccessMode, 0, len(values))
	for _, value := range values {
		result = append(result, corev1.PersistentVolumeAccessMode(value))
	}
	return result
}

func storageAccessModeStrings(values []corev1.PersistentVolumeAccessMode) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func storageResourceQuantity(values corev1.ResourceList) string {
	value, exists := values[corev1.ResourceStorage]
	if !exists {
		return ""
	}
	return value.String()
}

func persistentVolumeMode(value *corev1.PersistentVolumeMode) string {
	if value == nil {
		return string(corev1.PersistentVolumeFilesystem)
	}
	return string(*value)
}

func storageClassReclaimPolicy(value *corev1.PersistentVolumeReclaimPolicy) string {
	if value == nil {
		return string(corev1.PersistentVolumeReclaimDelete)
	}
	return string(*value)
}

func storageClassBindingMode(value *storagev1.VolumeBindingMode) string {
	if value == nil {
		return string(storagev1.VolumeBindingImmediate)
	}
	return string(*value)
}

func storageClassDefault(annotations map[string]string) bool {
	for _, key := range []string{"storageclass.kubernetes.io/is-default-class", "storageclass.beta.kubernetes.io/is-default-class"} {
		value, _ := strconv.ParseBool(annotations[key])
		if value {
			return true
		}
	}
	return false
}

func persistentVolumeSourceType(source corev1.PersistentVolumeSource) string {
	switch {
	case source.CSI != nil:
		return "csi"
	case source.NFS != nil:
		return "nfs"
	case source.Local != nil:
		return "local"
	case source.HostPath != nil:
		return "hostPath"
	case source.GCEPersistentDisk != nil:
		return "gcePersistentDisk"
	case source.AWSElasticBlockStore != nil:
		return "awsElasticBlockStore"
	case source.Glusterfs != nil:
		return "glusterfs"
	case source.RBD != nil:
		return "rbd"
	case source.ISCSI != nil:
		return "iscsi"
	case source.Cinder != nil:
		return "cinder"
	case source.CephFS != nil:
		return "cephfs"
	case source.FC != nil:
		return "fc"
	case source.Flocker != nil:
		return "flocker"
	case source.FlexVolume != nil:
		return "flexVolume"
	case source.AzureFile != nil:
		return "azureFile"
	case source.VsphereVolume != nil:
		return "vsphereVolume"
	case source.Quobyte != nil:
		return "quobyte"
	case source.AzureDisk != nil:
		return "azureDisk"
	case source.PhotonPersistentDisk != nil:
		return "photonPersistentDisk"
	case source.PortworxVolume != nil:
		return "portworxVolume"
	case source.ScaleIO != nil:
		return "scaleIO"
	case source.StorageOS != nil:
		return "storageos"
	default:
		return "unknown"
	}
}

func persistentVolumeSourceView(source corev1.PersistentVolumeSource) PersistentVolumeSourceView {
	result := PersistentVolumeSourceView{Type: persistentVolumeSourceType(source)}
	if source.CSI != nil {
		value := source.CSI
		result.CSI = &CSIPersistentVolumeSource{
			Driver: value.Driver, VolumeHandle: value.VolumeHandle, ReadOnly: value.ReadOnly, FSType: value.FSType,
			VolumeAttributes:           normalizedStringMap(value.VolumeAttributes),
			ControllerPublishSecretRef: storageSecretReference(value.ControllerPublishSecretRef),
			NodeStageSecretRef:         storageSecretReference(value.NodeStageSecretRef), NodePublishSecretRef: storageSecretReference(value.NodePublishSecretRef),
			ControllerExpandSecretRef: storageSecretReference(value.ControllerExpandSecretRef), NodeExpandSecretRef: storageSecretReference(value.NodeExpandSecretRef),
		}
	} else if source.NFS != nil {
		result.NFS = &NFSPersistentVolumeSource{Server: source.NFS.Server, Path: source.NFS.Path, ReadOnly: source.NFS.ReadOnly}
	} else if source.Local != nil {
		result.Local = &LocalPersistentVolumeSource{Path: source.Local.Path, FSType: stringPointerValue(source.Local.FSType)}
	}
	return result
}

func storageNodeSelectorView(value *corev1.VolumeNodeAffinity) *StorageNodeSelector {
	if value == nil || value.Required == nil {
		return nil
	}
	result := &StorageNodeSelector{Terms: make([]StorageNodeSelectorTerm, 0, len(value.Required.NodeSelectorTerms))}
	for _, term := range value.Required.NodeSelectorTerms {
		view := StorageNodeSelectorTerm{
			MatchExpressions: storageNodeSelectorRequirementViews(term.MatchExpressions),
			MatchFields:      storageNodeSelectorRequirementViews(term.MatchFields),
		}
		result.Terms = append(result.Terms, view)
	}
	return result
}

func storageNodeSelectorRequirementViews(values []corev1.NodeSelectorRequirement) []StorageNodeSelectorRequirement {
	result := make([]StorageNodeSelectorRequirement, 0, len(values))
	for _, requirement := range values {
		result = append(result, StorageNodeSelectorRequirement{
			Key: requirement.Key, Operator: string(requirement.Operator), Values: normalizedStrings(requirement.Values),
		})
	}
	return result
}

func storageSelectorView(value *metav1.LabelSelector) *WorkloadSelector {
	if value == nil {
		return nil
	}
	result := &WorkloadSelector{MatchLabels: normalizedStringMap(value.MatchLabels), MatchExpressions: make([]WorkloadSelectorRequirement, 0, len(value.MatchExpressions))}
	for _, expression := range value.MatchExpressions {
		result.MatchExpressions = append(result.MatchExpressions, WorkloadSelectorRequirement{Key: expression.Key, Operator: string(expression.Operator), Values: normalizedStrings(expression.Values)})
	}
	return result
}

func storageTopologyTerms(values []StorageTopologyTerm) []corev1.TopologySelectorTerm {
	result := make([]corev1.TopologySelectorTerm, 0, len(values))
	for _, term := range values {
		converted := corev1.TopologySelectorTerm{MatchLabelExpressions: make([]corev1.TopologySelectorLabelRequirement, 0, len(term.MatchLabelExpressions))}
		for _, requirement := range term.MatchLabelExpressions {
			converted.MatchLabelExpressions = append(converted.MatchLabelExpressions, corev1.TopologySelectorLabelRequirement{Key: requirement.Key, Values: slices.Clone(requirement.Values)})
		}
		result = append(result, converted)
	}
	return result
}

func storageTopologyViews(values []corev1.TopologySelectorTerm) []StorageTopologyTerm {
	result := make([]StorageTopologyTerm, 0, len(values))
	for _, term := range values {
		view := StorageTopologyTerm{MatchLabelExpressions: make([]StorageTopologyRequirement, 0, len(term.MatchLabelExpressions))}
		for _, requirement := range term.MatchLabelExpressions {
			view.MatchLabelExpressions = append(view.MatchLabelExpressions, StorageTopologyRequirement{Key: requirement.Key, Values: normalizedStrings(requirement.Values)})
		}
		result = append(result, view)
	}
	return result
}

func storageObjectReference(value *corev1.ObjectReference) *StorageObjectReference {
	if value == nil {
		return nil
	}
	return &StorageObjectReference{Namespace: value.Namespace, Name: value.Name, UID: string(value.UID)}
}

func storageSecretReference(value *corev1.SecretReference) *StorageSecretReference {
	if value == nil {
		return nil
	}
	return &StorageSecretReference{Namespace: value.Namespace, Name: value.Name}
}

func kubernetesSecretReference(value *StorageSecretReference) *corev1.SecretReference {
	if value == nil {
		return nil
	}
	return &corev1.SecretReference{Namespace: value.Namespace, Name: value.Name}
}

func normalizedStringMap(value map[string]string) map[string]string {
	result := maps.Clone(value)
	if result == nil {
		result = map[string]string{}
	}
	return result
}

func normalizedStrings(value []string) []string {
	if value == nil {
		return []string{}
	}
	return slices.Clone(value)
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func optionalStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalTimePointer(value *metav1.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.Time
	return &result
}
