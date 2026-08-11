package kubernetesresource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/validation"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const nodeDrainPodLimit int64 = 500

var ErrDrainInventoryTruncated = errors.New("node Pod inventory exceeded the drain safety limit")

type DrainNodeInput struct {
	ClusterID             string
	NodeName              string
	NodeUID               string
	DryRun                bool
	Confirm               bool
	ForceUnmanaged        bool
	DeleteEmptyDirData    bool
	GracePeriodSeconds    *int64
	IdempotencyKey        string
	SystemNamespaceManage bool
	AgentNamespaceManage  bool
}

type DrainPodDecision string

const (
	DrainPodEvict DrainPodDecision = "evict"
	DrainPodSkip  DrainPodDecision = "skip"
	DrainPodBlock DrainPodDecision = "block"
)

type DrainPodResult string

const (
	DrainPodPending    DrainPodResult = "pending"
	DrainPodEvicted    DrainPodResult = "evicted"
	DrainPodSkipped    DrainPodResult = "skipped"
	DrainPodBlocked    DrainPodResult = "blocked"
	DrainPodPDBBlocked DrainPodResult = "pdb_blocked"
	DrainPodFailed     DrainPodResult = "failed"
)

type DrainPod struct {
	Namespace string           `json:"namespace"`
	Name      string           `json:"name"`
	UID       string           `json:"uid"`
	Decision  DrainPodDecision `json:"decision"`
	Result    DrainPodResult   `json:"result"`
	Reason    string           `json:"reason"`
	Message   string           `json:"message"`
}

type DrainNodeResult struct {
	NodeName        string     `json:"node_name"`
	NodeUID         string     `json:"node_uid"`
	DryRun          bool       `json:"dry_run"`
	AlreadyCordoned bool       `json:"already_cordoned"`
	Cordoned        bool       `json:"cordoned"`
	CordonValidated bool       `json:"cordon_validated"`
	Blocked         bool       `json:"blocked"`
	Pods            []DrainPod `json:"pods"`
}

func (service *Service) DrainNode(ctx context.Context, input DrainNodeInput) (DrainNodeResult, error) {
	if !validation.IsUUID(input.ClusterID) ||
		!validPathSegment(input.NodeName) ||
		input.NodeUID == "" || len(input.NodeUID) > 128 ||
		(!input.DryRun && !input.Confirm) ||
		!validIdempotencyKey(input.IdempotencyKey) ||
		(input.GracePeriodSeconds != nil && *input.GracePeriodSeconds < 0) {
		return DrainNodeResult{}, ErrInvalidInput
	}
	node, err := service.GetNode(ctx, input.ClusterID, input.NodeName)
	if err != nil {
		return DrainNodeResult{}, err
	}
	if node.UID != input.NodeUID {
		return DrainNodeResult{}, ErrUpstreamConflict
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID:     input.ClusterID,
		Resource:      ResourceIdentity{Version: "v1", Resource: "pods"},
		Limit:         nodeDrainPodLimit,
		FieldSelector: "spec.nodeName=" + input.NodeName,
	})
	if err != nil {
		return DrainNodeResult{}, err
	}
	if page.ContinueToken != "" {
		return DrainNodeResult{}, ErrDrainInventoryTruncated
	}
	result := DrainNodeResult{
		NodeName:        input.NodeName,
		NodeUID:         input.NodeUID,
		DryRun:          input.DryRun,
		AlreadyCordoned: node.Unschedulable,
		Cordoned:        node.Unschedulable,
		Pods:            make([]DrainPod, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		var pod corev1.Pod
		if runtime.DefaultUnstructuredConverter.FromUnstructured(item, &pod) != nil ||
			pod.Name == "" || pod.Namespace == "" || pod.UID == "" || pod.Spec.NodeName != input.NodeName {
			return DrainNodeResult{}, ErrInvalidResponse
		}
		decision := service.classifyDrainPod(&pod, input)
		if decision.Decision == DrainPodBlock {
			result.Blocked = true
		}
		result.Pods = append(result.Pods, decision)
	}
	// A preview with blockers is useful, but an apply must never partially
	// cordon a Node before telling the operator it refused to start.
	if result.Blocked {
		return result, nil
	}
	if err := service.cordonForDrain(ctx, input); err != nil {
		return DrainNodeResult{}, err
	}
	result.CordonValidated = true
	if !input.DryRun {
		result.Cordoned = true
	}
	for index := range result.Pods {
		pod := &result.Pods[index]
		if pod.Decision != DrainPodEvict {
			continue
		}
		response, requestErr := service.evictDrainPod(ctx, input, *pod)
		if requestErr != nil {
			pod.Result = DrainPodFailed
			pod.Reason = "AgentRequestFailed"
			pod.Message = "Pod eviction request did not complete"
			for remaining := index + 1; remaining < len(result.Pods); remaining++ {
				if result.Pods[remaining].Decision == DrainPodEvict {
					result.Pods[remaining].Result = DrainPodFailed
					result.Pods[remaining].Reason = "AgentRequestAborted"
					result.Pods[remaining].Message = "Pod eviction was not attempted after the Agent request failed"
				}
			}
			break
		}
		if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
			if input.DryRun {
				pod.Result = DrainPodPending
			} else {
				pod.Result = DrainPodEvicted
			}
			continue
		}
		pod.Reason = response.GetReason()
		pod.Message = response.GetMessage()
		if response.GetKubernetesStatusCode() == http.StatusNotFound {
			pod.Result = DrainPodSkipped
			pod.Reason = "PodGone"
			pod.Message = "Pod disappeared after the drain inventory was read"
		} else if response.GetKubernetesStatusCode() == http.StatusTooManyRequests {
			pod.Result = DrainPodPDBBlocked
		} else {
			pod.Result = DrainPodFailed
		}
	}
	return result, nil
}

func classifyDrainPod(pod *corev1.Pod, input DrainNodeInput) DrainPod {
	return (&Service{agentNamespace: "zke-system"}).classifyDrainPod(pod, input)
}

func (service *Service) classifyDrainPod(pod *corev1.Pod, input DrainNodeInput) DrainPod {
	result := DrainPod{Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID)}
	if pod.DeletionTimestamp != nil {
		result.Decision, result.Result, result.Reason = DrainPodSkip, DrainPodSkipped, "Terminating"
		return result
	}
	if pod.Annotations[corev1.MirrorPodAnnotationKey] != "" {
		result.Decision, result.Result, result.Reason = DrainPodSkip, DrainPodSkipped, "MirrorPod"
		return result
	}
	for _, owner := range pod.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.Kind == "DaemonSet" {
			result.Decision, result.Result, result.Reason = DrainPodSkip, DrainPodSkipped, "DaemonSetPod"
			return result
		}
	}
	if pod.Namespace == service.agentNamespace && !input.AgentNamespaceManage {
		result.Decision, result.Result, result.Reason = DrainPodBlock, DrainPodBlocked, "AgentNamespacePermissionRequired"
		result.Message = "Pod eviction requires cluster.agent_namespace.manage"
		return result
	}
	if strings.HasPrefix(pod.Namespace, "kube-") && !input.SystemNamespaceManage {
		result.Decision, result.Result, result.Reason = DrainPodBlock, DrainPodBlocked, "SystemNamespacePermissionRequired"
		result.Message = "Pod eviction requires cluster.system_namespace.manage"
		return result
	}
	controlled := false
	for _, owner := range pod.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			controlled = true
			break
		}
	}
	if !controlled && !input.ForceUnmanaged {
		result.Decision, result.Result, result.Reason = DrainPodBlock, DrainPodBlocked, "UnmanagedPod"
		result.Message = "Pod has no controller; enable force_unmanaged to evict it"
		return result
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.EmptyDir != nil && !input.DeleteEmptyDirData {
			result.Decision, result.Result, result.Reason = DrainPodBlock, DrainPodBlocked, "EmptyDirData"
			result.Message = "Pod uses emptyDir; enable delete_empty_dir_data to accept data loss"
			return result
		}
	}
	result.Decision, result.Result, result.Reason = DrainPodEvict, DrainPodPending, "Evictable"
	return result
}

func (service *Service) cordonForDrain(ctx context.Context, input DrainNodeInput) error {
	patch, _ := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": input.NodeUID},
		{"op": "add", "path": "/spec/unschedulable", "value": true},
	})
	_, err := service.PatchResource(ctx, PatchResourceInput{
		ClusterID:      input.ClusterID,
		Resource:       ResourceIdentity{Version: "v1", Resource: "nodes"},
		Name:           input.NodeName,
		PatchType:      agentv1.PatchType_PATCH_TYPE_JSON,
		Patch:          patch,
		Options:        MutationOptions{DryRun: input.DryRun},
		Confirm:        input.Confirm,
		IdempotencyKey: drainIdempotencyKey(input.IdempotencyKey, "cordon"),
	})
	return err
}

func (service *Service) evictDrainPod(ctx context.Context, input DrainNodeInput, pod DrainPod) (*agentv1.ResourceResponse, error) {
	requester, ok := service.requester.(MutationResourceRequester)
	if !ok {
		return nil, ErrAgentUnsupported
	}
	eviction := policyv1.Eviction{
		TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "Eviction"},
		ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
		DeleteOptions: &metav1.DeleteOptions{
			DryRun:             drainDryRun(input.DryRun),
			GracePeriodSeconds: input.GracePeriodSeconds,
			Preconditions:      &metav1.Preconditions{UID: drainPodUID(pod.UID)},
		},
	}
	body, err := json.Marshal(eviction)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return requester.RequestResourceMutation(ctx, input.ClusterID, &agentv1.ResourceRequest{
		Verb:              agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
		Resource:          &agentv1.GroupVersionResource{Version: "v1", Resource: "pods"},
		Namespace:         pod.Namespace,
		Name:              pod.Name,
		Subresource:       "eviction",
		Representation:    agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		BodySize:          uint64(len(body)),
		MutationOptions:   &agentv1.MutationOptions{DryRun: input.DryRun},
		PodEvictionAccess: true,
	}, bytes.NewReader(body), io.Discard, drainIdempotencyKey(input.IdempotencyKey, pod.UID))
}

func drainDryRun(enabled bool) []string {
	if enabled {
		return []string{metav1.DryRunAll}
	}
	return nil
}

func drainPodUID(value string) *types.UID {
	uid := types.UID(value)
	return &uid
}

func drainIdempotencyKey(base string, suffix string) string {
	hash := sha256.Sum256([]byte(base + "\x00" + suffix))
	return hex.EncodeToString(hash[:])
}
