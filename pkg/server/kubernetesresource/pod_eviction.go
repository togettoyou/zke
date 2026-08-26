package kubernetesresource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/validation"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// ErrPodEvictionBlocked is Kubernetes refusing an eviction because removing the
// Pod now would take its workload below a PodDisruptionBudget.
//
// It is deliberately not folded into ErrUpstreamConflict: a conflict means the
// caller acted on a stale view and should re-read, while this means the view was
// right and the cluster is protecting availability. Retrying the first can
// succeed immediately; retrying the second succeeds only once other replicas are
// ready, and telling an operator to "reload and try again" would be wrong.
var ErrPodEvictionBlocked = errors.New("Kubernetes PodDisruptionBudget blocks the Pod eviction")

// PodEvictionBlocked carries the API Server's own account of which budget
// refused, which names an object the caller can go and look at.
type PodEvictionBlocked struct {
	Message string
}

func (blocked *PodEvictionBlocked) Error() string {
	return ErrPodEvictionBlocked.Error() + ": " + blocked.Message
}

// Detail is the API Server's account of the refusal. It describes a
// PodDisruptionBudget in the caller's own Namespace, never Server internals.
func (blocked *PodEvictionBlocked) Detail() string {
	return blocked.Message
}

func (blocked *PodEvictionBlocked) Unwrap() error {
	return ErrPodEvictionBlocked
}

type EvictPodInput struct {
	ClusterID          string
	Namespace          string
	Name               string
	UID                string
	GracePeriodSeconds *int64
	DryRun             bool
	Confirm            bool
	IdempotencyKey     string
}

type EvictPodResult struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	DryRun    bool   `json:"dry_run"`
	// Evicted is false for a preview: a DryRun eviction is the API Server
	// answering "this would be allowed", not the Pod going away.
	Evicted bool `json:"evicted"`
}

// EvictPod asks Kubernetes to remove one Pod through the eviction subresource
// rather than deleting the object.
//
// Deleting a Pod and evicting it end the same way, and they are still not the
// same operation: a delete goes through whatever disruption the workload is
// currently able to absorb, while an eviction is checked against the
// PodDisruptionBudgets that cover the Pod and is refused when honouring it would
// break one. That check is the whole reason this endpoint exists next to the
// delete one, and it is why both answer to `cluster.resource.delete`: the
// eviction is the same power used more carefully, not a wider one.
//
// The UID precondition is required, as it is for a delete. A Pod name is reused
// the moment its controller replaces it, so an eviction confirmed against a
// listing taken a minute ago would otherwise remove the replacement.
func (service *Service) EvictPod(
	ctx context.Context,
	input EvictPodInput,
) (EvictPodResult, error) {
	if !validation.IsUUID(input.ClusterID) ||
		len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(input.Name)) != 0 ||
		strings.TrimSpace(input.UID) == "" || len(input.UID) > 128 ||
		(!input.DryRun && !input.Confirm) ||
		!validIdempotencyKey(input.IdempotencyKey) ||
		(input.GracePeriodSeconds != nil && *input.GracePeriodSeconds < 0) {
		return EvictPodResult{}, ErrInvalidInput
	}
	requester, ok := service.requester.(MutationResourceRequester)
	if !ok {
		return EvictPodResult{}, ErrAgentUnsupported
	}
	uid := types.UID(input.UID)
	eviction := policyv1.Eviction{
		TypeMeta: metav1.TypeMeta{APIVersion: "policy/v1", Kind: "Eviction"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      input.Name,
			Namespace: input.Namespace,
		},
		DeleteOptions: &metav1.DeleteOptions{
			DryRun:             evictionDryRun(input.DryRun),
			GracePeriodSeconds: input.GracePeriodSeconds,
			Preconditions:      &metav1.Preconditions{UID: &uid},
		},
	}
	body, err := json.Marshal(eviction)
	if err != nil {
		return EvictPodResult{}, ErrInvalidInput
	}
	response, err := requester.RequestResourceMutation(ctx, input.ClusterID, &agentv1.ResourceRequest{
		Verb:              agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
		Resource:          &agentv1.GroupVersionResource{Version: "v1", Resource: "pods"},
		Namespace:         input.Namespace,
		Name:              input.Name,
		Subresource:       "eviction",
		Representation:    agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		BodySize:          uint64(len(body)),
		MutationOptions:   &agentv1.MutationOptions{DryRun: input.DryRun},
		PodEvictionAccess: true,
	}, bytes.NewReader(body), io.Discard, input.IdempotencyKey)
	if err != nil {
		return EvictPodResult{}, requestError(err)
	}
	result := EvictPodResult{
		Namespace: input.Namespace,
		Name:      input.Name,
		UID:       input.UID,
		DryRun:    input.DryRun,
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		result.Evicted = !input.DryRun
		return result, nil
	}
	// 429 is the one status the eviction subresource gives a meaning of its own:
	// the request was valid and was refused to keep a PodDisruptionBudget
	// satisfied. Everything else is an ordinary Kubernetes failure and is mapped
	// with the ordinary vocabulary.
	if response.GetKubernetesStatusCode() == http.StatusTooManyRequests {
		message := response.GetMessage()
		if strings.TrimSpace(message) == "" {
			message = "Kubernetes refused the eviction to keep a PodDisruptionBudget satisfied"
		}
		return EvictPodResult{}, &PodEvictionBlocked{Message: message}
	}
	return EvictPodResult{}, mutationResponseError(response, false)
}

// The `deleteOptions.dryRun` an Eviction carries, shared with Node Drain: both
// ask Kubernetes the same question about the same subresource.
func evictionDryRun(enabled bool) []string {
	if enabled {
		return []string{metav1.DryRunAll}
	}
	return nil
}
