package kubernetesresource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

const workloadRestartAnnotation = "zke.io/restart-request"

type WorkloadMutationInput struct {
	ClusterID      string
	Namespace      string
	Resource       WorkloadResource
	Name           string
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type ScaleWorkloadInput struct {
	WorkloadMutationInput
	Replicas int32
}

type SetWorkloadSuspensionInput struct {
	WorkloadMutationInput
	Suspended bool
}

type DeleteWorkloadInput struct {
	WorkloadMutationInput
	GracePeriodSeconds *int64
	Propagation        agentv1.DeletePropagation
	UID                string
	ResourceVersion    string
}

func (service *Service) ScaleWorkload(
	ctx context.Context,
	input ScaleWorkloadInput,
) (WorkloadDetail, error) {
	if input.Resource != WorkloadDeployments &&
		input.Resource != WorkloadStatefulSets ||
		input.Replicas < 0 {
		return WorkloadDetail{}, ErrInvalidInput
	}
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{"replicas": input.Replicas},
	})
	if err != nil {
		return WorkloadDetail{}, ErrInvalidInput
	}
	return service.patchWorkload(ctx, input.WorkloadMutationInput, patch)
}

func (service *Service) RestartWorkload(
	ctx context.Context,
	input WorkloadMutationInput,
) (WorkloadDetail, error) {
	if input.Resource != WorkloadDeployments &&
		input.Resource != WorkloadStatefulSets &&
		input.Resource != WorkloadDaemonSets {
		return WorkloadDetail{}, ErrInvalidInput
	}
	restartRequest := sha256.Sum256([]byte(input.IdempotencyKey))
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						workloadRestartAnnotation: hex.EncodeToString(restartRequest[:]),
					},
				},
			},
		},
	})
	if err != nil {
		return WorkloadDetail{}, ErrInvalidInput
	}
	return service.patchWorkload(ctx, input, patch)
}

func (service *Service) SetWorkloadSuspension(
	ctx context.Context,
	input SetWorkloadSuspensionInput,
) (WorkloadDetail, error) {
	if input.Resource != WorkloadCronJobs {
		return WorkloadDetail{}, ErrInvalidInput
	}
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{"suspend": input.Suspended},
	})
	if err != nil {
		return WorkloadDetail{}, ErrInvalidInput
	}
	return service.patchWorkload(ctx, input.WorkloadMutationInput, patch)
}

func (service *Service) DeleteWorkload(
	ctx context.Context,
	input DeleteWorkloadInput,
) error {
	identity, exists := WorkloadResourceIdentity(input.Resource)
	if !exists || strings.TrimSpace(input.UID) == "" {
		return ErrInvalidInput
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID:          input.ClusterID,
		Resource:           identity,
		Namespace:          input.Namespace,
		Name:               input.Name,
		DryRun:             input.DryRun,
		Confirm:            input.Confirm,
		GracePeriodSeconds: input.GracePeriodSeconds,
		Propagation:        input.Propagation,
		Preconditions: DeletePreconditions{
			UID:             input.UID,
			ResourceVersion: input.ResourceVersion,
		},
		IdempotencyKey: input.IdempotencyKey,
	})
}

func (service *Service) patchWorkload(
	ctx context.Context,
	input WorkloadMutationInput,
	patch json.RawMessage,
) (WorkloadDetail, error) {
	identity, exists := WorkloadResourceIdentity(input.Resource)
	if !exists {
		return WorkloadDetail{}, ErrInvalidInput
	}
	result, err := service.PatchResource(ctx, PatchResourceInput{
		ClusterID: input.ClusterID,
		Resource:  identity,
		Namespace: input.Namespace,
		Name:      input.Name,
		PatchType: agentv1.PatchType_PATCH_TYPE_MERGE,
		Patch:     patch,
		Options: MutationOptions{
			DryRun: input.DryRun,
		},
		Confirm:        input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return WorkloadDetail{}, err
	}
	return workloadDetail(result, input.Resource, input.Namespace, input.Name)
}
