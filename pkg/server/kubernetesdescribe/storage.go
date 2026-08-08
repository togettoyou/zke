package kubernetesdescribe

import (
	"context"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type PersistentVolumeClaimInput struct {
	ClusterID string
	Namespace string
	Name      string
}

// DescribePersistentVolumeClaim joins a claim's binding state with the Events
// from the provisioner and binder that explain why it is still Pending.
func (service *Service) DescribePersistentVolumeClaim(
	ctx context.Context,
	input PersistentVolumeClaimInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	claim, err := service.resources.GetStorageResource(
		ctx,
		input.ClusterID,
		input.Namespace,
		kubernetesresource.StoragePersistentVolumeClaims,
		input.Name,
	)
	if err != nil {
		return Result{}, err
	}
	if claim.UID == "" {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	result := Result{
		Target: Target{
			APIVersion:      claim.APIVersion,
			Kind:            claim.Kind,
			Namespace:       claim.Namespace,
			Name:            claim.Name,
			UID:             claim.UID,
			ResourceVersion: claim.ResourceVersion,
		},
		Family:           FamilyStorage,
		Storage:          &claim,
		Findings:         []Finding{},
		DegradedSections: []string{},
	}
	result.Events, result.DegradedSections = service.objectEvents(
		ctx, input.ClusterID, result.Target,
	)
	result.Findings = persistentVolumeClaimFindings(claim, result.Events.Items)
	return result, nil
}
